# Development

How to work on Remote Agent: the repository's layout, the tests, the scripted agent for trying the UI, and how releases are made. What the project does and how to install it: [README.md](README.md). The wire contract: [protocol/PROTOCOL.md](protocol/PROTOCOL.md). Notes for coding agents, with the rules that keep the clients in step and how to run each one against a scratch rad: [AGENTS.md](AGENTS.md).

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
protocol/                PROTOCOL.md + golden fixtures shared by the Go, Swift, web and Android tests
apple/                   XcodeGen project for the iPhone and iPad app, and the Server app
  RAKit/                 Swift package: protocol, network client, stores
  Shared/                the SwiftUI app
  iOS/                   its Info.plist and the iPhone's views: QR scanner, tabs…
  ServerApp/             Remote Agent Server, the menu bar app that runs rad on a Mac
gpui/                    the Mac app: Rust, GPUI and GPUI Kit
  src/store/             protocol, stores and sync rules (ported from RAKit)
  src/ui/                the views
  scripts/bundle.sh      builds Remote Agent Dev.app, or the release build
web/                     the web client: Vite, React and TypeScript
  src/protocol/          wire types, the WebSocket client, pairing
  src/store/             connection, session and server stores (ported from RAKit)
  src/ui/                the views
  e2e/smoke.mjs          drives the client in Chromium against a scratch rad
android/                 Gradle build for the Android app
  core/                  Kotlin library: protocol, network client, stores (ported from RAKit)
  app/                   the Compose app; ui/ are the views, platform/ the Android parts
  e2e/smoke.sh           drives the app on an emulator against a scratch rad
  scripts/symbols.py     writes the app's icons (Material Symbols) as vector drawables
