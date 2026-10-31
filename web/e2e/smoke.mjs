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
let lookupMode = "normal";
let lookupCount = 0;
let offline = false;
let liveSocket;
await page.routeWebSocket("**/v1/ws", (socket) => {
  if (offline) { void socket.close({ code: 1001, reason: "offline test" }); return; }
  liveSocket = socket;
  const server = socket.connectToServer();
  socket.onMessage((message) => {
    const request = JSON.parse(String(message));
    if (request.method === "session.commands") {
      lookupCount++;
      if (lookupMode === "pending") return;
      if (lookupMode === "missing") {
        socket.send(JSON.stringify({ id: request.id, error: { code: "method_not_found", message: "old server" } }));
        return;
      }
    }
    server.send(message);
  });
});
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));
page.on("console", (m) => {
  // A reconnect's valid-token authorization probe deliberately requests a missing image.
  if (m.text().includes("404") && m.location().url.endsWith("/v1/images/check")) return;
  if (m.type() === "error") errors.push(`${m.text()} (${m.location().url})`);
});
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
await page.getByRole("button", { name: "New session", exact: true }).first().waitFor();
await page.waitForFunction(() => !document.querySelector("button[aria-label='New session']:disabled"));
await shot("paired");

step("new session");
await page.getByRole("button", { name: "New session", exact: true }).first().click();
await page.getByRole("button", { name: "Add a folder…" }).click();
await page.locator(".folder-row").first().click(); // the root
await page.locator(".folder-row", { hasText: new RegExp(`^${project}`) }).first().click();
await page.getByRole("button", { name: "Use folder" }).click();
await page.locator(".harness-row", { hasText: "Fake" }).click();
await page.getByPlaceholder("What should the agent do?").fill("write a.txt");
await shot("new-session");
await page.getByRole("button", { name: "Start", exact: true }).click();

step("approval");
await page.getByRole("button", { name: "Allow", exact: true }).waitFor();
await shot("approval");
await page.getByRole("button", { name: "Allow", exact: true }).click();
await page.getByText("Echo: write a.txt").waitFor();

step("question");
const prompt = page.getByPlaceholder(/Prompt|Queue a follow-up/);
step("skill suggestions");
await prompt.fill("/prot");
const skillOption = page.getByRole("option", { name: /skill:prototype/ });
await skillOption.waitFor();
await shot("skill-suggestions");
await prompt.fill("/");
const suggestions = page.getByRole("listbox", { name: "Command suggestions" });
await page.getByRole("option", { name: /skill:example-12/ }).waitFor();
await suggestions.evaluate((el) => { el.scrollTop = el.scrollHeight; });
await prompt.fill("/ask");
await page.waitForFunction(() => {
  const list = document.getElementById("command-suggestions");
  const first = list?.querySelector("[role=option]");
  if (!list || !first?.textContent.includes("skill:ask-guide")) return false;
  const row = first.getBoundingClientRect(), panel = list.getBoundingClientRect();
  return row.top >= panel.top && row.bottom <= panel.bottom;
});
await prompt.fill("/prot");
await skillOption.click();
if (await prompt.inputValue() !== "/skill:prototype ") throw new Error("skill selection lost the draft");
if (!await prompt.evaluate((el) => el === document.activeElement)) throw new Error("skill selection lost focus");
await prompt.fill("/co");
await page.getByRole("option", { name: /compact/ }).waitFor();
await prompt.press("Enter");
if (await prompt.inputValue() !== "/compact ") throw new Error("Enter sent instead of completing");
await prompt.fill("/prot");
await skillOption.waitFor();
await prompt.press("Escape");
if (await page.getByRole("listbox", { name: "Command suggestions" }).count()) throw new Error("Escape did not dismiss suggestions");
await prompt.fill("/prototype arguments");
if (await page.getByRole("listbox", { name: "Command suggestions" }).count()) throw new Error("suggestions replaced arguments");
step("exact commands, focused discovery and compatibility");
await prompt.fill("/skill:prototype");
await skillOption.waitFor();
await prompt.press("Enter");
await page.getByText("Echo: /skill:prototype", { exact: true }).waitFor();
if (await prompt.inputValue() !== "") throw new Error("exact command did not send on Enter");
await prompt.fill("/prot");
await skillOption.waitFor();
await prompt.blur();
const beforeReload = lookupCount;
await page.reload();
await prompt.waitFor();
await page.waitForTimeout(300);
if (lookupCount !== beforeReload) throw new Error("saved slash draft fetched without focus");
await prompt.focus();
await skillOption.waitFor();
await prompt.fill("ordinary text");
lookupMode = "missing";
await prompt.fill("/");
await page.getByRole("option", { name: /compact/ }).waitFor();
await page.getByText("Loading skills…", { exact: true }).waitFor({ state: "hidden" });
if (await page.locator(".command-discovery-status").count()) throw new Error("old server showed a discovery error");
await prompt.fill("ordinary text");
lookupMode = "pending";
await prompt.fill("/");
await page.getByText("Loading skills…", { exact: true }).waitFor();
offline = true;
await liveSocket.close({ code: 1001, reason: "offline test" });
await page.getByText("Loading skills…", { exact: true }).waitFor({ state: "hidden" });
offline = false;
lookupMode = "normal";
await page.reload();
await prompt.waitFor();
await prompt.fill("question");
await prompt.press("Enter");
await page.getByRole("button", { name: "Blue" }).click();
await shot("question");
// The last choice is an answer in the user's own words, which replaces Blue.
await page.getByPlaceholder("Other…").fill("teal, not green");
if (await page.getByRole("button", { name: "Blue" }).getAttribute("aria-pressed") !== "false") throw new Error("the own answer kept Blue");
await shot("question-own-answer");
await page.getByRole("button", { name: "Submit" }).click();
await page.getByText("Which color? → teal, not green").waitFor();

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

