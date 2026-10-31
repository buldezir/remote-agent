# Remote Agent wire protocol (v1)

This is the contract between `rad` (the Go server on the dev machine) and its clients (the iOS and Mac apps, the Android app, the web client, `ra` in the terminal, `rad debug`).
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

A session's shell runs on the server, in the session's folder (its worktree, for a worktree session, or the folder the agent moved to, `agentPath`, when the shell starts), and keeps running when no client is attached. Each session has at most one. It ends when the user exits it or closes it, when the session is archived, or when rad stops. Clients show the menu item only when `server.info` returns `terminal: true`.

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

## MCP

`/v1/mcp` is rad's [MCP](https://modelcontextprotocol.io) server, for agents rather than clients: its tools start and drive sessions. It speaks Streamable HTTP without streams: each `POST` carries one JSON-RPC message, a request's reply is the body (`application/json`), a notification gets `202`, and `GET` gets `405`. Requests take `Authorization: Bearer <token>`: the token rad gives the agent of a session (in the agent's environment as `RAD_MCP_TOKEN`; it lapses when the session is archived or deleted, or rad restarts), or a device's. A request from a page whose `Origin` isn't in `web_origins` gets `403`.

| Tool | Arguments | Does |
|---|---|---|
| `list_projects` | — | the projects |
| `list_agents` | — | the installed, signed-in agents, with their models, efforts and modes |
| `list_sessions` | `project?`, `archived?` | the sessions, newest first |
| `start_session` | `project`, `prompt`, `agent?`, `model?`, `effort?`, `mode?`, `worktree?`, `branch?`, `title?` | `session.create`; a folder under `roots` becomes a project |
| `send_prompt` | `session`, `prompt`, `steer?` | `session.prompt` (`steer`: into the running turn, as below); `list_agents` says `steers` for agents that take it |
| `read_session` | `session`, `turns?` (default 1; 0: all) | the status and transcript, as text |
| `wait_for_session` | `session`, `timeoutSeconds?` (default 600, at most 3600) | waits until the agent stops working; its status and last reply |
| `stop_session` | `session` | `session.interrupt` |
| `rename_session` | `session`, `title` | `session.rename` (empty: rad names it again) |
| `fork_session` | `session`, `turn?`, `agent?`, `model?`, `effort?`, `mode?`, `worktree?`, `branch?`, `prompt?` | `session.fork` |

`session` is an id or a unique start of one (six characters or more). A session's agent can't send to, wait for or stop its own session; it may rename it. The sessions it starts or forks get its own mode with its own agent, or else the agent's default mode: no other mode, so that it can't start one that asks the user less than it does. They begin with a `notice` ("Started by the agent of “…”."), and may start sessions in turn, three deep at most. Approvals and questions of the sessions it starts go to the user, as any session's do; no tool answers them. A device's tools have none of these limits.

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
  workspace: { kind: "root"|"worktree", path, branch?, baseRef?, agentPath? },
                                     // branch: checked out where the agent works, refreshed at each turn start/end and within seconds of a switch (session.upserted); absent if detached
                                     // agentPath: the folder the agent moved to, such as a worktree it made itself (Claude's EnterWorktree); absent while it works in path
  status: "idle"|"running"|"awaiting_approval"|"stopped"|"error",
  nativeId?, title, error?, archived,
  customTitle?,                      // true: the title is the user's (session.rename, or a title given to session.create,
                                     // session.fork or session.import), which rad's naming leaves alone; absent: false
  unread?,                           // true: a turn ended, or the agent asked for an answer, since a client
                                     // last showed the session (session.markRead); absent: false
  draft?: { text, images?: [ImageRef] },
                                     // a prompt saved to send later (session.saveDraft); absent: none
  context?: { used, window? },       // tokens in the agent's context at its last model call; window absent if unknown
  modelInfo?: { id?, name?, effort? },
                                     // the model and reasoning effort the agent reported it runs with, once it
                                     // has started; name absent: look id up in harness.list models, else show id
  backgroundTasks?: [{ id, kind: "command"|"agent"|"other", description }],
                                     // what the agent left running (a command it watches, a subagent), while its
                                     // process runs; absent: none
  forkedFrom?: { sessionId, turn },  // the session this one is a fork of, and the turn it continues after (session.fork)
  createdAt, updatedAt
}

