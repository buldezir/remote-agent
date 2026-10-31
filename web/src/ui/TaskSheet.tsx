import { Check, CircleCheck, Copy, ExternalLink, TriangleAlert } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { ServerTask } from "../protocol/models";
import { useObserved } from "../store/observable";
import type { ServerTaskProgress, TaskPhase } from "../store/serverTask";
import { useConnection } from "./context";
import { Modal, Spinner } from "./primitives";

/** Opens a web address from the server, such as a sign-in page, in a new
 *  tab; only http and https. */
export function openLink(url: string) {
  try {
    const u = new URL(url);
    if (u.protocol !== "http:" && u.protocol !== "https:") return;
    window.open(u.href, "_blank", "noopener");
  } catch {
    // not a URL
  }
}

/** What a task does, as its sheet's title. */
function taskTitle(t: ServerTask, agent: string): string {
  switch (t.kind) {
    case "update_server":
      return "Updating rad";
    case "update_agent":
      return `Updating ${agent}`;
    case "login":
      return `Signing In to ${agent}`;
    default:
      return t.kind;
  }
}

function doneText(t: ServerTask, agent: string): string {
  switch (t.kind) {
    case "update_server":
      return t.version ? `rad ${t.version} is running.` : "rad is running.";
    case "update_agent":
      return t.version ? `${agent} ${t.version}` : "Updated.";
    case "login":
      return "Signed in.";
    default:
      return "Done.";
  }
}

/** An update or a sign-in on the server, as it goes: its status, the
 *  sign-in page and a field for the code it gives, and the command's output.
 *  Closing it leaves the task running. */
export function TaskSheet({ progress, onClose }: { progress: ServerTaskProgress; onClose: () => void }) {
  const connection = useConnection();
  useObserved(progress);
  const t = progress.task;
  const phase = progress.phase;
  const running = phase.kind === "running";
  const agent = t.harness ? (connection.harness(t.harness)?.name ?? t.harness) : "";

  const [code, setCode] = useState("");
  const [error, setError] = useState<string>();
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(timer);
  }, [copied]);

  const send = async () => {
    const text = code;
    if (!text) return;
    setError(undefined);
    try {
      await progress.send(text);
      setCode("");
    } catch (e) {
      setError((e as Error).message);
    }
  };

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

  return (
    <Modal
      title={taskTitle(t, agent)}
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
        <TaskStatus phase={phase} task={t} agent={agent} />
        {running && t.url && (
          <div className="task-actions">
            <button className="button prominent" onClick={() => openLink(t.url!)}>
              <ExternalLink size={15} /> Open Sign-In Page
            </button>
            <button className="button" onClick={() => void copy()} disabled={!navigator.clipboard?.writeText}>
              {copied ? <Check size={15} /> : <Copy size={15} />} Copy Link
            </button>
          </div>
        )}
        {running && t.input && (
          <div className="task-input">
            <input
              className="text-field"
              placeholder="Paste the code here"
              value={code}
              onChange={(e) => setCode(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  void send();
                }
              }}
              autoCapitalize="off"
              autoCorrect="off"
              autoComplete="off"
              spellCheck={false}
              aria-label="Code"
            />
            <button className="button" disabled={!code} onClick={() => void send()}>
              Send
            </button>
          </div>
        )}
        {error && (
          <div className="c-red with-icon">
            <TriangleAlert size={15} /> {error}
          </div>
        )}
        <TaskOutput text={t.output} />
      </div>
    </Modal>
  );
}

function TaskStatus({ phase, task, agent }: { phase: TaskPhase; task: ServerTask; agent: string }) {
  switch (phase.kind) {
    case "running":
      return (
        <div className="task-status">
          <Spinner /> {task.kind === "login" && task.url ? "Waiting for you to sign in…" : "Running…"}
        </div>
      );
    case "restarting":
      return (
        <div className="task-status">
          <Spinner /> Restarting rad…
        </div>
      );
    case "done":
      return (
        <div className="task-status">
          <CircleCheck size={17} className="c-green" /> {doneText(task, agent)}
        </div>
      );
    case "failed":
      return <div className="task-status c-red">Failed: {phase.message}</div>;
    case "cancelled":
      return <div className="task-status">Cancelled.</div>;
  }
}

/** The command's output, kept scrolled to the end as it grows unless
 *  scrolled up to read. */
export function TaskOutput({ text }: { text: string }) {
  const ref = useRef<HTMLPreElement>(null);
  const atEnd = useRef(true);
  useLayoutEffect(() => {
    const el = ref.current;
    if (el && atEnd.current) el.scrollTop = el.scrollHeight;
  }, [text]);
  if (!text) return null;
  return (
    <pre
      ref={ref}
      className="task-output"
      onScroll={(e) => {
        const el = e.currentTarget;
        atEnd.current = el.scrollHeight - el.scrollTop - el.clientHeight < 8;
      }}
    >
      {text}
    </pre>
  );
}
