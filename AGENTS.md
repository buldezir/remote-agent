# AGENTS.md

Notes for coding agents working on this repo. What the project is and how it is laid out: [README.md](README.md). The wire contract: [protocol/PROTOCOL.md](protocol/PROTOCOL.md).

## Development process

- **Server:** a Go module in `server/` (`cmd/rad` and `internal/`). Run Go commands from `server/`; it builds into `server/bin/rad`.
- **Apple clients:** everything is in `apple/`.
  - `apple/RAKit` holds the protocol, network client and stores, all testable with `swift test`.
  - Two app targets, `RemoteAgent-iOS` (iPhone and iPad) and `RemoteAgent-macOS`, both compile `apple/Shared`, the SwiftUI app. Each adds its own folder, `apple/iOS` or `apple/macOS`, with its `Info.plist` and the views only it uses: the QR scanner and the iPhone server list, or the Mac menu bar and entitlements (App Sandbox, network client, microphone, files the user picks).
  - A whole file for one platform goes in its folder. A small difference inside a shared file goes in `#if os(iOS)` / `#if os(macOS)`. `Shared/Views/Platform.swift` holds the shims, such as `inlineTitle()`, `plainTextInput()` and `Platform.deviceName`.
  - A third target, `RemoteAgentServer` in `apple/ServerApp`, is the Mac menu bar app that runs rad. It shares no code with the clients. Its last build phase, `apple/scripts/build-rad.sh`, compiles rad from `server/` into the app and signs it.
- **Web client:** `web/`, Vite, React and TypeScript, with no UI kit. It mirrors the apps:
  - `src/protocol` and `src/store` port RAKit: the same sync rules (`synchronized`, gaps, `resync`), stores and outbox. A change to the client logic in RAKit goes into both.
  - `src/ui` follows `apple/Shared/Views`: the split layout of the iPad and Mac in a wide window, the iPhone's stack in a narrow one. Colours are the palette's, as CSS variables in `src/ui/styles.css`; text sizes and fonts are variables that `src/ui/settings.ts` sets.
  - Browsers can't set headers on a WebSocket, so the client sends its token as the subprotocol `rad.token.<token>`. rad answers browsers only from the origins in its config's `web_origins` (default: localhost).
  - Agent Markdown goes through react-markdown, which doesn't render raw HTML; keep it that way, and keep the CSP in `index.html`, since tokens live in localStorage.
  - `public/icon.png` and `apple-touch-icon.png` are copies of the Mac icon (`AppIcon-Mac-32@2x.png`, `-128@2x.png`); copy them again after rerunning `make-app-icon.swift`.
- **Changing a wire type:** keep these in step:
  - `server/internal/model/model.go`
  - `apple/RAKit/Sources/RAKit/Protocol/Models.swift`
  - `web/src/protocol/models.ts`
  - `protocol/PROTOCOL.md`

  Then regenerate the fixtures with `cd server && go test ./internal/api -update`, and assert the new field in `apple/RAKit/Tests/RAKitTests/FixtureTests.swift` and `web/src/protocol/fixtures.test.ts`. Clients ignore unknown fields, so prefer adding optional fields over changing existing ones.
- **Images:** users upload with `POST /v1/images`, and prompts carry the ids. Adapters store images from agent output through `OpenOptions.Images` and put the refs on the item. Clients fetch them with `GET /v1/images/<id>`. The bytes never travel over the WebSocket.
  - Agents show image files in replies as Markdown images with their paths. The orchestrator stores those files and rewrites the links to `rad-image:<id>` (`orchestrator/linkedimages.go`), and the apps draw them with `ReplyImageProvider`. The text that tells agents to do this goes to each adapter as `OpenOptions.Instructions`; a new adapter has to pass it on too.
- **Changing a harness adapter:** never call real agent CLIs from tests. Adapter tests replay recorded transcripts:
  - `server/internal/harness/*/testdata/*.ndjson`, one `{"dir":"in"|"out","frame":…}` per line, driven by `internal/harness/replaytest`.
  - Assert on the emitted `harness.Event`s.
  - To exercise the orchestrator, extend `server/internal/harness/fake`.
