# AGENTS.md

Notes for coding agents working on this repo. What the project is and how it is laid out: [README.md](README.md). The wire contract: [protocol/PROTOCOL.md](protocol/PROTOCOL.md).

## Development process

- **Server:** a Go module in `server/` (`cmd/rad` and `internal/`). Run Go commands from `server/`; it builds into `server/bin/rad`.
- **Apple clients:** everything is in `apple/`.
  - `apple/RAKit` holds the protocol, network client and stores, all testable with `swift test`.
  - Two app targets, `RemoteAgent-iOS` (iPhone and iPad) and `RemoteAgent-macOS`, both compile `apple/Shared`, the SwiftUI app. Each adds its own folder, `apple/iOS` or `apple/macOS`, with its `Info.plist` and the views only it uses: the QR scanner and the iPhone server list, or the Mac menu bar and entitlements (App Sandbox, network client, microphone, files the user picks).
  - A whole file for one platform goes in its folder. A small difference inside a shared file goes in `#if os(iOS)` / `#if os(macOS)`. `Shared/Views/Platform.swift` holds the shims, such as `inlineTitle()`, `plainTextInput()` and `Platform.deviceName`.
  - Agent Markdown is Textual's `StructuredText`, in `Shared/Views/AgentMarkdown.swift`. Textual selects across a reply's blocks, which SwiftUI's `textSelection` can't do across separate `Text` views. A horizontal scroll view starts a selection of its own, so code blocks wrap. Its overlay takes clicks over the whole text, which is why a reply's images sit between the texts. Raw HTML stays text: Foundation's parser keeps inline HTML as text, and `AgentMarkdown` makes HTML blocks, which Textual would drop, paragraphs of text. It also turns task-list boxes, which the parser leaves as `[ ]` and `[x]`, into ☐ and ☑.
  - A third target, `RemoteAgentServer` in `apple/ServerApp`, is the Mac menu bar app that runs rad. It shares no code with the clients. Its last build phase, `apple/scripts/build-rad.sh`, compiles rad from `server/` into the app and signs it.
- **Web client:** `web/`, Vite, React and TypeScript, with no UI kit. It mirrors the apps:
  - `src/protocol` and `src/store` port RAKit: the same sync rules (`synchronized`, gaps, `resync`), stores and outbox. A change to the client logic in RAKit goes into the web client and `android/core` too.
  - `src/ui` follows `apple/Shared/Views`: the split layout of the iPad and Mac in a wide window, with the sidebar in the chosen layout (see "Sidebar layouts"), and the iPhone's stack in a narrow one. Colours are the palette's, as CSS variables in `src/ui/styles.css`; text sizes and fonts are variables that `src/ui/settings.ts` sets.
  - Browsers can't set headers on a WebSocket, so the client sends its token as the subprotocol `rad.token.<token>`. rad answers browsers only from the origins in its config's `web_origins` (default: localhost).
  - Agent Markdown goes through react-markdown, which doesn't render raw HTML; keep it that way, and keep the CSP in `index.html`, since tokens live in localStorage.
  - `public/icon.png` and `apple-touch-icon.png` are copies of the Mac icon (`AppIcon-Mac-32@2x.png`, `-128@2x.png`); copy them again after rerunning `make-app-icon.swift`.
