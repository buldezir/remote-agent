# AGENTS.md

Notes for coding agents working on this repo. What the project is and how it is laid out: [README.md](README.md). The wire contract: [protocol/PROTOCOL.md](protocol/PROTOCOL.md).

## Development process

- **Server:** Go in `cmd/rad` and `internal/`.
- **iOS client:** `ios/RAKit` holds the protocol, network client and stores, all testable with `swift test`. `ios/RemoteAgent` holds the SwiftUI views.
- **Changing a wire type:** keep these in step:
  - `internal/model/model.go`
  - `ios/RAKit/Sources/RAKit/Protocol/Models.swift`
  - `protocol/PROTOCOL.md`

  Then regenerate the fixtures with `go test ./internal/api -update`, and assert the new field in `ios/RAKit/Tests/RAKitTests/FixtureTests.swift`. Clients ignore unknown fields, so prefer adding optional fields over changing existing ones.
- **Changing a harness adapter:** never call real agent CLIs from tests. Adapter tests replay recorded transcripts:
  - `internal/harness/*/testdata/*.ndjson`, one `{"dir":"in"|"out","frame":…}` per line, driven by `internal/harness/replaytest`.
  - Assert on the emitted `harness.Event`s.
  - To exercise the orchestrator, extend `internal/harness/fake`.
- **Xcode project:** `ios/project.yml` is the source of truth. `RemoteAgent.xcodeproj` is generated and gitignored, and `Info.plist` is generated from `project.yml`, so edit permissions and plist keys there. Run `xcodegen` after adding files or changing the spec.
- **Swift 6 strict concurrency:**
  - Callbacks that run on other threads (AVAudioEngine taps, Speech results, URLSession delegates) must be created in `nonisolated` functions. A closure formed in `@MainActor` code is main-actor isolated and traps when called off the main thread.
  - Caches that view bodies fill lazily are `@ObservationIgnored`.
  - Navigation destinations are registered once at the root (`ServersView`), with values that carry the server id.
- **Commits:** tests green first. Don't commit `bin/`, `ios/build/` or the `.xcodeproj`.

## Testing

```bash
gofmt -l internal cmd && go vet ./... && go test -race ./...
cd ios/RAKit && swift test
cd ios && xcodegen && xcodebuild -project RemoteAgent.xcodeproj -scheme RemoteAgent \
  -destination 'platform=iOS Simulator,name=iPhone 17' -derivedDataPath build build
```

## Running rad locally

The developer may have their own `rad serve` running on the default port 7421, with real sessions and paired devices. Don't stop it, don't send prompts into its sessions, and don't change `~/.config/remote-agent`. Use a scratch instance instead.

1. Write `$SCRATCH/radhome/config.toml`:

   ```toml
   port = 7499
   fake = true                      # adds the scripted "Fake" harness (or pass --fake)
   roots = ["/path/to/scratch"]
   ```

2. Build and start the server:

   ```bash
   go build -o bin/rad ./cmd/rad
   RAD_HOME=$SCRATCH/radhome ./bin/rad serve &
   ```

3. Drive it:

   ```bash
   RAD_HOME=$SCRATCH/radhome ./bin/rad debug run --cwd $SCRATCH/proj [--worktree] "write a.txt"
   RAD_HOME=$SCRATCH/radhome ./bin/rad debug prompt <sessionId> "again"
   ```

Fake prompt keywords are listed in the README: `write <file>`, `question`, `slow`, `fail`. Prefer the fake harness for UI work. A real harness spends tokens; use it only when the task is about that adapter.

## Simulator

The simulator reaches the host's `127.0.0.1`, so a scratch rad on loopback works as-is.

```bash
APP=ios/build/Build/Products/Debug-iphonesimulator/RemoteAgent.app
xcrun simctl install booted $APP && xcrun simctl launch booted dev.remote-agent.app
xcrun simctl openurl booted "$(RAD_HOME=$SCRATCH/radhome ./bin/rad pair --print-url)"   # pair, then tap Pair
xcrun simctl io booted screenshot /tmp/shot.png
xcrun simctl spawn booted log show --last 2m --predicate 'subsystem == "dev.remote-agent.app"'
```

- **Pairing:** the camera and QR scanning don't work in the simulator. Pair with the deep link above. The pairing sheet appears on the server list, so go back to the list if a server is open.
- **Speech:** recognition doesn't work in the simulator; it fails with `kLSRErrorDomain 300`. Dictation can only be verified on a device. The UI states and permission prompts can still be checked.
- **Screenshots:** they can lag a push or pop animation. Wait about a second, or take a second screenshot, before concluding a tap did nothing.
- **Cleanup:** the app may already be paired with the developer's own server. Add your scratch server next to it. When done, remove the scratch server in the app (… → Remove server, which also unpairs it on rad), then stop your scratch rad.
- **Coverage:** the simulator doesn't cover Tailscale, LAN pairing, the camera or microphone input. Say so when a change depends on them.
