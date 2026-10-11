# Remote Agent

Drive coding agents running on your computer from your iPhone, iPad, Mac or a browser.

- **`rad`**: a Go server that runs on the dev machine. It spawns agent CLIs as child processes, normalizes what they do into one event model, persists it in SQLite, and serves it over a WebSocket.
- **Remote Agent**: a native SwiftUI app for iPhone, iPad and Mac. You pair it by QR code, or on the Mac by pasting a link. On iPad and Mac, a server's sessions stay in a sidebar next to the one you have open. From the app you can:
  - start sessions in any project
  - watch agents stream their work
  - dictate prompts, in any of your keyboard languages
  - attach images to prompts, and see the ones agents return, such as screenshots
  - approve or deny tool calls
  - answer the agent's questions
  - review per-turn git diffs and revert them
- **Web client**: the same app for a browser, in `web/` (React and TypeScript). For now you run it from its dev server on your computer; see [Running the web client](#running-the-web-client).

<p>
  <img src="docs/screenshots/sessions.png" width="19%" alt="Sessions on a server, with one waiting for approval">
  <img src="docs/screenshots/new-session.png" width="19%" alt="New session: project, agent, model and permissions">
  <img src="docs/screenshots/approval.png" width="19%" alt="Claude Code asking to edit a file, with the change and Allow / Allow all edits / Deny">
  <img src="docs/screenshots/transcript.png" width="19%" alt="A finished turn: tool calls, approvals and the agent's summary">
  <img src="docs/screenshots/diff.png" width="19%" alt="The diff of a file the agent changed">
</p>
<p>
  <img src="docs/screenshots/ipad-approval.png" width="49%" alt="iPad: the server's sessions in a sidebar, and Claude Code asking to edit a file">
  <img src="docs/screenshots/ipad-transcript.png" width="49%" alt="iPad: a finished turn, with another session waiting for approval in the sidebar">
</p>

Supported harnesses:

| Harness | How rad talks to it |
|---|---|
| Claude Code | `claude --input-format stream-json --output-format stream-json --permission-prompt-tool stdio`: the same control protocol the Agent SDKs use |
| Codex | `codex app-server` (JSON-RPC over stdio) |
| Pi | `pi --mode rpc` (JSON commands and events over stdio) |
| Cursor, OpenCode, Gemini, Oh My Pi, … | [Agent Client Protocol](https://agentclientprotocol.com): `cursor-agent acp`, `opencode acp`, `gemini --acp`, `omp acp` (configurable) |

Prior art: [Happy](https://github.com/slopus/happy) (mobile client and relay for Claude Code) and [T3 Code](https://github.com/pingdotgg/t3code) (local server that owns agent processes, worktrees and checkpoints).

## Quick start

The server lives in `server/` and builds into `server/bin/rad`:

```bash
cd server
go build -o bin/rad ./cmd/rad
./bin/rad serve          # first run writes ~/.config/remote-agent/config.yaml and prints a pairing QR
```

Or download rad for Linux or macOS, arm64 or x86-64, from [Releases](https://github.com/buldezir/remote-agent/releases), unpack it and run `rad serve`.

On the phone, open Remote Agent and tap **Pair a server**, then scan the QR code. On the Mac, or in the simulator, paste the link that `rad pair` prints instead.

`rad serve` and `rad pair` list the agents that are ready, and any installed agent that still needs a login. The phone only sees agents installed on the server.

rad listens on all interfaces (`0.0.0.0:7421`). The pairing link tells the phone which addresses to try:

- **Tailscale (recommended):** the Tailscale address (100.x) is in the link by default, and WireGuard encrypts the traffic.
- **Same Wi-Fi:** run `rad serve --lan` / `rad pair --lan`, or set `lan = true` in the config, to add the LAN addresses too. Traffic on the LAN is plain HTTP.
- **Your own choice:** set `pair_urls` in the config to put exactly those addresses in the link, in that order, e.g. a Tailscale MagicDNS name or one specific LAN IP. Plain `http://` works with IP addresses, `.local` names and `*.ts.net`; any other host name needs `https://`.

To keep rad running in the background:

```bash
./bin/rad install-service          # --uninstall to remove
```

- **macOS:** a LaunchAgent that starts at login. Logs go to `~/Library/Logs/rad.log`.
- **Linux:** a systemd user unit, `remote-agent.service`. Read the logs with `journalctl --user-unit remote-agent -f`.
  - rad turns on lingering for your user, so it starts at boot and keeps running after you log out. Without polkit (as on a minimal Debian server) that needs root: run `sudo loginctl enable-linger $USER` first.
  - Run it from a login of that user, e.g. over SSH, not through `su` or `sudo`.

Both capture your current `PATH`, so rad finds the agent CLIs the way your shell does. Run `install-service` again after rebuilding rad, or after installing an agent CLI somewhere new.

On a Mac, the [Remote Agent Server app](#running-rad-as-a-mac-app) can run rad in the background instead, and lets you give agents the macOS permissions they need.

To remove rad completely, run `./bin/rad uninstall`. It lists everything first and asks before removing. It removes:
- the service
- the config
- the data: the database, logs and session worktrees (uncommitted changes in them are lost)
- the `refs/ra/*` checkpoints in your project repos

It keeps the session branches and the rad binary.

### Updating

```bash
rad update --check     # is there a newer release?
rad update             # install it
```

`rad update` downloads the newest release for this platform from GitHub and checks it against the release's `SHA256SUMS`. It runs the new binary once, then puts it in place of the one you ran and keeps the old one as `rad.old` next to it.
- **As the background service:** if `install-service` set up this binary, the service is restarted and `rad update` waits for the new version to answer. A restart interrupts running turns, so while any session is busy (a turn running, or an approval waiting) it stops before downloading and names them. Pass `--now` to go ahead anyway.
- **In a terminal:** restart `rad serve` yourself.
- **Development builds:** a build from source (`rad version` says `dev`) isn't replaced without `--force`.
- **Remote Agent Server app:** update its rad from the app's menu, below.

## Running rad as a Mac app

**Remote Agent Server** is a menu bar app that runs rad. rad and the agents it starts are the app's children, so macOS asks for their privacy permissions in the app's name, and you can grant them up front while you're at the Mac. Otherwise a prompt that comes up while you're driving an agent from your phone holds that agent until someone answers it at the Mac. The app also starts rad with the environment of your login shell, so it finds the agent CLIs.

1. Build it and copy it to /Applications. You need Go and Xcode; the build compiles rad into the app.

   ```bash
   (cd apple && xcodegen) && xcodebuild -project apple/RemoteAgent.xcodeproj -scheme RemoteAgentServer -configuration Release -derivedDataPath apple/build build
   cp -R "apple/build/Build/Products/Release/Remote Agent Server.app" /Applications/
   ```

   Run it from one place only: macOS treats each copy separately and can list both in System Settings.

   Or, on Apple silicon, download `Remote-Agent-Server-macOS-arm64.zip` from [Releases](https://github.com/buldezir/remote-agent/releases). That build is signed ad hoc and not notarized, so macOS blocks its first launch. Allow it under System Settings › Privacy & Security, or run `xattr -dr com.apple.quarantine "/Applications/Remote Agent Server.app"`.
2. Signing uses the team saved in `apple/Local.xcconfig` (step 3 for the iPhone). Without one, macOS forgets the app's permissions every time you rebuild it, or install a new release.
3. Open it. It runs rad with your usual config and opens **Permissions** the first time:
   - **Desktop, Documents and Downloads:** macOS asks once for each folder. **Full Disk Access** covers them and everything else, and is switched on in System Settings.
   - **Accessibility, Screen Recording and Automation of System Events:** for agents that click, type, take screenshots or run AppleScript. Other apps ask the first time an agent scripts them.
   - **Local Network:** for agents that reach other devices on your network. CLIs run from Terminal get this automatically; under the app, macOS asks. Your phone reaches rad without it.
   - **Developer Tools:** add the app under System Settings › Privacy & Security › Developer Tools, and programs your agents build skip Gatekeeper's checks.

   After turning on Full Disk Access or Screen Recording, choose **Restart rad** in the menu.

The menu shows whether rad is running, its addresses and the agents it found:
- **Pair a Device…** shows the pairing QR code.
- **Start at Login** replaces `rad install-service`. If that service is installed, the menu offers to remove it, since only one rad can serve a config at a time.
- **Open Log** opens `~/Library/Logs/Remote Agent Server/rad.log`.
- **Update rad to …** appears when a newer release is out; the app checks once a day, or when you choose **Check for Updates…**.
  - The app downloads the new rad to `~/Library/Application Support/Remote Agent Server/` and restarts rad from there. It asks first if that would interrupt a session.
  - The app itself doesn't change, so macOS keeps its permissions, and they cover the new rad too.
  - When you later install a newer app, its own rad takes over and the download is deleted. A download that fails to start is deleted too, and the app goes back to its own rad.
  - Builds from Xcode (version `0.1`) always run the rad built with them, and offer no updates.
- Quitting the app stops rad and its agents.

The `rad` CLI keeps working next to the app (`rad pair`, `rad devices`); the app's copy is `/Applications/Remote Agent Server.app/Contents/MacOS/rad`, or after an update the `rad-<version>` it downloaded.

## Installing the app on an iPhone or iPad

You need an iPhone or iPad on iOS 27 or later, Xcode, an Apple ID and, for the first run, a cable.

- **Free Apple ID:** builds expire after 7 days; run from Xcode again to renew.
- **Paid Developer Program membership:** builds last a year.

1. In Xcode → Settings → Accounts, add your Apple ID.
2. Generate and open the project (commands here run from the repo root):

   ```bash
   (cd apple && xcodegen) && open apple/RemoteAgent.xcodeproj
   ```

3. Select the **RemoteAgent-iOS** target → Signing & Capabilities, and choose your team.
   - If Xcode says the bundle ID is taken, see step 4.
   - xcodegen regenerates the project and forgets this choice. Save the team in the gitignored `apple/Local.xcconfig` so it survives:

     ```bash
     grep -m1 -o 'DEVELOPMENT_TEAM = [A-Z0-9]*' apple/RemoteAgent.xcodeproj/project.pbxproj > apple/Local.xcconfig
     ```

4. If the bundle ID was taken, add `PRODUCT_BUNDLE_IDENTIFIER = com.yourname.remote-agent` to `apple/Local.xcconfig`, then run `xcodegen` again.
5. Prepare the device:
   1. Connect it, unlock it and tap **Trust**.
   2. Turn on Settings → Privacy & Security → **Developer Mode**. The switch appears once Xcode has seen the phone, and turning it on restarts the phone.
6. Pick the device as the run destination and press ⌘R.
   - With a free Apple ID, the first launch is blocked until you trust yourself as a developer: Settings → General → VPN & Device Management → your Apple ID → Trust.
   - After the first run, Xcode can also install over Wi-Fi.
7. Make sure the phone can reach the computer. Use Tailscale, or set `lan = true` for the same Wi-Fi (see Quick start).
8. Pair the phone:
   1. Run `server/bin/rad pair` on the computer. Add `--lan` if the phone will connect over Wi-Fi rather than Tailscale.
   2. In the app, tap **Pair a server** (or **+**) and scan the QR code. The Camera app can scan it too.
   3. Allow Local Network access when iOS asks.

The app icon is drawn by `apple/scripts/make-app-icon.swift`. Run it again after changing it:

```bash
cd apple && swift scripts/make-app-icon.swift Shared/Assets.xcassets/AppIcon.appiconset
```

## Running the Mac app

The Mac app is built from the same code as the iPhone app, as its own target, **RemoteAgent-macOS**. You need macOS 27 or later and Xcode.

1. Generate the project and build the app (commands here run from the repo root):

   ```bash
   (cd apple && xcodegen) && xcodebuild -project apple/RemoteAgent.xcodeproj -scheme RemoteAgent-macOS -configuration Release -derivedDataPath apple/build build
   cp -R "apple/build/Build/Products/Release/Remote Agent.app" /Applications/
   ```

   Or open `apple/RemoteAgent.xcodeproj`, pick the **RemoteAgent-macOS** scheme and press ⌘R.

   Or download `Remote-Agent-macOS-arm64.zip` from [Releases](https://github.com/buldezir/remote-agent/releases). Like the Server app's release build, it is signed ad hoc and not notarized, so allow its first launch under System Settings › Privacy & Security.
2. Signing uses the team saved in `apple/Local.xcconfig` (step 3 for the iPhone). Without one, the app is signed to run on this Mac only. That works, but macOS may ask you to let each new build or release read the pairing tokens it saved in your keychain.
3. Pair it: run `server/bin/rad pair`, then paste the link in **File › Pair a Server…**. When rad runs on the same Mac, `open "$(server/bin/rad pair --print-url)"` opens the link in the app.

On the Mac, ⌘N starts a session and ⌥⌘N opens another window. In the composer, Return sends, ⇧Return or ⌥Return starts a new line and ⌘. stops the agent. ⌘V pastes an image you copied, and you can drop image files on the session. The server menu at the foot of the sidebar switches servers. Dictation listens in your preferred languages from System Settings.

## Running the web client

The web client in `web/` does what the apps do, in a browser. It needs Node.js 22 or later. For now it runs from Vite's server on your computer rather than from a host:

```bash
cd web && npm install && npm run dev                          # serves http://localhost:5173
server/bin/rad pair --web http://localhost:5173 --print-url    # open the link it prints, then press Pair
```

Or open http://localhost:5173, choose **Pair a server** and paste the link that `rad pair` prints. The page keeps each server's token in the browser's local storage.

- **Which pages may use rad:** a browser tells rad which page a request comes from. rad answers only pages on the origins in its config's `web_origins`, which by default are pages on `localhost` and `127.0.0.1`. Add an origin there, such as `"https://agents.example.com"` or `"my-mac.tail1234.ts.net:*"`, to open the client from another address.
- **HTTPS:** a page served over `https://` can't call rad on plain `http://`. Serve the client over `http://` too, or put rad behind HTTPS, for example with `tailscale serve`.
- **Composer:** Return sends, ⇧Return starts a new line, and ⌘. or Ctrl+. stops the agent. You can paste or drop images. Dictation uses the browser's speech recognizer where it has one (Chrome, Edge, Safari).
- `npm run build` writes a static site to `web/dist`, which any web server can serve.
- **Docker:** `web/Dockerfile` serves the client with nginx on port 8080, and `web/compose.example.yaml` runs it. [`.github/workflows/web-image.yml`](.github/workflows/web-image.yml) pushes the image to `ghcr.io/buldezir/remote-agent-web`: `edge` from main, `latest` and the version from a `v*` tag. Add the address you open it at to rad's `web_origins`, such as `"my-server.local:8080"`, then pair with `rad pair --web http://my-server.local:8080`.

## Using it

- **New session:**
  1. Pick a project folder; you can browse anything under `roots` from the config.
  2. Pick an agent, a model, an effort and a permission mode. The app remembers what you last started each agent with.
  3. Optionally choose **Isolated git worktree**, which puts the agent on its own branch in `~/Library/Application Support/remote-agent/worktrees/…`.
  4. Write the first prompt, or leave it empty to open the session and write it there.
- **Images:** the + beside the prompt attaches photos, image files or the clipboard's image. You can also drop images on the session. The app scales them to 2048 px and strips their metadata before uploading.
  - Claude Code, Codex, Pi and ACP agents that accept images see them directly. Other ACP agents get the files' paths on your computer.
  - Images agents produce show up in the transcript: an MCP tool's screenshot, an image file the agent read, or Codex viewing or generating one. Tap one to open it in Quick Look: zoom in, share it or mark it up. On iPhone and iPad it fills the screen; on the Mac it opens in a window you can resize.
  - Asked for a screenshot, an agent can show it without reading it, which would cost context. rad tells every agent that you aren't at the computer, and to put an image file in its reply as a Markdown image with its path, such as `![Screenshot](/tmp/shot.png)`. rad then sends the file to the app, which shows it in the reply. Claude Code and Pi get this in their system prompt, Codex as developer instructions, and ACP agents before the first prompt.
  - rad keeps every image in `<data>/images`, named by its content hash.
- **Approvals:** cards appear inline. The buttons come from the agent itself, e.g. *Allow*, *Allow all edits this session*, *Deny*. When denying, you can give the agent a reason.
  - Pi doesn't ask before running tools. Oh My Pi asks only clients that run its file and terminal tools for it, which rad doesn't, so expect no approvals from it either.
  - Pi extensions can still ask: their yes/no and multiple-choice dialogs show up as cards, e.g. from Pi's example `permission-gate.ts`. The app can't answer their free-text prompts yet, so those are cancelled.
- **Questions and plans:** Claude's `AskUserQuestion` and `ExitPlanMode` show up as choice and plan cards.
- **Changes:** rad snapshots the workspace before and after every turn into hidden git refs (`refs/ra/cp/…`), without touching your index or HEAD. You can view each turn's diff or the whole session's.
- **Revert:** in worktree sessions you can revert to before any turn. The agent is told about it on the next prompt.
- **Commands:** a session's ⋯ menu runs slash commands. For now there is `/compact`, which has the agent summarize the conversation so far to free up context, for Claude Code, Codex and Pi. The transcript marks it with *Context compacted*, and the context gauge drops.
  - Typing `/compact` as a prompt does the same, and `/compact <instructions>` tells Claude Code or Pi what the summary should keep; Codex takes no instructions for it.
  - Claude Code runs it itself. rad runs it with Codex's and Pi's own compaction, since they don't take `/compact` in a prompt.
- **Stop:** this interrupts the current turn. *Force stop* kills the agent process. Idle agents are stopped after `idle_timeout` and resume transparently on the next prompt.
- **Text size:** Settings has two sizes: one for the interface, such as lists and labels, and one for messages: your prompts, the agent's replies and its tool calls. On the Mac it is under Remote Agent › Settings… (⌘,). On iPhone and iPad, tap the gear on the server list or open a server's ⋯ menu. On iOS the interface follows the system's text size until you turn off **Match System**.
  - On the Mac, Settings also picks the messages' font, from the system font's designs or any installed family, and a code font from the installed fixed-width ones. The code font is used for code in messages, tool calls and diffs.
- **Colours:** the apps use [Catppuccin](https://catppuccin.com): Frappé in dark mode, Latte in light mode, with mauve as the accent. They follow the system's appearance, or Settings › Theme keeps them light or dark.

## CLI

```
rad serve [--lan] [--fake] [-v]   run the server (--fake adds a scripted test harness)
rad pair [--lan] [--print-url]    new single-use pairing code (10 min) and the agents that are ready; --lan adds LAN addresses to the link
rad pair --web <url>              also a link that opens the web client at <url> to pair it
rad devices                       list paired devices
rad devices revoke <id-prefix>    revoke one; its open connections close within 30s
rad install-service [--uninstall]  run rad in the background (launchd on macOS, systemd on Linux)
rad update [--check] [--now]      install the latest release over this rad and restart its service
rad uninstall [--yes]             remove the service, config, data, worktrees and checkpoints
rad debug run --harness claude --cwd DIR [--mode M] [--model M] [--effort E] [--worktree] [--approve] [--diff] "prompt"
rad debug prompt <sessionId> "…"  follow-up turn, streamed to the terminal
rad debug call <method> '{json}'  raw RPC (see protocol/PROTOCOL.md)
```

## Configuration

`~/.config/remote-agent/config.yaml`. Data lives in `~/Library/Application Support/remote-agent` on macOS and `~/.local/share/remote-agent` on Linux. Set `RAD_HOME=/some/dir` to keep the config and data together, e.g. for tests.

```yaml
name: Work laptop               # what the app calls this server after pairing (default: host name)
port: 7421
lan: false                      # also put LAN addresses in the pairing link
# pair_urls: [my-mac.tail1234.ts.net, 192.168.1.20]   # or list the link's addresses yourself
                                # (bare hosts get http:// and the port; also host:port, https://…)
# listen: ["127.0.0.1:7421", "100.101.102.103:7421"]  # bind only these instead of 0.0.0.0
# web_origins: ["localhost:*", "127.0.0.1:*"]          # web pages that may use rad (this is the default; [] for none)
roots: [~/projects]             # what the phone may browse and add as projects ("~" in quotes for home itself)
idle_timeout: 30m
acp:                            # any ACP agent can be added here
  - {id: cursor,   name: Cursor,   command: cursor-agent, args: [acp]}
  - {id: opencode, name: OpenCode, command: opencode,     args: [acp]}
  - {id: gemini,   name: Gemini,   command: gemini,       args: [--acp]}
  - {id: omp,      name: Oh My Pi, command: omp,          args: [acp]}
harness:
  claude: {command: claude}     # optional: args: [...], env: {KEY: value}
  codex: {command: codex}
  pi: {command: pi}             # e.g. args: [-e, /path/to/extension.ts] to load an extension
```

Unknown keys are errors, so a typo stops rad with the line number rather than being ignored.

## Security model

- A paired device can make agents run arbitrary commands on your machine. Treat device tokens like SSH keys.
- rad listens on all interfaces, but every request except pairing needs a device token. To keep it off other networks entirely, set `listen` to loopback and your Tailscale address.
- Pairing codes are single-use and expire after 10 minutes.
- Tokens are 256-bit random values. Only their SHA-256 hash is stored, and the phone keeps the token in the Keychain. The web client keeps it in the browser's local storage, for its origin only.
- Browsers send the origin of the page making a request. rad answers browsers only from the origins in `web_origins`, so another site you visit can't use rad. The apps aren't browsers and aren't affected.
- Revoking a device closes its live sockets.
- The phone can browse and add only folders under `roots`.
- Permission modes are enforced by each agent itself. rad never auto-approves on the agent's behalf.
- Pi has no permission modes: it runs tools without asking.

## Layout

```
server/                  Go module for rad
  cmd/rad/               CLI: serve, pair, devices, debug, install-service, update, uninstall
  internal/model         domain types (Session, Turn, Item, Event…) = wire format
  internal/store         SQLite; per-stream change feed (latest event per entity)
  internal/events        live fan-out to subscribers
  internal/orchestrator  session actors: prompts, approvals, turns, checkpoints, recovery
  internal/harness/      adapter interface + claude/ codex/ pi/ acp/ fake/ proc/ jsonrpc/
  internal/gitx          worktrees, checkpoints, diffs, revert (git CLI)
  internal/images        images in transcripts, stored by content hash
  internal/api           HTTP pairing and images + WebSocket JSON-RPC
  internal/update        finds, checks and installs rad's GitHub releases
protocol/                PROTOCOL.md + golden fixtures shared by the Go, Swift and web tests
apple/                   XcodeGen project for the iPhone, iPad and Mac apps
  RAKit/                 Swift package: protocol, network client, stores
  Shared/                SwiftUI app code both apps build
  iOS/  macOS/           what only one platform uses: Info.plist, QR scanner, Mac menu bar…
  ServerApp/             Remote Agent Server, the menu bar app that runs rad on a Mac
web/                     the web client: Vite, React and TypeScript
  src/protocol/          wire types, the WebSocket client, pairing
  src/store/             connection, session and server stores (ported from RAKit)
  src/ui/                the views
  e2e/smoke.mjs          drives the client in Chromium against a scratch rad
```

## Development

```bash
cd server && go test ./... -race              # server tests (adapters replay recorded CLI transcripts)
cd server && go test ./internal/api -update   # regenerate protocol/fixtures after changing wire types
cd apple/RAKit && swift test                  # client protocol and sync tests
cd web && npm run typecheck && npm test       # web client types, protocol and sync tests
cd apple && xcodegen && xcodebuild -scheme RemoteAgent-iOS -destination 'platform=iOS Simulator,name=iPhone 17' build
cd apple && xcodebuild -scheme RemoteAgent-macOS -destination 'platform=macOS' build
cd apple && xcodebuild -scheme RemoteAgentServer -destination 'platform=macOS' build
```

To exercise the UI without spending tokens, run `rad serve --fake` and pick **Fake (scripted)**. Its prompt keywords are:
- `write <file>`: asks for approval, then writes the file
- `question`: asks a multiple-choice question
- `slow`: streams slowly, useful for testing interrupt
- `fail`: makes the turn fail
- `screenshot`: runs a tool that returns an image
- `picture`: writes `picture-<turn>.png` in the working directory and shows it in the reply, as agents are told to
- `/compact`: marks the transcript with *Context compacted* and reports less context in use

It also says how many images came with a prompt. `rad debug run --image shot.png "…"` attaches one from the command line.

To release, push a tag such as `v0.2.0`. [`.github/workflows/release.yml`](.github/workflows/release.yml) runs the server tests, builds rad for Linux and macOS and both Mac apps for Apple silicon, and publishes them as that tag's release with a `SHA256SUMS` file. A tag with a hyphen, such as `v0.3.0-beta.1`, becomes a prerelease.

The raw protocol frames for each session are logged to `<data>/logs/<sessionId>.ndjson`.
