# Remote Agent for macOS, in GPUI (experimental)

A Mac client for rad written in Rust with [GPUI](https://gpui.rs), Zed's GPU-accelerated UI framework, and [GPUI Kit](https://gpui-kit.com)'s components. It's an experiment beside the SwiftUI Mac app in `apple/`, not a replacement for it.

## What it does

- **Pairing:** paste the link `rad pair --print-url` prints, or pass it on the command line.
- **The sidebar:** every paired server's sessions in the Inbox layout, Needs You first, then Recent.
- **A session:** its transcript, with runs of tool calls folded into one row ("Ran 4 commands, edited 3 files") as in the other clients. A tool call opens to show its command and output.
- **Agent replies:** Markdown, with raw HTML kept as text.
- **Approvals:** tool approvals, plans, and questions (options only).
- **The composer:** Return sends and Shift-Return starts a new line. A prompt sent while the agent works waits in the queue, where Send now and Cancel act on it. Stop interrupts the turn, and a session's draft can be sent.
- **New sessions (⌘N):** pick a project and an agent, and optionally a worktree. The form starts with the server's default agent.
- **Unread state:** a session is marked read on rad while it shows in the window in front.
- **Connections:** the client reconnects with backoff and resubscribes from where it left off.
- **Appearance:** Catppuccin, Frappé when the Mac is dark and Latte when it's light.

Not yet: images (attaching, or showing the ones in replies and tool output), diffs and revert, forks, commits, the terminal, search, renaming and archiving, models, efforts and modes, answers to questions in your own words, Server Settings and usage limits, notifications, and the Projects and Columns layouts.

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
| `src/net.rs` | The socket to one rad. It runs on a tokio runtime of its own, reconnects, and matches responses to requests. The UI gets what it hears over a channel. |
| `src/store/` | The client logic, free of UI and ported from RAKit and ra's `internal/client`: the streams' sync rules (`sync.rs`), the index and sessions, tool runs (with the shared test vectors), the sidebar's grouping, the saved servers, and their tokens in the Keychain (`tokens.rs`; tests use an in-memory store). |
| `scripts/` | `bundle.sh` builds the signed app bundle; `sign.sh` signs a build; `run-signed.sh` is cargo's runner, which signs the app and runs it. |
| `src/conn.rs` | A GPUI entity per server: it applies events to the stores, and subscribes to a session while a view shows it. |
| `src/ui/` | The window (`app.rs`), the session (`session_view.rs`), the new session form, and the palette. `assets/theme.json` holds the kit's theme, in the same colours. |

GPUI Kit pins its own GPUI snapshot (the `gpui-pre` crate), so `Cargo.toml` pins `gpui-kit` exactly. Bump them together.