step("terminal");
const terminalRows = (text) => page.locator(".terminal .xterm-rows", { hasText: text });
await page.getByRole("button", { name: "Session", exact: true }).click();
await page.getByRole("menuitem", { name: "Terminal" }).click();
await page.locator(".terminal .xterm").waitFor();
await page.waitForTimeout(1000); // the shell starting
await page.keyboard.type("echo $((6*7))");
await page.keyboard.press("Enter");
await terminalRows("42").waitFor();
await page.keyboard.press("Escape"); // the shell's, not the page's
await shot("terminal-over");
// Hidden and shown again, the same shell, with its output.
await page.getByRole("button", { name: "Hide Terminal" }).click();
await page.getByText("Echo: write a.txt").waitFor();
await page.getByRole("button", { name: "Session", exact: true }).click();
await page.getByRole("menuitem", { name: "Terminal" }).click();
await terminalRows("42").waitFor();
// ⌘J hides it and gives the prompt the keyboard; Ctrl+` there shows it
// again. In the terminal, Ctrl+J is the shell's.
await page.locator(".terminal textarea:focus").waitFor();
await page.keyboard.press("Meta+j");
await page.locator(".composer-field:focus").waitFor();
await page.keyboard.press("Control+Backquote");
await page.locator(".terminal textarea:focus").waitFor();
await page.keyboard.press("Control+j");
await page.waitForTimeout(300);
await terminalRows("42").waitFor({ timeout: 1000 });
for (const placement of ["below", "right"]) {
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await page.getByLabel("Opens").selectOption(placement);
  await page.getByRole("button", { name: "Done" }).click();
  await page.locator(`.terminal-${placement} .terminal .xterm-rows`, { hasText: "42" }).waitFor();
  await shot("terminal-" + placement);
}
await page.getByRole("button", { name: "Terminal", exact: true }).click();
await page.getByRole("menuitem", { name: "Close Shell" }).click();
await page.getByText("The shell was closed.").waitFor();
await page.getByRole("button", { name: "Hide Terminal" }).click();

