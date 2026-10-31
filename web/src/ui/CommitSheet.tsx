import {
  Check,
  ChevronDown,
  ChevronRight,
  Circle,
  CircleCheck,
  CircleMinus,
  CircleX,
  Copy,
  ExternalLink,
  GitBranch,
  GitPullRequestArrow,
  RotateCw,
  TriangleAlert,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";
import type { CommitPreview, FileStat, ServerTask, Session, TaskStep } from "../protocol/models";
import { useObserved } from "../store/observable";
import { ServerTaskProgress } from "../store/serverTask";
import { storage } from "../store/storage";
import { useConnection } from "./context";
import { Modal, Spinner } from "./primitives";
import { openLink, TaskOutput } from "./TaskSheet";

/** How far a commit goes. */
export type CommitScope = "commit" | "push" | "pullRequest";

const scopes: { id: CommitScope; label: string }[] = [
  { id: "commit", label: "Commit only" },
  { id: "push", label: "Commit & push" },
  { id: "pullRequest", label: "Commit, push & open PR" },
];

/** The scope this browser used last. */
function savedScope(): CommitScope | undefined {
  const s = storage.get("commitScope");
  return s === "commit" || s === "push" || s === "pullRequest" ? s : undefined;
}

/** The scope to start with: the one asked for, if the preview allows it,
 *  else the last one used, else a push where there's somewhere to push. */
export function commitScope(preview: CommitPreview, chosen: CommitScope | undefined): CommitScope {
  const canPush = !!preview.remote && !!preview.branch;
  let s = chosen ?? savedScope() ?? (canPush ? "push" : "commit");
  if (!canPush) s = "commit";
  else if (s === "commit" && preview.files.length === 0) s = "push";
  return s;
}

/** The button's label: what it will do. */
export function commitButtonLabel(scope: CommitScope, hasFiles: boolean): string {
  switch (scope) {
    case "commit":
      return "Commit";
    case "push":
      return hasFiles ? "Commit & Push" : "Push";
    case "pullRequest":
      return hasFiles ? "Commit, Push & Open PR" : "Push & Open PR";
  }
}

/** Commits a session's changes, and pushes them and opens a pull request
 *  as far as the user picks, with the message rad's system agent wrote,
 *  which they can edit; then follows the commit on the server step by
 *  step. A commit that runs for the session already shows at once.
 *  Closing it leaves the commit running. */
export function CommitSheet({ session, onClose }: { session: Session; onClose: () => void }) {
  const connection = useConnection();
  const [preview, setPreview] = useState<CommitPreview>();
  const [loadError, setLoadError] = useState<string>();
  const [message, setMessage] = useState("");
  const [scope, setScope] = useState<CommitScope>();
  const [progress, setProgress] = useState<ServerTaskProgress>();
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState<string>();
  const live = useRef(true);

  const follow = (t: ServerTask) => {
    const p = new ServerTaskProgress(t, connection);
    p.start();
    setProgress(p);
  };

  useEffect(() => () => progress?.stop(), [progress]);
  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
    };
  }, []);

  const load = async () => {
    setLoadError(undefined);
    setPreview(undefined);
    try {
      const p = await connection.commitPreview(session.id);
      if (!live.current) return;
      setPreview(p);
      setMessage(p.message);
    } catch (e) {
      if (live.current) setLoadError((e as Error).message);
    }
  };

  // A commit already running for the session shows as it goes; otherwise
  // the changes, and a message for them.
  useEffect(() => {
    void (async () => {
      const tasks = await connection.tasks(session.id).catch(() => [] as ServerTask[]);
      const running = tasks.filter((t) => t.kind === "commit" && t.status === "running").pop();
      if (!live.current) return;
      if (running) follow(running);
      else void load();
    })();
  }, []);

  const start = async () => {
    if (!preview || starting) return;
    const s = commitScope(preview, scope);
    setStarting(true);
    setError(undefined);
    try {
      const t = await connection.commit({
        sessionId: session.id,
        message: preview.files.length ? message : undefined,
        push: s !== "commit",
        pullRequest: s === "pullRequest",
      });
      storage.set("commitScope", s);
      if (live.current) follow(t);
    } catch (e) {
      if (live.current) setError((e as Error).message);
    } finally {
      if (live.current) setStarting(false);
    }
  };

  if (progress) return <CommitProgress progress={progress} onClose={onClose} />;

  return (
    <Modal title="Commit" onClose={onClose}>
      {preview ? (
        <CommitForm
          preview={preview}
          message={message}
          setMessage={setMessage}
          scope={commitScope(preview, scope)}
          setScope={setScope}
          starting={starting}
          error={error}
          onStart={() => void start()}
        />
      ) : loadError ? (
        <div className="task-sheet">
          <div className="c-red with-icon">
            <TriangleAlert size={15} /> {loadError}
          </div>
          <div>
            <button className="button" onClick={() => void load()}>
              <RotateCw size={15} /> Retry
            </button>
          </div>
        </div>
      ) : (
        <div className="task-sheet">
          <div className="task-status hint">
            <Spinner /> Looking at the changes…
          </div>
        </div>
      )}
    </Modal>
  );
}

