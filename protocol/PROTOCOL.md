# Remote Agent wire protocol (v1)

This is the contract between `rad` (the Go server on the dev machine) and its clients (the iOS app, `rad debug`).
Go types in `internal/model` and `internal/api` are the source of truth. Golden frames in `protocol/fixtures/` are decoded by both the Go and the Swift test suites.

## Transport

- **HTTP**: `http://<host>:7421`. The default port is 7421. rad listens on loopback and Tailscale addresses, plus LAN with `--lan`.
- **WebSocket**: `GET /v1/ws` with the header `Authorization: Bearer <deviceToken>`. Text frames carry one JSON object each.
- **Keepalive**: the server pings every 25 s. If a client is revoked, its socket is closed with code `4001` within 30 s.

## Pairing

1. `rad pair` prints a QR code and link:
   `remoteagent://pair?v=1&name=<host>&code=<one-time code>&url=<base>&url=<base>…`
   - The `url` values are base URLs in preference order: Tailscale, then LAN, then loopback.
   - Codes are single-use and expire after 10 minutes.
2. The client tries each `url` in order:
   `POST <url>/v1/pair {"code": "...", "deviceName": "Sasha's iPhone"}`
3. A successful response returns `200`:
   `{"serverId", "name", "protocolVersion", "version", "token", "deviceId"}`
   - The client stores `token` in the Keychain and remembers the URL that worked, plus the others as fallbacks.
4. Failures:
   - `401 {"code":"unauthorized"}`: the code is wrong or expired.
   - `GET /v1/health` responds without auth and returns `{"serverId","name","protocolVersion","version"}`.

## Frames

| Direction | Shape |
|---|---|
| client → server request | `{"id": 1, "method": "session.prompt", "params": {...}}` |
| server → client response | `{"id": 1, "result": {...}}` or `{"id": 1, "error": {"code": "...", "message": "..."}}` |
| server → client notification | `{"method": "events" \| "synchronized" \| "resync", "params": {...}}` |

`id` is any JSON number or string chosen by the client. The server handles requests concurrently, so responses may arrive out of order.

**Error codes**:
- `invalid`, `not_found`, `conflict`, `unavailable`
- `unauthorized`, `method_not_found`, `internal`

### Idempotency

Mutating requests take a client-generated `commandId` (a UUID): `session.create`, `session.prompt` and `approval.respond`.
If the client retries with the same `commandId`, it gets the original result back and nothing is applied twice. Always retry with the same id after a dropped connection.

## Streams and sync

State is delivered as **streams of change events**. There are two kinds:
- `index`: projects, plus a summary of every session (status, title, timestamps).
- `session:<sessionId>`: the session itself, its turns and its transcript items.

Each stream has a per-stream, gap-free `seq`. The server keeps only the **latest event per entity**, so replaying a stream from `seq` 0 is a full snapshot.

Subscribing works like this:

1. Send `subscribe {"stream": "...", "afterSeq": N}`. Use `0`, or omit it, when you have no cache.
2. The server sends zero or more `events` notifications with `{"stream", "events": [Event…]}`.
   - These are every entity changed after `N`, in seq order.
   - Replayed batches are **not** contiguous, because older versions of an entity were compacted away.
   - **Buffer them**; do not apply them yet.
3. The server sends `synchronized {"stream", "seq": S, "reset": bool}`.
   - If `reset` is true, drop all cached state for the stream. This happens when `afterSeq` was 0, or when it was ahead of the server.
   - Then apply the buffered replay and set your cursor to `S`.
4. The `subscribe` response arrives: `{"stream", "seq": S}`.
5. Live `events` with `seq` values `S+1, S+2, …` follow `synchronized`. They **are** contiguous, and some may arrive before the `subscribe` response. Key off `synchronized`, not the response.
   - If you see a gap, or the server sends `resync {"stream"}` because you were too slow, call `subscribe` again with your last applied seq.

`unsubscribe {"stream"}` stops delivery.

### Event

```json
{"stream": "session:…", "seq": 42, "type": "item.upserted", "ts": "2026-10-09T01:02:03Z",
 "item": { …Item… }}
```

Each event carries exactly one of `project`, `session`, `turn` or `item`, or `id` for the `*.removed` types.

| `type` | payload | streams |
|---|---|---|
| `project.upserted` | `project` | index |
| `project.removed` | `id` | index |
| `session.upserted` | `session` | index and `session:<id>` |
| `session.removed` | `id` | index |
| `turn.upserted` | `turn` | `session:<id>` |
| `item.upserted` | `item` | `session:<id>` |

Clients must ignore unknown event types and unknown fields.

## Entities