step("worktree session: changes and revert");
await page.getByRole("button", { name: "New session", exact: true }).first().click();
await page.locator(".harness-row", { hasText: "Fake" }).click();
await page.getByLabel("Isolated git worktree").check();
await page.getByPlaceholder("What should the agent do?").fill("write b.txt");
await page.getByRole("button", { name: "Start", exact: true }).click();
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
step("commit the worktree's changes, from the Changes view");
await page.getByRole("button", { name: "Commit…" }).click();
await page.getByLabel("Commit message").waitFor();
await page.locator(".commit-file", { hasText: "b.txt" }).waitFor();
// Only a commit: the project may have a remote, which this leaves alone.
await page.getByRole("radio", { name: "Commit only" }).click();
await shot("commit");
await page.getByRole("button", { name: "Commit", exact: true }).click();
await page.locator(".task-status", { hasText: "Done." }).waitFor();
await shot("committed");
await page.getByRole("button", { name: "Done" }).click();
await page.getByText(/^Committed [0-9a-f]{7} /).waitFor();
await page.getByRole("button", { name: "Session", exact: true }).click();
await page.getByRole("menuitem", { name: "Changes" }).click();
await page.locator(".file-row", { hasText: "b.txt" }).waitFor();
await page.getByRole("button", { name: /Revert files/ }).click();
await page.getByRole("menuitem", { name: "Before turn 1" }).click();
await page.getByRole("button", { name: "Revert to before turn 1" }).click();
await page.locator(".form-message", { hasText: "Reverted 1 file" }).waitFor();
await shot("worktree-reverted");
await page.getByRole("button", { name: "Done" }).click();

step("empty session: its prompt takes the keyboard");
await page.getByRole("button", { name: "New session", exact: true }).first().click();
await page.locator(".harness-row", { hasText: "Fake" }).click();
await page.getByRole("button", { name: "Start", exact: true }).click();
await page.getByText("No prompts yet").waitFor();
await page.keyboard.type("hi");
if ((await prompt.inputValue()) !== "hi") throw new Error("typing in a session started empty doesn't reach its prompt");
await shot("empty-session");
await prompt.fill("");

step("draft: saved from New Session, edited, saved from the composer, sent");
const draftText = "keep this for later";
const draftBubble = page.locator(".user-message.draft");
const draftIcon = page.getByRole("img", { name: "Draft" });
const isFocused = (l) => l.evaluate((el) => el === document.activeElement);
await page.getByRole("button", { name: "New session", exact: true }).first().click();
await page.locator(".harness-row", { hasText: "Fake" }).click();
const sheet = page.locator("dialog.modal");
const sheetSave = sheet.getByRole("button", { name: "Save as Draft" });
if (!(await sheetSave.isDisabled())) throw new Error("Save as Draft is offered without a prompt");
await sheet.getByPlaceholder("What should the agent do?").fill(draftText);
await sheet.locator("input[type=file]").setInputFiles({ name: "draft.png", mimeType: "image/png", buffer: png });
await sheet.locator(".attachment img").waitFor();
await page.waitForFunction(() => !document.querySelector("dialog .attachment .spinner"));
await sheetSave.scrollIntoViewIfNeeded();
await shot("new-session-draft");
await page.emulateMedia({ colorScheme: "light" });
await shot("new-session-draft-light");
await page.emulateMedia({ colorScheme: "dark" });
await sheetSave.click();
// The session opens with the draft at its foot; nothing runs, and its
// prompt doesn't take the keyboard.
await draftBubble.locator(".remote-image img").waitFor();
await draftBubble.locator(".bubble", { hasText: draftText }).waitFor();
await page.locator(".session-heading h1", { hasText: draftText }).waitFor();
await page.waitForTimeout(500);
if (await page.getByText("Working…").count()) throw new Error("a draft started a turn");
if (await page.getByText(/^Echo/).filter({ hasText: draftText }).count()) throw new Error("a draft was sent");
if (await page.getByText("No prompts yet").count()) throw new Error("a session with a draft says it has no prompts");
if (await isFocused(prompt)) throw new Error("a session saved as a draft focused its prompt");
if (await page.getByRole("button", { name: "Save as Draft" }).count()) throw new Error("the composer offers a second draft");
await draftIcon.waitFor(); // its row has the pencil
await shot("draft");
await page.emulateMedia({ colorScheme: "light" });
await shot("draft-light");
await page.emulateMedia({ colorScheme: "dark" });
await page.setViewportSize({ width: 390, height: 844 });
await shot("phone-draft");
await page.getByRole("button", { name: "Back" }).click();
const draftRow = page.locator(".session-row", { has: page.locator(".status-icon[title=draft]") });
await draftRow.locator(".session-title", { hasText: draftText }).waitFor();
await shot("phone-draft-row");
await draftRow.locator(".session-row-main").click();
await page.setViewportSize({ width: 1280, height: 820 });