Turn {
  id, sessionId, n,                  // n = 1, 2, … within a session
  status: "running"|"completed"|"interrupted"|"failed",
  checkpointBefore?, checkpointAfter?,   // git commit shas (git projects only)
  usage?: { inputTokens?, outputTokens?, cacheReadTokens?, cacheWriteTokens?, costUsd? },
  error?,
  forkPoint?,                        // where the agent's own conversation stood at the turn's end, for forks; rad's, clients ignore it
  startedAt, endedAt?
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
  questions?: [{ question, header?, multiSelect?, options: [{label, description?}], optionsOnly? }],
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
| `server.info` | — | `{serverId, name, protocolVersion, version, roots, deviceId, terminal?, drafts?, forks?, commit?, search?, rename?}` (`terminal`: `GET /v1/terminal` is there; `drafts`: it keeps drafts; `forks`: it has `session.fork`; `commit`: it has `git.commitPreview` and `git.commit`; `search`: it has `session.search`; `rename`: it has `session.rename`; see below) |
| `device.unpair` | — | `{}` (revokes the calling device's token) |
| `harness.list` | `{refresh?}` | `{harnesses: [HarnessInfo]}`, only the ones installed on the server |
| `harness.update` | `{harness}` | `Task` (runs the agent's own updater; `caps.update`) |
| `harness.login` | `{harness}` | `Task` (signs the agent in again; `caps.login`) |
| `server.settings` | — | `ServerSettings` |
| `server.setSettings` | `ServerSettings` | `ServerSettings` (replaces them all; `invalid` for an agent that can't be the system agent) |
| `server.checkUpdate` | `{refresh?}` | `UpdateInfo` (`refresh` looks again rather than use the answer of the last 10 minutes) |
| `server.update` | — | `Task` (`unavailable` when rad can't update, `conflict` when it is up to date) |
| `task.list` | `{sessionId?}` | `{tasks: [Task]}`, running and lately ended, oldest first: the server's own (updates and sign-ins), or with `sessionId` that session's (commits) |
| `task.get` | `{id}` | `Task` (`not_found` once forgotten, as after a restart) |
| `task.input` | `{id, text}` | `{}` (types `text` and Return into the task's command) |
| `task.cancel` | `{id}` | `{}` |
| `fs.list` | `{path?}` (omit for roots) | `{path, parent?, entries: [{name, path, isGitRepo}]}` |
| `project.list` | — | `{projects}` |
| `project.add` | `{path}` (must be under a root) | `Project` |
| `project.remove` | `{id}` | `{}` (`conflict` if it has unarchived sessions) |
| `session.list` | — | `{sessions}` |
| `session.search` | `{query, archived?, limit?}` | `SearchResults` (`archived`: archived sessions too; `limit`: sessions at most, default 30, at most 100; see below) |
| `session.create` | `{commandId, projectId, harness, model?, effort?, mode?, workspace: {kind, branch?, baseRef?}, prompt?, images?, title?, draft?}` | `Session` (`draft`: keep the prompt as the session's draft, see below) |
| `session.fork` | `{commandId, sessionId, turn?, harness?, model?, effort?, mode?, workspace?: {kind, branch?}, prompt?, images?, title?, draft?}` | `Session` (see below) |
| `session.importable` | `{projectId, harness}` | `{sessions: [ImportableSession]}`, newest first (`caps.import`; see below) |
| `session.import` | `{commandId, projectId, harness, nativeId, model?, effort?, mode?, title?}` | `Session` (see below) |
| `session.commands` | `{sessionId}` | `{commands: [{name, description?, kind?, scope?}]}` |
| `session.prompt` | `{commandId, sessionId, text, images?, steer?}` | `Item` (the user message; `queued` if a turn is running, unless it steers; see below) |
| `session.saveDraft` | `{sessionId, text, images?}` | `Session` (see below) |
| `session.sendDraft` | `{commandId, sessionId}` | `Item` (the user message, as `session.prompt`; `conflict` without a draft) |
| `session.interrupt` | `{sessionId, force?}` | `{}` (`force` kills the agent process) |
| `session.sendNow` | `{sessionId, itemId}` | `{}` (see below) |
| `session.steer` | `{sessionId, itemId}` | `{}` (`invalid` for an agent without `caps.steer`, or a slash command; see below) |
| `session.cancelQueued` | `{sessionId, itemId}` | `{}` (see below) |
| `session.setMode` | `{sessionId, mode}` | `{}` |
| `session.rename` | `{sessionId, title}` | `Session` (`title` `""`: rad names it again; see below) |
| `session.markRead` | `{sessionId}` | `{}` (see below) |
| `session.archive` | `{sessionId, removeWorktree?}` | `{}` (also ends the session's shell) |
| `session.delete` | `{sessionId, removeWorktree?}` | `{}` (see below) |
| `approval.respond` | `{commandId, sessionId, approvalId, optionId, message?, answers?}` | `{}` |
| `subscribe` | `{stream, afterSeq?}` | `{stream, seq}` (see above) |
| `unsubscribe` | `{stream}` | `{}` |
| `git.branches` | `{projectId}` | `{branches, current}` |
| `git.turnDiff` | `{turnId, paths?}` | `Diff` |
| `git.sessionDiff` | `{sessionId, paths?}` | `Diff` |
| `git.revert` | `{sessionId, turn}` | `{files: [FileStat]}` (worktree sessions only) |
| `git.commitPreview` | `{sessionId}` | `CommitPreview` (see below) |
| `git.commit` | `{sessionId, message?, push?, pullRequest?}` | `Task` (commits, pushes and opens a pull request; see below) |

```ts
HarnessInfo {
  id, name, protocol: "claude"|"codex"|"pi"|"acp"|"fake",
  installed, version?, authOk, hint?,          // usable when authOk; hint explains what's missing
  models?: [{id, name, description?, efforts?: [Choice]}],  // a model's efforts, when listed, replace the harness's
  efforts?: [Choice],                          // reasoning efforts for the default model; absent: not selectable
  modes?: [Choice], defaultMode?,              // Choice = {id, name, description?}
  commands?: [{name, description?}],           // slash commands to offer, such as {name: "compact"}
  caps: { resume, interrupt, setMode, freeModel, modelSelect,
          update?, login?, oneShot?,            // harness.update, harness.login, and the system agent
          steer?,                               // takes a prompt into the running turn (session.steer)
          import? }                             // picks up the agent's own sessions (session.importable, session.import)
}

ImportableSession {
  id,                                          // the agent's own id for it; the session's nativeId once imported
  title,                                       // the agent's or the user's name for it, else its first prompt's first line
  messages,                                    // the prompts and the agent's replies
  updatedAt,                                   // when the agent last wrote to it
  active?                                      // written in the last minute: it may still run in a terminal
}

ServerSettings { systemAgent?: { harness, model?, effort? } }   // absent: none

UpdateInfo {
  current, latest?, url?,                      // latest absent: the lookup failed (reason)
  available,                                   // latest is newer than current
  via?: "rad" | "app",                         // how it installs; absent: it can't, and reason says why
  reason?, busy: [title]                       // sessions a restart would interrupt
}

Task {
  id, kind: "update_server" | "update_agent" | "login" | "commit", harness?, sessionId?,   // sessionId: commit
  status: "running" | "succeeded" | "failed" | "cancelled",
  steps?: [{ id, title, status: "pending" | "running" | "succeeded" | "skipped" | "failed", detail? }],
                                               // what it does, in order, as it gets there: a commit's "commit", "push", "pull_request"
  output,                                      // the command's output as a terminal shows it, without colours: its last 300 lines
  url?,                                        // the last web address in it, such as a sign-in page; for a commit, the pull request
  input?,                                      // takes task.input now
  error?, version?,                            // version: installed by an update, or the agent's
  startedAt, endedAt?
}

SearchResults {
  results: [SearchResult],                     // best first
  more?,                                       // more sessions match than the limit
  indexing?                                    // rad is still indexing sessions an older rad wrote: some may be missing
}
SearchResult {
  session: Session,
  itemId?, itemKind?,                          // the item that matches best; absent: only the title or the branch matched
  snippet: [{ text, match? }],                 // the match in its context, on one line; match: the words the query found
  matches                                      // how many of the session's items match
}

Diff { from, to, files: [FileStat], patch, truncated }   // patch: unified diff, ≤ 512 KiB
CommitPreview {
  branch?,                                     // checked out where the agent works; absent: detached
  files: [FileStat],                           // everything uncommitted, untracked files included
  message,                                     // for them, by the system agent, else rad's; "" without files
  remote?,                                     // where a push goes; absent: nowhere
  base?,                                       // the branch a pull request goes into; absent: the repository's default
  ahead                                        // the branch's commits its remote branch lacks (before the first push: that base lacks)
}
FileStat { path, oldPath?, status: "added"|"modified"|"deleted"|"renamed", additions, deletions, binary? }
```

- **Slash commands.** A prompt of `/<name>`, optionally followed by text for the command, runs the command; send it with `session.prompt` like any other. Its turn has the prompt as its user message and, for `/compact`, a `notice` item once the agent has summarized the conversation; the session's `context` then drops. The harness's `commands` are the ones the apps offer in a menu; others typed in a prompt go to the agent as they are.
- **Composer suggestions.** `session.commands` discovers commands and user-invocable skills in the session's effective workspace, without sending a prompt or starting a turn. It may start or resume the agent process. `name` has no leading slash; `kind` is `skill`, `prompt`, or `extension` when known, and `scope` is the agent-reported source scope (such as `user` or `project`). Metadata may be absent. Archived sessions return an empty array. Clients fetch only while the composer is focused on a leading `/` token, rank exact and prefix name matches before substring and description matches, and insert `/<name> ` for editing. Enter sends an exact command; otherwise Enter or Tab completes the highlighted suggestion. Suggestions stop once arguments begin. Discovery failures (including an older server's `method_not_found`) leave ordinary prompts and the built-in command menu usable. Clients may append the harness's known commands, deduplicating by name. Codex resolves a matching discovered leading `/skill-name` or `$skill-name` to its native `$skill-name` plus an exact skill input; Claude uses `/name` and Pi `/skill:name`. Paths stay on the server. Sending uses the catalog the last lookup returned; when none has succeeded since the agent started, a leading `/` or `$` prompt looks the skills up once, briefly, and a failed lookup leaves the text as it is. A lookup whose runtime was replaced returns `conflict` so the client can offer a retry. Discovery is refreshed when the picker reopens; Claude's catalog comes from its process initialization and refreshes when that runtime restarts.
- **Unread sessions.** rad marks a session `unread` when a turn ends (completed, failed or interrupted, including by a restart) and when the agent asks for an answer. A client calls `session.markRead` while it shows the session, in a window in front, so that every device stops marking it; it keeps the session's `updatedAt`. Reading a session that isn't unread changes nothing. Archiving a session reads it. Older servers answer `method_not_found`, which clients ignore.
- **Drafts.** A prompt can wait in a session, unsent, for any device to send later: `session.create` with `draft: true` keeps its prompt and images as the session's `draft` and starts no turn, and `session.saveDraft` keeps one in an existing session, replacing the one it had. Clients show it at the foot of the transcript, apart from the composer. A draft names the session as a first prompt does: its title, and with the system agent its worktree's branch, when the session still has the default title "New session". `session.sendDraft` sends it as `session.prompt` would, and the `session.upserted` that removes the draft comes with the user message's `item.upserted`. Saving no text and no images removes the draft; a session that never had a prompt then gets the default title back, so that its next draft or prompt names it. Prompts sent meanwhile leave the draft as it is. Archived sessions take no drafts (`conflict`). Servers that keep drafts say `drafts: true` in `server.info`; clients offer drafts only then, since an older server would ignore `draft` in `session.create` and start the session with the prompt.
- **The agent's own worktree.** An agent may move into a worktree it makes itself, as Claude Code does with its EnterWorktree tool (in `.claude/worktrees/<name>`, on the branch `worktree-<name>`), and back out with ExitWorktree. rad follows it there: the session's `agentPath` is that folder, and its `branch`, its turns' checkpoints and diffs, `git.revert` and a new terminal are that folder's. The turn in which it moves starts its changes over in the new folder, and `git.sessionDiff` shows the changes since the move. Back in `path`, `agentPath` goes away. A folder that no longer exists counts as `path`. The agent still starts in `path`, and Claude Code goes back to its worktree when it resumes.
- **Forks.** `session.fork` makes a new session that continues `sessionId` after `turn` (absent: the last turn that ended; `conflict` while that turn runs, `invalid` before any turn). The fork starts with a copy of the session's transcript and turns up to and including that turn, with new ids, followed by a `notice` ("Forked from “…” after turn n."), and `forkedFrom` names its source; its next turn is `n + 1`. The source is left as it is, archived ones included.
  - **Agent.** `harness` defaults to the source's, and so does `mode` with the source's harness, else that harness's default. Absent `model` and `effort` are the harness's defaults, as in `session.create`: clients start their pickers at the source's and send them. The same agent, where it can, copies its own conversation, cut after the turn, when the fork first starts it (Claude Code's `--fork-session` with `--resume-session-at`, Codex's `thread/fork` with `lastTurnId`). Any other agent, or one that can't fork, starts afresh, and its first prompt brings the conversation before it as a transcript: the first request, and as much of the rest as fits, latest first. If the copy fails, a `notice` says so and the transcript goes instead.
  - **Workspace.** `workspace.kind` defaults to the source's. `worktree` checks out a new branch (`branch`, or one rad names as for a new session) at the commit the turn's last checkpoint was taken on, with the files as they were after the turn (`invalid` outside git, or for a turn without a checkpoint). `root` works in the project folder as it is now. The fork's copied turns keep their diffs.
  - `prompt`, `images`, `title` and `draft` work as in `session.create`. The title defaults to the source's with " (fork)"; with the system agent, a prompt names it. Servers that fork say `forks: true` in `server.info`; older ones answer `method_not_found`.
- **Picking up a session from the terminal.** Agents keep their own sessions on the server, such as the ones the user ran in a terminal: Claude Code in `~/.claude/projects/` (or `CLAUDE_CONFIG_DIR`), Codex in `~/.codex/sessions/` (or `CODEX_HOME`), Pi in `~/.pi/agent/sessions/` (or `PI_CODING_AGENT_DIR`), as the harness's `env` in rad's config or rad's own environment sets them. For a harness with `caps.import`, `session.importable` lists the ones that ran in the project's folder, newest first, thirty at most. It leaves out the ones rad has a session for (by `nativeId`, archived ones included) and any other that rad started, sub-agents', and sessions with no reply yet.
  - `session.import` makes a session of one: its `nativeId` is the agent's id, so its next prompt resumes it as rad resumes its own (`claude --resume`, Codex's `thread/resume`, Pi's `--session-id`). Its workspace is the project folder (`root`), where the agent keeps it. `model`, `effort` and `mode` are what it goes on with, as in `session.create`. It is `conflict` when rad has a session for it already, and `not_found` when the agent has no such session in that folder.
  - Its transcript is the conversation on the branch it stands on (an agent's rewinds leave the rest out): a turn for each prompt, with the agent's replies, reasoning and tool calls with their results, as rad's own items, and turns the agent started itself (a background task's end). What doesn't map is left out. The turns have no checkpoints, so no diffs; they keep where the agent's conversation stood, so they fork as rad's own do. A `notice` ends it, which warns when the agent wrote to it in the last minute: one still open in a terminal would write over the session, so clients warn about `active` ones before they import them. The title is the agent's or the user's name for it, else the first prompt, which the system agent then names, as for a new session.
- **Committing.** A session's work can be committed, pushed and opened as a pull request from the clients, in the folder the agent works in (its worktree, or `agentPath`), for sessions in a git repository. `git.commitPreview` shows what a commit would take, with a message the system agent writes from the diff (rad's own, the session's title or the files, when there's no system agent or it fails); it may take as long as the system agent does, up to a minute, so clients wait longer for it than for other calls. `git.commit` then runs as a task (below) of kind `commit` for the session, with a step for each part that was asked for:
  1. `commit` stages everything (`git add -A`) and commits it with `message` (absent: one written as for the preview), running the repository's hooks; `skipped` when there's nothing to commit. Prompts sent meanwhile wait in the queue, as during a turn.
  2. `push`, with `push` or `pullRequest`: pushes the branch to the remote it tracks a branch of its own name on, else to `origin`, setting that as its upstream (a worktree made from `origin/main` tracks `main`, and isn't pushed there). It fails without a remote, or on a detached HEAD.
  3. `pull_request`, with `pullRequest`: opens one with GitHub's `gh` (`gh pr create`), into `base`, its title and description written by the system agent from the branch's commits (else the lone commit's, or the session's title and the commits' subjects). If one is open for the branch already, the step says so and takes that one. It fails when `gh` isn't installed or signed in (`gh auth login` on the computer), or for the repository's default branch. The task's `url` is the pull request.

  Each step's `detail` says what it did (`a1b2c3d Fix the redirect`, `ra/fix → origin`, `#12 Fix the redirect`, `Already open: #3 …`) or why it failed, which is the task's `error` too; the steps after a failed one stay `pending`. `output` shows the commands and what they printed. Neither git nor gh can ask for anything (a password, a passphrase): they run without a terminal, and fail instead. Once the task ends, the transcript has a `notice` with what it did, and the agent hears of it with the next prompt. `git.commit` is `conflict` while a turn runs (and the task fails if one started before the commit), or for an archived session, and `invalid` outside git; starting it while the session's commit runs returns that task. `task.list` lists a session's tasks only with its `sessionId`, so that a client following the server's tasks doesn't take a commit for an update. Servers that commit say `commit: true` in `server.info`; older ones answer `method_not_found`.
- **Search.** `session.search` finds sessions by what was said in them, and by their title and branch. It searches the text of prompts, the agent's replies, notices and errors, tool calls' titles and commands (an input's `command`), plans, and a proposed plan's or a question's text: what an item's `SearchText` is in `server/internal/model/search.go`. Reasoning, tool output, approvals of tool calls and sub-agents' items aren't searched: they're long, folded away in the clients, and would bury the conversation.
  - **Query.** Every word must be there, as the start of a word (`redir` finds “redirect”), or as a whole word when it's shorter than three letters. A `"quoted phrase"` must be there as it is. Case and accents don't matter, and punctuation separates words (`src/ui` is the phrase “src ui”). The query's own characters are never search syntax.
  - **Results.** A session comes once, with its best-matching item, else its title or branch, and how many of its items match. Sessions rank by how well they match (BM25; a title or branch that matches doubles a session's score), damped by age, so that among good matches the recent ones come first: a session last updated 30 days ago counts half as much as one updated today. Clients open a result's session at `itemId`, which they scroll to and highlight for a moment, and show `archived` ones marked as such.
  - **Find in a session.** Clients find in the open session's transcript on their own, over the same text as `SearchText`, as they have the whole transcript: case-insensitively, matching what the user typed as it is, and moving from match to match by item.
  - rad indexes as it writes. The first start of a rad with search indexes the sessions an older rad wrote, newest first, in the background (some 25 s for 300,000 items); until it's done, results say `indexing`. Servers that search say `search: true` in `server.info`; older ones answer `method_not_found`, and the clients search only the servers that say it.
- **Renaming a session.** A session's title comes from its first prompt (or draft), then the system agent's name for it (see "The system agent"), unless the user gave one. `session.rename` gives it the user's `title`: on one line (line breaks and runs of spaces become one space), without the spaces around it, cut at 120 characters. The session then has `customTitle`, and rad never names it again: not a later first prompt or draft (even with the title "New session"), not removing a draft, and not the system agent, whose answer for a request sent before the rename is dropped. A fork of such a session keeps its title with " (fork)" and `customTitle`; a title given to `session.create`, `session.fork` or `session.import` is the user's too, while an imported session's own name from the agent's files isn't. An empty `title` gives the session back to rad: the title its first prompt gives (else its draft's, else "New session"), which the system agent, if set, names again; a worktree's branch stays as it is. A fork's is its source's title with " (fork)". Renaming keeps the session's `updatedAt`, so that it keeps its place in the lists, and the `session.upserted` with the new title reaches every client; search finds the session by it at once. Archived sessions can be renamed. Servers that rename say `rename: true` in `server.info`; clients offer "Rename…" only then, since older ones answer `method_not_found`.
- **Deleting a session.** `session.delete` works on archived and unarchived sessions. It stops the agent and the session's shell, and deletes the session, its turns, its transcript and its checkpoints; `removeWorktree` deletes its worktree too, and the branch is kept either way. Images stay, since other sessions may show them. The session's stream then holds only a `session.removed` event, the next in its seq, which reaches its subscribers; later commands on the session are `not_found`.
- **Queued prompts.** A prompt sent while a turn runs waits as a `queued` user message, and the next turn starts with it once the turn ends. Interrupting the turn cancels every queued prompt. `session.sendNow` sends one of them at once instead: rad interrupts the turn and starts the next turn with that prompt, ahead of the others, which stay queued behind it. `session.cancelQueued` takes one of them out of the queue, `cancelled`, and leaves the turn and the other prompts as they are. A prompt no longer queued (it started, or was cancelled) is left as it is. Older servers answer `method_not_found`, which clients show as a reason to update rad.
- **Steering.** `session.steer` adds one of the queued prompts to the running turn, without interrupting it, for agents with `caps.steer`: the agent reads it at its next step, such as after the tool call it runs, and goes on in the same turn, which ends once. Its user message becomes `completed`, with the turn's `turnId`, and moves below what the turn has shown so far (a new `order`), where the agent got it. `session.prompt` with `steer` does the same for a new prompt: while a turn runs, it joins that turn where the agent can take it, and is queued as usual where it can't (any other agent, a slash command); with no turn running, it starts one. Clients offer "Steer" beside "Send now" while the agent works and its harness has `caps.steer`, and not for a prompt starting with `/`: slash commands run only as prompts of their own (`invalid`). A prompt no longer queued is left as it is; one whose turn ends before the agent takes it stays queued and starts the next turn. Older servers answer `session.steer` with `method_not_found`, and ignore `steer` in `session.prompt`, which then queues the prompt.
  - Claude Code takes a prompt written to it mid-turn (stream-json) after the tool calls it runs; one that comes while it writes its reply, it reads in a turn of its own right after, which rad shows as the same turn. Codex takes it with `turn/steer` and Pi with its `steer` command (as many at a time as Pi's own steering mode says); stopping the turn clears Pi's queue first. ACP agents can't.
- **Images in prompts.** `images` lists ids from `POST /v1/images`. A prompt needs text, images or both. An unknown id is `invalid`.
- **Approval answers.** For a `question` approval, put `answers` (question text → chosen label; join multiple labels with `", "`) in the request, along with the option of kind `allow_once`.
- **Answers in the user's own words.** Clients offer one more choice under each question's options, a text field, for an answer of the user's own: it replaces the chosen option, or with `multiSelect` it comes after the chosen labels (`"Red, Blue, teal, not green"`). A question may have no options, and then the text is its only answer. `optionsOnly` questions take only their options (Pi's dialogs), so clients show no text field for them.
- **Deny reasons.** For a deny, `message` is passed to the agent where the harness supports it.

### Managing the server

- **Tasks.** Updates, sign-ins and commits run on the server as tasks, which clients poll with `task.get` (about once a second) while they run. Starting one that already runs (same kind, and agent or session) returns it. rad keeps ended tasks for 15 minutes, and forgets them all when it restarts.
- **Signing an agent in.** `harness.login` runs the agent's sign-in command in a pseudo-terminal on the server, with `BROWSER` set so that nothing opens there: Claude Code's `claude auth login`, Codex's `codex login --device-auth`, Cursor's `cursor-agent login`. The task's `url` is the page to open on the client. Claude's page then shows a code to paste: send it with `task.input`. Codex shows a code to enter on the page, in `output`, and Cursor needs nothing more. The task succeeds once the command exits and a probe finds the agent signed in.
  - `authOk` comes from a probe, which may not see that a login expired. When a turn fails because the agent isn't signed in, rad shows the agent as not signed in (`authOk: false`, `hint` with the agent's error) until it signs in or a turn of its succeeds.
- **Updating an agent.** `harness.update` runs the agent's own updater (`claude update`, `codex update`, `pi update`, `cursor-agent update`, `opencode upgrade`); `version` is the version afterwards.
- **Updating rad.** `server.update` installs the latest release. With `via: "rad"`, rad downloads it as `rad update` does, then restarts as the new version in the same process: the socket closes, and the client reconnects to find the new `version` in `server.info`. With `via: "app"`, rad runs under the Remote Agent Server app, which updates itself and its rad, then opens again. The task succeeds when the install starts; the restart interrupts the sessions in `busy`, so clients ask first.
- **The system agent.** With `systemAgent` set, rad has that agent and model name each new session from its first prompt: its title, unless `session.create` gave one or the user renamed it meanwhile, and its worktree's branch (`ra/<words>`), unless it gave one. The session starts at once with names made from the prompt, and its `session.upserted` brings the new ones a few seconds later. The calls run without tools, in an empty folder, and leave no session or history with the agent (`claude -p --no-session-persistence --safe-mode`, `codex exec --ephemeral`, `pi --print --no-session`). An agent can be the system agent when `caps.oneShot`.
