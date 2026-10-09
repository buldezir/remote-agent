# AGENTS.md

Notes for coding agents working on this repo. What the project is and how it is laid out: [README.md](README.md). The wire contract: [protocol/PROTOCOL.md](protocol/PROTOCOL.md).

## Development process

- **Server:** a Go module in `server/` (`cmd/rad` and `internal/`). Run Go commands from `server/`; it builds into `server/bin/rad`.
- **Apple clients:** everything is in `apple/`.
  - `apple/RAKit` holds the protocol, network client and stores, all testable with `swift test`.
  - Two app targets, `RemoteAgent-iOS` (iPhone and iPad) and `RemoteAgent-macOS`, both compile `apple/Shared`, the SwiftUI app. Each adds its own folder, `apple/iOS` or `apple/macOS`, with its `Info.plist` and the views only it uses: the QR scanner and the iPhone server list, or the Mac menu bar and entitlements (App Sandbox, network client, microphone, files the user picks).
  - A whole file for one platform goes in its folder. A small difference inside a shared file goes in `#if os(iOS)` / `#if os(macOS)`. `Shared/Views/Platform.swift` holds the shims, such as `inlineTitle()`, `plainTextInput()` and `Platform.deviceName`.
  - A third target, `RemoteAgentServer` in `apple/ServerApp`, is the Mac menu bar app that runs rad. It shares no code with the clients. Its last build phase, `apple/scripts/build-rad.sh`, compiles rad from `server/` into the app and signs it.
- **Changing a wire type:** keep these in step:
  - `server/internal/model/model.go`
  - `apple/RAKit/Sources/RAKit/Protocol/Models.swift`
  - `protocol/PROTOCOL.md`

  Then regenerate the fixtures with `cd server && go test ./internal/api -update`, and assert the new field in `apple/RAKit/Tests/RAKitTests/FixtureTests.swift`. Clients ignore unknown fields, so prefer adding optional fields over changing existing ones.
- **Images:** users upload with `POST /v1/images`, and prompts carry the ids. Adapters store images from agent output through `OpenOptions.Images` and put the refs on the item. Clients fetch them with `GET /v1/images/<id>`. The bytes never travel over the WebSocket.
- **Changing a harness adapter:** never call real agent CLIs from tests. Adapter tests replay recorded transcripts:
  - `server/internal/harness/*/testdata/*.ndjson`, one `{"dir":"in"|"out","frame":…}` per line, driven by `internal/harness/replaytest`.
  - Assert on the emitted `harness.Event`s.
  - To exercise the orchestrator, extend `server/internal/harness/fake`.
- **Xcode project:** `apple/project.yml` is the source of truth. `RemoteAgent.xcodeproj` is generated and gitignored, and the `Info.plist` and entitlements files are generated from `project.yml`, so edit permissions and plist keys there. Run `xcodegen` after adding files or changing the spec.
- **Signing:** the bundle ID lives in `apple/Signing.xcconfig`. It includes the developer's gitignored `apple/Local.xcconfig`, which holds their `DEVELOPMENT_TEAM`. The Mac app shares the bundle ID; without a team it is signed ad hoc.
- **App icon:** drawn by `apple/scripts/make-app-icon.swift`, for iOS and, in the macOS shape, for the Mac. Edit the script and rerun it; don't edit the PNGs.
- **Swift 6 strict concurrency:**
  - Callbacks that run on other threads (AVAudioEngine taps, Speech results, URLSession delegates) must be created in `nonisolated` functions. A closure formed in `@MainActor` code is main-actor isolated and traps when called off the main thread.
  - Caches that view bodies fill lazily are `@ObservationIgnored`.
  - Navigation destinations are registered once at the root (`ServersView`), with values that carry the server id.
- **Commits:** tests green first. Don't commit `server/bin/`, `apple/build/`, the `.xcodeproj` or `apple/Local.xcconfig`.

## Testing

```bash
cd server && gofmt -l internal cmd && go vet ./... && go test -race ./...
cd apple/RAKit && swift test
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

Fake prompt keywords are listed in the README: `write <file>`, `question`, `slow`, `fail`, `screenshot`. Prefer the fake harness for UI work. A real harness spends tokens; use it only when the task is about that adapter.

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
open "apple/build/Build/Products/Debug/Remote Agent.app"
open "$(RAD_HOME=$SCRATCH/radhome server/bin/rad pair --print-url)"   # opens the pairing sheet; press Pair
osascript -e 'tell application id "dev.remote-agent.app" to quit'      # fails while a sheet is open
```

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
- **Leave the Mac's settings alone:** don't press Allow in the Permissions window, answer the system prompts, change Privacy & Security, or turn on Start at Login. They change the developer's privacy settings and login items. Checking the statuses is safe; the window only checks what macOS can report without asking.
- **To see which app macOS holds responsible for an agent's request:** `log stream --predicate 'subsystem == "com.apple.TCC"'`.