// Edit takes it back into the composer, images and all.
await draftBubble.getByRole("button", { name: "Edit" }).click();
await draftBubble.waitFor({ state: "detached" });
if ((await prompt.inputValue()) !== draftText) throw new Error("Edit didn't put the draft in the composer");
await page.locator(".composer .attachment img").waitFor();
if (!(await isFocused(prompt))) throw new Error("Edit didn't focus the prompt");
await draftIcon.waitFor({ state: "detached" });
await page.getByText("No prompts yet").waitFor();
const composerSave = page.locator(".composer").getByRole("button", { name: "Save as Draft" });
await composerSave.waitFor();
await shot("draft-edit");
await page.emulateMedia({ colorScheme: "light" });
await shot("draft-edit-light");
await page.emulateMedia({ colorScheme: "dark" });

// Ctrl+S saves it again from the composer, which empties.
await page.keyboard.press("Control+s");
await draftBubble.locator(".remote-image img").waitFor();
if ((await prompt.inputValue()) !== "") throw new Error("saving a draft left its text in the composer");
if (await page.locator(".composer .attachment").count()) throw new Error("saving a draft left its images in the composer");
await draftIcon.waitFor();

// And so does the button, at a phone's width too. (A view of the other
// width is a new one, without the images attached in this one.)
await page.setViewportSize({ width: 390, height: 844 });
await draftBubble.getByRole("button", { name: "Edit" }).click();
await page.locator(".composer .attachment img").waitFor();
await shot("phone-draft-edit");
await composerSave.click();
await draftBubble.locator(".remote-image img").waitFor();
await shot("phone-draft-saved");
await page.setViewportSize({ width: 1280, height: 820 });

// Send runs it, and it becomes a prompt like any other.
await draftBubble.getByRole("button", { name: "Send" }).click();
await page.getByText(`Echo (1 image(s)): ${draftText}`).waitFor();
await page.locator(".user-message:not(.draft) .bubble", { hasText: draftText }).waitFor();
if (await draftBubble.count()) throw new Error("a sent draft is still shown as one");
await draftIcon.waitFor({ state: "detached" });
// Drafts are for the first prompt only.
await prompt.fill("one more");
if (await composerSave.count()) throw new Error("the composer offers a draft in a session with prompts");
await prompt.fill("");
await shot("draft-sent");

step("search sessions, and open a result at the item that matched");
// A session whose match is far above its end.
await page.getByRole("button", { name: "New session", exact: true }).first().click();
await page.locator(".form-row", { hasText: "Project" }).locator("select").selectOption({ label: project });
await page.locator(".harness-row", { hasText: "Fake" }).click();
await page.getByPlaceholder("What should the agent do?").fill("zebrafish swim upstream");
await page.getByRole("button", { name: "Start", exact: true }).click();
await page.getByText("Echo: zebrafish swim upstream").waitFor();
for (let k = 1; k <= 10; k++) {
  await prompt.fill(`filler ${k}`);
  await prompt.press("Enter");
  await page.getByText(`Echo: filler ${k}`, { exact: true }).waitFor();
}
await page.locator(".session-row .row-title", { hasText: "write a.txt" }).first().click();
await page.getByText("Echo: write a.txt").waitFor();
// Ctrl+Shift+F focuses the sidebar's search.
const searchField = page.getByPlaceholder("Search sessions");
await page.keyboard.press("Control+Shift+F");
await page.locator(".sidebar-search input:focus").waitFor();
await page.keyboard.type("zebrafish");
const searchResult = page.locator(".search-result", { hasText: "zebrafish swim upstream" });
await searchResult.locator(".snippet-match", { hasText: /zebrafish/i }).first().waitFor();
await page.getByText("Include archived").waitFor();
await shot("search-results");
await searchResult.locator(".session-row-main").click();
await page.locator(".session-heading h1", { hasText: "zebrafish swim upstream" }).waitFor();
// It flashes the item, scrolled into view rather than to the end.
const flashed = await page.waitForFunction(() => {
  const a = document.getAnimations().find((x) => x.effect?.pseudoElement === "::after" && x.effect.target?.dataset?.itemId);
  return a ? a.effect.target.dataset.itemId : false;
});
const flashedID = await flashed.jsonValue();
const shownAtItem = await page.evaluate((id) => {
  const el = document.querySelector(`[data-item-id="${CSS.escape(id)}"]`);
  const sc = document.querySelector(".transcript-scroll");
  const item = el.getBoundingClientRect(), view = sc.getBoundingClientRect();
  return { text: el.textContent, inView: item.top >= view.top - 1 && item.bottom <= view.bottom + 1, atEnd: sc.scrollHeight - sc.scrollTop - sc.clientHeight < 40 };
}, flashedID);
if (!/zebrafish/i.test(shownAtItem.text)) throw new Error(`the result opened at an item without the match: ${shownAtItem.text}`);
if (!shownAtItem.inView || shownAtItem.atEnd) throw new Error("the result's item isn't in view, or the session opened at its end");
// The results stay, with the session selected.
await searchResult.locator(".session-row-main[aria-current=page]").waitFor();
await shot("search-opened");

