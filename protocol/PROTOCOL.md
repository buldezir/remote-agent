# Remote Agent wire protocol (v1)

This is the contract between `rad` (the Go server on the dev machine) and its clients (the iOS and Mac apps, the Android app, the web client, `rad debug`).
Go types in `server/internal/model` and `server/internal/api` are the source of truth. Golden frames in `protocol/fixtures/` are decoded by the Go, Swift, web and Android test suites.

## Transport

- **HTTP**: `http://<host>:7421`. The default port is 7421. rad listens on loopback and Tailscale addresses, plus LAN with `--lan`.
- **WebSocket**: `GET /v1/ws` with the header `Authorization: Bearer <deviceToken>`. Text frames carry one JSON object each.
  - Browsers can't set that header, so a web page offers the token as a subprotocol instead: `new WebSocket(url, ["rad.v1", "rad.token.<deviceToken>"])`. rad answers with `rad.v1`.
- **Web pages**: rad accepts browser requests only from the origins in its config's `web_origins`, by default pages on `localhost` and `127.0.0.1`.
  - Patterns match the origin's host (`localhost:*`) or, with a scheme, the whole origin (`https://agents.example.com`). `[]` allows none.
  - For those origins rad answers CORS preflights, including Chrome's Private Network Access, and accepts WebSocket upgrades. Other origins get no CORS headers, and their upgrades get `403`.
  - Requests without an `Origin` header (the apps, `rad debug`) aren't affected.
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
   - The client stores `token` securely (the Keychain; on Android, encrypted with a Keystore key) and remembers the URL that worked, plus the others as fallbacks.
   - The web client takes the same link, pasted, or in its page's fragment as `#pair=<link, URL-encoded>`. `rad pair --web <web client URL>` prints such a link.
4. Failures:
   - `401 {"code":"unauthorized"}`: the code is wrong or expired.
   - `GET /v1/health` responds without auth and returns `{"serverId","name","protocolVersion","version"}`.

## Images

Images travel over HTTP, beside the WebSocket, with the same `Authorization: Bearer <deviceToken>` header. A missing or revoked token gets `401 {"code":"unauthorized"}`.

- **Upload**: `POST /v1/images`, with the image's bytes as the body. rad tells the type from the bytes and takes PNG, JPEG, GIF and WebP up to 20 MB.
  - `200` returns an `ImageRef`. Put its `id` in `session.prompt` or `session.create`.
  - `400 {"code":"invalid"}` means the body isn't one of those types, and `413` means it is too large.
  - Uploading the same bytes again returns the same `id`.
- **Download**: `GET /v1/images/<id>` returns the bytes with their `Content-Type`, or `404`.
  - An id names its content, so it never changes. Clients can cache it for good: the response says `Cache-Control: private, max-age=31536000, immutable`.
  - A web page can't put the header on an `<img src>`, so it fetches the bytes with `fetch` and shows them from a blob URL.
- **Agents** see attached images inline where they can. Agents that can't take images in a prompt (some ACP agents) get the files' paths on the server instead.
- **Images in replies**: rad tells agents that the user isn't at the server, and to show an image file as a Markdown image with its path, such as `![Screenshot](/tmp/shot.png)`, rather than read it. rad keeps a copy of each PNG, JPEG, GIF or WebP file a reply links to that way (outside code), points the link at it as `rad-image:<id>`, and lists it in the item's `images`. A link to anything else stays as the agent wrote it.
- **Sizing**: agents' APIs reject very large images (Claude: 5 MB, 8000 px on a side). Clients should scale and re-encode before uploading. The iOS and Mac apps send at most 2048 px on the long side.

```ts
ImageRef { id, mimeType, width?, height?, size }
// id: SHA-256 of the bytes plus an extension, "<64 hex>.png"; width/height in pixels, absent when unknown (WebP)
```

## Terminal

A session's shell runs on the server, in the session's folder (its worktree, for a worktree session), and keeps running when no client is attached. Each session has at most one. It ends when the user exits it or closes it, when the session is archived, or when rad stops. Clients show the menu item only when `server.info` returns `terminal: true`.

- **Socket**: `GET /v1/terminal?session=<id>&cols=<n>&rows=<n>`, a WebSocket with the subprotocol `rad.terminal.v1`.
  - Auth is the same as for `/v1/ws`: the `Authorization` header, or in a browser the subprotocols `["rad.terminal.v1", "rad.token.<deviceToken>"]`. A missing or revoked token gets `401` before the upgrade. Keepalive and `4001` work the same too.
  - `cols` and `rows` are the client's size, kept between 2 and 1000; they default to 80×24.
  - `create=1` starts a shell if the session has none. Only the user's own action sends it, so a reconnect never starts a shell the user would take for the old one.
  - `id=<shell id>&offset=<n>` resumes: the stream continues from the client's offset, if that shell is still running and the offset is still in its buffer.
