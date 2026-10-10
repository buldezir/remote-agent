import {
  Brain,
  ChevronRight,
  CircleCheck,
  CircleDashed,
  CircleX,
  ClipboardList,
  FileText,
  Globe,
  Hand,
  Pencil,
  Search,
  Terminal,
  TriangleAlert,
  Users,
  Wrench,
  type LucideIcon,
} from "lucide-react";
import { useState } from "react";
import type { ImageRef, Item, JSONValue, Plan, Turn } from "../../protocol/models";
import type { SessionStore } from "../../store/sessionStore";
import { ImageGallery } from "../images";
import { Markdown } from "../Markdown";
import { Spinner } from "../primitives";
import { ApprovalView } from "./ApprovalView";

export function ItemView({ item, store }: { item: Item; store: SessionStore }) {
  switch (item.kind) {
    case "user_message":
      return <UserBubble text={item.text ?? ""} images={item.images ?? []} pending={item.status === "queued"} cancelled={item.status === "cancelled"} />;
    case "assistant_message":
      return (
        <div className="assistant">
          <Markdown text={item.text ?? ""} images={item.images ?? []} />
        </div>
      );
    case "reasoning":
      return <ReasoningView item={item} />;
    case "tool_call":
      return <ToolCallView item={item} store={store} />;
    case "approval":
      return <ApprovalView item={item} store={store} />;
    case "plan":
      return <PlanView plan={item.plan} />;
    case "notice":
      return <div className="notice">{item.text}</div>;
    case "error":
      return (
        <div className="item-error">
          <TriangleAlert size={14} /> {item.text || "Error"}
        </div>
      );
    default:
      return null;
  }
}

export function UserBubble({ text, images = [], pending = false, cancelled = false }: { text: string; images?: ImageRef[]; pending?: boolean; cancelled?: boolean }) {
  const faded = pending || cancelled;
  return (
    <div className="user-message">
      {images.length > 0 && (
        <div style={{ opacity: faded ? 0.55 : 1 }}>
          <ImageGallery images={images} height={120} align="end" />
        </div>
      )}
      {text && (
        <div className="bubble" style={{ opacity: faded ? 0.55 : 1 }}>
          {text}
        </div>
      )}
      {pending && <div className="bubble-note">Queued</div>}
      {cancelled && <div className="bubble-note">Not sent</div>}
    </div>
  );
}

function ReasoningView({ item }: { item: Item }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="reasoning">
      <button className="disclosure" onClick={() => setOpen(!open)} aria-expanded={open}>
        <ChevronRight size={13} className={"chevron" + (open ? " open" : "")} />
        <Brain size={14} />
        {item.status === "in_progress" ? "Thinking…" : "Thought"}
      </button>
      {open && <div className="reasoning-text">{item.text}</div>}
    </div>
  );
}

const toolIcons: Record<string, LucideIcon> = {
  read: FileText,
  edit: Pencil,
  execute: Terminal,
  search: Search,
  fetch: Globe,
  think: Users,
};

function ToolCallView({ item, store }: { item: Item; store: SessionStore }) {
  const [open, setOpen] = useState(false);
  const tool = item.tool;
  const children = store.childrenOf(item);
  const Icon = toolIcons[tool?.kind ?? ""] ?? Wrench;
  const input = tool?.input;
  const hasInput = input !== undefined && input !== null && !(typeof input === "object" && !Array.isArray(input) && Object.keys(input).length === 0);
  return (
    <div className="tool-call card">
      <button className="tool-header" onClick={() => setOpen(!open)} aria-expanded={open}>
        <Icon size={15} className="c-subtext tool-icon" />
        <span className={"tool-title" + (open ? "" : " clamp2")}>{tool?.title || tool?.name || "Tool"}</span>
        <span className="tool-status">
          <ItemStatusIcon status={item.status} />
        </span>
      </button>
      {/* Shown without expanding: a screenshot is often the point. */}
      {item.images && item.images.length > 0 && <ImageGallery images={item.images} height={160} />}
      {open && (
        <>
          {hasInput && <ToolInputPreview name={tool?.name} input={input} />}
          {tool?.output && <CodeBlock title={tool.exitCode !== undefined ? `Output (exit ${tool.exitCode})` : "Output"} text={tool.output} />}
          {children.length > 0 && (
            <div className="tool-children">
              {children.map((c) => (
                <ItemView key={c.id} item={c} store={store} />
              ))}
            </div>
          )}
        </>
      )}
      {!open && children.length > 0 && (
        <div className="tool-steps">
          {children.length} sub-agent step{children.length === 1 ? "" : "s"}
        </div>
      )}
    </div>
  );
}