step("find in the session");
await page.getByRole("button", { name: "Session", exact: true }).click();
await page.getByRole("menuitem", { name: "Find in Session" }).click();
const findField = page.getByPlaceholder("Find in session");
await page.locator(".find-bar input:focus").waitFor();
await findField.fill("filler");
const findCount = page.locator(".find-count");
// The current match is the newest.
await page.waitForFunction(() => /^(\d+) of \1$/.test(document.querySelector(".find-count")?.textContent ?? ""));
const findTotal = Number((await findCount.textContent()).split(" of ")[1]);
if (findTotal < 10) throw new Error(`find found ${findTotal} items with "filler"`);
if ((await page.locator(".item-row.find-match").count()) !== findTotal - 1 || (await page.locator(".item-row.find-current").count()) !== 1) {
  throw new Error("find's tints don't match its count");
}
// The words themselves are marked too, in Chromium, which has the Custom Highlight API.
await page.waitForFunction(() => CSS.highlights.get("find-current")?.size > 0, undefined, { timeout: 5000 });
await shot("find");
// Return goes on, round to the first; Shift+Return back.
await findField.press("Enter");
await findCount.filter({ hasText: new RegExp(`^1 of ${findTotal}$`) }).waitFor();
await findField.press("Shift+Enter");
await findCount.filter({ hasText: new RegExp(`^${findTotal} of ${findTotal}$`) }).waitFor();
await findField.fill("no such words here");
await findCount.filter({ hasText: "No matches" }).waitFor();
await findField.press("Escape");
await page.locator(".find-bar").waitFor({ state: "detached" });
if (await page.locator(".item-row.find-match, .item-row.find-current").count()) throw new Error("closing find left its tints");
// Ctrl+F opens it again, in place of the browser's.
await prompt.focus();
await page.keyboard.press("Control+f");
await page.locator(".find-bar input:focus").waitFor();
await page.locator(".find-bar").getByRole("button", { name: "Done" }).click();
await page.locator(".find-bar").waitFor({ state: "detached" });

step("phone: search in the Inbox tab, and back to the results");
await page.setViewportSize({ width: 390, height: 844 });
await page.evaluate(() => {
  location.hash = "#/";
});
await page.locator(".bar-title", { hasText: "Inbox" }).waitFor();
if ((await searchField.inputValue()) !== "zebrafish") throw new Error("the Inbox tab lost the search");
await searchResult.locator(".session-row-main").click();
await page.locator(".session-heading h1", { hasText: "zebrafish swim upstream" }).waitFor();
await shot("phone-search-opened");
await page.getByRole("button", { name: "Back" }).click();
await searchResult.waitFor();
await shot("phone-search");
// Esc clears it, back to the Inbox's rows.
await searchField.press("Escape");
await searchResult.waitFor({ state: "detached" });
await page.locator(".list-header", { hasText: "Recent" }).waitFor();
await page.setViewportSize({ width: 1280, height: 820 });

