import { ChevronRight, Ellipsis, RefreshCw, TriangleAlert, UserRound } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { effortsForModel, installable, type AgentSpec, type HarnessInfo, type ServerSettings, type ServerTask, type UpdateInfo } from "../protocol/models";
import { RPCError } from "../protocol/rpc";
import { useObserved } from "../store/observable";
import { ServerTasks, type ServerTaskProgress } from "../store/serverTask";
import { HarnessIcon } from "./components";
import { useConnection } from "./context";
import { Confirm, Menu, Modal, Spinner } from "./primitives";
import { openLink, TaskSheet } from "./TaskSheet";

/** "1 busy session", "2 busy sessions". */
function count(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}

/** An rad older than the method: it manages neither updates nor settings from here. */
function tooOld(e: unknown): boolean {
  return e instanceof RPCError && e.code === "method_not_found";
}

/** A server's own settings, as the apps' Server Settings: rad's version and
 *  updates, the agents installed there, the agent new sessions start with,
 *  and the system agent. */
export function ServerSettingsView({ onClose }: { onClose: () => void }) {
  const connection = useConnection();
  useObserved(connection);
  const tasks = useMemo(() => new ServerTasks(connection), [connection]);
  useObserved(tasks);
  const connected = connection.state.kind === "connected";

  const [update, setUpdate] = useState<UpdateInfo>();
  const [checking, setChecking] = useState(false);
  const [updateError, setUpdateError] = useState<string>();
  /** rad doesn't have server.checkUpdate. */
  const [updateTooOld, setUpdateTooOld] = useState(false);
  const [settings, setSettings] = useState<ServerSettings>();
  const [settingsError, setSettingsError] = useState<string>();
  /** rad doesn't have server.settings: no default or system agent. */
  const [settingsTooOld, setSettingsTooOld] = useState(false);
  /** A change that failed, beside the section it was made in. */
  const [saveError, setSaveError] = useState<{ field: AgentField; message: string }>();
  /** A task that couldn't start, beside the section it was started from. */
  const [startError, setStartError] = useState<{ section: "rad" | "agents"; message: string }>();
  const [starting, setStarting] = useState<string>();
  const [openTask, setOpenTask] = useState<string>();
  const [confirmUpdate, setConfirmUpdate] = useState(false);

  // Polling doesn't outlive the screen.
  useEffect(() => {
    tasks.start();
    return () => tasks.stop();
  }, [tasks]);

  const live = useRef(true);
  useEffect(
    () => () => {
      live.current = false;
    },
    [],
  );

  const check = async (refresh: boolean) => {
    setChecking(true);
    setUpdateError(undefined);
    try {
      const u = await connection.checkUpdate(refresh);
      if (!live.current) return;
      setUpdate(u);
      setUpdateTooOld(false);
    } catch (e) {
      if (!live.current) return;
      setUpdate(undefined);
      setUpdateTooOld(tooOld(e));
      if (!tooOld(e)) setUpdateError((e as Error).message);
    } finally {
      if (live.current) setChecking(false);
    }
  };

  /** The settings as the server last answered, to go back to when a change fails. */
  const confirmed = useRef<ServerSettings>(undefined);
  const saves = useRef(0);

  // On opening, and again each time the connection comes back, as after rad
  // restarted into an update.
  useEffect(() => {
    if (!connected) return;
    void check(false);
    setSettingsError(undefined);
    connection.settings().then(
      (s) => {
        if (!live.current) return;
        confirmed.current = s;
        setSettings(s);
        setSettingsTooOld(false);
      },
      (e) => {
        if (!live.current) return;
        confirmed.current = undefined;
        setSettings(undefined);
        setSettingsTooOld(tooOld(e));
        if (!tooOld(e)) setSettingsError((e as Error).message);
      },
    );
    // Tasks still running, as one started on another device, get rows.
    connection.tasks().then(
      (list) => {
        if (live.current) for (const t of list) if (t.status === "running") tasks.follow(t);
      },
      () => {},
    );
    // Without probing: the list rad has shows an agent that a failed turn
    // found signed out since the connection last looked.
    void connection.refreshHarnesses();
  }, [connected]);

  /** Starts a task and shows its sheet. */
  const begin = async (section: "rad" | "agents", key: string, start: () => Promise<ServerTask>) => {
    setStartError(undefined);
    setStarting(key);
    try {
      const p = tasks.follow(await start());
      if (live.current) setOpenTask(p.id);
    } catch (e) {
      if (live.current) setStartError({ section, message: (e as Error).message });
    } finally {
      if (live.current) setStarting(undefined);
    }
  };

  const updateServer = () => void begin("rad", "rad", () => connection.updateServer());

  /** Saves the whole settings, changed in `field`; a failure puts back what
   *  the server has. */
  const save = async (field: AgentField, next: ServerSettings) => {
    const n = ++saves.current;
    setSettings(next);
    setSaveError(undefined);
    try {
      const s = await connection.setSettings(next);
      confirmed.current = s;
      if (live.current && n === saves.current) setSettings(s);
    } catch (e) {
      if (live.current && n === saves.current) {
        setSaveError({ field, message: (e as Error).message });
        setSettings(confirmed.current);
      }
    }
  };

  const harnesses = connection.harnesses;
  /** An agent's rows, or the settings' loading state in their place. */
  const agentRows = (field: AgentField, offered: HarnessInfo[], none: string) =>
    settings ? (
      <AgentSpecRows
        settings={settings}
        field={field}
        harnesses={harnesses ?? []}
        offered={offered}
        none={none}
        disabled={!connected}
        onChange={(s) => void save(field, s)}
      />
    ) : settingsError ? (
      <div className="form-row c-red">
        <TriangleAlert size={15} /> Couldn't load the settings: {settingsError}
      </div>
    ) : (
      <div className="form-row hint">
        <Spinner /> Loading…
      </div>
    );

  const serverTask = tasks.active("update_server");
  const shownTask = tasks.get(openTask);

  return (
    <Modal
      title={
        <span className="title-stack">
          <span>{connection.server.name}</span> <span className="modal-subtitle">Server</span>
        </span>
      }
      onClose={onClose}
      leading={<span />}
      trailing={
        <button className="link strong" onClick={onClose}>
          Done
        </button>
      }
    >
      <div className="form">
        <h4 className="form-header">rad</h4>
        <section className="form-section">
          <div className="form-row">
            <span>Version</span>
            {/* The rad connected to, which an update's restart changes before the check comes back. */}
            <span className="hint">{connection.info?.version ?? update?.current}</span>
          </div>
          {serverTask ? (
            <TaskRow progress={serverTask} label="Updating…" onOpen={() => setOpenTask(serverTask.id)} />
          ) : updateTooOld && !checking ? (
            <div className="form-row small hint">
              <span>
                This rad is too old to update from here. Update it on the computer with <code>rad update</code>, or from the Remote Agent Server app's menu.
              </span>
            </div>
          ) : checking ? (
            <div className="form-row hint">
              <Spinner /> Checking for updates…
            </div>
          ) : updateError ? (
            <div className="form-row c-red">
              <TriangleAlert size={15} /> Couldn't check for updates: {updateError}
            </div>
          ) : (
            update && (
              <UpdateStatus
                update={update}
                starting={starting === "rad"}
                onUpdate={() => (update.busy.length ? setConfirmUpdate(true) : updateServer())}
              />
            )
          )}
          {!(updateTooOld && !checking) && (
            <button className="form-row link" disabled={checking || !connected} onClick={() => void check(true)}>
              Check Again
            </button>
          )}
        </section>
        {!updateTooOld && <RadFooter update={update} />}
        {startError?.section === "rad" && <p className="form-footer c-red">{startError.message}</p>}

        <h4 className="form-header">Agents</h4>
        <section className="form-section">
          {harnesses ? (
            <>
              {harnesses.map((h) => {
                const login = tasks.active("login", h.id);
                const running = login ?? tasks.active("update_agent", h.id);
                return (
                  <AgentRow
                    key={h.id}
                    harness={h}
                    running={running}
                    runningLabel={login ? "Signing in…" : "Updating…"}
                    disabled={!connected || starting === h.id}
                    onOpen={(p) => setOpenTask(p.id)}
                    onUpdate={() => void begin("agents", h.id, () => connection.updateHarness(h.id))}
                    onLogin={() => void begin("agents", h.id, () => connection.loginHarness(h.id))}
                  />
                );
              })}
              {harnesses.length === 0 && <div className="form-row hint">No agents are installed on the server.</div>}
            </>
          ) : (
            <div className="form-row hint">
              <Spinner /> Checking installed agents…
            </div>
          )}
        </section>
        {/* An older rad sends neither cap, so it offers neither. */}
        {harnesses?.some((h) => h.caps.update || h.caps.login) && (
          <p className="form-footer">
            Update runs the agent's own updater on the server. Sign In shows the agent's sign-in page here, and takes the code it gives you.
          </p>
        )}
        {startError?.section === "agents" && <p className="form-footer c-red">{startError.message}</p>}

        {/* An older rad has no settings: no default agent, and no system agent. */}
        {!settingsTooOld && (
          <>
            <h4 className="form-header">New Sessions</h4>
            <section className="form-section">{agentRows("defaultAgent", harnesses ?? [], "Last Used")}</section>
            <p className="form-footer">
              New sessions start with this agent, model and effort, on every device, and so do the sessions agents start with rad's tools unless they choose
              their own. With Last Used, each device starts with the agent it used last.
            </p>
            {saveError?.field === "defaultAgent" && <p className="form-footer c-red">{saveError.message}</p>}

            <h4 className="form-header">System Agent</h4>
            <section className="form-section">{agentRows("systemAgent", (harnesses ?? []).filter((h) => h.caps.oneShot), "None")}</section>
            <p className="form-footer">
              Names new sessions and their worktree branches from the first prompt, in a call without tools that leaves no history with the agent. A small,
              fast model is best. With None, a session's title is its prompt's first line.
            </p>
            {saveError?.field === "systemAgent" && <p className="form-footer c-red">{saveError.message}</p>}
          </>
        )}
      </div>

      {confirmUpdate && update && (
        <Confirm
          title={`Interrupt ${count(update.busy.length, "session")}?`}
          message={`Updating restarts rad, which interrupts ${update.busy.map((t) => `“${t}”`).join(", ")}.`}
          actions={[{ label: "Update Anyway", destructive: true, onSelect: updateServer }]}
          onClose={() => setConfirmUpdate(false)}
        />
      )}
      {shownTask && <TaskSheet progress={shownTask} onClose={() => setOpenTask(undefined)} />}
    </Modal>
  );
}