function ItemStatusIcon({ status }: { status: string }) {
  switch (status) {
    case "in_progress":
      return <Spinner size={13} />;
    case "pending":
      return <Hand size={14} strokeWidth={2.25} className="c-yellow" />;
    case "failed":
      return <CircleX size={14} className="c-red" />;
    case "completed":
      return <CircleCheck size={14} className="c-green" />;
    default:
      return null;
  }
}

function field(v: JSONValue | undefined, key: string): JSONValue | undefined {
  return v && typeof v === "object" && !Array.isArray(v) ? v[key] : undefined;
}

function str(v: JSONValue | undefined): string | undefined {
  return typeof v === "string" ? v : undefined;
}

/** Shows a tool's input the way a reviewer wants to see it: the command for
 *  shell calls, a mini diff for edits, the content for writes, JSON otherwise. */
export function ToolInputPreview({ name, input }: { name?: string; input: JSONValue }) {
  const cmd = str(field(input, "command"));
  if (cmd !== undefined) return <CodeBlock title="Command" text={cmd} />;
  const oldS = str(field(input, "old_string"));
  const newS = str(field(input, "new_string"));
  if (oldS !== undefined && newS !== undefined) return <EditPreview edits={[[oldS, newS]]} />;
  const edits = field(input, "edits");
  if (Array.isArray(edits)) {
    return (
      <EditPreview
        edits={edits.flatMap((e) => {
          const o = str(field(e, "old_string"));
          const n = str(field(e, "new_string"));
          return o !== undefined && n !== undefined ? [[o, n] as [string, string]] : [];
        })}
      />
    );
  }
  const content = str(field(input, "content"));
  if (content !== undefined) return <CodeBlock title="New content" text={content} />;
  return <CodeBlock title={name || "Input"} text={JSON.stringify(input, null, 2)} />;
}

function EditPreview({ edits }: { edits: [string, string][] }) {
  const lines = edits.flatMap(([o, n]) => [...o.split("\n").map((t) => ({ text: "- " + t, added: false })), ...n.split("\n").map((t) => ({ text: "+ " + t, added: true }))]);
  return (
    <div className="inset">
      <div className="inset-title">Change</div>
      <div className="edit-lines">
        {lines.slice(0, 60).map((l, i) => (
          <div key={i} className={l.added ? "line-add" : "line-del"}>
            {l.text || " "}
          </div>
        ))}
      </div>
      {lines.length > 60 && <div className="inset-title">… {lines.length - 60} more lines</div>}
    </div>
  );
}

export function CodeBlock({ title, text }: { title: string; text: string }) {
  const [full, setFull] = useState(false);
  const lines = text.split("\n");
  const shown = full || lines.length <= 30 ? text : lines.slice(0, 30).join("\n");
  return (
    <div className="inset">
      <div className="inset-title">{title}</div>
      <pre className="code">{shown}</pre>
      {lines.length > 30 && !full && (
        <button className="link small" onClick={() => setFull(true)}>
          Show all {lines.length} lines
        </button>
      )}
    </div>
  );
}

function PlanView({ plan }: { plan?: Plan }) {
  return (
    <div className="plan card">
      <div className="plan-title">
        <ClipboardList size={14} /> Plan
      </div>
      {plan?.text && <Markdown text={plan.text} />}
      {(plan?.entries ?? []).map((e, i) => (
        <div key={i} className={"plan-entry " + e.status}>
          {e.status === "completed" ? (
            <CircleCheck size={14} className="c-green" />
          ) : e.status === "in_progress" ? (
            <CircleDashed size={14} className="c-peach" />
          ) : (
            <span className="plan-dot" />
          )}
          <span>{e.content}</span>
        </div>
      ))}
    </div>
  );
}

/** Marks a turn that did not finish normally. */
export function TurnFooter({ turn }: { turn: Turn }) {
  return turn.status === "failed" ? <div className="turn-footer c-red">Failed</div> : <div className="turn-footer">Interrupted</div>;
}