step("continue a terminal session");
const continueTerminal = async (name) => {
  await page.getByRole("button", { name: "New session", exact: true }).first().click();
  await page.locator(".form-row", { hasText: "Project" }).locator("select").selectOption({ label: project });
  await page.locator(".harness-row", { hasText: "Fake" }).click();
  if (name) {
    await page.getByRole("button", { name: "Continue a terminal session…" }).scrollIntoViewIfNeeded();
    await shot(name);
  }
  await page.getByRole("button", { name: "Continue a terminal session…" }).click();
  await page.getByRole("heading", { name: "Terminal sessions" }).waitFor();
};
const terminalRow = (title) => page.locator(".import-row", { hasText: title });
const activeWarning = "Active in the last minute: if it still runs in a terminal, quit it there first.";
await continueTerminal("new-session-continue");
await terminalRow("Fix the login redirect").waitFor();
await terminalRow("Add a dark theme to the settings page").waitFor();
// Only the one written just now warns that it may still run.
await terminalRow("Add a dark theme to the settings page").getByText(activeWarning).waitFor();
if (await terminalRow("Fix the login redirect").getByText(activeWarning).count()) throw new Error("a session from two hours ago warns that it's active");
await terminalRow("Fix the login redirect").getByText(/^4 messages · /).waitFor();
await page.getByText("Picks up a session Fake (scripted) ran in this folder outside Remote Agent.").waitFor();
await shot("import-list");
await page.emulateMedia({ colorScheme: "light" });
await shot("import-list-light");
await page.emulateMedia({ colorScheme: "dark" });
await page.setViewportSize({ width: 390, height: 844 });
await shot("phone-import-list");
await page.setViewportSize({ width: 1280, height: 820 });
// It opens with the agent's transcript and a notice, and goes on from there.
await terminalRow("Fix the login redirect").click();
await page.locator(".session-heading h1", { hasText: "Fix the login redirect" }).waitFor();
await page.getByText("Done in the terminal: add a test for it.").waitFor();
await page.locator(".notice", { hasText: "Picked up from Fake (scripted)" }).waitFor();
if (await page.getByRole("heading", { name: "Terminal sessions" }).count()) throw new Error("the list stayed open after the import");
if (!(await isFocused(prompt))) throw new Error("a session picked up from the terminal didn't focus its prompt");
await shot("imported");
await prompt.fill("carry on");
await prompt.press("Enter");
await page.getByText(/^Picked up terminal-1-[0-9a-f]+\. Echo: carry on/).waitFor();
await shot("imported-continued");
await page.emulateMedia({ colorScheme: "light" });
await shot("imported-light");
await page.emulateMedia({ colorScheme: "dark" });
await page.setViewportSize({ width: 390, height: 844 });
await shot("phone-imported");
await page.setViewportSize({ width: 1280, height: 820 });
// Once picked up, it's no longer offered.
await continueTerminal();
await terminalRow("Add a dark theme to the settings page").waitFor();
if (await terminalRow("Fix the login redirect").count()) throw new Error("a session picked up already is offered again");
await page.getByRole("button", { name: "Back" }).click();
await page.getByRole("button", { name: "Continue a terminal session…" }).waitFor();
await page.getByRole("button", { name: "Cancel" }).click();

step("settings and light theme");
await page.getByRole("button", { name: "Settings", exact: true }).click();
await page.locator("select").first().selectOption("light");
await shot("settings-light");
await page.getByRole("button", { name: "Done" }).click();
await shot("light");

step("phone: the Servers and Inbox tabs, each keeping its place");
await page.setViewportSize({ width: 390, height: 844 });
await shot("phone-session");
const tabBar = page.locator(".tab-bar");
const tab = (name) => tabBar.getByRole("button", { name: new RegExp(`^${name}`) });
// A session a wide window opened is the Servers tab's: back is its server's sessions.
await page.getByRole("button", { name: "Back" }).click();
const serverSessions = page.locator("header.bar").getByRole("button", { name: "Servers", exact: true });
await serverSessions.waitFor();
if ((await tab("Servers").getAttribute("aria-current")) !== "page") throw new Error("a server's sessions aren't in the Servers tab");
await shot("phone-sessions");
await tab("Inbox").click();
await page.locator(".bar-title", { hasText: "Inbox" }).waitFor();
await page.locator(".list-header", { hasText: "Recent" }).waitFor();
if (await page.locator(".list-header", { hasText: "Servers" }).count()) throw new Error("the Inbox tab lists the servers");
await shot("phone-inbox");
await page.locator(".session-row .row-title", { hasText: "write a.txt" }).first().click();
await page.getByText("Echo: write a.txt").waitFor();
if (await tabBar.count()) throw new Error("the tab bar shows over a session");
await page.getByRole("button", { name: "Back" }).click();
await page.locator(".bar-title", { hasText: "Inbox" }).waitFor();
await tab("Servers").click();
await serverSessions.waitFor(); // where the tab was
await tab("Servers").click(); // and again, its root
await page.locator(".bar-title", { hasText: "Servers" }).waitFor();
await shot("phone-servers");
await tab("Inbox").click();
await page.locator(".bar-title", { hasText: "Inbox" }).waitFor();

