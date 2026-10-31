import { ChevronLeft, RotateCw, TriangleAlert } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import type { HarnessInfo, ImportableSession, Project, Session } from "../protocol/models";
import { RelativeTime } from "./components";
import { useConnection } from "./context";
import { Modal, Spinner } from "./primitives";

/** The sessions `harness` ran in `project`'s folder outside Remote Agent,
 *  such as in a terminal, to pick one up. A click imports it at once, with
 *  the model, effort and mode New Session chose; `onImported` gets the new
 *  session. */
export function ImportSession({
  project,
  harness,
  model,
  effort,
  mode,
  onBack,
  onImported,
}: {
  project: Project;
  harness: HarnessInfo;
  model?: string;
  effort?: string;
  mode?: string;
  onBack: () => void;
  onImported: (s: Session) => void;
}) {
  const connection = useConnection();
  const [sessions, setSessions] = useState<ImportableSession[]>();
  const [loadError, setLoadError] = useState<string>();
  /** The id of the session being imported. */
  const [importing, setImporting] = useState<string>();
  const [error, setError] = useState<string>();
  const live = useRef(true);

  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
    };
  }, []);

  const load = async () => {
    setLoadError(undefined);
    setSessions(undefined);
    try {
      const list = await connection.importable(project.id, harness.id);
      if (live.current) setSessions(list);
    } catch (e) {
      if (live.current) setLoadError((e as Error).message);
    }
  };

  useEffect(() => {
    void load();
  }, []);

  const pick = async (s: ImportableSession) => {
    if (importing) return;
    setImporting(s.id);
    setError(undefined);
    try {
      const session = await connection.importSession({ projectId: project.id, harness: harness.id, nativeId: s.id, model, effort, mode });
      onImported(session);
    } catch (e) {
      if (live.current) setError((e as Error).message);
    } finally {
      if (live.current) setImporting(undefined);
    }
  };

  return (
    <Modal
      title="Terminal sessions"
      onClose={onBack}
      leading={
        <button className="link with-icon" onClick={onBack}>
          <ChevronLeft size={18} /> Back
        </button>
      }
    >
      <div className="form">
        <section className="form-section">
          {sessions ? (
            <>
              {sessions.map((s) => (
                <button key={s.id} className="form-row import-row" disabled={!!importing} onClick={() => void pick(s)}>
                  <span className="import-row-text">
                    <span className="clamp2">{s.title}</span>
                    <span className="small c-subtext">
                      {s.messages} message{s.messages === 1 ? "" : "s"} · <RelativeTime date={s.updatedAt} />
                    </span>
                    {s.active && (
                      <span className="small c-yellow import-warning">
                        <TriangleAlert size={13} /> Active in the last minute: if it still runs in a terminal, quit it there first.
                      </span>
                    )}
                  </span>
                  {importing === s.id && <Spinner />}
                </button>
              ))}
              {sessions.length === 0 && (
                <div className="form-row hint">
                  No {harness.name} sessions to continue in {project.name}.
                </div>
              )}
            </>
          ) : loadError ? (
            <div className="form-row stack import-error">
              <span className="c-red with-icon">
                <TriangleAlert size={15} /> {loadError}
              </span>
              <button className="button" onClick={() => void load()}>
                <RotateCw size={15} /> Retry
              </button>
            </div>
          ) : (
            <div className="form-row center">
              <Spinner />
            </div>
          )}
        </section>
        <p className="form-footer">
          Picks up a session {harness.name} ran in this folder outside Remote Agent. Its next prompt goes on with it here, in the project folder.
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