- **Slash commands:** a prompt of `/<name>` goes to the harness like any other; `Input.Command` recognizes one. Claude Code runs them itself. An adapter runs the ones its agent takes only as a request of its own (Codex's `thread/compact/start`, Pi's `compact`) and ends the turn when it's done. `HarnessInfo.Commands` lists the ones the apps offer in the session's ⋯ menu, so list a command there only once the adapter runs it.
- **Xcode project:** `apple/project.yml` is the source of truth. `RemoteAgent.xcodeproj` is generated and gitignored, and the `Info.plist` and entitlements files are generated from `project.yml`, so edit permissions and plist keys there. Run `xcodegen` after adding files or changing the spec.
- **Signing:** the bundle ID lives in `apple/Signing.xcconfig`. It includes the developer's gitignored `apple/Local.xcconfig`, which holds their `DEVELOPMENT_TEAM`. The Mac app shares the bundle ID; without a team it is signed ad hoc.
- **App icon:** drawn by `apple/scripts/make-app-icon.swift`, for iOS and, in the macOS shape, for the Mac. Edit the script and rerun it; don't edit the PNGs.
- **Text sizes and fonts:** Settings scales the interface and, separately, the messages (`Shared/Views/TextSize.swift`). On the Mac it also picks a message font and a code font. iOS scales the interface with Dynamic Type; the Mac has none. So that the Mac follows the settings:
  - In shared views, set fonts with `scaledFont(.footnote)`, `scaledFont(.caption, weight: .semibold, design: .monospaced)` and so on, not `font(.footnote)`. Monospaced `scaledFont` uses the code font.
  - Mac lists and sidebars ignore the window's default font. Give each list row `scaledFont(.body)`, and each list section header `listHeaderStyle()`. Form sections take `formHeaderFont()` and `formFooterFont()`.
  - Prompt text takes `messageFont()`; Markdown gets the message and code fonts through `agentMarkdown()`. Tool calls follow the message size too, with `messageRelativeFont(_:)`.
  - Each sheet applies `.appStyle()` again, which sets the text sizes, fonts and colours, because iOS doesn't pass the Dynamic Type size into sheets.
- **Colours:** Catppuccin, Frappé in dark mode and Latte in light mode (`Shared/Views/Palette.swift`). The `AccentColor` asset matches `Palette.accent`.
  - Take colours from `Palette`, not `.red`, `Color.accentColor` or the system backgrounds.
  - Lists and forms set their background with `listBackground(_:)` (forms get it from `compactForm()`). iOS draws each row on the system's background, so the rows need `paletteRows()`: on the rows, or on a `Group` around a form's sections. A list ignores it on itself.
  - A row background also hides the list's selection, so a list with selection draws it, as the iPad and Mac sidebar does in `ServerHomeView`. On the Mac this also keeps the selection off the system accent.
  - Markdown sets its text style's colour on its blocks, and with none it uses the system's, so `paletteText()` doesn't reach Markdown. `agentMarkdown()` sets `Palette.text` in its text style; a block style that changes the colour does it with `ForegroundColor`.
  - `paletteText()` gives text the palette's colours. Use it on the transcript and list rows, not whole screens: it also overrides the tint of buttons inside.
  - Text on an accent colour, such as a prominent button's label, takes `Palette.onAccent`.
- **Swift 6 strict concurrency:**
  - Callbacks that run on other threads (AVAudioEngine taps, Speech results, URLSession delegates) must be created in `nonisolated` functions. A closure formed in `@MainActor` code is main-actor isolated and traps when called off the main thread.
  - Caches that view bodies fill lazily are `@ObservationIgnored`.
  - Navigation destinations are registered once at the root (`ServersView`), with values that carry the server id.
