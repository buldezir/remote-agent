// Drives the web client against a scratch rad with the fake harness, saving
// screenshots. See AGENTS.md, "Web client".
//
//   node e2e/smoke.mjs "$(rad pair --web http://localhost:5173 --print-url)" <out dir> <project folder name>
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright";

const [pairURL, out = "e2e-shots", project = "proj"] = process.argv.slice(2);
if (!pairURL) {
  console.error("usage: node e2e/smoke.mjs <web pairing link> [out dir] [project folder]");
  process.exit(2);
}
mkdirSync(out, { recursive: true });

const browser = await chromium.launch();
const context = await browser.newContext({ viewport: { width: 1280, height: 820 }, colorScheme: "dark" });
const page = await context.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));
page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
let n = 0;
const shot = async (name) => {
  await page.waitForTimeout(300);
  const path = join(out, `${String(++n).padStart(2, "0")}-${name}.png`);
  await page.screenshot({ path });
  console.log("shot", path);
};
const step = (s) => console.log("step:", s);

step("pair");
await page.goto(pairURL);
await page.getByRole("heading", { name: "Pair a server" }).waitFor();
await shot("pair");
await page.getByRole("button", { name: "Pair", exact: true }).click();
await page.getByRole("button", { name: "New session" }).first().waitFor();
await page.waitForFunction(() => !document.querySelector("button[aria-label='New session']:disabled"));
await shot("paired");

step("new session");
await page.getByRole("button", { name: "New session" }).first().click();
await page.getByRole("button", { name: "Add a folder…" }).click();
await page.locator(".folder-row").first().click(); // the root
await page.locator(".folder-row", { hasText: new RegExp(`^${project}`) }).first().click();
await page.getByRole("button", { name: "Use folder" }).click();
await page.locator(".harness-row", { hasText: "Fake" }).click();
await page.getByPlaceholder("What should the agent do?").fill("write a.txt");
await shot("new-session");
await page.getByRole("button", { name: "Start" }).click();

step("approval");
await page.getByRole("button", { name: "Allow", exact: true }).waitFor();
await shot("approval");
await page.getByRole("button", { name: "Allow", exact: true }).click();
await page.getByText("Echo: write a.txt").waitFor();

step("question");
const prompt = page.getByPlaceholder(/Prompt|Queue a follow-up/);
await prompt.fill("question");
await prompt.press("Enter");
await page.getByRole("button", { name: "Blue" }).click();
await shot("question");
await page.getByRole("button", { name: "Submit" }).click();
await page.getByText(/answer:/).first().waitFor();

step("picture and screenshot");
await prompt.fill("picture and screenshot");
await prompt.press("Enter");
await page.locator(".reply-image img").first().waitFor();
await page.locator(".tool-call .remote-image img").first().waitFor();
await shot("images");

step("upload an image");
const dataURL = await page.evaluate(() => {
  const c = document.createElement("canvas");
  c.width = 300;
  c.height = 200;
  const g = c.getContext("2d");
  g.fillStyle = "#8caaee";
  g.fillRect(0, 0, 300, 200);
  g.fillStyle = "#e78284";
  g.fillRect(40, 40, 120, 80);
  return c.toDataURL("image/png");
});
const png = Buffer.from(dataURL.split(",")[1], "base64");
await page.locator("input[type=file]").first().setInputFiles({ name: "shot.png", mimeType: "image/png", buffer: png });
await page.locator(".attachment img").waitFor();
await page.waitForFunction(() => !document.querySelector(".attachment .spinner"));
await prompt.fill("look at this");
await shot("attachment");
await prompt.press("Enter");
await page.getByText(/Echo \(1 image\(s\)\)/).waitFor();

step("slow, then stop");
await prompt.fill("slow slow slow slow slow slow slow slow slow slow slow slow slow slow");
await prompt.press("Enter");
await page.getByRole("button", { name: "Stop", exact: true }).waitFor();
await shot("running");
await page.getByRole("button", { name: "Stop", exact: true }).click();
await page.getByText("Interrupted").last().waitFor();

step("fail");
await prompt.fill("fail");
await prompt.press("Enter");
await page.getByText("Failed", { exact: true }).last().waitFor();

step("/compact");
await page.getByRole("button", { name: "Session", exact: true }).click();
await shot("menu");
await page.getByRole("menuitem", { name: /\/compact/ }).click();
await page.getByText(/compacted/i).waitFor();

step("expand a tool call");
await page.locator(".tool-header").first().click();
await shot("transcript");

step("changes");
await page.getByRole("button", { name: "Session", exact: true }).click();
await page.getByRole("menuitem", { name: "Changes" }).click();
await page.getByText(/file/).first().waitFor();
await page.waitForTimeout(500);
await shot("changes");
const fileRow = page.locator(".file-row").first();
if (await fileRow.count()) {
  await fileRow.click();
  await page.locator(".file-diff").waitFor();
  await shot("file-diff");
}
await page.keyboard.press("Escape");

step("worktree session: changes and revert");
await page.getByRole("button", { name: "New session" }).first().click();
await page.locator(".harness-row", { hasText: "Fake" }).click();
await page.getByLabel("Isolated git worktree").check();
await page.getByPlaceholder("What should the agent do?").fill("write b.txt");
await page.getByRole("button", { name: "Start" }).click();
await page.getByRole("button", { name: "Allow", exact: true }).click();
await page.getByText("Echo: write b.txt").waitFor();
await page.getByRole("button", { name: "Session", exact: true }).click();
await page.getByRole("menuitem", { name: "Changes" }).click();
await page.locator(".file-row", { hasText: "b.txt" }).waitFor();
await shot("worktree-changes");
await page.locator(".file-row", { hasText: "b.txt" }).click();
await page.locator(".diff-line.add").first().waitFor();
await shot("worktree-file-diff");
await page.getByRole("button", { name: "Changes" }).click();
await page.getByRole("button", { name: /Revert files/ }).click();
await page.getByRole("menuitem", { name: "Before turn 1" }).click();
await page.getByRole("button", { name: "Revert to before turn 1" }).click();
await page.locator(".form-message", { hasText: "Reverted 1 file" }).waitFor();
await shot("worktree-reverted");
await page.getByRole("button", { name: "Done" }).click();

step("settings and light theme");
await page.locator(".server-menu-button").click();
await page.getByRole("menuitem", { name: "Settings" }).click();
await page.locator("select").first().selectOption("light");
await shot("settings-light");
await page.getByRole("button", { name: "Done" }).click();
await shot("light");

step("phone");
await page.setViewportSize({ width: 390, height: 844 });
await shot("phone-session");
await page.getByRole("button", { name: "Back" }).click();
await shot("phone-sessions");

step("archive");
await page.setViewportSize({ width: 1280, height: 820 });
await page.locator(".session-row").first().hover();
await page.getByRole("button", { name: "Archive" }).first().click();
await page.getByRole("button", { name: "Archive", exact: true }).last().click();
await page.getByText(/Archived/).waitFor();

step("remove server");
await page.locator(".server-menu-button").click();
await page.getByRole("menuitem", { name: /Remove/ }).click();
await page.getByRole("button", { name: "Remove", exact: true }).click();
await page.getByText("No servers").waitFor();
await shot("removed");

await browser.close();
if (errors.length) {
  console.error("page errors:\n" + errors.join("\n"));
  process.exit(1);
}
console.log("ok");
