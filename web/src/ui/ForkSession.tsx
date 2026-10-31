import { TriangleAlert } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { effortsForModel, type Session } from "../protocol/models";
import { useObserved } from "../store/observable";
import { useConnection } from "./context";
import { AgentOptions, custom, HarnessChoiceRow } from "./NewSession";
import { Modal, Spinner } from "./primitives";

/** Forks `session` after one of its turns, into a new session that goes on
 *  from there. `onForked` gets the fork, and whether it opens without a
 *  prompt. */
export function ForkSession({
  session,
  onClose,
  onForked,
}: {
  session: Session;
  onClose: () => void;
  onForked: (s: Session, empty: boolean) => void;
}) {
  const connection = useConnection();
  useObserved(connection);
  const store = useMemo(() => connection.sessionStore(session.id), [connection, session.id]);
  useObserved(store);

  /** The turns it can go on after, newest first, with their prompts' first lines. */
  const turns = useMemo(() => {
    const prompts = new Map<string, string>();
    for (const it of store.transcript) {
      if (it.kind === "user_message" && it.turnId && !prompts.has(it.turnId)) prompts.set(it.turnId, (it.text ?? "").trim().split("\n")[0]);
    }
    return store.sortedTurns
      .filter((t) => t.status !== "running")
      .reverse()
      .map((t) => ({ n: t.n, prompt: prompts.get(t.id) ?? "" }));
  }, [store.transcript, store.turns, store.getVersion()]);

  const [turn, setTurn] = useState<number>();
  const [harnessID, setHarnessID] = useState(session.harness);
  const [model, setModel] = useState("");
  const [customModel, setCustomModel] = useState("");
  const [effort, setEffort] = useState("");
  const [mode, setMode] = useState("");
  const [useWorktree, setUseWorktree] = useState(session.workspace.kind === "worktree");
  const [branch, setBranch] = useState("");
  const [prompt, setPrompt] = useState("");
  const [forking, setForking] = useState(false);
  const [error, setError] = useState<string>();

  const project = connection.projects.get(session.projectId);
  const harness = connection.harness(harnessID);
  const harnesses = connection.harnesses;
  const chosenModel = (model === custom || !harness?.models?.length ? customModel : model) || undefined;
  const efforts = harness ? effortsForModel(harness, chosenModel) : [];
  const n = turn ?? turns[0]?.n;

  useEffect(() => {
    if (!harnesses) void connection.refreshHarnesses();
  }, [harnesses]);

  // The session's own agent starts where the session is; another, at its defaults.
  useEffect(() => {
    const same = harnessID === session.harness;
    const m = same ? (session.model ?? "") : "";
    const listed = !m || !!harness?.models?.some((x) => x.id === m);
    setModel(listed ? m : harness?.models?.length ? custom : "");
    setCustomModel(listed ? "" : m);
    setEffort(same ? (session.effort ?? "") : "");
    setMode(same && session.mode ? session.mode : (harness?.defaultMode ?? harness?.modes?.[0]?.id ?? ""));
  }, [harnessID, !!harness]);

  useEffect(() => {
    if (harness && !efforts.some((e) => e.id === effort)) setEffort("");
  }, [chosenModel]);

  const fork = async () => {
    if (!harness || n === undefined || forking) return;
    setForking(true);
    setError(undefined);
    try {
      const s = await connection.fork({
        sessionId: session.id,
        turn: n,
        harness: harness.id,
        model: chosenModel,
        effort: effort || undefined,
        mode: mode || undefined,
        workspace: project?.isGitRepo ? (useWorktree ? { kind: "worktree", branch: branch || undefined } : { kind: "root" }) : undefined,
        prompt: prompt.trim() ? prompt : undefined,
      });
      onClose();
      onForked(s, !prompt.trim());
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setForking(false);
    }
  };

  const canFork = !!harness && n !== undefined;
  return (
    <Modal
      title="Fork Session"
      onClose={onClose}
      trailing={
        forking ? (
          <Spinner />
        ) : (
          <button className="link strong" disabled={!canFork} onClick={() => fork()}>
            Fork
          </button>
        )
      }
    >
      <div className="form">
        <h4 className="form-header">From</h4>
        <section className="form-section">
          <label className="form-row">
            <span>After turn</span>
            <select value={n ?? ""} onChange={(e) => setTurn(Number(e.target.value))}>
              {turns.map((t) => (
                <option key={t.n} value={t.n}>
                  {`Turn ${t.n}` + (t.prompt ? ` · ${short(t.prompt)}` : "")}
                </option>
              ))}
            </select>
          </label>
          {turns.length === 0 && <div className="form-row hint">{store.synced ? "No turn has ended yet." : <Spinner />}</div>}
        </section>

        <h4 className="form-header">Agent</h4>
        <section className="form-section">
          {harnesses ? (
            harnesses.map((h) => <HarnessChoiceRow key={h.id} harness={h} selected={h.id === harnessID} onSelect={() => setHarnessID(h.id)} />)
          ) : (
            <div className="form-row hint">
              <Spinner /> Checking installed agents…
            </div>
          )}
        </section>

        {harness && (
          <AgentOptions
            harness={harness}
            model={model}
            setModel={setModel}
            customModel={customModel}
            setCustomModel={setCustomModel}
            effort={effort}
            setEffort={setEffort}
            efforts={efforts}
            mode={mode}
            setMode={setMode}
          />
        )}

        {project?.isGitRepo && (
          <>
            <h4 className="form-header">Workspace</h4>
            <section className="form-section">
              <label className="form-row">
                <span>Isolated git worktree</span>
                <input type="checkbox" className="switch" checked={useWorktree} onChange={(e) => setUseWorktree(e.target.checked)} />
              </label>
              {useWorktree && (
                <label className="form-row">
                  <span>Branch</span>
                  <input className="text-field" placeholder="auto" value={branch} onChange={(e) => setBranch(e.target.value)} autoCapitalize="off" autoCorrect="off" spellCheck={false} />
                </label>
              )}
            </section>
            <p className="form-footer">
              {useWorktree ? "The fork gets its own branch, with the files as they were after that turn." : "The fork works in the project folder, as it is now."}
            </p>
          </>
        )}

        <h4 className="form-header">Prompt</h4>
        <section className="form-section">
          <div className="form-row">
            <textarea
              className="prompt-field"
              rows={4}
              placeholder="What should the agent do next?"
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && canFork) {
                  e.preventDefault();
                  void fork();
                }
              }}
            />
          </div>
        </section>
        <p className="form-footer">
          Optional. Without one, the fork opens with the conversation so far, and you write the next prompt in it. The same agent picks up its own
          conversation; another agent gets it as a transcript with the first prompt.
        </p>

        {error && (
          <div className="form-message c-red">
            <TriangleAlert size={15} /> {error}
          </div>
        )}
      </div>
    </Modal>
  );
}

function short(s: string): string {
  return s.length > 48 ? s.slice(0, 47) + "…" : s;
}
