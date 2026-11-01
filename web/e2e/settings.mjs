// Focused settings navigation check against an isolated rad with fake: true.
// node e2e/settings.mjs <web pairing link> [screenshot path]
import assert from "node:assert/strict";
import { chromium } from "playwright";

const [pairURL, screenshot = "/tmp/remote-agent-settings.png"] = process.argv.slice(2);
if (!pairURL) throw new Error("Pass a scratch rad's web pairing link.");
const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
  page.setDefaultTimeout(10000);
  const errors = [];
  page.on("pageerror", e => errors.push(e.message));
  const base = new URL(pairURL).origin;
  const openSettings = async () => page.getByRole("button", { name: "Settings", exact: true }).first().click();
  const done = async () => page.getByRole("button", { name: "Done", exact: true }).last().click();
  await page.goto(base);
  await openSettings();
  await page.getByRole("button", { name: "Servers", exact: true }).click();
  await page.getByText("No saved servers.").waitFor();
  await page.getByRole("button", { name: "Add Server", exact: true }).click();
  await page.getByRole("heading", { name: "Pair a server" }).waitFor();
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  console.log("empty settings passed; pairing from Settings");
  await page.getByRole("button", { name: "Add Server", exact: true }).click();
  const link = new URLSearchParams(new URL(pairURL).hash.slice(1)).get("pair");
  await page.getByPlaceholder("remoteagent://pair?…").fill(link);
  await page.getByRole("button", { name: "Pair", exact: true }).click();
  await page.locator(".settings-server-detail h3").waitFor();
  await done();
  for (const layout of ["inbox", "projects", "columns"]) {
    console.log("checking", layout);
    await page.evaluate(value => localStorage.setItem("sidebarLayout", value), layout);
    await page.reload();
    await page.locator(".server-header, .server-row").first().hover();
    await page.getByRole("button", { name: / actions$/ }).first().click();
    await page.getByRole("menuitem", { name: "Server Settings…" }).click();
    await page.locator(".settings-server-detail h3").waitFor();
    console.log("shortcut opened");
    await page.getByRole("heading", { name: "System Agent", exact: true }).waitFor();
    await page.getByRole("button", { name: "Reconnect", exact: true }).click();
    await page.waitForFunction(() => !document.querySelector(".server-settings-form").disabled);
    await page.getByRole("button", { name: "Remove Server…", exact: true }).click();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await page.setViewportSize({ width: 390, height: 844 });
    await page.locator(".settings-tabs").waitFor({ state: "hidden" });
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.locator(".settings-server-detail h3").waitFor();
    const dot = await page.locator(".settings-server-row .connection-dot").boundingBox();
    assert.equal(dot.width, dot.height);
    await page.screenshot({ path: screenshot });
    await done();
  }

  console.log("all layouts passed; checking offline removal");
  // A saved server that cannot connect stays selectable and removable.
  await page.evaluate(() => {
    const list = JSON.parse(localStorage.getItem("servers.v1"));
    list.push({ ...list[0], id: "offline-test", name: "Offline test", urls: ["http://127.0.0.1:1"] });
    localStorage.setItem("servers.v1", JSON.stringify(list));
  });
  await page.reload();
  await openSettings();
  await page.getByRole("button", { name: "Servers", exact: true }).click();
  await page.locator(".settings-server-row", { hasText: "Offline test" }).click();
  await page.waitForFunction(() => document.querySelector(".server-settings-form").disabled);
  await page.getByRole("button", { name: "Remove Server…", exact: true }).click();
  await page.getByRole("button", { name: "Remove", exact: true }).click();
  await page.getByText("Select a server to manage it, or add a server.").waitFor();
  assert.equal(await page.locator(".settings-server-row", { hasText: "Offline test" }).count(), 0);
  await page.locator(".settings-server-row").click();
  await page.getByRole("button", { name: "Remove Server…", exact: true }).click();
  await page.getByRole("button", { name: "Remove", exact: true }).click();
  await page.getByText("No saved servers.").waitFor();
  assert.deepEqual(errors, []);
  console.log("Settings: empty/add, all sidebar shortcuts, reconnect, removal, offline, and resizing passed.");
} finally {
  await browser.close();
}
