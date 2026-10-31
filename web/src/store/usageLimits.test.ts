// The vectors every client's usage-limits formatting shares (ra's
// internal/client/limits_test.go, RAKit, android/core), read from
// protocol/vectors as fixtures.test.ts reads protocol/fixtures.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { parseDate, type UsageLimits, type UsageWindow } from "../protocol/models";
import {
  limitDuration,
  limitedNow,
  limitNotice,
  limitsDetails,
  limitsGauge,
  limitsLevel,
  windowLevel,
  windowPercent,
  type LimitLevel,
} from "./usageLimits";

interface Vectors {
  now: string;
  cases: {
    name: string;
    limits: UsageLimits;
    gauge: string;
    level: LimitLevel;
    windowLevels: LimitLevel[];
    windowPercents: number[];
    details: string[];
  }[];
  notices: { window: UsageWindow; text: string }[];
  durations: { seconds: number; future: string; past: string }[];
}

const v: Vectors = JSON.parse(readFileSync(join(import.meta.dirname, "../../../protocol/vectors/usage_limits.json"), "utf8"));
const now = parseDate(v.now);

describe("usage limits vectors", () => {
  it("has cases, notices and durations", () => {
    expect(v.cases.length).toBeGreaterThan(0);
    expect(v.notices.length).toBeGreaterThan(0);
    expect(v.durations.length).toBeGreaterThan(0);
  });

  for (const c of v.cases) {
    it(c.name, () => {
      const l = c.limits;
      expect(limitsGauge(l, now)).toBe(c.gauge);
      expect(limitsLevel(l, now)).toBe(c.level);
      expect(l.windows.map((w) => windowLevel(w, now))).toEqual(c.windowLevels);
      expect(l.windows.map((w) => windowPercent(w, now))).toEqual(c.windowPercents);
      expect(limitsDetails(l, now)).toEqual(c.details);
    });
  }

  it("notices", () => {
    for (const n of v.notices) expect(limitNotice(n.window, now)).toBe(n.text);
  });

  it("durations", () => {
    for (const d of v.durations) {
      const ms = d.seconds * 1000;
      expect([limitDuration(ms, true), limitDuration(ms, false)], `${d.seconds}s`).toEqual([d.future, d.past]);
    }
  });
});

describe("usage limits", () => {
  const at = (minutes: number) => new Date(now + minutes * 60_000).toISOString();
  const limits = (over: Partial<UsageLimits> = {}): UsageLimits => ({ harness: "claude", windows: [], updatedAt: at(0), ...over });

  it("lets a limit lapse at its reset", () => {
    const l = limits({ limited: true, resetsAt: at(1) });
    expect(limitedNow(l, now)).toBe(true);
    expect(limitedNow(l, now + 60_000)).toBe(false);
    expect(limitsLevel(l, now + 60_000)).toBe("normal");
    // A reset nobody knows holds until the agent says otherwise.
    expect(limitedNow(limits({ limited: true }), now + 1e9)).toBe(true);
  });

  it("counts a window that reset as unused", () => {
    const w: UsageWindow = { id: "five_hour", label: "5h", usedPercent: 100, resetsAt: at(10) };
    expect(windowLevel(w, now)).toBe("limited");
    expect(windowPercent(w, now + 10 * 60_000)).toBe(0);
    expect(limitNotice(w, now + 10 * 60_000 + 1)).toBe("5h limit reached · reset 1m ago");
  });

  it("tolerates missing windows from the wire", () => {
    const l = { harness: "codex", updatedAt: at(0) } as unknown as UsageLimits;
    expect(limitsGauge(l, now)).toBe("");
    expect(limitsLevel(l, now)).toBe("normal");
    expect(limitsDetails(l, now)).toEqual(["Updated just now"]);
  });
});