- **Frames**: binary frames carry the terminal's bytes, the shell's output from rad and the user's input from the client. Clients send input in frames of 16 KiB or less. Text frames carry one `TerminalMessage` each.
- **From rad**:
  - `attached` comes first. With `reset`, the client clears its screen (a full reset, `CAN ESC c`), and the output that follows starts at `offset`. Without it, the output continues where the client left off.
  - Output counts towards the stream's offset, byte by byte. A client keeps `offset` from `attached` or `reset` plus the bytes since, and resumes with it.
  - `reset` comes when the client has fallen behind the buffer: the client clears its screen again, and the output starts again at `offset`.
  - `exit` comes after the last output, with the shell's `exitCode` (`128 + signal` if a signal ended it). rad then closes the socket.
  - `error` comes in place of `attached`, and rad closes the socket. Its `code` is `not_found` (no such session), `archived`, `no_dir` (the session's folder is gone), `gone` (no shell, and no `create`), or `unavailable` (rad is shutting down, or the shell didn't start). Clients go by this frame, not the close code.
- **From the client**: `resize` with `cols` and `rows`, whenever its size changes; with two clients attached, the last one wins. `close` ends the shell for everyone.
- **Replay**: rad keeps the shell's last 256 KiB of output. A client that starts afresh gets it from the first line break, preceded by the sequences that restore the terminal modes in effect there: the alternate screen, cursor keys and keypad, mouse and focus reports, bracketed paste, autowrap and the cursor's visibility. Then the program in the foreground is told to redraw (`SIGWINCH`).
  - Queries in output written before the client attached, the replay or what a resuming client missed, come blanked out: each byte of a sequence that asks the terminal for an answer (device attributes, `XTVERSION`, `DECRQM`, status and window reports, colour and clipboard queries, `DECRQSS`, `XTGETTCAP`, kitty keyboard flags and graphics) is a NUL. Their programs have had their answers or stopped waiting, so a terminal answering again would type into the shell. Output from after the client attached comes as it is.
- **Environment**: the user's login shell (`-l`), with `TERM=xterm-256color` and `COLORTERM=truecolor`, and without the variables of the terminal rad was started in.

```ts
TerminalMessage {
  type: "attached" | "reset" | "exit" | "error"   // from rad
      | "resize" | "close",                       // from the client
  id?, offset?, reset?,      // attached; offset also on reset
  cols?, rows?,              // attached, resize
  exitCode?,                 // exit
  code?, message?            // error
}
```

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
| `session.removed` | `id` | index and `session:<id>` (the session was deleted) |
| `turn.upserted` | `turn` | `session:<id>` |
| `item.upserted` | `item` | `session:<id>` |

Clients must ignore unknown event types and unknown fields.

## Entities

