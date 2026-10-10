import { Check, ChevronLeft, ChevronRight, Folder, FolderGit2, FolderPlus, ImagePlus, TriangleAlert } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { effortsForModel, usable, type FSListing, type HarnessInfo, type Session } from "../protocol/models";
import { Attachments } from "../store/attachments";
import { useObserved } from "../store/observable";
import { storage } from "../store/storage";
import { HarnessIcon } from "./components";
import { useConnection } from "./context";
import { AttachButton, AttachmentStrip, imageFiles, useImageDrop } from "./images";
import { Modal, Spinner } from "./primitives";

const custom = "__custom";

export function NewSession({ onClose, onCreated }: { onClose: () => void; onCreated: (s: Session) => void }) {
  const connection = useConnection();
  useObserved(connection);
  const attachments = useMemo(() => new Attachments(connection), [connection]);
  useObserved(attachments);

  const [projectID, setProjectID] = useState("");
  const [harnessID, setHarnessID] = useState("");
  const [model, setModel] = useState("");
  const [customModel, setCustomModel] = useState("");
  const [effort, setEffort] = useState("");
  const [mode, setMode] = useState("");
  const [useWorktree, setUseWorktree] = useState(false);
  const [branch, setBranch] = useState("");
  const [baseRef, setBaseRef] = useState("");
  const [branches, setBranches] = useState<string[]>([]);
  const [prompt, setPrompt] = useState("");
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<string>();
  const [browsing, setBrowsing] = useState(false);

  const project = connection.projects.get(projectID);
  const harness = connection.harness(harnessID);
  const harnesses = connection.harnesses;

  /** The model id to request; undefined for the harness default. */
  const chosenModel = (model === custom || !harness?.models?.length ? customModel : model) || undefined;

  // Start from the last project and agent.
  useEffect(() => {
    if (!harnesses) {
      void connection.refreshHarnesses();
      return;
    }
    const lastProject = storage.get("lastProject") ?? "";
    if (!projectID && connection.projects.has(lastProject)) setProjectID(lastProject);
    if (!harnessID) {
      const last = storage.get("lastHarness") ?? "claude";
      const h = connection.harness(last);
      setHarnessID(h && usable(h) ? last : (harnesses.find(usable)?.id ?? ""));
    }
  }, [harnesses]);

  // Starts from the model, effort and permission mode last used with this
  // agent, if it still offers them.
  useEffect(() => {
    setModel("");
    setCustomModel("");
    setEffort("");
    setMode(harness?.defaultMode ?? harness?.modes?.[0]?.id ?? "");
    if (!harness) return;
    const models = harness.models ?? [];
    const m = storage.get("lastModel." + harness.id);
    let restored: string | undefined;
    if (m) {
      if (models.some((x) => x.id === m)) {
        setModel(m);
        restored = m;
      } else if (harness.caps.freeModel) {
        setModel(models.length ? custom : "");
        setCustomModel(m);
        restored = m;
      }
    }
    const e = storage.get("lastEffort." + harness.id);
    if (e && effortsForModel(harness, restored).some((x) => x.id === e)) setEffort(e);
    const md = storage.get("lastMode." + harness.id);
    if (md && harness.modes?.some((x) => x.id === md)) setMode(md);
  }, [harnessID, !!harness]);

  // Models support different effort levels.
  useEffect(() => {
    if (harness && !effortsForModel(harness, chosenModel).some((e) => e.id === effort)) setEffort("");
  }, [chosenModel]);

  useEffect(() => {
    setUseWorktree(false);
    setBranches([]);
    if (!project?.isGitRepo) return;
    let live = true;
    connection.branches(project.id).then(
      (r) => live && setBranches(r.branches),
      () => {},
    );
    return () => {
      live = false;
    };
  }, [projectID]);

  const create = async () => {
    if (!project || !harness || creating) return;
    setCreating(true);
    setError(undefined);
    try {
      const s = await connection.createSession({
        projectId: project.id,
        harness: harness.id,
        model: chosenModel,
        effort: effort || undefined,
        mode: mode || undefined,
        workspace: useWorktree ? { kind: "worktree", branch: branch || undefined, baseRef: baseRef || undefined } : { kind: "root" },
        prompt,
        images: attachments.refs.length ? attachments.refs.map((r) => r.id) : undefined,
      });
      storage.set("lastHarness", harness.id);
      storage.set("lastProject", project.id);
      storage.set("lastModel." + harness.id, chosenModel ?? "");
      storage.set("lastEffort." + harness.id, effort);
      storage.set("lastMode." + harness.id, mode);
      onClose();
      onCreated(s);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setCreating(false);
    }
  };

  const efforts = harness ? effortsForModel(harness, chosenModel) : [];
  const drop = useImageDrop(attachments);
  const canStart = !!project && !!harness && attachments.ready;

  if (browsing) {
    return (
      <DirectoryBrowser
        onCancel={() => setBrowsing(false)}
        onPick={async (path) => {
          setBrowsing(false);
          try {
            setProjectID((await connection.addProject(path)).id);
          } catch (e) {
            setError((e as Error).message);
          }
        }}
      />
    );
  }

  return (
    <Modal
      title="New session"
      onClose={onClose}
      trailing={
        creating ? (
          <Spinner />
        ) : (
          <button className="link strong" disabled={!canStart} onClick={create}>
            Start
          </button>
        )
      }
    >
      <div className={"form" + (drop.over ? " dropping" : "")} {...drop.handlers}>
        <h4 className="form-header">Project</h4>
        <section className="form-section">
          <label className="form-row">
            <span>Project</span>
            <select value={projectID} onChange={(e) => setProjectID(e.target.value)}>
              <option value="">Choose…</option>
              {connection.sortedProjects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </label>
          <button className="form-row link with-icon" onClick={() => setBrowsing(true)}>
            <FolderPlus size={17} /> Add a folder…
          </button>
          {project && <div className="form-row mono small hint">{project.path}</div>}
        </section>

        <h4 className="form-header">Agent</h4>
        <section className="form-section">
          {harnesses ? (
            <>
              {harnesses.map((h) => (
                <HarnessChoiceRow key={h.id} harness={h} selected={h.id === harnessID} onSelect={() => setHarnessID(h.id)} />
              ))}
              {harnesses.length === 0 && (
                <div className="form-row hint">No agents are installed on the server. Install Claude Code, Codex, Pi or an ACP agent there.</div>
              )}
            </>
          ) : (
            <div className="form-row hint">
              <Spinner /> Checking installed agents…
            </div>
          )}
        </section>

        {harness && (
          <>
            <h4 className="form-header">Options</h4>
            <section className="form-section">
              {!!harness.models?.length && (
                <label className="form-row">
                  <span>Model</span>
                  <select value={model} onChange={(e) => setModel(e.target.value)}>
                    <option value="">Default</option>
                    {harness.models.map((m) => (
                      <option key={m.id} value={m.id}>
                        {m.name}
                      </option>
                    ))}
                    {harness.caps.freeModel && <option value={custom}>Custom…</option>}
                  </select>
                </label>
              )}
              {(model === custom || (harness.caps.freeModel && !harness.models?.length)) && (
                <label className="form-row">
                  <span>Model id</span>
                  <input className="text-field" value={customModel} onChange={(e) => setCustomModel(e.target.value)} autoCapitalize="off" autoCorrect="off" spellCheck={false} />
                </label>
              )}
              {efforts.length > 0 && (
                <>
                  <label className="form-row">
                    <span>Effort</span>
                    <select value={effort} onChange={(e) => setEffort(e.target.value)}>
                      <option value="">Default</option>
                      {efforts.map((e) => (
                        <option key={e.id} value={e.id}>
                          {e.name}
                        </option>
                      ))}
                    </select>
                  </label>
                  {efforts.find((e) => e.id === effort)?.description && (
                    <div className="form-row small hint">{efforts.find((e) => e.id === effort)?.description}</div>
                  )}
                </>
              )}
              {!!harness.modes?.length && (
                <>
                  <label className="form-row">
                    <span>Permissions</span>
                    <select value={mode} onChange={(e) => setMode(e.target.value)}>
                      {harness.modes.map((m) => (
                        <option key={m.id} value={m.id}>
                          {m.name}
                        </option>
                      ))}
                    </select>
                  </label>
                  {harness.modes.find((m) => m.id === mode)?.description && (
                    <div className="form-row small hint">{harness.modes.find((m) => m.id === mode)?.description}</div>
                  )}
                </>
              )}
            </section>
          </>
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
                <>
                  <label className="form-row">
                    <span>Branch</span>
                    <input className="text-field" placeholder="auto" value={branch} onChange={(e) => setBranch(e.target.value)} autoCapitalize="off" autoCorrect="off" spellCheck={false} />
                  </label>
                  <label className="form-row">
                    <span>Based on</span>
                    <select value={baseRef} onChange={(e) => setBaseRef(e.target.value)}>
                      <option value="">Current HEAD</option>
                      {branches.map((b) => (
                        <option key={b} value={b}>
                          {b}
                        </option>
                      ))}
                    </select>
                  </label>
                </>
              )}
            </section>
            <p className="form-footer">
              {useWorktree
                ? "The agent works on its own branch in a separate checkout. You can revert its turns."
                : "The agent works directly in the project folder."}
            </p>
          </>
        )}

        <h4 className="form-header">Prompt</h4>
        <section className="form-section">
          <div className="form-row">
            <textarea
              className="prompt-field"
              rows={4}
              placeholder="What should the agent do?"
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && canStart) {
                  e.preventDefault();
                  void create();
                }
              }}
              onPaste={(e) => {
                const files = imageFiles(e.clipboardData);
                if (files.length) {
                  e.preventDefault();
                  void attachments.add(files);
                }
              }}
            />
          </div>
          {!attachments.isEmpty && (
            <div className="form-row">
              <AttachmentStrip attachments={attachments} />
            </div>
          )}
          <AttachButton attachments={attachments} className="form-row link with-icon">
            <ImagePlus size={17} /> Add Images
          </AttachButton>
        </section>
        <p className="form-footer">Optional. Without one, the session starts empty and you write the first prompt in it.</p>

        {(error || attachments.error) && (
          <div className="form-message c-red">
            <TriangleAlert size={15} /> {error ?? attachments.error}
          </div>
        )}
      </div>
    </Modal>
  );
}