step("sidebar layouts");
await page.setViewportSize({ width: 1280, height: 820 });
const layout = async (name) => {
  await page.getByRole("button", { name: "Settings", exact: true }).first().click();
  await page.getByLabel("Layout").selectOption(name);
  await page.getByRole("button", { name: "Done" }).click();
};
await page.locator(".list-header", { hasText: "Servers" }).waitFor(); // the Inbox, the default
await layout("columns");
await page.locator(".session-column .session-row").first().waitFor();
await page.locator(".collection-row", { hasText: project }).click();
await page.locator(".session-column .bar-title", { hasText: project }).waitFor();
await shot("layout-columns");
// A project's collection, and a project's +, start a new session in that
// project. Forgetting the project used last leaves only them to choose it.
const startsInProject = async (what) => {
  const chosen = await page.locator(".form-row", { hasText: "Project" }).locator("select").evaluate((s) => s.selectedOptions[0]?.textContent);
  if (chosen !== project) throw new Error(`${what} starts a session in ${chosen}, not ${project}`);
  await page.getByRole("button", { name: "Cancel" }).click();
};
await page.evaluate(() => localStorage.removeItem("lastProject"));
await page.locator(".session-column").getByRole("button", { name: "New session", exact: true }).click();
await startsInProject("the project's collection");
await page.locator(".collection-row", { hasText: project }).hover();
await page.getByRole("button", { name: `New session in ${project}` }).click();
await startsInProject("the collection's +");
await layout("projects");
await page.locator(".project-row", { hasText: project }).waitFor();
await shot("layout-projects");
await page.locator(".project-row", { hasText: project }).hover();
await shot("project-plus");
await page.getByRole("button", { name: `New session in ${project}` }).click();
await startsInProject("the project's +");

step("forget a project");
await page.getByRole("button", { name: "New session", exact: true }).first().click();
const projectPicker = page.locator(".form-row", { hasText: "Project" }).locator("select");
await projectPicker.selectOption({ label: project });
// The server keeps a project while it has sessions that aren't archived.
await page.getByRole("button", { name: `Forget ${project}` }).click();
await page.getByText(/archive them first/).waitFor();
await shot("forget-refused");
const addRoot = async () => {
  await page.getByRole("button", { name: "Add a folder…" }).click();
  await page.locator(".folder-row").first().click(); // the root
  await page.getByRole("button", { name: "Use folder" }).click();
  // The folder added is chosen once the server answers.
  const added = await page.waitForFunction((p) => {
    const name = document.querySelector(".form-row select").selectedOptions[0]?.textContent;
    return name !== p && name !== "Choose…" && name;
  }, project);
  return added.jsonValue();
};
const root = await addRoot();
// The Projects layout shows it without sessions, until it's forgotten.
const rootRow = page.locator(".project-row", { hasText: root });
await rootRow.waitFor();
if ((await rootRow.locator(".row-plus-count").textContent()) !== "0") throw new Error(`${root} doesn't show that it has no sessions`);
await shot("project-without-sessions");
await page.getByRole("button", { name: `Forget ${root}` }).click();
await page.waitForFunction((name) => {
  const s = document.querySelector(".form-row select");
  return s.value === "" && ![...s.options].some((o) => o.textContent === name);
}, root);
await rootRow.waitFor({ state: "detached" });
await addRoot();
await page.getByRole("button", { name: "Cancel" }).click();
// From its menu in the sidebar too. In three columns, forgetting the chosen
// project shows All Sessions.
await layout("columns");
const projectMenu = async (name) => {
  await page.locator(".collection-row", { hasText: name }).hover();
  await page.getByRole("button", { name: `${name} actions` }).click();
};
await projectMenu(project);
if (!(await page.getByRole("menuitem", { name: "Forget project" }).isDisabled())) throw new Error(`${project} can be forgotten with its sessions`);
await page.keyboard.press("Escape");
await page.locator(".collection-row", { hasText: root }).click();
await projectMenu(root);
await shot("project-menu");
await page.getByRole("menuitem", { name: "Forget project" }).click();
await page.locator(".collection-row", { hasText: root }).waitFor({ state: "detached" });
await page.locator(".session-column .bar-title", { hasText: "All Sessions" }).waitFor();
await layout("projects");

