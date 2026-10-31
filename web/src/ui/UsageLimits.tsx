import { Gauge } from "lucide-react";
import { useState, useSyncExternalStore, type MouseEvent } from "react";
import { parseDate, type UsageLimits, type UsageWindow } from "../protocol/models";
import { limitNotice, limitsDetails, limitsGauge, limitsLevel, type LimitLevel } from "../store/usageLimits";

/** The palette colour of each level: as the session's other text, amber, red. */
const levelClass: Record<LimitLevel, string> = { normal: "c-subtext", warning: "c-yellow", limited: "c-red" };

// One clock for every gauge and notice shown, ticking while any is.
const clockInterval = 15_000;
let clockNow = Date.now();
let clockTimer: ReturnType<typeof setInterval> | undefined;
const clockListeners = new Set<() => void>();

function watchClock(l: () => void): () => void {
  clockListeners.add(l);
  if (!clockTimer) {
    clockNow = Date.now();
    clockTimer = setInterval(() => {
      clockNow = Date.now();
      for (const f of [...clockListeners]) f();
    }, clockInterval);
  }
  return () => {
    clockListeners.delete(l);
    if (!clockListeners.size && clockTimer) {
      clearInterval(clockTimer);
      clockTimer = undefined;
    }
  };
}

/** The time now, in ms, which re-renders the component every 15 seconds:
 *  what's said of resets ("resets in 2h 14m") changes with it. */
export function useClock(): number {
  return useSyncExternalStore(watchClock, () => clockNow);
}

/** An agent's usage limits as a gauge, "5h 34% · week 61%", coloured by the
 *  nearest limit, with each window's reset, the plan and their age on hover
 *  and, for touch, on a click, which shows them under it. Nothing when the
 *  agent said nothing to show. `inButton`: it sits in a button, such as a
 *  choice of agent, so it's text that takes the click rather than a button
 *  of its own. */
export function LimitsGauge({ limits, inButton = false }: { limits: UsageLimits; inButton?: boolean }) {
  const now = useClock();
  const [open, setOpen] = useState(false);
  const gauge = limitsGauge(limits, now);
  if (!gauge) return null;
  const details = limitsDetails(limits, now);
  const toggle = (e: MouseEvent) => {
    e.stopPropagation();
    e.preventDefault();
    setOpen((o) => !o);
  };
  const label = (
    <>
      <Gauge size={12} strokeWidth={2.25} /> {gauge}
    </>
  );
  const className = "limits-gauge small " + levelClass[limitsLevel(limits, now)];
  return (
    <span className="limits">
      {inButton ? (
        <span className={className} title={details.join("\n")} onClick={toggle}>
          {label}
        </span>
      ) : (
        <button type="button" className={className} title={details.join("\n")} aria-expanded={open} aria-label={`Usage limits: ${gauge}`} onClick={toggle}>
          {label}
        </button>
      )}
      {open && (
        <span className="limits-details small hint">
          {details.map((line, i) => (
            <span key={i}>{line}</span>
          ))}
        </span>
      )}
    </span>
  );
}

/** An error whose turn hit a usage limit: "5h limit reached · resets in 2h
 *  14m", red until it resets. The agent's own words, which name the reset in
 *  the server's time zone, on hover. */
export function LimitNotice({ limit, text }: { limit: UsageWindow; text?: string }) {
  const now = useClock();
  const resets = parseDate(limit.resetsAt);
  const holds = Number.isNaN(resets) || resets > now;
  return (
    <div className={"limit-notice " + (holds ? "c-red" : "c-subtext")} title={text}>
      <Gauge size={14} strokeWidth={2.25} /> {limitNotice(limit, now)}
    </div>
  );
}