- **Releases:** `.github/workflows/release.yml` runs on `v*` tags:
  - The Go tests, then rad cross-compiled for linux and darwin, amd64 and arm64, on Ubuntu.
  - `RemoteAgent-macOS` and `RemoteAgentServer` in Release, arm64 only and signed ad hoc, on the `xcode-27` runner. The apps target macOS 27, and the `macos-26` image has only Xcode 26.
  - The tag sets the version: `MARKETING_VERSION` for the Mac apps, and `api.Version` for rad.

  When the build changes (for example `build-rad.sh`, the schemes, or a new deployment target), change the workflow to match. Pushing tags is the developer's job.
  - Each release's notes start with a changelog of what changed for users (new features, changed config or behaviour, fixes they would notice), not internal changes. Push the tag, then create the release with those notes and the asset list from the workflow's `NOTES`: `gh release create <tag> --verify-tag --notes-file …`. The workflow then uploads its assets to that release instead of creating one.
- **Updates:** `internal/update` finds the latest GitHub release, checks its tarball against `SHA256SUMS`, runs the new binary once (`rad version`) and renames it into place.
  - `rad update` replaces the binary it was run as. It restarts the background service only when the service runs that same binary.
  - The Server app runs its bundled rad with `update --check --json` and `update --to <dir>`. It keeps downloads in `~/Library/Application Support/Remote Agent Server/`, or `$RAD_HOME/Remote Agent Server/` when `RAD_HOME` is set. `RadBinary.choose()` picks the binary on each start.
  - The release asset names (`rad-<os>-<arch>.tar.gz`, `SHA256SUMS`) are what the updater looks for, so change both together.
  - Tests use httptest, never GitHub. Don't run `rad update` on the developer's rad, its service, or agents-base unless asked. A scratch binary built with `-ldflags "-X remote-agent/internal/api.Version=0.1.2"`, or `--to` a scratch folder, is safe.
- **Commits:** tests green first. Don't commit `server/bin/`, `apple/build/`, the `.xcodeproj`, `apple/Local.xcconfig`, `web/node_modules` or `web/dist`.

## Testing

```bash
cd server && gofmt -l internal cmd && go vet ./... && go test -race ./...
cd apple/RAKit && swift test
cd web && npm ci && npm run typecheck && npm test && npm run build
cd apple && xcodegen && xcodebuild -project RemoteAgent.xcodeproj -scheme RemoteAgent-iOS \
  -destination 'platform=iOS Simulator,name=iPhone 17' -derivedDataPath build build
cd apple && xcodebuild -project RemoteAgent.xcodeproj -scheme RemoteAgent-macOS \
  -destination 'platform=macOS' -derivedDataPath build build
cd apple && xcodebuild -project RemoteAgent.xcodeproj -scheme RemoteAgentServer \
  -destination 'platform=macOS' -derivedDataPath build build
```

## Running rad locally

The developer may have their own `rad serve` running on the default port 7421, with real sessions and paired devices. Don't stop it, don't send prompts into its sessions, and don't change `~/.config/remote-agent`. Use a scratch instance instead.

1. Write `$SCRATCH/radhome/config.yaml`:

   ```yaml
   listen: ["127.0.0.1:7499"]       # loopback only, unlike the default 0.0.0.0
   fake: true                       # adds the scripted "Fake" harness (or pass --fake)
   roots: [/path/to/scratch]
   ```

2. Build and start the server:

   ```bash
   go -C server build -o bin/rad ./cmd/rad
   RAD_HOME=$SCRATCH/radhome server/bin/rad serve &
   ```

3. Drive it:

   ```bash
   RAD_HOME=$SCRATCH/radhome server/bin/rad debug run --cwd $SCRATCH/proj [--worktree] [--image a.png] "write a.txt"
   RAD_HOME=$SCRATCH/radhome server/bin/rad debug prompt <sessionId> "again"
   ```

4. Stop it by its port: `kill $(lsof -tiTCP:7499 -sTCP:LISTEN)`. Don't use `pkill -f "rad serve"`: that matches the developer's rad too, including the one the Server app runs.