```ts
Project { id, path, name, isGitRepo, createdAt }

Session {
  id, projectId, harness,            // "claude" | "codex" | "acp:<id>" | "fake"
  model?, effort?, mode?,            // ids from harness.list; model and effort are what was requested (absent: the harness default)
  workspace: { kind: "root"|"worktree", path, branch?, baseRef? },
                                     // branch: checked out in path, refreshed at each turn start/end; absent if detached
  status: "idle"|"running"|"awaiting_approval"|"stopped"|"error",
  nativeId?, title, error?, archived,
  context?: { used, window? },       // tokens in the agent's context at its last model call; window absent if unknown
  modelInfo?: { id?, name?, effort? },
                                     // the model and reasoning effort the agent reported it runs with, once it
                                     // has started; name absent: look id up in harness.list models, else show id
  createdAt, updatedAt
}

Turn {
  id, sessionId, n,                  // n = 1, 2, … within a session
  status: "running"|"completed"|"interrupted"|"failed",
  checkpointBefore?, checkpointAfter?,   // git commit shas (git projects only)
  usage?: { inputTokens?, outputTokens?, cacheReadTokens?, cacheWriteTokens?, costUsd? },
  error?, startedAt, endedAt?
}

Item {
  id, sessionId, turnId?, parentItemId?,   // parentItemId: sub-agent output nests under its tool call
  order,                                   // transcript position; sort by this
  kind: "user_message"|"assistant_message"|"reasoning"|"tool_call"|"approval"|"plan"|"notice"|"error",
  status: "queued"|"in_progress"|"completed"|"failed"|"pending"|"resolved"|"cancelled"|"expired",
  text?,                                   // user_message, assistant_message (markdown), reasoning, notice, error
  tool?: { name, kind: "read"|"edit"|"execute"|"search"|"fetch"|"think"|"other",
           title?, input?, output?, exitCode?, paths? },
  approval?: Approval,
  plan?: { entries?: [{content, status: "pending"|"in_progress"|"completed"}], text? },
  createdAt, updatedAt
}

Approval {
  toolItemId?, toolName?, title, detail?, input?,
  options: [{ id, label, kind: "allow_once"|"allow_session"|"deny" }],
  special?: "plan" | "question",
  planText?,                                  // special = plan (markdown)
  questions?: [{ question, header?, multiSelect?, options: [{label, description?}] }],
  decision?: { optionId, message?, answers?, at }
}
```

**Streaming text.** Assistant text and reasoning stream as repeated `item.upserted` events for the same `id`, each carrying the **full** text so far. Updates are coalesced to at most 10 per second.

**Approvals.** A `kind = approval` item with `status = pending` needs an answer. Its status then becomes:
- `resolved` once answered
- `cancelled` if the turn ended or was interrupted
- `expired` if the server restarted

## Methods

| method | params | result |
|---|---|---|
| `server.info` | — | `{serverId, name, protocolVersion, version, roots, deviceId}` |
| `device.unpair` | — | `{}` (revokes the calling device's token) |
| `harness.list` | `{refresh?}` | `{harnesses: [HarnessInfo]}` |
| `fs.list` | `{path?}` (omit for roots) | `{path, parent?, entries: [{name, path, isGitRepo}]}` |
| `project.list` | — | `{projects}` |
| `project.add` | `{path}` (must be under a root) | `Project` |
| `project.remove` | `{id}` | `{}` (`conflict` if it has unarchived sessions) |
| `session.list` | — | `{sessions}` |
| `session.create` | `{commandId, projectId, harness, model?, effort?, mode?, workspace: {kind, branch?, baseRef?}, prompt?, title?}` | `Session` |
| `session.prompt` | `{commandId, sessionId, text}` | `Item` (the user message; `queued` if a turn is running) |
| `session.interrupt` | `{sessionId, force?}` | `{}` (`force` kills the agent process) |
| `session.setMode` | `{sessionId, mode}` | `{}` |
| `session.archive` | `{sessionId, removeWorktree?}` | `{}` |
| `approval.respond` | `{commandId, sessionId, approvalId, optionId, message?, answers?}` | `{}` |
| `subscribe` | `{stream, afterSeq?}` | `{stream, seq}` (see above) |
| `unsubscribe` | `{stream}` | `{}` |
| `git.branches` | `{projectId}` | `{branches, current}` |
| `git.turnDiff` | `{turnId, paths?}` | `Diff` |
| `git.sessionDiff` | `{sessionId, paths?}` | `Diff` |
| `git.revert` | `{sessionId, turn}` | `{files: [FileStat]}` (worktree sessions only) |

```ts
HarnessInfo {
  id, name, protocol: "claude"|"codex"|"acp"|"fake",
  installed, version?, authOk, hint?,          // hint explains what's missing
  models?: [{id, name, description?, efforts?: [Choice]}],  // a model's efforts, when listed, replace the harness's
  efforts?: [Choice],                          // reasoning efforts for the default model; absent: not selectable
  modes?: [Choice], defaultMode?,              // Choice = {id, name, description?}
  caps: { resume, interrupt, setMode, freeModel, modelSelect }
}

Diff { from, to, files: [FileStat], patch, truncated }   // patch: unified diff, ≤ 512 KiB
FileStat { path, oldPath?, status: "added"|"modified"|"deleted"|"renamed", additions, deletions, binary? }
```

- **Approval answers.** For a `question` approval, put `answers` (question text → chosen label; join multiple labels with `", "`) in the request, along with the option of kind `allow_once`.
- **Deny reasons.** For a deny, `message` is passed to the agent where the harness supports it.