function CommitForm({
  preview,
  message,
  setMessage,
  scope,
  setScope,
  starting,
  error,
  onStart,
}: {
  preview: CommitPreview;
  message: string;
  setMessage: (s: string) => void;
  scope: CommitScope;
  setScope: (s: CommitScope) => void;
  starting: boolean;
  error: string | undefined;
  onStart: () => void;
}) {
  const files = preview.files;
  const hasFiles = files.length > 0;
  const canPush = !!preview.remote && !!preview.branch;
  const canStart = (hasFiles || scope !== "commit") && !starting;
  const adds = files.reduce((n, f) => n + f.additions, 0);
  const dels = files.reduce((n, f) => n + f.deletions, 0);

  return (
    <div className="form commit-form">
      <h4 className="form-header">Branch</h4>
      <section className="form-section">
        <div className="form-row">
          <GitBranch size={16} className="c-subtext" />
          <span className="commit-branch mono">
            {preview.branch ?? "Detached HEAD"}
            {preview.branch && preview.remote && <span className="c-subtext"> → {preview.remote}</span>}
          </span>
        </div>
        {scope === "pullRequest" && (
          <div className="form-row">
            <GitPullRequestArrow size={16} className="c-subtext" />
            <span>Pull request into {preview.base ? <span className="mono">{preview.base}</span> : "the default branch"}</span>
          </div>
        )}
      </section>
      {!preview.remote && <p className="form-footer">No remote to push to.</p>}
      {preview.remote && !preview.branch && <p className="form-footer">Check out a branch to push it.</p>}

      <h4 className="form-header">
        {hasFiles ? (
          <>
            {files.length} file{files.length === 1 ? "" : "s"} changed{"  "}
            <span className="c-green">+{adds}</span> <span className="c-red">−{dels}</span>
          </>
        ) : (
          "Changes"
        )}
      </h4>
      <section className="form-section">
        {hasFiles ? (
          <div className="commit-files">
            {files.map((f) => (
              <CommitFileRow key={f.path} file={f} />
            ))}
          </div>
        ) : (
          <div className="form-row stack">
            <span>Nothing to commit.</span>
            {preview.ahead > 0 && (
              <span className="small c-subtext">
                {preview.ahead} commit{preview.ahead === 1 ? "" : "s"} to push
              </span>
            )}
          </div>
        )}
      </section>

      {hasFiles && (
        <>
          <h4 className="form-header">Message</h4>
          <section className="form-section">
            <div className="form-row">
              <textarea
                className="prompt-field commit-message"
                rows={4}
                value={message}
                placeholder="Leave it empty, and rad writes one."
                onChange={(e) => setMessage(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && canStart) {
                    e.preventDefault();
                    onStart();
                  }
                }}
                aria-label="Commit message"
                spellCheck
              />
            </div>
          </section>
        </>
      )}

      <h4 className="form-header">Action</h4>
      <section className="form-section" role="radiogroup" aria-label="Action">
        {scopes.map((s) => {
          const disabled = s.id === "commit" ? !hasFiles : !canPush;
          return (
            <button key={s.id} className="form-row commit-scope" role="radio" aria-checked={scope === s.id} disabled={disabled} onClick={() => setScope(s.id)}>
              <span>{s.label}</span>
              {scope === s.id && <Check size={17} className="c-accent" strokeWidth={2.5} />}
            </button>
          );
        })}
      </section>

      {error && (
        <div className="form-message c-red">
          <TriangleAlert size={15} /> {error}
        </div>
      )}
      <div className="commit-actions">
        <button className="button prominent" disabled={!canStart} onClick={onStart}>
          {starting && <Spinner size={15} />}
          {commitButtonLabel(scope, hasFiles)}
        </button>
      </div>
    </div>
  );
}

const statusLetters: Record<string, [string, string]> = {
  added: ["A", "c-green"],
  modified: ["M", "c-yellow"],
  deleted: ["D", "c-red"],
  renamed: ["R", "c-blue"],
};