Fake prompt keywords are listed in the README: `write <file>`, `question`, `slow`, `fail`, `screenshot`, `picture`, and the prompt `/compact`. Prefer the fake harness for UI work. A real harness spends tokens; use it only when the task is about that adapter.

## Web client

Run it against a scratch rad. Its `listen` is on loopback, and the default `web_origins` lets `http://localhost:5173` in.

```bash
cd web && npm run dev &                                                           # http://localhost:5173
RAD_HOME=$SCRATCH/radhome server/bin/rad pair --web http://localhost:5173 --print-url   # a link that opens the pairing sheet
```

- **Drive it with Playwright:** `web/e2e/smoke.mjs` pairs a fresh browser profile and walks the client through the fake harness's keywords, a worktree diff and revert, the light theme and a phone-sized window. It saves screenshots, then removes the server, which unpairs it.

  ```bash
  cd web && npx playwright install chromium   # once
  node e2e/smoke.mjs "$(RAD_HOME=$SCRATCH/radhome ../server/bin/rad pair --web http://localhost:5173 --print-url)" $SCRATCH/shots proj
  ```

  The last argument is a folder under the scratch root, a git repo, that the script adds as a project.
- **Port 5173 may be in use:** the dev server has `strictPort`. If something else holds it, don't stop it; run `npx vite --port 5174` and pass that URL to `rad pair --web` and the script. `localhost:*` covers any port.
- **Don't use the developer's browser:** it may hold their own servers' tokens. Playwright starts a fresh profile each time.
- **Stop the dev server** by its port when done: `kill $(lsof -tiTCP:5173 -sTCP:LISTEN)`.
- **Coverage:** headless Chromium has no speech recognizer and no clipboard. Say so when a change depends on dictation or pasting.

## Simulator

The simulator reaches the host's `127.0.0.1`, so a scratch rad on loopback works as-is.

```bash
APP=apple/build/Build/Products/Debug-iphonesimulator/RemoteAgent.app
xcrun simctl install booted $APP && xcrun simctl launch booted dev.remote-agent.app
xcrun simctl openurl booted "$(RAD_HOME=$SCRATCH/radhome server/bin/rad pair --print-url)"   # pair, then tap Pair
xcrun simctl io booted screenshot /tmp/shot.png
xcrun simctl spawn booted log show --last 2m --predicate 'subsystem == "dev.remote-agent.app"'
```

- **Pairing:** the camera and QR scanning don't work in the simulator. Pair with the deep link above. On iPhone the pairing sheet appears on the server list, so go back to the list if a server is open.
- **iPad:** the app picks its layout by device. iPhone gets the stack in `ServersView`. iPad gets `ServerSplitView`: one server's sessions in the sidebar, the chosen session beside it, and the title menu to switch servers. Both reuse `ServerHomeView` and `SessionView`, so check changes to either on an iPad simulator too, such as `iPad Pro 13-inch (M5)`. With two simulators booted, use the device name in place of `booted`.
- **Speech:** the dictation models don't run in the simulator, so the mic button ends with "The speech model for your language isn't available on this device". The UI states and permission prompts can still be checked there. Two ways to check dictation:
  - On a device.
  - On the Mac: `LiveTranscription.swift` has no UIKit, so a small driver can feed it a recording made with `say -o clip.aiff "…"`, and the Mac's installed dictation models transcribe it. Build the driver with `xcrun swiftc -swift-version 6 -parse-as-library apple/Shared/LiveTranscription.swift driver.swift`.
- **Screenshots:** they can lag a push or pop animation. Wait about a second, or take a second screenshot, before concluding a tap did nothing.
- **Keychain after signing changes:** setting or changing `DEVELOPMENT_TEAM` changes the keychain access group, so the simulator app loses tokens it saved earlier. Servers then show "Not connected", and the log shows `NSURLErrorDomain -1011` on the WebSocket. Re-pair; the server doesn't need to revoke anything.
- **Cleanup:** the app may already be paired with the developer's own server. Add your scratch server next to it. When done, remove the scratch server in the app (… → Remove server, which also unpairs it on rad), then stop your scratch rad.
- **Coverage:** the simulator doesn't cover Tailscale, LAN pairing, the camera or microphone input. Say so when a change depends on them.

