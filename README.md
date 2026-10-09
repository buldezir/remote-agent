# Remote Agent

Drive coding agents running on your computer from your iPhone.

- **`rad`**: a Go server that runs on the dev machine. It spawns agent CLIs as child processes, normalizes what they do into one event model, persists it in SQLite, and serves it over a WebSocket.
- **Remote Agent**: a native SwiftUI iOS app. You pair it by QR code. From the phone you can:
  - start sessions in any project
  - watch agents stream their work
  - approve or deny tool calls
  - answer the agent's questions
  - review per-turn git diffs and revert them

Supported harnesses:

| Harness | How rad talks to it |
|---|---|
| Claude Code | `claude --input-format stream-json --output-format stream-json --permission-prompt-tool stdio`: the same control protocol the Agent SDKs use |
| Codex | `codex app-server` (JSON-RPC over stdio) |
| Cursor, OpenCode, Gemini, … | [Agent Client Protocol](https://agentclientprotocol.com): `cursor-agent acp`, `opencode acp`, `gemini --acp` (configurable) |

Prior art: [Happy](https://github.com/slopus/happy) (mobile client and relay for Claude Code) and [T3 Code](https://github.com/pingdotgg/t3code) (local server that owns agent processes, worktrees and checkpoints).

## Quick start

```bash
go build -o bin/rad ./cmd/rad
./bin/rad serve          # first run writes ~/.config/remote-agent/config.toml and prints a pairing QR
```

On the phone, open Remote Agent and tap **Pair a server**, then scan the QR code. In the simulator, paste the link from `rad pair --print-url` instead.

The phone has to reach the computer. Two ways:

- **Tailscale (recommended):** rad listens on its Tailscale address (100.x) by default, and WireGuard encrypts the traffic.
- **Same Wi-Fi:** run `rad serve --lan`, or set `lan = true` in the config.

To keep rad running in the background on macOS:

```bash
./bin/rad install-launchagent      # logs: ~/Library/Logs/rad.log; --uninstall to remove
```

## Using it

- **New session:**
  1. Pick a project folder; you can browse anything under `roots` from the config.
  2. Pick an agent, a model and a permission mode.
  3. Optionally choose **Isolated git worktree**, which puts the agent on its own branch in `~/Library/Application Support/remote-agent/worktrees/…`.
- **Approvals:** cards appear inline. The buttons come from the agent itself, e.g. *Allow*, *Allow all edits this session*, *Deny*. When denying, you can give the agent a reason.
- **Questions and plans:** Claude's `AskUserQuestion` and `ExitPlanMode` show up as choice and plan cards.
- **Changes:** rad snapshots the workspace before and after every turn into hidden git refs (`refs/ra/cp/…`), without touching your index or HEAD. You can view each turn's diff or the whole session's.
- **Revert:** in worktree sessions you can revert to before any turn. The agent is told about it on the next prompt.
- **Stop:** this interrupts the current turn. *Force stop* kills the agent process. Idle agents are stopped after `idle_timeout` and resume transparently on the next prompt.

## CLI

```
rad serve [--lan] [--fake] [-v]   run the server (--fake adds a scripted test harness)
rad pair [--print-url]            new single-use pairing code (10 min)
rad devices                       list paired devices
rad devices revoke <id-prefix>    revoke one; its open connections close within 30s
rad debug run --harness claude --cwd DIR [--mode M] [--worktree] [--approve] [--diff] "prompt"
rad debug prompt <sessionId> "…"  follow-up turn, streamed to the terminal
rad debug call <method> '{json}'  raw RPC (see protocol/PROTOCOL.md)
```

## Configuration

`~/.config/remote-agent/config.toml`. Set `RAD_HOME=/some/dir` to keep the config and data together, e.g. for tests.

```toml
port = 7421
lan = false
roots = ["~/projects"]          # what the phone may browse and add as projects
idle_timeout = "30m"
acp = [                         # any ACP agent can be added here
  { id = "cursor",   name = "Cursor",   command = "cursor-agent", args = ["acp"] },
  { id = "opencode", name = "OpenCode", command = "opencode",     args = ["acp"] },
  { id = "gemini",   name = "Gemini",   command = "gemini",       args = ["--acp"] },
]
[harness.claude]
command = "claude"              # optional: args = [...], env = {...}
[harness.codex]
command = "codex"
```

## Security model

- A paired device can make agents run arbitrary commands on your machine. Treat device tokens like SSH keys.
- By default rad binds only to loopback and Tailscale addresses.
- Pairing codes are single-use and expire after 10 minutes.
- Tokens are 256-bit random values. Only their SHA-256 hash is stored, and the phone keeps the token in the Keychain.
- Revoking a device closes its live sockets.
- The phone can browse and add only folders under `roots`.
- Permission modes are enforced by each agent itself. rad never auto-approves on the agent's behalf.

## Layout

```
cmd/rad/                 CLI: serve, pair, devices, debug, install-launchagent
internal/model           domain types (Session, Turn, Item, Event…) = wire format
internal/store           SQLite; per-stream change feed (latest event per entity)
internal/events          live fan-out to subscribers
internal/orchestrator    session actors: prompts, approvals, turns, checkpoints, recovery
internal/harness/        adapter interface + claude/ codex/ acp/ fake/ proc/ jsonrpc/
internal/gitx            worktrees, checkpoints, diffs, revert (git CLI)
internal/api             HTTP pairing + WebSocket JSON-RPC
protocol/                PROTOCOL.md + golden fixtures shared by Go and Swift tests
ios/                     XcodeGen project; RAKit (protocol, client, stores) + SwiftUI app
```

## Development

```bash
go test ./... -race                       # server tests (adapters replay recorded CLI transcripts)
go test ./internal/api -update            # regenerate protocol/fixtures after changing wire types
cd ios/RAKit && swift test                # client protocol and sync tests
cd ios && xcodegen && xcodebuild -scheme RemoteAgent -destination 'platform=iOS Simulator,name=iPhone 17' build
```

To exercise the UI without spending tokens, run `rad serve --fake` and pick **Fake (scripted)**. Its prompt keywords are:
- `write <file>`: asks for approval, then writes the file
- `question`: asks a multiple-choice question
- `slow`: streams slowly, useful for testing interrupt
- `fail`: makes the turn fail

The raw protocol frames for each session are logged to `<data>/logs/<sessionId>.ndjson`.