function CommitFileRow({ file }: { file: FileStat }) {
  const [letter, color] = statusLetters[file.status] ?? statusLetters.modified;
  return (
    <div className="commit-file">
      <span className={"commit-file-status mono strong " + color} title={file.status}>
        {letter}
      </span>
      <span className="commit-file-path mono">{file.oldPath ? `${file.oldPath} → ${file.path}` : file.path}</span>
      {file.binary ? (
        <span className="hint small">binary</span>
      ) : (
        <span className="mono small">
          <span className="c-green">+{file.additions}</span> <span className="c-red">−{file.deletions}</span>
        </span>
      )}
    </div>
  );
}

/** The commit on the server as it goes: its steps, the pull request, and
 *  what git and gh printed. */
function CommitProgress({ progress, onClose }: { progress: ServerTaskProgress; onClose: () => void }) {
  useObserved(progress);
  const t = progress.task;
  const phase = progress.phase;
  const running = phase.kind === "running";
  const failed = phase.kind === "failed";
  const [showOutput, setShowOutput] = useState(false);
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState<string>();

  useEffect(() => {
    if (failed) setShowOutput(true);
  }, [failed]);

  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(timer);
  }, [copied]);

  const cancel = async () => {
    setError(undefined);
    try {
      await progress.cancel();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const copy = async () => {
    if (!t.url || !navigator.clipboard?.writeText) return;
    try {
      await navigator.clipboard.writeText(t.url);
      setCopied(true);
    } catch {
      // the browser refused
    }
  };

  const steps = t.steps ?? [];
  return (
    <Modal
      title="Commit"
      onClose={onClose}
      leading={
        running ? (
          <button className="link" onClick={() => void cancel()}>
            Cancel
          </button>
        ) : (
          <span />
        )
      }
      trailing={
        <button className="link strong" onClick={onClose}>
          Done
        </button>
      }
    >
      <div className="task-sheet">
        <section className="form-section commit-steps">
          {steps.length === 0 && (
            <div className="form-row hint">
              <Spinner /> Starting…
            </div>
          )}
          {steps.map((s) => (
            <div key={s.id} className="form-row commit-step" data-status={s.status}>
              <StepIcon status={s.status} />
              <span className="harness-row-text">
                <span>{s.title}</span>
                {s.detail && <span className={"small " + (s.status === "failed" ? "c-red" : "c-subtext")}>{s.detail}</span>}
              </span>
            </div>
          ))}
        </section>
        <CommitStatus progress={progress} />
        {t.url && (
          <div className="task-actions">
            <button className="button prominent" onClick={() => openLink(t.url!)}>
              <ExternalLink size={15} /> Open Pull Request
            </button>
            <button className="button" onClick={() => void copy()} disabled={!navigator.clipboard?.writeText}>
              {copied ? <Check size={15} /> : <Copy size={15} />} Copy Link
            </button>
          </div>
        )}
        {error && (
          <div className="c-red with-icon">
            <TriangleAlert size={15} /> {error}
          </div>
        )}
        {t.output && (
          <div className="commit-output">
            <button className="link with-icon" onClick={() => setShowOutput(!showOutput)} aria-expanded={showOutput}>
              {showOutput ? <ChevronDown size={15} /> : <ChevronRight size={15} />} Output
            </button>
            {showOutput && <TaskOutput text={t.output} />}
          </div>
        )}
      </div>
    </Modal>
  );
}

function StepIcon({ status }: { status: TaskStep["status"] }) {
  switch (status) {
    case "running":
      return <Spinner size={17} />;
    case "succeeded":
      return <CircleCheck size={17} className="c-green" aria-label="Done" />;
    case "skipped":
      return <CircleMinus size={17} className="c-subtext" aria-label="Skipped" />;
    case "failed":
      return <CircleX size={17} className="c-red" aria-label="Failed" />;
    default:
      return <Circle size={17} className="c-subtext" aria-label="Not yet" />;
  }
}

function CommitStatus({ progress }: { progress: ServerTaskProgress }) {
  const phase = progress.phase;
  switch (phase.kind) {
    case "running":
    case "restarting":
      return (
        <div className="task-status">
          <Spinner /> Working…
        </div>
      );
    case "done":
      return (
        <div className="task-status">
          <CircleCheck size={17} className="c-green" /> Done.
        </div>
      );
    case "failed":
      return <div className="task-status c-red">Failed: {phase.message}</div>;
    case "cancelled":
      return <div className="task-status">Cancelled.</div>;
  }
}