## Mac app

The Mac app runs on the developer's own desktop, so keep checks short and leave nothing open.

```bash
APP="$PWD/apple/build/Build/Products/Debug/Remote Agent.app"
open "$APP"
open -a "$APP" "$(RAD_HOME=$SCRATCH/radhome server/bin/rad pair --print-url)"   # opens the pairing sheet; press Pair
osascript -e 'tell application id "dev.remote-agent.app" to quit'      # fails while a sheet is open
```

- **Name the build:** a link opened without `-a` goes to whichever copy macOS picks, such as a release build in `/Applications`, which is the developer's.
- **Text settings:** `open "$APP" --args -interfaceTextSize 6 -messageTextSize 18 -messageFont Georgia -codeFont Menlo` tries them without saving them. Changing them in Settings saves them to the developer's settings.
- **Its data is the developer's:** the app shares the iOS bundle ID, keeps its settings in `~/Library/Containers/dev.remote-agent.app` and its tokens in the login keychain. The developer may have paired it with their own server. Add your scratch server next to it, and remove it when done (server menu at the foot of the sidebar → Remove).
- **Driving it:** with Accessibility access, a small Swift tool can press controls through the AX API (`AXUIElementPerformAction(…, kAXPressAction)`) and set text with `kAXValueAttribute`; capture the window with `screencapture -l<window id>`. Mouse events posted to the app's process don't reach SwiftUI, and special keys (Return, ⌘N) only arrive through System Events while the app is in front.
- **Pasting images:** the pasteboard is the developer's. Don't put test images on it. The pasteboard rules in `Platform.pasteboardImages` take a pasteboard, so test them on a private one (`NSPasteboard(name:)`) in a small driver built with `xcrun swiftc`.
- **Keychain:** a build signed with the team keeps reading the tokens it saved. An ad-hoc build is a new app to the keychain each time, so macOS may ask for the login password; don't answer it, pair again instead.

## Server app

Remote Agent Server runs `rad serve --supervised` as its child (`server/cmd/rad/supervised.go`): rad writes JSON status lines to stdout, logs to stderr, and shuts down when its stdin closes, so it never outlives the app. macOS attributes rad and its agents to the app, which is the point: the permissions granted in its Permissions window apply to them.

- **rad's guards:** `serve` takes a lock on `<data>/rad.lock` and binds its ports before it touches the store. A second rad on the same config or port exits with status 75, and the app shows it as running elsewhere rather than restarting it.
- **Test it against a scratch home,** never the developer's config:

  ```bash
  open -n --env RAD_HOME=$SCRATCH/radhome "apple/build/Build/Products/Debug/Remote Agent Server.app"
  osascript -e 'tell application id "dev.remote-agent.app.server" to quit'
  ```

  With `RAD_HOME` set, the log is `$RAD_HOME/rad.log`. The menu lives in the menu bar extra; drive it through the AX API (the app's `AXExtrasMenuBar`).
- **Updates:** a build from Xcode is version `0.1`, a development build, so it offers no updates. To try Update rad, build with an older release's version (`xcodebuild … MARKETING_VERSION=0.1.2 build`) and run it against a scratch `RAD_HOME`; the download lands in `$RAD_HOME/Remote Agent Server/`.
- **Leave the Mac's settings alone:** don't press Allow in the Permissions window, answer the system prompts, change Privacy & Security, or turn on Start at Login. They change the developer's privacy settings and login items. Checking the statuses is safe; the window only checks what macOS can report without asking.
- **To see which app macOS holds responsible for an agent's request:** `log stream --predicate 'subsystem == "com.apple.TCC"'`.