- **Android client:** `android/`, a Gradle build (AGP with its built-in Kotlin, a version catalog in `gradle/libs.versions.toml`). minSdk 36, target 37.
  - `core` is plain Kotlin (no Android), a port of RAKit with the same names: `RpcClient`, `Pairing`, `ServerConnection`, `SessionStore`, `ServerStore`, `TerminalSession`, `ImageCache`. State is `StateFlow`s, changed on the scope the app passes in (the main thread). Its JVM tests read `protocol/fixtures`, and port the Swift and web tests.
  - As in RAKit and web, a `SessionStore` (and the index) counts subscriptions that haven't synchronized yet, and only the last one's `synchronized` makes it live. Otherwise a view subscribing while the connection resubscribes everything, such as a session a window reopens at launch, gets a second `synchronized` with `reset` after the first replay was applied, and the store empties.
  - `app` is Compose: `ui/` follows `apple/Shared/Views` (`SessionView.kt`, `ItemViews.kt`, `NewSessionView.kt`…), `platform/` holds the Android parts (Keystore token store, image coding, dictation). Navigation 3 keys carry the server id. On a wide window `ListDetailSceneStrategy` puts `ServerSplitView`, the sidebar in the chosen layout, beside the open one, and on a narrow one a phone's stack; `normalizedBackStack` fixes the stack when the width changes.
  - Colours come from `ui/Palette.kt` (`palette.text`, `palette.mantle`…), not Material's defaults. Interface text takes `Type.*`, which follows the interface size; messages take `messageStyle()` and code `codeStyle()`, which follow the message size and ignore the interface size, as on iOS.
  - Icons are Material Symbols, written as vector drawables by `android/scripts/symbols.py`; add a name there and rerun it. `ui/Icons.kt` names them, with the SF Symbol each replaces. The launcher icon comes from `apple/scripts/make-app-icon.swift --android ../android/app/src/main/res`.
  - The terminal is ConnectBot's termlib, pinned exactly; only `ui/TerminalHost.kt` imports it.
  - Markdown is mikepenz's renderer, wrapped in `ui/AgentMarkdown.kt`, which shows raw HTML as text; keep it that way.
  - Pickers, the QR scanner and the share sheet are activities of their own, so the app stops while they're open and its sockets close. `ServerConnection.uploadImage` waits for the connection to come back.
  - Cleartext is allowed to any host (`res/xml/network_security_config.xml`): rad is plain http/ws on Tailscale and LAN addresses, which a domain config can't name.