/** Whether rad has an update, and the button that installs it. */
function UpdateStatus({ update, starting, onUpdate }: { update: UpdateInfo; starting: boolean; onUpdate: () => void }) {
  const available = `rad ${update.latest} is available.`;
  if (installable(update)) {
    return (
      <>
        <div className="form-row">
          <span className="spacer">{available}</span>
          {update.url && (
            <button className="link" onClick={() => openLink(update.url!)}>
              What's New
            </button>
          )}
        </div>
        <div className="form-row">
          <button className="button prominent" disabled={starting} onClick={onUpdate}>
            {starting && <Spinner size={14} />} Update to {update.latest}
          </button>
        </div>
      </>
    );
  }
  if (update.available) {
    return (
      <div className="form-row stack">
        <span>{available}</span>
        {update.reason && <span className="small hint">{update.reason}</span>}
      </div>
    );
  }
  if (update.latest && !update.reason) return <div className="form-row">Up to date.</div>;
  if (update.reason) return <div className="form-row small hint">{update.reason}</div>;
  return null;
}

function RadFooter({ update }: { update: UpdateInfo | undefined }) {
  if (!update?.via) return null;
  const how =
    update.via === "app"
      ? "The Remote Agent Server app on the Mac installs the update and restarts, with rad."
      : "rad installs the update and restarts. Sessions reconnect by themselves.";
  const busy = installable(update) && update.busy.length > 0 ? ` Updating now interrupts ${count(update.busy.length, "busy session")}: ${update.busy.join(", ")}.` : "";
  return <p className="form-footer">{how + busy}</p>;
}