```ts
Project { id, path, name, isGitRepo, createdAt }

Session {
  id, projectId, harness,            // "claude" | "codex" | "pi" | "acp:<id>" | "fake"
  model?, effort?, mode?,            // ids from harness.list; model and effort are what was requested (absent: the harness default)
  workspace: { kind: "root"|"worktree", path, branch?, baseRef? },
                                     // branch: checked out in path, refreshed at each turn start/end; absent if detached
  status: "idle"|"running"|"awaiting_approval"|"stopped"|"error",
  nativeId?, title, error?, archived,
  context?: { used, window? },       // tokens in the agent's context at its last model call; window absent if unknown
  modelInfo?: { id?, name?, effort? },
                                     // the model and reasoning effort the agent reported it runs with, once it
                                     // has started; name absent: look id up in harness.list models, else show id
  backgroundTasks?: [{ id, kind: "command"|"agent"|"other", description }],
                                     // what the agent left running (a command it watches, a subagent), while its
                                     // process runs; absent: none
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
  images?: [ImageRef],                     // user_message: attached to the prompt; tool_call: in the tool's output (a screenshot, an image file read);
                                           // assistant_message: the rad-image:<id> links in its text, in order
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

**Background tasks.** An agent may leave a command or a subagent running after its turn ends, and report on it later. While one runs, the session lists it in `backgroundTasks`, and rad keeps the agent running even when the session is idle. When a task ends between turns, the agent starts a turn of its own: a turn whose first item is a `notice` ("Background task finished: …") rather than a user message. It runs, queues prompts and stops like any other. A background subagent's items stay in the turn that started them and keep updating after it ends. Claude Code is the only harness that reports background tasks so far.

**Approvals.** A `kind = approval` item with `status = pending` needs an answer. Its status then becomes:
- `resolved` once answered
- `cancelled` if the turn ended or was interrupted
- `expired` if the server restarted

## Methods

| method | params | result |
|---|---|---|
| `server.info` | — | `{serverId, name, protocolVersion, version, roots, deviceId, terminal?}` (`terminal`: `GET /v1/terminal` is there) |
| `device.unpair` | — | `{}` (revokes the calling device's token) |
| `harness.list` | `{refresh?}` | `{harnesses: [HarnessInfo]}`, only the ones installed on the server |
| `harness.update` | `{harness}` | `Task` (runs the agent's own updater; `caps.update`) |
| `harness.login` | `{harness}` | `Task` (signs the agent in again; `caps.login`) |
| `server.settings` | — | `ServerSettings` |
| `server.setSettings` | `ServerSettings` | `ServerSettings` (replaces them all; `invalid` for an agent that can't be the system agent) |
| `server.checkUpdate` | `{refresh?}` | `UpdateInfo` (`refresh` looks again rather than use the answer of the last 10 minutes) |
| `server.update` | — | `Task` (`unavailable` when rad can't update, `conflict` when it is up to date) |
| `task.list` | — | `{tasks: [Task]}`, running and lately ended, oldest first |
| `task.get` | `{id}` | `Task` (`not_found` once forgotten, as after a restart) |
| `task.input` | `{id, text}` | `{}` (types `text` and Return into the task's command) |
| `task.cancel` | `{id}` | `{}` |
| `fs.list` | `{path?}` (omit for roots) | `{path, parent?, entries: [{name, path, isGitRepo}]}` |
| `project.list` | — | `{projects}` |
| `project.add` | `{path}` (must be under a root) | `Project` |
| `project.remove` | `{id}` | `{}` (`conflict` if it has unarchived sessions) |
| `session.list` | — | `{sessions}` |
| `session.create` | `{commandId, projectId, harness, model?, effort?, mode?, workspace: {kind, branch?, baseRef?}, prompt?, images?, title?}` | `Session` |
| `session.commands` | `{sessionId}` | `{commands: [{name, description?, kind?, scope?}]}` |
| `session.prompt` | `{commandId, sessionId, text, images?}` | `Item` (the user message; `queued` if a turn is running) |
| `session.interrupt` | `{sessionId, force?}` | `{}` (`force` kills the agent process) |
| `session.sendNow` | `{sessionId, itemId}` | `{}` (see below) |
| `session.setMode` | `{sessionId, mode}` | `{}` |
| `session.archive` | `{sessionId, removeWorktree?}` | `{}` (also ends the session's shell) |
| `session.delete` | `{sessionId, removeWorktree?}` | `{}` (see below) |
| `approval.respond` | `{commandId, sessionId, approvalId, optionId, message?, answers?}` | `{}` |
| `subscribe` | `{stream, afterSeq?}` | `{stream, seq}` (see above) |
| `unsubscribe` | `{stream}` | `{}` |
| `git.branches` | `{projectId}` | `{branches, current}` |
| `git.turnDiff` | `{turnId, paths?}` | `Diff` |
| `git.sessionDiff` | `{sessionId, paths?}` | `Diff` |
| `git.revert` | `{sessionId, turn}` | `{files: [FileStat]}` (worktree sessions only) |

```ts
HarnessInfo {
  id, name, protocol: "claude"|"codex"|"pi"|"acp"|"fake",
  installed, version?, authOk, hint?,          // usable when authOk; hint explains what's missing
  models?: [{id, name, description?, efforts?: [Choice]}],  // a model's efforts, when listed, replace the harness's
  efforts?: [Choice],                          // reasoning efforts for the default model; absent: not selectable
  modes?: [Choice], defaultMode?,              // Choice = {id, name, description?}
  commands?: [{name, description?}],           // slash commands to offer, such as {name: "compact"}
  caps: { resume, interrupt, setMode, freeModel, modelSelect,
          update?, login?, oneShot? }           // harness.update, harness.login, and the system agent
}

ServerSettings { systemAgent?: { harness, model?, effort? } }   // absent: none

UpdateInfo {
  current, latest?, url?,                      // latest absent: the lookup failed (reason)
  available,                                   // latest is newer than current
  via?: "rad" | "app",                         // how it installs; absent: it can't, and reason says why
  reason?, busy: [title]                       // sessions a restart would interrupt
}

Task {
  id, kind: "update_server" | "update_agent" | "login", harness?,
  status: "running" | "succeeded" | "failed" | "cancelled",
  output,                                      // the command's output as a terminal shows it, without colours: its last 300 lines
  url?,                                        // the last web address in it, such as a sign-in page
  input?,                                      // takes task.input now
  error?, version?,                            // version: installed by an update, or the agent's
  startedAt, endedAt?
}