- **Sidebar layouts:** a wide window (iPad, Mac, the web, an Android tablet) shows sessions in one of three layouts, the `sidebarLayout` setting: Inbox (every server's sessions by state, Needs You first), Projects (each server's projects folding open to their sessions) and Columns (Mail's three columns: collections, their sessions, the session). Settings › Sidebar picks one, and on the Mac View › as Inbox, as Projects, as Columns (⌘1–3).
  - The grouping, the collections and the short ages are in `SidebarLayout` (`Shared/Views/SidebarLayout.swift`, `web/src/ui/sidebarLayout.ts`, Android's `ui/SidebarLayout.kt`), the rows in `SidebarRows`, one file per layout (`InboxSidebar`, `ProjectsSidebar`, `ColumnsSidebar`), and the shared headers and status rows in `SessionsSidebar`. A change to a layout goes to all three clients.
  - The split view (`ServerSplitView`, `Split` in `web/src/ui/App.tsx`) shows the new-session, archive and remove-server confirmations for every layout; rows ask for them through `SessionActions` (`ServerActions` on the web). On iPad each confirmation sits on the column its rows are in, where the popover points.
  - On Android, Columns puts the collections and the session list side by side in the list pane, made wider through the scaffold directive (`listPaneWidth`): a scene keeps the pane width it was first given.
  - The iPhone and Android phones keep a server's own list, with `SessionRow`.
- **Changing a wire type:** keep these in step:
  - `server/internal/model/model.go`
  - `apple/RAKit/Sources/RAKit/Protocol/Models.swift`
  - `web/src/protocol/models.ts`
  - `android/core/src/main/kotlin/dev/remoteagent/core/protocol/Models.kt`
  - `protocol/PROTOCOL.md`

  Then regenerate the fixtures with `cd server && go test ./internal/api -update`, and assert the new field in `apple/RAKit/Tests/RAKitTests/FixtureTests.swift`, `web/src/protocol/fixtures.test.ts` and `android/core/src/test/kotlin/dev/remoteagent/core/FixtureTests.kt`. Clients ignore unknown fields, so prefer adding optional fields over changing existing ones.
- **Images:** users upload with `POST /v1/images`, and prompts carry the ids. Adapters store images from agent output through `OpenOptions.Images` and put the refs on the item. Clients fetch them with `GET /v1/images/<id>`. The bytes never travel over the WebSocket.
  - Agents show image files in replies as Markdown images with their paths. The orchestrator stores those files and rewrites the links to `rad-image:<id>` (`orchestrator/linkedimages.go`), and the apps draw them: `AgentMarkdown` on Apple puts an image that stands alone in a paragraph between the texts, where a tap opens it. The text that tells agents to do this goes to each adapter as `OpenOptions.Instructions`; a new adapter has to pass it on too.
- **Changing a harness adapter:** never call real agent CLIs from tests. Adapter tests replay recorded transcripts:
  - `server/internal/harness/*/testdata/*.ndjson`, one `{"dir":"in"|"out","frame":…}` per line, driven by `internal/harness/replaytest`.
  - Assert on the emitted `harness.Event`s.
  - To exercise the orchestrator, extend `server/internal/harness/fake`.
- **Server management:** clients update rad and the agents, sign agents in again and pick the system agent from a server's Server Settings (see "Managing the server" in PROTOCOL.md).
  - Updates and sign-ins are tasks (`internal/tasks`): the agent's command runs in a pseudo-terminal, with `BROWSER=true` so nothing opens on the server, and clients poll `task.get`. `tasks/screen.go` turns the output into the text a terminal would show.
  - An adapter offers them by implementing `harness.Maintainer` (its update and login commands, which the config's `update` and `login` replace) and `harness.OneShotter` (the system agent's call). A one-shot must leave nothing with the agent: no session file, no history, no tools, no MCP servers. Check a new one against the agent's own folders (`~/.claude`, `~/.codex`…) before and after a real call.
  - rad updates itself in `cmd/rad/selfupdate.go`: it replaces its binary and `exec`s it, keeping its PID; under the Server app it asks the app with a status line (`{"event":"update"}`) and reads the app's answers on its stdin (`AppUpdater.installForClient`). The clients follow the task with `ServerTaskProgress` (RAKit, `web/src/store`, `android/core`).
  - To try sign-ins with real agents, give the scratch rad's agents empty config folders (`harness: {claude: {env: {CLAUDE_CONFIG_DIR: …}}, codex: {env: {CODEX_HOME: …}}}`), so that they're signed out and the developer's logins stay as they are. Don't run a real agent's update or sign-in against the developer's own config unless asked.
- **Slash commands:** a prompt of `/<name>` goes to the harness like any other; `Input.Command` recognizes one. Claude Code runs them itself. An adapter runs the ones its agent takes only as a request of its own (Codex's `thread/compact/start`, Pi's `compact`) and ends the turn when it's done. `HarnessInfo.Commands` lists the ones the apps offer in the session's ⋯ menu, so list a command there only once the adapter runs it.
- **Terminal:** `server/internal/terminal` runs a login shell per session in a pseudo-terminal (`creack/pty`) and keeps its last 256 KiB of output, so clients resume or redraw; `internal/api/terminal.go` serves it on `GET /v1/terminal` (see "Terminal" in PROTOCOL.md). The clients' side is `TerminalSession` in RAKit and `web/src/store/terminalSession.ts`, shown with SwiftTerm (`Shared/Views/TerminalHost.swift`, the only file that imports it) and xterm.js (`web/src/ui/TerminalPanel.tsx`).
  - Tests run `/bin/sh` in a real pty, never the developer's shell, and wait for output the shell computes (`echo $((6*7))` → `42`), since the terminal echoes what's typed.
  - SwiftTerm is pinned to an exact version: 1.99 is the 2.0 preview, with a different API. It needs Xcode's Metal Toolchain (`xcodebuild -downloadComponent MetalToolchain`) and `-skipPackagePluginValidation` for its build plugin.
  - When testing, open terminals only on a scratch rad: a shell on the developer's rad is theirs.
- **Xcode project:** `apple/project.yml` is the source of truth. `RemoteAgent.xcodeproj` is generated and gitignored, and the `Info.plist` and entitlements files are generated from `project.yml`, so edit permissions and plist keys there. Run `xcodegen` after adding files or changing the spec.
- **Signing:** the bundle ID lives in `apple/Signing.xcconfig`. It includes the developer's gitignored `apple/Local.xcconfig`, which holds their `DEVELOPMENT_TEAM`. The Mac app shares the bundle ID; without a team it is signed ad hoc, and without the hardened runtime, under which it couldn't load Sparkle.framework (`HARDENED_RUNTIME_WITHOUT_TEAM` in `project.yml`).
- **App icon:** drawn by `apple/scripts/make-app-icon.swift`, for iOS and, in the macOS shape, for the Mac. Edit the script and rerun it; don't edit the PNGs.
- **Text sizes and fonts:** Settings scales the interface and, separately, the messages (`Shared/Views/TextSize.swift`). On the Mac it also picks a message font and a code font. iOS scales the interface with Dynamic Type; the Mac has none. So that the Mac follows the settings:
  - In shared views, set fonts with `scaledFont(.footnote)`, `scaledFont(.caption, weight: .semibold, design: .monospaced)` and so on, not `font(.footnote)`. Monospaced `scaledFont` uses the code font.
  - Mac lists and sidebars ignore the window's default font. Give each list row `scaledFont(.body)`, and each list section header `listHeaderStyle()`. Form sections take `formHeaderFont()` and `formFooterFont()`.
  - Prompt text takes `messageFont()`; Markdown, drawn by `AgentMarkdown` (`Shared/Views/AgentMarkdown.swift`), takes the message and code fonts itself. Tool calls follow the message size too, with `messageRelativeFont(_:)`.
  - Each sheet applies `.appStyle()` again, which sets the text sizes, fonts and colours, because iOS doesn't pass the Dynamic Type size into sheets.
- **Colours:** Catppuccin, Frappé in dark mode and Latte in light mode (`Shared/Views/Palette.swift`). The `AccentColor` asset matches `Palette.accent`.
  - Take colours from `Palette`, not `.red`, `Color.accentColor` or the system backgrounds.
  - Lists and forms set their background with `listBackground(_:)` (forms get it from `compactForm()`). iOS draws each row on the system's background, so the rows need `paletteRows()`: on the rows, or on a `Group` around a form's sections. A list ignores it on itself.
  - A row background also hides the list's selection, so a list with selection draws it, as the iPad and Mac sidebars do (`SidebarSelection` in `SessionsSidebar.swift`). On the Mac this also keeps the selection off the system accent.
  - `AgentMarkdown` sets `Palette.text` as its foreground style; its block styles take palette colours, and inline styles (code, links) set theirs as text properties.
  - `paletteText()` gives text the palette's colours. Use it on the transcript and list rows, not whole screens: it also overrides the tint of buttons inside.
  - Text on an accent colour, such as a prominent button's label, takes `Palette.onAccent`.
- **Swift 6 strict concurrency:**
  - Callbacks that run on other threads (AVAudioEngine taps, Speech results, URLSession delegates) must be created in `nonisolated` functions. A closure formed in `@MainActor` code is main-actor isolated and traps when called off the main thread.
  - Caches that view bodies fill lazily are `@ObservationIgnored`.
  - Navigation destinations are registered once at the root (`ServersView`), with values that carry the server id.
- **Releases:** `.github/workflows/release.yml` runs on `v*` tags:
  - The Go tests, then rad cross-compiled for linux and darwin, amd64 and arm64, on Ubuntu.
  - `RemoteAgent-macOS` and `RemoteAgentServer` in Release, arm64 only, on the `xcode-27` runner. The apps target macOS 27, and the `macos-26` image has only Xcode 26. They are signed with the Developer ID certificate in the repository's secrets (`MACOS_CERTIFICATE_P12_BASE64`, `MACOS_CERTIFICATE_PASSWORD`, `APPLE_TEAM_ID`), then notarized with an App Store Connect API key (`NOTARY_API_KEY_BASE64`, `NOTARY_API_KEY_ID`, `NOTARY_API_ISSUER_ID`) and stapled. A tag without them fails; a run by hand builds ad hoc. `build-rad.sh` timestamps rad's signature for Developer ID, since notarization rejects a binary without one.
  - Each Mac job also writes its app's Sparkle feed, `<name>-appcast.xml`, with `apple/scripts/appcast.sh`: the zip's EdDSA signature (`SPARKLE_PRIVATE_KEY`, checked against the app's `SUPublicEDKey`) and the release's notes up to its `## Downloads`, if the release is written by then. The release job uploads the feeds last, after the zips. A run by hand takes a `version` input, such as `0.10.0-test.1`, to build apps that offer and take updates, and a `bundle_id`, such as `dev.remote-agent.updatetest`, to keep them apart from the developer's installed apps.
  - The Android app, on Ubuntu: tests, lint, then `assembleRelease`, signed with the key in the repository's secrets (`ANDROID_KEYSTORE_BASE64`, `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD`). A tag without them fails; a run by hand builds unsigned. Without `RA_KEYSTORE_FILE` and its passwords in the environment, a local `assembleRelease` makes an unsigned APK. Never commit a keystore.
  - The tag sets the version: `MARKETING_VERSION` for the Mac apps, `api.Version` for rad, and `-PversionName` for Android, whose `versionCode` is derived from it so that it only grows (`0.3.0-beta.2` → 30002, `0.3.0` → 30099).

  - The iOS app goes to TestFlight by hand: `apple/scripts/testflight.sh <version>` archives it unsigned and exports it to App Store Connect, signed through the Xcode account's Apple Distribution certificate (`-allowProvisioningUpdates`). The version is numbers only and the build number is the tag's, as above. Uploading is the developer's call.
  - App Store Connect rejects an iOS build that calls an API on Apple's required-reason list without a reason in `apple/iOS/PrivacyInfo.xcprivacy`, packages included (SwiftTerm's `stat` and `fstat` are there). After adding a package or such a call, check the device build's `nm -u` against the list.

  When the build changes (for example `build-rad.sh`, the schemes, or a new deployment target), change the workflow to match. Pushing tags is the developer's job.
  - Each release's notes start with a changelog of what changed for users (new features, changed config or behaviour, fixes they would notice), not internal changes. Push the tag, then create the release with those notes and the asset list from the workflow's `NOTES`: `gh release create <tag> --verify-tag --notes-file …`. The workflow then uploads its assets to that release instead of creating one.
- **Updates:** `internal/update` finds the latest GitHub release, checks its tarball against `SHA256SUMS`, runs the new binary once (`rad version`) and renames it into place.
  - `rad update` replaces the binary it was run as. It restarts the background service only when the service runs that same binary.
  - The Mac apps update themselves with Sparkle (`AppUpdater`, in `apple/macOS` and in `apple/ServerApp`), from the latest release's feed (`SUFeedURL`). An update to the Server app brings its rad; the app asks `rad update --busy --json` which sessions an install would interrupt, and offers to wait until it quits. On launch it deletes the `rad-<version>` files that Update rad downloaded before 0.10.
  - The client is sandboxed, so Sparkle replaces it through its installer service (`SUEnableInstallerLauncherService`, and the `-spks` and `-spki` mach-lookup entitlements). `scripts/sign-sparkle.sh` signs Sparkle's helpers with Developer ID, as notarization requires.
  - Sparkle refuses to update an app that macOS runs from a temporary copy (App Translocation: its path has `/AppTranslocation/`), which happens to a downloaded app that wasn't moved into Applications on its own, such as both apps dragged in together. `xattr -dr com.apple.quarantine` on the app in Applications fixes it; the README and the release notes say so.
  - Builds from Xcode (version `0.1`) don't update. Sparkle compares `CFBundleVersion`, which tags set from the version as Android's `versionCode` (`CURRENT_PROJECT_VERSION`).
  - The release asset names (`rad-<os>-<arch>.tar.gz`, `SHA256SUMS`, and the apps' `Remote-Agent-macOS-appcast.xml` and `Remote-Agent-Server-macOS-appcast.xml`) are what the updaters look for, so change both together.
  - Tests use httptest, never GitHub. Don't run `rad update` on the developer's rad, its service, or agents-base unless asked. A scratch binary built with `-ldflags "-X remote-agent/internal/api.Version=0.1.2"` is safe.
- **Commits:** tests green first. Don't commit `server/bin/`, `apple/build/`, the `.xcodeproj`, `apple/Local.xcconfig`, `web/node_modules`, `web/dist`, Android's `build/` folders, `.gradle`, `.kotlin`, `local.properties` or a keystore.

## Testing

```bash
cd server && gofmt -l internal cmd && go vet ./... && go test -race ./...
cd apple/RAKit && swift test
cd web && npm ci && npm run typecheck && npm test && npm run build
cd android && ./gradlew :core:test :app:testDebugUnitTest :app:lintDebug :app:assembleDebug
cd apple && xcodegen && xcodebuild -project RemoteAgent.xcodeproj -scheme RemoteAgent-iOS \
  -destination 'platform=iOS Simulator,name=iPhone 17' -derivedDataPath build -skipPackagePluginValidation build
cd apple && xcodebuild -project RemoteAgent.xcodeproj -scheme RemoteAgent-macOS \
  -destination 'platform=macOS' -derivedDataPath build -skipPackagePluginValidation build
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

Fake prompt keywords are listed in the README: `write <file>`, `question`, `slow`, `fail`, `screenshot`, `picture`, `markdown`, `background [duration]`, and the prompt `/compact`. Prefer the fake harness for UI work. A real harness spends tokens; use it only when the task is about that adapter.

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

- **The simulator's window:** Xcode 27 has no Simulator.app. The simulators show in **DeviceHub** (`/Applications/Xcode.app/Contents/Applications/DeviceHub.app`, bundle id `com.apple.dt.Devices`); `simctl` works without it. Open it with `open -a "Device Hub"`: opened by its path, or with `-g`, it can run without a window.
- **Tapping:** DeviceHub doesn't pass the app's accessibility tree to the Mac's AX API, but it takes clicks, with Accessibility access:
  - Reach it by its pid, `pgrep -x DeviceHub`, with `AXUIElementCreateApplication(pid)`. Looked up by bundle id, `NSRunningApplication` can return a process whose element has no attributes. Its sidebar lists the devices; setting `AXSelected` on a device's `AXOutlineRow` shows that device.
  - Its `AXGroup` with the subrole `iOSContentGroup` is the device's screen. Map a point on the device (iPhone 17: 402×874 points; `simctl io … screenshot` is in pixels, 3×) to the screen as `origin + point / deviceSize × size` of that element's frame.
  - Bring DeviceHub to the front, and post a mouse down and up there with `CGEvent(…).post(tap: .cghidEventTap)`. This moves the Mac's pointer.
  - Keys arrive as key codes: a key event's Unicode string is ignored, so type with each character's virtual key code.
  - When done, `xcrun simctl shutdown all` and `osascript -e 'tell application id "com.apple.dt.Devices" to quit'`.
- **Pairing:** the camera and QR scanning don't work in the simulator. Pair with the deep link above. On iPhone the pairing sheet appears on the server list, so go back to the list if a server is open.
- **iPad:** the app picks its layout by device. iPhone gets the stack in `ServersView`, which pushes a server's sessions (`iOS/ServerHomeView.swift`). iPad gets `ServerSplitView`, as the Mac does: the sidebar in the chosen layout, with the chosen session beside it. Both reuse `SessionView`, so check changes to it on an iPad simulator too, such as `iPad Pro 13-inch (M5)`. A launch argument tries a sidebar layout without saving it: `xcrun simctl launch booted dev.remote-agent.app -sidebarLayout columns`. With two simulators booted, use the device name in place of `booted`.
- **Speech:** the dictation models don't run in the simulator, so the mic button ends with "The speech model for your language isn't available on this device". The UI states and permission prompts can still be checked there. Two ways to check dictation:
  - On a device.
  - On the Mac: `LiveTranscription.swift` has no UIKit, so a small driver can feed it a recording made with `say -o clip.aiff "…"`, and the Mac's installed dictation models transcribe it. Build the driver with `xcrun swiftc -swift-version 6 -parse-as-library apple/Shared/LiveTranscription.swift driver.swift`.
- **Screenshots:** they can lag a push or pop animation. Wait about a second, or take a second screenshot, before concluding a tap did nothing.
- **Keychain after signing changes:** setting or changing `DEVELOPMENT_TEAM` changes the keychain access group, so the simulator app loses tokens it saved earlier. Servers then show "Not connected", and the log shows `NSURLErrorDomain -1011` on the WebSocket. Re-pair; the server doesn't need to revoke anything.
- **Cleanup:** the app may already be paired with the developer's own server. Add your scratch server next to it. When done, remove the scratch server in the app (… → Remove server, which also unpairs it on rad), then stop your scratch rad.
- **Coverage:** the simulator doesn't cover Tailscale, LAN pairing, the camera or microphone input. Say so when a change depends on them.

## Emulator

Gradle needs the Android SDK and a JDK. Android Studio's will do; tell Gradle where they are if your shell doesn't:

```bash
export ANDROID_HOME=~/Library/Android/sdk JAVA_HOME="/Applications/Android Studio.app/Contents/jbr/Contents/Home"
```

Run the emulator read-only, so tests leave the developer's virtual device as it was, and point it at a scratch rad. `adb reverse` makes the emulator's `127.0.0.1:7499` the host's, so the link `rad pair --print-url` prints works unchanged.

```bash
$ANDROID_HOME/emulator/emulator -avd <name> -read-only -no-snapshot -no-window &
adb wait-for-device && adb reverse tcp:7499 tcp:7499
(cd android && ./gradlew :app:installDebug)
adb shell am start -n dev.remoteagent.app.debug/dev.remoteagent.app.MainActivity
adb shell "am start -a android.intent.action.VIEW -d '$(RAD_HOME=$SCRATCH/radhome server/bin/rad pair --print-url)' dev.remoteagent.app.debug"
adb exec-out screencap -p > /tmp/shot.png
adb logcat -d -s RemoteAgent:D AndroidRuntime:E   # debug builds log sync events under RemoteAgent
```

- **Quote the link:** `adb shell` hands its command to the device's shell, where the link's `&` would end the command.
- **Wait for the app** before opening a link at launch; a link that arrives while it starts can be lost.
- **Driving it:** `android/e2e/lib.sh` has `tap`, `fill`, `wait_for`, `texts` and `shot`, which find views in a `uiautomator dump` by text, content description or test tag (the app shows its test tags as resource ids).
  - A dialog, menu or full-screen sheet is a window of its own, and the dump shows only the one in front.
  - Type only once the keyboard is up (`fill` waits): keys sent before it attaches go to the window, where Space presses the focused button, such as a sheet's Cancel.
  - Turn the animation scales off while driving it.
- **Smoke test:** `android/e2e/smoke.sh` walks the app through the fake harness's keywords, a worktree session with its changes and revert, the terminal and the dark theme, saving screenshots to `$SCRATCH/shots`. It removes the server at the end, which unpairs it.
- **Tablet:** `adb shell wm size 2560x1600 && adb shell wm density 320` gives an expanded width; `wm size reset` and `wm density reset` undo it.
- **Release build:** R8 shrinks it. Try it after adding a library or a class made by reflection: sign `assembleRelease` with a scratch keystore and run the smoke test with `PKG=dev.remoteagent.app`.
- **Cleanup:** remove the scratch server in the app, `adb reverse --remove-all`, `adb emu kill`, then stop the scratch rad. A read-only emulator forgets the app when it stops, without unpairing; revoke leftover devices on the scratch rad with `rad devices revoke`.
- **Coverage:** the emulator has no camera or speech models, so scanning a QR code and dictation itself need a phone; the scanner opening and the dictation states and permission prompt can be checked here. Say so when a change depends on them.

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
- **Its data is the developer's:** the app shares the iOS bundle ID, keeps its settings in `~/Library/Containers/dev.remote-agent.app` and its tokens in the login keychain. The developer may have paired it with their own server. Add your scratch server next to it, and remove it when done (right-click its name in the sidebar → Remove).
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
- **Updates:** a build from Xcode is version `0.1`, a development build, so it offers no updates. To try them on either app:
  - Make a scratch key pair (CryptoKit's `Curve25519.Signing.PrivateKey`: the private key is its `rawRepresentation` in base64, the public key its public key's), and build an older and a newer version under a test bundle ID, so that they don't share the developer's settings: `xcodebuild … DEVELOPMENT_TEAM= CODE_SIGN_IDENTITY=- MARKETING_VERSION=0.0.1 CURRENT_PROJECT_VERSION=199 SPARKLE_PUBLIC_KEY=<public> PRODUCT_BUNDLE_IDENTIFIER=dev.remote-agent.updatetest build`, then `0.0.2` and `299`. The Server app takes `dev.remote-agent.updatetest.server`.
  - Zip the newer one with `ditto -c -k --keepParent`, write its feed with `SPARKLE_PRIVATE_KEY=<private> apple/scripts/appcast.sh`, serve both on 127.0.0.1, and launch a copy of the older one with `--args -SUFeedURL http://127.0.0.1:<port>/<feed>`. Launch the client with `-notifications NO` too, and write `notifications` and `SUFeedURL` into its container's defaults, so that the copy Sparkle relaunches doesn't ask for notifications.
  - A Server app that an update relaunches runs without `RAD_HOME`, on the developer's config, and deletes the developer's downloaded rad. Don't let it relaunch: keep a session busy (`sleep 900 | rad debug run "write a.txt"` waits at the approval), choose Later when it asks, and quit; the update then installs without relaunching.
- **Leave the Mac's settings alone:** don't press Allow in the Permissions window, answer the system prompts, change Privacy & Security, or turn on Start at Login. They change the developer's privacy settings and login items. Checking the statuses is safe; the window only checks what macOS can report without asking.
- **To see which app macOS holds responsible for an agent's request:** `log stream --predicate 'subsystem == "com.apple.TCC"'`.