/** A task that runs, in place of its section's controls; a click shows it. */
function TaskRow({ progress, label, onOpen }: { progress: ServerTaskProgress; label: string; onOpen: () => void }) {
  useObserved(progress);
  return (
    <button className="form-row task-row" onClick={onOpen}>
      <Spinner size={14} />
      <span className="spacer">{label}</span>
      <ChevronRight size={15} className="c-overlay" />
    </button>
  );
}

/** An installed agent: its version and sign-in state, and Update and Sign In. */
function AgentRow({
  harness: h,
  running,
  runningLabel,
  disabled,
  onOpen,
  onUpdate,
  onLogin,
}: {
  harness: HarnessInfo;
  running: ServerTaskProgress | undefined;
  runningLabel: string;
  disabled: boolean;
  onOpen: (p: ServerTaskProgress) => void;
  onUpdate: () => void;
  onLogin: () => void;
}) {
  const name = (
    <span>
      {h.name} {h.version && <span className="mono small c-overlay">{h.version}</span>}
    </span>
  );
  const icon = (
    <span className="harness-row-icon">
      <HarnessIcon id={h.id} size={18} />
    </span>
  );
  if (running) {
    return (
      <button className="form-row harness-row" onClick={() => onOpen(running)}>
        {icon}
        <span className="harness-row-text">
          {name}
          <span className="small hint with-icon">
            <Spinner size={12} /> {runningLabel}
          </span>
        </span>
        <ChevronRight size={15} className="c-overlay" />
      </button>
    );
  }
  const canLogin = !!h.caps.login;
  const menu = [
    h.caps.update && { label: "Update", icon: RefreshCw, onSelect: onUpdate },
    canLogin && h.authOk && { label: "Sign In Again", icon: UserRound, onSelect: onLogin },
  ];
  return (
    <div className="form-row harness-row">
      {icon}
      <span className="harness-row-text">
        {name}
        {h.authOk ? <span className="small c-green">Ready</span> : <span className="small c-yellow">{h.hint || "Not signed in"}</span>}
      </span>
      {canLogin && !h.authOk && (
        <button className="button" disabled={disabled} onClick={onLogin}>
          Sign In
        </button>
      )}
      {menu.some(Boolean) && <Menu label={<Ellipsis size={16} />} title={`${h.name} actions`} entries={menu} disabled={disabled} />}
    </div>
  );
}