function HarnessChoiceRow({ harness, selected, onSelect }: { harness: HarnessInfo; selected: boolean; onSelect: () => void }) {
  const ok = usable(harness);
  return (
    <button className="form-row harness-row" disabled={!ok} onClick={onSelect} aria-pressed={selected} style={{ opacity: ok ? 1 : 0.5 }}>
      <span className="harness-row-icon">
        <HarnessIcon id={harness.id} size={18} />
      </span>
      <span className="harness-row-text">
        <span>
          {harness.name} {harness.version && <span className="mono small c-overlay">{harness.version}</span>}
        </span>
        {harness.hint && !ok && <span className="small c-yellow">{harness.hint}</span>}
      </span>
      {selected && <Check size={17} className="c-accent" strokeWidth={2.5} />}
    </button>
  );
}

/** Drills down from rad's roots to a folder to add as a project. */
function DirectoryBrowser({ onCancel, onPick }: { onCancel: () => void; onPick: (path: string) => void }) {
  const connection = useConnection();
  const [stack, setStack] = useState<(string | undefined)[]>([undefined]);
  const path = stack[stack.length - 1];
  const [listing, setListing] = useState<FSListing>();
  const [error, setError] = useState<string>();

  useEffect(() => {
    let live = true;
    setListing(undefined);
    setError(undefined);
    connection.listDirectory(path).then(
      (l) => live && setListing(l),
      (e) => live && setError(e.message),
    );
    return () => {
      live = false;
    };
  }, [path]);

  const title = path ? path.slice(path.replace(/\/$/, "").lastIndexOf("/") + 1) || path : "Folders";
  return (
    <Modal
      title={title}
      onClose={onCancel}
      leading={
        stack.length > 1 ? (
          <button className="link with-icon" onClick={() => setStack(stack.slice(0, -1))}>
            <ChevronLeft size={18} /> Back
          </button>
        ) : undefined
      }
      trailing={
        path && (
          <button className="link strong" onClick={() => onPick(path)}>
            Use folder
          </button>
        )
      }
    >
      <div className="form">
        <section className="form-section">
          {error && (
            <div className="form-row c-red">
              <TriangleAlert size={15} /> {error}
            </div>
          )}
          {listing ? (
            <>
              {listing.entries.map((e) => (
                <button key={e.path} className="form-row folder-row" onClick={() => setStack([...stack, e.path])}>
                  {e.isGitRepo ? <FolderGit2 size={17} className="c-accent" /> : <Folder size={17} className="c-accent" />}
                  <span className="folder-name">{path === undefined ? e.path : e.name}</span>
                  {e.isGitRepo && <span className="chip-tag">git</span>}
                  <ChevronRight size={15} className="c-overlay" />
                </button>
              ))}
              {listing.entries.length === 0 && <div className="form-row hint">No subfolders</div>}
            </>
          ) : (
            !error && (
              <div className="form-row center">
                <Spinner />
              </div>
            )
          )}
        </section>
      </div>
    </Modal>
  );
}
