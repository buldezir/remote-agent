import { ArrowRightLeft, ChevronLeft, ChevronRight, FileMinus, FilePen, FilePlus, TriangleAlert, Undo2, WrapText, type LucideIcon } from "lucide-react";
import { useEffect, useState } from "react";
import type { Diff, FileStat, Turn } from "../protocol/models";
import { useObserved } from "../store/observable";
import type { SessionStore } from "../store/sessionStore";
import { patchLines, splitPatch } from "./patch";
import { Confirm, Menu, Modal, Spinner } from "./primitives";
import { settings, useSettings } from "./settings";

/** The session's changes, or one turn's, file by file. */
export function DiffView({ store, canRevert, onClose }: { store: SessionStore; canRevert: boolean; onClose: () => void }) {
  useObserved(store);
  const [scope, setScope] = useState("session"); // or a turn id
  const [diff, setDiff] = useState<Diff>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string>();
  const [reverted, setReverted] = useState<string>();
  const [revertTurn, setRevertTurn] = useState<Turn>();
  const [file, setFile] = useState<FileStat>();

  const load = async () => {
    setLoading(true);
    setError(undefined);
    try {
      setDiff(scope === "session" ? await store.sessionDiff() : await store.turnDiff(scope));
    } catch (e) {
      setDiff(undefined);
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, [scope]);

  const turns = store.sortedTurns.filter((t) => t.checkpointBefore);
  const sections = diff ? splitPatch(diff.patch) : new Map<string, string>();

  const revert = async (t: Turn) => {
    try {
      const files = await store.revert(t.n);
      setReverted(`Reverted ${files.length} file${files.length === 1 ? "" : "s"}`);
      await load();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  if (file) {
    return (
      <Modal
        title={baseName(file.path)}
        wide
        onClose={onClose}
        leading={
          <button className="link with-icon" onClick={() => setFile(undefined)}>
            <ChevronLeft size={18} /> Changes
          </button>
        }
        trailing={<WrapToggle />}
      >
        <FileDiff file={file} patch={sections.get(file.path) ?? ""} />
      </Modal>
    );
  }

  const adds = diff?.files.reduce((n, f) => n + f.additions, 0) ?? 0;
  const dels = diff?.files.reduce((n, f) => n + f.deletions, 0) ?? 0;
  return (
    <Modal
      title="Changes"
      wide
      onClose={onClose}
      leading={
        canRevert ? (
          <Menu
            className="link with-icon"
            align="start"
            label={
              <>
                <Undo2 size={16} /> Revert files…
              </>
            }
            disabled={store.isRunning || turns.length === 0}
            entries={[...turns].reverse().map((t) => ({ label: `Before turn ${t.n}`, onSelect: () => setRevertTurn(t) }))}
          />
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
      <div className="form">
        <section className="form-section">
          <label className="form-row">
            <span>Scope</span>
            <select value={scope} onChange={(e) => setScope(e.target.value)}>
              <option value="session">Whole session</option>
              {turns.map((t) => (
                <option key={t.id} value={t.id}>
                  Turn {t.n}
                </option>
              ))}
            </select>
          </label>
        </section>
        {error && (
          <div className="form-message c-red">
            <TriangleAlert size={15} /> {error}
          </div>
        )}
        {reverted && (
          <div className="form-message c-green">
            <Undo2 size={15} /> {reverted}
          </div>
        )}
        {diff ? (
          <>
            <h4 className="form-header">
              {diff.files.length} file{diff.files.length === 1 ? "" : "s"}{"  "}
              <span className="c-green">+{adds}</span> <span className="c-red">−{dels}</span>
            </h4>
            <section className="form-section">
              {diff.files.length === 0 && <div className="form-row hint">No changes</div>}
              {diff.files.map((f) => (
                <button key={f.path} className="form-row file-row" onClick={() => setFile(f)}>
                  <FileRow file={f} />
                  <ChevronRight size={15} className="c-overlay" />
                </button>
              ))}
            </section>
            {diff.truncated && <p className="form-footer">The patch was truncated; some files may show no lines.</p>}
          </>
        ) : (
          loading && (
            <div className="center pad">
              <Spinner />
            </div>
          )
        )}
      </div>
      {revertTurn && (
        <Confirm
          title="Revert files?"
          message={`Files changed in turn ${revertTurn.n} and later are restored in the worktree. The conversation is kept, and the agent is told about the revert.`}
          actions={[{ label: `Revert to before turn ${revertTurn.n}`, destructive: true, onSelect: () => void revert(revertTurn) }]}
          onClose={() => setRevertTurn(undefined)}
        />
      )}
    </Modal>
  );
}

const fileIcons: Record<string, [LucideIcon, string]> = {
  added: [FilePlus, "c-green"],
  deleted: [FileMinus, "c-red"],
  renamed: [ArrowRightLeft, "c-blue"],
  modified: [FilePen, "c-peach"],
};

function baseName(p: string) {
  return p.slice(p.lastIndexOf("/") + 1);
}

function FileRow({ file }: { file: FileStat }) {
  const [Icon, color] = fileIcons[file.status] ?? fileIcons.modified;
  const dir = file.path.includes("/") ? file.path.slice(0, file.path.lastIndexOf("/")) : "";
  return (
    <>
      <Icon size={16} className={color} />
      <span className="file-name">
        <span className="strong">{baseName(file.path)}</span>
        {(file.oldPath || dir) && <span className="file-dir mono">{file.oldPath ? `${file.oldPath} → ${file.path}` : dir}</span>}
      </span>
      {file.binary ? (
        <span className="hint small">binary</span>
      ) : (
        <span className="mono small">
          <span className="c-green">+{file.additions}</span> <span className="c-red">−{file.deletions}</span>
        </span>
      )}
    </>
  );
}

function WrapToggle() {
  const s = useSettings();
  return (
    <button className={"icon-button" + (s.diffWrap ? " on" : "")} onClick={() => settings.update({ diffWrap: !s.diffWrap })} title="Wrap lines" aria-pressed={s.diffWrap}>
      <WrapText size={18} />
    </button>
  );
}

function FileDiff({ file, patch }: { file: FileStat; patch: string }) {
  const s = useSettings();
  const lines = patchLines(patch);
  return (
    <div className={"file-diff" + (s.diffWrap ? " wrap" : "")}>
      {lines.length === 0 && <div className="hint pad">{file.binary ? "Binary file" : "No textual changes"}</div>}
      <div className="file-diff-lines">
        {lines.map((l, i) => (
          <div key={i} className={"diff-line " + l.kind}>
            {l.text || " "}
          </div>
        ))}
      </div>
    </div>
  );
}