/** The settings that name an agent, with its model and effort. */
type AgentField = "systemAgent" | "defaultAgent";

/** The agent, model and effort in one of the settings: `offered` are the
 *  agents to choose from, `none` the choice of none. A change sends the
 *  settings with only that one changed, since the server replaces them all. */
function AgentSpecRows({
  settings,
  field,
  harnesses,
  offered,
  none,
  disabled,
  onChange,
}: {
  settings: ServerSettings;
  field: AgentField;
  harnesses: HarnessInfo[];
  offered: HarnessInfo[];
  none: string;
  disabled: boolean;
  onChange: (s: ServerSettings) => void;
}) {
  const agent = settings[field];
  const harness = agent && harnesses.find((h) => h.id === agent.harness);
  const models = harness?.models ?? [];
  const efforts = harness ? effortsForModel(harness, agent?.model) : [];
  const set = (a: AgentSpec | undefined) => onChange({ ...settings, [field]: a });

  return (
    <>
      <label className="form-row">
        <span>Agent</span>
        <select value={agent?.harness ?? ""} disabled={disabled} onChange={(e) => set(e.target.value ? { harness: e.target.value } : undefined)}>
          <option value="">{none}</option>
          {offered.map((h) => (
            <option key={h.id} value={h.id}>
              {h.name}
            </option>
          ))}
          {agent && !offered.some((h) => h.id === agent.harness) && <option value={agent.harness}>{harness?.name ?? agent.harness}</option>}
        </select>
      </label>
      {agent && (
        <label className="form-row">
          <span>Model</span>
          <select
            value={agent.model ?? ""}
            disabled={disabled}
            onChange={(e) => {
              const model = e.target.value || undefined;
              // Models support different effort levels.
              const keep = harness && agent.effort && effortsForModel(harness, model).some((x) => x.id === agent.effort);
              set({ harness: agent.harness, model, effort: keep ? agent.effort : undefined });
            }}
          >
            <option value="">Default</option>
            {models.map((m) => (
              <option key={m.id} value={m.id}>
                {m.name}
              </option>
            ))}
            {agent.model && !models.some((m) => m.id === agent.model) && <option value={agent.model}>{agent.model}</option>}
          </select>
        </label>
      )}
      {agent && efforts.length > 0 && (
        <label className="form-row">
          <span>Effort</span>
          <select value={agent.effort ?? ""} disabled={disabled} onChange={(e) => set({ ...agent, effort: e.target.value || undefined })}>
            <option value="">Default</option>
            {efforts.map((x) => (
              <option key={x.id} value={x.id}>
                {x.name}
              </option>
            ))}
            {agent.effort && !efforts.some((x) => x.id === agent.effort) && <option value={agent.effort}>{agent.effort}</option>}
          </select>
        </label>
      )}
    </>
  );
}