Diff { from, to, files: [FileStat], patch, truncated }   // patch: unified diff, ≤ 512 KiB
FileStat { path, oldPath?, status: "added"|"modified"|"deleted"|"renamed", additions, deletions, binary? }
```

- **Slash commands.** A prompt of `/<name>`, optionally followed by text for the command, runs the command; send it with `session.prompt` like any other. Its turn has the prompt as its user message and, for `/compact`, a `notice` item once the agent has summarized the conversation; the session's `context` then drops. The harness's `commands` are the ones the apps offer in a menu; others typed in a prompt go to the agent as they are.
- **Composer suggestions.** `session.commands` discovers commands and user-invocable skills in the session's effective workspace, without sending a prompt or starting a turn. It may start or resume the agent process. `name` has no leading slash; `kind` is `skill`, `prompt`, or `extension` when known, and `scope` is the agent-reported source scope (such as `user` or `project`). Metadata may be absent. Archived sessions return an empty array. Clients fetch only while the composer is focused on a leading `/` token, rank exact and prefix name matches before substring and description matches, and insert `/<name> ` for editing. Enter sends an exact command; otherwise Enter or Tab completes the highlighted suggestion. Suggestions stop once arguments begin. Discovery failures (including an older server's `method_not_found`) leave ordinary prompts and the built-in command menu usable. Clients may append the harness's known commands, deduplicating by name. Codex resolves a matching discovered leading `/skill-name` or `$skill-name` to its native `$skill-name` plus an exact skill input; Claude uses `/name` and Pi `/skill:name`. Paths stay on the server. Sending uses cached metadata without another lookup; a cold native `$skill-name` is left for Codex to resolve. A lookup whose runtime was replaced returns `conflict` so the client can offer a retry. Discovery is refreshed when the picker reopens; Claude's catalog comes from its process initialization and refreshes when that runtime restarts.
- **Deleting a session.** `session.delete` works on archived and unarchived sessions. It stops the agent and the session's shell, and deletes the session, its turns, its transcript and its checkpoints; `removeWorktree` deletes its worktree too, and the branch is kept either way. Images stay, since other sessions may show them. The session's stream then holds only a `session.removed` event, the next in its seq, which reaches its subscribers; later commands on the session are `not_found`.
- **Queued prompts.** A prompt sent while a turn runs waits as a `queued` user message, and the next turn starts with it once the turn ends. Interrupting the turn cancels every queued prompt. `session.sendNow` sends one of them at once instead: rad interrupts the turn and starts the next turn with that prompt, ahead of the others, which stay queued behind it. A prompt no longer queued (it started, or was cancelled) is left as it is.
- **Images in prompts.** `images` lists ids from `POST /v1/images`. A prompt needs text, images or both. An unknown id is `invalid`.
- **Approval answers.** For a `question` approval, put `answers` (question text → chosen label; join multiple labels with `", "`) in the request, along with the option of kind `allow_once`.
- **Deny reasons.** For a deny, `message` is passed to the agent where the harness supports it.

### Managing the server

- **Tasks.** Updates and sign-ins run on the server as tasks, which clients poll with `task.get` (about once a second) while they run. Starting one that already runs (same kind and agent) returns it. rad keeps ended tasks for 15 minutes, and forgets them all when it restarts.
- **Signing an agent in.** `harness.login` runs the agent's sign-in command in a pseudo-terminal on the server, with `BROWSER` set so that nothing opens there: Claude Code's `claude auth login`, Codex's `codex login --device-auth`, Cursor's `cursor-agent login`. The task's `url` is the page to open on the client. Claude's page then shows a code to paste: send it with `task.input`. Codex shows a code to enter on the page, in `output`, and Cursor needs nothing more. The task succeeds once the command exits and a probe finds the agent signed in.
  - `authOk` comes from a probe, which may not see that a login expired. When a turn fails because the agent isn't signed in, rad shows the agent as not signed in (`authOk: false`, `hint` with the agent's error) until it signs in or a turn of its succeeds.
- **Updating an agent.** `harness.update` runs the agent's own updater (`claude update`, `codex update`, `pi update`, `cursor-agent update`, `opencode upgrade`); `version` is the version afterwards.
- **Updating rad.** `server.update` installs the latest release. With `via: "rad"`, rad downloads it as `rad update` does, then restarts as the new version in the same process: the socket closes, and the client reconnects to find the new `version` in `server.info`. With `via: "app"`, rad runs under the Remote Agent Server app, which updates itself and its rad, then opens again. The task succeeds when the install starts; the restart interrupts the sessions in `busy`, so clients ask first.
- **The system agent.** With `systemAgent` set, rad has that agent and model name each new session from its first prompt: its title, unless `session.create` gave one, and its worktree's branch (`ra/<words>`), unless it gave one. The session starts at once with names made from the prompt, and its `session.upserted` brings the new ones a few seconds later. The calls run without tools, in an empty folder, and leave no session or history with the agent (`claude -p --no-session-persistence --safe-mode`, `codex exec --ephemeral`, `pi --print --no-session`). An agent can be the system agent when `caps.oneShot`.