step("collapse the server, which a reload keeps");
const header = page.locator(".server-header-main");
await header.click();
await page.locator(".server-header-main[aria-expanded=false]").waitFor();
await shot("collapsed");
await page.reload();
await page.locator(".server-header-main[aria-expanded=false]").waitFor();
await header.click();
await page.locator(".session-row").first().waitFor();

step("archive");
await page.locator(".session-row").first().hover();
await page.getByRole("button", { name: "Archive" }).first().click();
await page.getByRole("button", { name: "Archive", exact: true }).last().click();
await page.getByText(/Archived/).waitFor();

step("server settings: update and sign in the fake agent, and name sessions with it");
const fake = "Fake (scripted)";
const openServerSettings = async () => {
  await page.locator(".server-header").hover();
  await page.getByRole("button", { name: / actions$/ }).click();
  await page.getByRole("menuitem", { name: "Server Settings…" }).click();
  await page.locator(".modal-subtitle", { hasText: "Server" }).waitFor();
  await page.waitForFunction(() => !document.body.innerText.includes("Checking for updates…"));
};
const fakeMenu = async (item) => {
  await page.getByRole("button", { name: `${fake} actions` }).click();
  await page.getByRole("menuitem", { name: item, exact: true }).click();
};
const closeTask = () => page.getByRole("button", { name: "Done" }).last().click();
const systemAgent = page.getByLabel(/^Agent/);
await openServerSettings();
await page.getByText("rad dev is a development build, which doesn't update itself.").waitFor();
await page.locator(".harness-row", { hasText: fake }).getByText("Ready").waitFor();
await shot("server-settings");
await fakeMenu("Update");
await page.getByRole("heading", { name: `Updating ${fake}` }).waitFor();
await page.locator(".task-status", { hasText: `${fake} 1` }).waitFor();
await shot("server-task-updated");
await closeTask();
await fakeMenu("Sign In Again");
await page.getByText("Waiting for you to sign in…").waitFor();
await page.getByPlaceholder("Paste the code here").waitFor();
await shot("server-task-sign-in");
// The sign-in page opens in a tab of its own, without the network.
await context.route("https://example.com/**", (r) => r.fulfill({ body: "<h1>Sign in</h1>", contentType: "text/html" }));
const [signInPage] = await Promise.all([context.waitForEvent("page"), page.getByRole("button", { name: "Open Sign-In Page" }).click()]);
if (signInPage.url() !== "https://example.com/fake-login?session=42") throw new Error("Open Sign-In Page opened " + signInPage.url());
await signInPage.close();
await page.getByPlaceholder("Paste the code here").fill("code42");
await page.getByRole("button", { name: "Send", exact: true }).click();
await page.locator(".task-status", { hasText: "Signed in." }).waitFor();
await shot("server-task-signed-in");
await closeTask();
await systemAgent.selectOption({ label: fake });
await page.getByLabel(/^Model/).waitFor();
await page.getByRole("button", { name: "Done" }).click();
await page.getByRole("button", { name: "New session", exact: true }).first().click();
await page.locator(".form-row", { hasText: "Project" }).locator("select").selectOption({ label: project });
await page.locator(".harness-row", { hasText: "Fake" }).click();
await page.getByPlaceholder("What should the agent do?").fill("tidy up the readme please");
await page.getByRole("button", { name: "Start", exact: true }).click();
await page.locator(".session-heading h1", { hasText: "Named: tidy up the readme" }).waitFor();
await shot("named-session");
// Back to no system agent.
await openServerSettings();
await systemAgent.selectOption({ label: "None" });
await page.getByLabel(/^Model/).waitFor({ state: "detached" });
await page.getByRole("button", { name: "Done" }).click();

step("remove server");
await page.locator(".server-header").hover();
await page.getByRole("button", { name: / actions$/ }).click();
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
