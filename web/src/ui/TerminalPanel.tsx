import { FitAddon } from "@xterm/addon-fit";
import { Terminal, type ITheme } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { Ellipsis, SquareTerminal, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useObserved } from "../store/observable";
import type { TerminalSession } from "../store/terminalSession";
import { useConnection } from "./context";
import { Menu } from "./primitives";
import { defaultInterface, interfaceSizes, settings as appSettings, useSettings } from "./settings";

/** A session's shell on the server, as apple/Shared/Views/TerminalPanel.swift.
 *  It keeps running when this goes away; showing it again resumes it. */
export default function TerminalPanel({ sessionID, folder, onHide, style }: { sessionID: string; folder: string; onHide: () => void; style?: React.CSSProperties }) {
  const connection = useConnection();
  useObserved(connection);
  const settings = useSettings();
  const box = useRef<HTMLDivElement>(null);
  const term = useRef<Terminal>(undefined);
  const [session, setSession] = useState<TerminalSession>();
  useObserved(session);
  const [ctrl, setCtrl] = useState(false);
  const ctrlRef = useRef(false);
  ctrlRef.current = ctrl;

  useEffect(() => {
    const s = connection.terminal(sessionID);
    setSession(s);
    const t = new Terminal({ cursorBlink: true, scrollback: 5000, fontFamily: codeFont(), fontSize: fontSize(), theme: theme() });
    term.current = t;
    const fit = new FitAddon();
    t.loadAddon(fit);
    t.open(box.current!);
    s.onOutput = (d) => t.write(d);
    s.onReset = () => t.reset();
    const enc = new TextEncoder();
    t.onData((d) => {
      // The touch key row's Ctrl applies to the next key typed.
      if (ctrlRef.current && d.length === 1) {
        const c = d.toUpperCase().charCodeAt(0);
        if (c >= 64 && c < 96) d = String.fromCharCode(c - 64);
        setCtrl(false);
      }
      s.send(enc.encode(d));
    });
    t.onBinary((d) => s.send(Uint8Array.from(d, (c) => c.charCodeAt(0) & 0xff)));
    // xterm reports a mouse selection once the button comes up, so this copies
    // after a drag, a double or triple click, or a shift-click.
    t.onSelectionChange(() => {
      if (appSettings.copyOnSelect && t.hasSelection()) void navigator.clipboard?.writeText(t.getSelection()).catch(() => {});
    });
    // Connect once the box has a size, so the shell starts at it.
    let started = false;
    let starting: ReturnType<typeof setTimeout> | undefined;
    const ro = new ResizeObserver(() => {
      const el = box.current;
      if (!el || el.clientWidth === 0 || el.clientHeight === 0) return;
      fit.fit();
      if (started) {
        s.resize(t.cols, t.rows);
        return;
      }
      clearTimeout(starting);
      starting = setTimeout(() => {
        started = true;
        s.start(t.cols, t.rows, true);
        t.focus();
      }, 100);
    });
    ro.observe(box.current!);
    return () => {
      clearTimeout(starting);
      ro.disconnect();
      s.detach();
      t.dispose();
      term.current = undefined;
    };
  }, [connection, sessionID]);

  // The theme and font follow Settings.
  useEffect(() => {
    const t = term.current;
    if (!t) return;
    t.options.theme = theme();
    t.options.fontFamily = codeFont();
    t.options.fontSize = fontSize();
  }, [settings.getVersion()]);

  useEffect(() => session?.connectionChanged(connection.isConnected), [session, connection.state.kind]);

  const state = session?.state;
  const key = (seq: string) => {
    session?.send(new TextEncoder().encode(seq));
    term.current?.focus();
  };
  const arrow = (c: string) => key((term.current?.modes.applicationCursorKeysMode ? "\x1bO" : "\x1b[") + c);

  return (
    <div
      className="terminal"
      style={style}
      // Files dropped here aren't for the prompt.
      onDragOver={(e) => e.stopPropagation()}
      onDrop={(e) => {
        e.preventDefault();
        e.stopPropagation();
      }}
    >
      <div className="terminal-bar">
        <SquareTerminal size={14} />
        <span className="terminal-folder" title={folder}>
          <bdi>{folder}</bdi>
        </span>
        <Menu
          label={<Ellipsis size={16} />}
          title="Terminal"
          entries={[{ label: "Close Shell", destructive: true, onSelect: state?.kind === "open" ? () => session?.closeShell() : undefined }]}
        />
        <button className="icon-button" onClick={onHide} aria-label="Hide Terminal" title="Hide the terminal (⌘J). The shell keeps running.">
          <X size={16} />
        </button>
      </div>
      <div className="terminal-screen">
        <div className="terminal-xterm" ref={box} />
        {state?.kind === "reconnecting" && <div className="terminal-status">Reconnecting…</div>}
        {state?.kind === "exited" && (
          <Banner
            text={session?.closedHere ? "The shell was closed." : state.code === 0 ? "The shell exited." : `The shell exited with code ${state.code}.`}
            action="Start New Shell"
            onAction={() => session?.restart()}
          />
        )}
        {state?.kind === "gone" && <Banner text="The shell has ended." action="Start New Shell" onAction={() => session?.restart()} />}
        {state?.kind === "failed" && <Banner text={state.message} action="Try Again" onAction={() => session?.restart()} />}
      </div>
      {coarse && (
        // A phone's keyboard has no Esc, Ctrl, Tab or arrows.
        <div className="terminal-keys" onMouseDown={(e) => e.preventDefault()}>
          <button onClick={() => key("\x1b")}>esc</button>
          <button className={ctrl ? "on" : ""} onClick={() => setCtrl(!ctrl)}>
            ctrl
          </button>
          <button onClick={() => key("\t")}>tab</button>
          <button onClick={() => arrow("D")} aria-label="Left">←</button>
          <button onClick={() => arrow("A")} aria-label="Up">↑</button>
          <button onClick={() => arrow("B")} aria-label="Down">↓</button>
          <button onClick={() => arrow("C")} aria-label="Right">→</button>
        </div>
      )}
    </div>
  );
}

function Banner({ text, action, onAction }: { text: string; action: string; onAction: () => void }) {
  return (
    <div className="terminal-banner">
      <span>{text}</span>
      <button className="button prominent" onClick={onAction}>
        {action}
      </button>
    </div>
  );
}

const coarse = matchMedia("(pointer: coarse)").matches;

const css = (name: string) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();

function codeFont(): string {
  return css("--code-font");
}

/** 12 px, as diffs, scaled with the interface text size. */
function fontSize(): number {
  const ui = parseFloat(css("--ui-size")) || interfaceSizes[defaultInterface];
  return Math.round((12 * ui) / interfaceSizes[defaultInterface]);
}

const ansi = ["black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"] as const;

/** The palette's terminal colours: xterm takes values, not variables. */
function theme(): ITheme {
  const t: Record<string, string> = {
    background: css("--base"),
    foreground: css("--text"),
    cursor: css("--term-cursor"),
    cursorAccent: css("--base"),
    selectionBackground: css("--term-selection"),
    scrollbarSliderBackground: css("--surface1") + "80",
    scrollbarSliderHoverBackground: css("--surface1") + "c0",
    scrollbarSliderActiveBackground: css("--surface1"),
  };
  ansi.forEach((name, i) => {
    t[name] = css(`--term-${i}`);
    t["bright" + name[0].toUpperCase() + name.slice(1)] = css(`--term-${i + 8}`);
  });
  return t as ITheme;
}
