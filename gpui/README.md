# Remote Agent for macOS, in GPUI (experimental)

A Mac client for rad written in Rust with [GPUI](https://gpui.rs), Zed's GPU-accelerated UI framework, and [GPUI Kit](https://gpui-kit.com)'s components. It's an experiment beside the SwiftUI Mac app in `apple/`, not a replacement for it.

## What it does

It does what the SwiftUI Mac client does, with the gaps listed below.

- **Pairing:** paste the link `rad pair --print-url` prints, or pass it on the command line. Settings › Servers reconnects, removes (unpairs) and adds servers.
- **The sidebar:** Inbox (Needs You, Recent, Archived, Servers), Projects (each server's projects with their sessions) or Columns (Needs You, Running, All Sessions, each project and the archive, beside their sessions). View › as Inbox, as Projects, as Columns (⌘1–3). Right-click a session to rename, archive, unarchive or delete it, a server for New Session, Reconnect, Server Settings and Remove, a project for New Session and Forget Project. "Archive old sessions" archives what had nothing new for a week.
- **Search (⇧⌘F):** every connected server's sessions, merged round-robin, with snippets; a result opens its session at the item, which lights up for a moment. **Find in the session (⌘F, ⌘G, ⇧⌘G)** over the transcript, opening the run that holds the match.
- **A session:** its transcript, with runs of tool calls folded into one row ("Ran 4 commands, edited 3 files"); a tool call opens to its command, edit, new content or input, and its output (30 lines, then "Show all"). Sub-agents' steps sit in their tool call. Replies are Markdown, with raw HTML kept as text, and their `rad-image:` pictures, the prompts' and the tools' images in a viewer. The header shows the model, effort, permissions and context, the worktree and branch; the foot, what runs in the background and a session's error.
- **The ⋯ menu:** Permissions, Find, Changes, Commit…, Terminal, Rename…, Fork…, Archive…, Delete…, the branch, and Force stop while it runs. The Session menu has them too (⌥⌘C commits).
- **Approvals:** tool approvals (Deny asks for a reason), plans, and questions with their options and an answer in your own words.
- **The composer:** Return sends, Shift-Return starts a new line, ⇧⌘Return steers into the running turn for agents that take it. Slash commands are suggested as you type `/`. Images attach from Files…, ⌘V, Paste Image or a drop, scaled to 2048 px and uploaded at once. Queued prompts have Send now, Steer and Cancel; Stop (⌘.) interrupts. Save as Draft (⌘S) and a session's draft (Edit, Send). Sending to an archived session offers to unarchive it. A prompt you haven't sent stays for the next launch. Dictation, in the app bundle.
- **Changes:** the session's diff or a turn's, file by file, and Revert files… to before a turn in a worktree.
- **Commit:** the preview, the message, Commit, Commit & push, or Commit, push & open PR, and the steps as rad runs them.
- **Fork:** after which turn, the agent and its options, the project folder, a new worktree or this session's.
- **The terminal (⌘J):** the session's shell on the server, instead of the messages, below them or beside them, with copy on select. Hiding it leaves the shell running; it resumes where it left off.
- **New sessions (⌘N):** the project (or a folder added as one, or Forget), the agent with its model, effort and permissions and its usage gauge, the project folder, a new worktree (a branch, its name and base) or an existing one, the prompt with images, Save as Draft, and "Continue a terminal session…" to import one. It starts with the server's default agent, else the one used last.
- **Settings (⌘,):** theme, text sizes, the message font and the code font (replies' code, tool calls, diffs and the terminal), grouping of tool calls, the sidebar layout, where the terminal opens, notifications; and each server's settings: rad's version and update, the agents with their versions, usage limits, Sign In and Update (with the sign-in page and its code), and the default and system agents.
- **Notifications:** when a session needs you, finishes or fails and the window doesn't show it; clicking one opens the session.
- **Unread state, connections, appearance:** a session is marked read while it shows in the window in front; the client reconnects with backoff and resubscribes from where it left off; Catppuccin, Frappé when dark and Latte when light.

Not yet: the Dock badge and bounce, the Photo Library, and app updates (Sparkle). The fonts are installed families only: GPUI loads a font by family name, so the system font's other designs (New York, SF Pro Rounded, SF Mono) aren't offered, and code is Menlo unless you pick another. Notifications reach Notification Center only from the app bundle; a bare build shows them in the window. The terminal draws with the code font; Menlo lacks Nerd Font glyphs, so pick a Nerd Font there for them.

## Building and running

It needs Rust (stable, edition 2024), Xcode's Metal toolchain, which GPUI uses to compile its shaders, and an Apple Development certificate in your login keychain (Xcode › Settings › Accounts › Manage Certificates).

```bash
cd gpui
scripts/bundle.sh                                  # target/release/Remote Agent GPUI.app
cargo run --release                                # or run it from cargo
cargo run --release -- "$(rad pair --print-url)"   # pairs at launch
cargo test
```

- **The app:** `scripts/bundle.sh` builds `target/release/Remote Agent GPUI.app`, with the Mac app's icon, signed. Open it from Finder, or drag it to Applications. Double-click the app, not `target/release/remote-agent-gpui`: Finder opens a bare executable in Terminal. Pass a pairing link with `open "target/release/Remote Agent GPUI.app" --args "$(rad pair --print-url)"`.
- **Signing:** `cargo run` signs the app before it runs it (`.cargo/config.toml` makes `scripts/run-signed.sh` its runner), and `bundle.sh` signs the bundle. Both use your Apple Development identity, or the one `RA_GPUI_SIGN_IDENTITY` names, and the identifier `dev.remote-agent.gpui`, so the bundle and `cargo run`'s builds share their tokens. To run a bare build some other way, sign it first: `scripts/sign.sh target/release/remote-agent-gpui`.
- **Why it's signed:** the device tokens live in your login keychain. macOS lets a new build read, replace and delete the items an earlier build saved without asking only when both are signed with the same Apple-issued certificate (a Team ID). An ad hoc or self-signed build is a new app each time, and macOS asks for your login password. Without an Apple Development identity, `sign.sh` signs ad hoc and says so.
- **Where it keeps servers:** in `~/Library/Application Support/Remote Agent GPUI/servers.json` (0600), without the tokens. The tokens are generic passwords for the service `dev.remote-agent.gpui`, one per server id. A file from before the Keychain held its tokens itself: the first launch moves them into the Keychain.
- **`RA_GPUI_HOME`:** keeps that file in another folder, such as a scratch one for testing. Its tokens get a service of their own, `dev.remote-agent.gpui (<folder>)`, so they never mix with yours.
- **`RA_GPUI_APPEARANCE=light|dark`:** tries a theme without changing the Mac's appearance.

## How it's laid out

| File | What it holds |
|---|---|
| `src/protocol.rs` | The wire types. Enums are open, unknown fields are ignored, and the tests decode `protocol/fixtures`. |
| `src/images.rs` | Uploading and fetching images (`/v1/images`), scaled for agents, and cached on disk and in memory. |
| `src/net.rs` | The socket to one rad. It runs on a tokio runtime of its own, reconnects, and matches responses to requests. The UI gets what it hears over a channel. |
| `src/store/` | The client logic, free of UI and ported from RAKit and ra's `internal/client`: the streams' sync rules (`sync.rs`), the index and sessions, tool runs (with the shared test vectors), the sidebar's layouts and collections, search, find and slash commands (`search.rs`), usage limits (`limits.rs`, with the shared vectors), commit scopes, alerts, the app's prefs (`prefs.rs`), the saved servers, and their tokens in the Keychain (`tokens.rs`; tests use an in-memory store). |
| `scripts/` | `bundle.sh` builds the signed app bundle; `sign.sh` signs a build; `run-signed.sh` is cargo's runner, which signs the app and runs it. |
| `src/conn.rs` | A GPUI entity per server: it applies events to the stores, and subscribes to a session while a view shows it. |
| `src/ui/` | The window (`app.rs`, `sidebar.rs`), the session (`session/`: its state, transcript, composer and header), the terminal (`terminal.rs`, alacritty's emulator on its own socket), the dialogs (`diff.rs`, `commit.rs`, `fork.rs`, `settings.rs`, `server_settings.rs`, `viewer.rs`), the new session form with its folder browser and import, the shared agent picker, task progress, and the palette. `assets/theme.json` holds the kit's theme, in the same colours. |

GPUI Kit pins its own GPUI snapshot (the `gpui-pre` crate), so `Cargo.toml` pins `gpui-kit` exactly. Bump them together.
