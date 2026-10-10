import { CircleCheck, CircleMinus, CircleX, ClipboardList, Clock, Hand, MessageCircleQuestion } from "lucide-react";
import { useState } from "react";
import type { Approval, ApprovalOption, Item, Question } from "../../protocol/models";
import type { SessionStore } from "../../store/sessionStore";
import { Markdown } from "../Markdown";
import { Confirm } from "../primitives";
import { ToolInputPreview } from "./ItemView";

export function ApprovalView({ item, store }: { item: Item; store: SessionStore }) {
  if (item.status === "pending" && item.approval) return <PendingApproval item={item} approval={item.approval} store={store} />;
  return <ResolvedApproval item={item} />;
}

function PendingApproval({ item, approval: a, store }: { item: Item; approval: Approval; store: SessionStore }) {
  const [answers, setAnswers] = useState<Record<string, string[]>>({});
  const [askingReason, setAskingReason] = useState<ApprovalOption>();
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();

  const Icon = a.special === "question" ? MessageCircleQuestion : a.special === "plan" ? ClipboardList : Hand;
  const allAnswered = (a.questions ?? []).every((q) => (answers[q.question] ?? []).length > 0);

  const respond = async (o: ApprovalOption, message?: string) => {
    setBusy(true);
    setError(undefined);
    const flat = Object.fromEntries(Object.entries(answers).map(([q, v]) => [q, [...v].sort().join(", ")]));
    try {
      await store.respond(item, o.id, message || undefined, a.special === "question" ? flat : undefined);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const pick = (q: Question, label: string) => {
    setAnswers((prev) => {
      const set = prev[q.question] ?? [];
      const next = q.multiSelect ? (set.includes(label) ? set.filter((l) => l !== label) : [...set, label]) : [label];
      return { ...prev, [q.question]: next };
    });
  };

  return (
    <div className="approval card">
      <div className="approval-title">
        <Icon size={16} strokeWidth={2.25} className="c-yellow" />
        <span>{a.title}</span>
      </div>
      {a.detail && <div className="approval-detail">{a.detail}</div>}
      {a.special === "plan" && a.planText ? (
        <div className="approval-plan">
          <Markdown text={a.planText} />
        </div>
      ) : a.special === "question" ? (
        (a.questions ?? []).map((q) => (
          <div key={q.question} className="question">
            <div className="question-text">{q.question}</div>
            <div className="chips">
              {q.options.map((o) => {
                const on = (answers[q.question] ?? []).includes(o.label);
                return (
                  <button key={o.label} className={"chip" + (on ? " on" : "")} onClick={() => pick(q, o.label)} aria-pressed={on}>
                    {on ? <CircleCheck size={14} /> : <span className="chip-dot" />}
                    <span>
                      {o.label}
                      {o.description && <span className="chip-detail">{o.description}</span>}
                    </span>
                  </button>
                );
              })}
            </div>
          </div>
        ))
      ) : a.input !== undefined && a.input !== null ? (
        <ToolInputPreview name={a.toolName} input={a.input} />
      ) : null}
      {error && <div className="c-red small">{error}</div>}
      <div className="approval-buttons">
        {(a.options ?? []).map((o) => (
          <button
            key={o.id}
            className={"button prominent " + (o.kind === "deny" ? "deny" : o.kind === "allow_session" ? "allow-session" : "allow")}
            disabled={busy || (a.special === "question" && o.kind !== "deny" && !allAnswered)}
            onClick={() => (o.kind === "deny" && a.special !== "question" ? setAskingReason(o) : void respond(o))}
          >
            {o.label}
          </button>
        ))}
      </div>
      {askingReason && (
        <Confirm
          title="Reason (optional)"
          onClose={() => setAskingReason(undefined)}
          actions={[{ label: "Deny", destructive: true, onSelect: () => void respond(askingReason, reason) }]}
        >
          <input className="text-field" data-autofocus placeholder="Tell the agent why" value={reason} onChange={(e) => setReason(e.target.value)} />
        </Confirm>
      )}
    </div>
  );
}

function ResolvedApproval({ item }: { item: Item }) {
  const a = item.approval;
  const option = a?.options?.find((o) => o.id === a.decision?.optionId);
  let Icon = CircleMinus,
    color = "c-subtext",
    verb = "Cancelled";
  if (item.status === "resolved" && option?.kind === "deny") [Icon, color, verb] = [CircleX, "c-red", option?.label ?? "Denied"];
  else if (item.status === "resolved") [Icon, color, verb] = [CircleCheck, "c-green", option?.label ?? "Allowed"];
  else if (item.status === "expired") [Icon, color, verb] = [Clock, "c-subtext", "Expired"];
  const answers = Object.entries(a?.decision?.answers ?? {}).sort(([x], [y]) => x.localeCompare(y));
  return (
    <div className="resolved-approval">
      <div className={"resolved-line " + color}>
        <Icon size={13} />
        <span className="clamp2">
          {verb}: {a?.title}
        </span>
      </div>
      {answers.map(([q, ans]) => (
        <div key={q} className="resolved-detail">
          {q} → {ans}
        </div>
      ))}
      {a?.decision?.message && <div className="resolved-detail">“{a.decision.message}”</div>}
    </div>
  );
}