```

**The terminal client, removed.** There used to be `ra`, a terminal client for the rad on the same machine (Go, Bubble Tea), with `rad install-ra` and `ra-<os>-<arch>.tar.gz` in releases. It was removed after commit `dd81763`, which still has it: `server/cmd/ra`, `server/internal/client` (its port of RAKit), `server/internal/tui`, `server/cmd/rad/installra.go`, the release workflow's ra build, and its sections in README.md and AGENTS.md. To bring it back, start from `git show dd81763 --stat` and restore those paths; its client logic will need the rules the other clients gained since.

## Tests

```bash
cd server && go test ./... -race              # server tests (adapters replay recorded CLI transcripts)
cd server && go test ./internal/api -update   # regenerate protocol/fixtures after changing wire types
cd apple/RAKit && swift test                  # client protocol and sync tests
cd web && npm run typecheck && npm test       # web client types, protocol and sync tests
cd android && ./gradlew :core:test :app:testDebugUnitTest :app:lintDebug :app:assembleDebug   # Android client
cd apple && xcodegen && xcodebuild -scheme RemoteAgent-iOS -destination 'platform=iOS Simulator,name=iPhone 17' build
cd gpui && cargo fmt --check && cargo clippy --all-targets && cargo test   # Mac app
cd apple && xcodebuild -scheme RemoteAgentServer -destination 'platform=macOS' build
```

## The fake agent

To exercise the UI without spending tokens, run `rad serve --fake` and pick **Fake (scripted)**. Its prompt keywords are:
- `write <file>`: asks for approval, then writes the file
- `question`: asks a multiple-choice question
- `slow`: streams slowly, useful for testing interrupt and steering: a prompt steered in cuts the reply short at its next word, and the turn answers it with *Steered: …*
- `fail`: makes the turn fail
- `screenshot`: runs a tool that returns an image
- `tools`: works through a long run of tool calls, as an agent fixing a test does: commands, reads, a search, a fetch and edits, with its thinking between them, and a test that fails before it passes; with `slow`, each call takes a second and a half
- `picture`: writes `picture-<turn>.png` in the working directory and shows it in the reply, as agents are told to
- `markdown`: adds Markdown to the reply: a heading, lists, a task list, a code block, a quote and a table
- `background [duration]`: leaves a task running in the background (5s by default, or for example `background 1m`), then starts a turn of its own to report on it
- `logout`: fails the turn as an agent whose login expired does, so Server Settings shows Fake as signed out
- `enter worktree [name]`: moves into a git worktree of its own, `.claude/worktrees/<name>` on the branch `worktree-<name>`, as Claude Code's EnterWorktree does, so the session's branch and changes follow it there; `exit worktree` moves back
- `/compact`: marks the transcript with *Context compacted* and reports less context in use
- `limit`: fails the turn on the plan's 5-hour limit, as Claude Code and Codex do when it's spent; the next turn starts the 5 hours afresh
- `limits <n>`: sets the 5-hour limit's use to n%, such as `limits 85` to see the gauge turn amber

Its plan's usage limits are shared by its sessions, as an account's are: rad reads them as it starts (5h 12%, week 34%), and each turn uses a little more. It also says how many images came with a prompt, and a fork's first reply says which conversation it was forked from. In every folder it has two sessions of its own to continue, as if it ran them in a terminal: "Fix the login redirect", from two hours ago, and "Add a dark theme to the settings page", written to just now, so the list warns about it. The first reply in one you picked up says so. `rad debug run --image shot.png "…"` attaches one from the command line.

In Server Settings, Fake's **Update** prints a progress line and finishes in a second, and its **Sign In** shows a link and asks for a code: any code signs it in. As the system agent, it names a session "Named: " and the prompt's first words, writes "Change " and the files as a commit's message, and titles a pull request "Fake: " and its latest commit's subject.

## Debugging

The raw protocol frames for each session are logged to `<data>/logs/<sessionId>.ndjson`.

## Building

The app icon is drawn by `apple/scripts/make-app-icon.swift`. Run it again after changing it:

```bash
cd apple && swift scripts/make-app-icon.swift Shared/Assets.xcassets/AppIcon.appiconset
```

The Mac app's development build (`gpui/scripts/bundle.sh`) is an app of its own, **Remote Agent Dev**, with its own servers, so it sits in Applications beside a release. Instead of Sparkle it updates from the Mac that built it, over ssh (**Check for Update…**); see [gpui/README.md](gpui/README.md).

For a separately installed Android test build, run `cd android && ./gradlew :app:assembleBeta`. The debug-signed APK is `android/app/build/outputs/apk/beta/app-beta.apk`, labeled **Remote Agent Beta**, with package ID `dev.remoteagent.app.beta` and its own settings and pairing tokens.

The Mac client's rustfmt, clippy and tests run on pushes to `main` that touch `gpui/` or `protocol/`, in [`.github/workflows/gpui.yml`](.github/workflows/gpui.yml), not in releases. That workflow also fills the Rust cache the release build reads: a tag's run can only read caches saved by `main` or by that tag itself, so without it each release compiled every crate afresh.

The web client's image, `ghcr.io/buldezir/remote-agent-web`, is built by [`.github/workflows/web-image.yml`](.github/workflows/web-image.yml) only when `web/` changed: a tag that leaves `web/` as it was puts its tags on the previous release's image.

## Releasing

To release, push a tag such as `v0.2.0`. [`.github/workflows/release.yml`](.github/workflows/release.yml) runs the server tests, builds rad for Linux and macOS, both Mac apps for Apple silicon and the Android app, and publishes them as that tag's release with a `SHA256SUMS` file. A tag with a hyphen, such as `v0.3.0-beta.1`, becomes a prerelease.

The Android APK is signed with a key kept in the repository's secrets. Create it once, outside the repo, and keep a backup: Android installs an update only over an app signed with the same key, so with a new key everyone has to uninstall first.

```bash
keytool -genkeypair -keystore ~/remote-agent-release.jks -alias remote-agent -keyalg RSA -keysize 4096 -validity 10000
base64 -i ~/remote-agent-release.jks | gh secret set ANDROID_KEYSTORE_BASE64
gh secret set ANDROID_KEYSTORE_PASSWORD
gh secret set ANDROID_KEY_ALIAS      # remote-agent
gh secret set ANDROID_KEY_PASSWORD
```

The Mac apps are signed with a Developer ID Application certificate and notarized, which takes a paid Apple Developer account. Create the certificate in Xcode › Settings › Accounts › Manage Certificates, export it from Keychain Access as a `.p12`, and make an App Store Connect API key (Users and Access › Integrations › Team Keys, with Developer access) for the notary service:

```bash
base64 -i DeveloperID.p12 | gh secret set MACOS_CERTIFICATE_P12_BASE64
gh secret set MACOS_CERTIFICATE_PASSWORD
gh secret set APPLE_TEAM_ID              # from developer.apple.com › Membership details
base64 -i AuthKey_<KEYID>.p8 | gh secret set NOTARY_API_KEY_BASE64
gh secret set NOTARY_API_KEY_ID
gh secret set NOTARY_API_ISSUER_ID
```

The Mac apps update themselves with [Sparkle](https://sparkle-project.org). Each release carries a feed for each app (`Remote-Agent-macOS-appcast.xml`, `Remote-Agent-Server-macOS-appcast.xml`) that says where its zip is, signed with an EdDSA key. The apps install only updates signed with that key, so make it once and keep a backup of the private half; its public half is `SPARKLE_PUBLIC_KEY` in `apple/project.yml`, and in `gpui/scripts/bundle.sh`.

```bash
mkdir -p ~/sparkle && curl -fsSL https://github.com/sparkle-project/Sparkle/releases/download/2.10.0/Sparkle-2.10.0.tar.xz | tar -xf - -C ~/sparkle
~/sparkle/bin/generate_keys --account remote-agent         # prints the public key
~/sparkle/bin/generate_keys --account remote-agent -x ~/sparkle/private-key
gh secret set SPARKLE_PRIVATE_KEY < ~/sparkle/private-key
```

To try the updates before a release, run the workflow by hand with a version, such as `0.10.0-test.1`: its Mac apps then offer and take updates, like a release's.

The iPhone and iPad app goes to TestFlight from a Mac where Xcode › Settings › Accounts has an Apple ID on the team in `apple/Local.xcconfig`, after its app record exists in App Store Connect (bundle ID `dev.remote-agent.app`):

```bash
apple/scripts/testflight.sh 0.17.0      # or 0.17.0-beta.1, which goes up as 0.16.0 build 170001
```

It archives the app and uploads it; Xcode signs it with the team's Apple Distribution certificate, making it the first time. External testers get a new version only after Apple's beta review, but a new build of it without one, so the release is in the build number and the version changes only with the major: 0.x goes up as 0.16.0, 1.x as 1.0.0. App Store Connect takes each build number once, so each release or beta goes up once.
