import { describe, expect, it } from "vitest";
import type { HarnessInfo } from "../protocol/models";
import { custom, startingHarness, startingOptions } from "./NewSession";

const caps = { resume: true, interrupt: true, setMode: true, freeModel: false, modelSelect: true };
const harness = (id: string, p: Partial<HarnessInfo> = {}): HarnessInfo => ({ id, name: id, protocol: "acp", installed: true, authOk: true, caps, ...p });

describe("new session", () => {
  it("starts with the server's default agent, else the last used", () => {
    const list = [harness("claude"), harness("codex"), harness("pi", { authOk: false }), harness("fake")];
    // The default, while it's signed in.
    expect(startingHarness(list, { harness: "codex" }, "fake")).toBe("codex");
    expect(startingHarness(list, { harness: "pi" }, "fake")).toBe("fake");
    // Not installed: the last used.
    expect(startingHarness(list, { harness: "opencode" }, "fake")).toBe("fake");
    // Without a default, the last used; Claude Code before any.
    expect(startingHarness(list, null, "fake")).toBe("fake");
    expect(startingHarness(list, null, null)).toBe("claude");
    // Neither usable: the first that is.
    expect(startingHarness(list, null, "pi")).toBe("claude");
    expect(startingHarness(list.slice(1), null, null)).toBe("codex");
    expect(startingHarness([harness("pi", { authOk: false })], { harness: "pi" }, "pi")).toBe("");
    expect(startingHarness([], null, null)).toBe("");
  });

  it("starts with a model and effort the agent still offers", () => {
    const efforts = [
      { id: "low", name: "Low" },
      { id: "high", name: "High" },
    ];
    const h = harness("codex", {
      models: [
        { id: "gpt-5.5", name: "GPT-5.5", efforts: [...efforts, { id: "xhigh", name: "Extra High" }] },
        { id: "mini", name: "Mini" },
      ],
      efforts,
    });
    expect(startingOptions(h, "gpt-5.5", "xhigh")).toEqual({ model: "gpt-5.5", customModel: "", effort: "xhigh" });
    expect(startingOptions(h, "mini", "high")).toEqual({ model: "mini", customModel: "", effort: "high" });
    // An effort the model doesn't offer: its default.
    expect(startingOptions(h, "mini", "xhigh")).toEqual({ model: "mini", customModel: "", effort: "" });
    // A model no longer offered: the default model, with an effort it offers.
    expect(startingOptions(h, "gpt-4", "high")).toEqual({ model: "", customModel: "", effort: "high" });
    expect(startingOptions(h, "gpt-4", "xhigh")).toEqual({ model: "", customModel: "", effort: "" });
    // Nothing chosen: the defaults.
    expect(startingOptions(h, undefined, undefined)).toEqual({ model: "", customModel: "", effort: "" });
    expect(startingOptions(h, "", null)).toEqual({ model: "", customModel: "", effort: "" });
  });

  it("keeps a model of its own for an agent that takes any", () => {
    const free = { ...caps, freeModel: true };
    const listed = harness("pi", { caps: free, models: [{ id: "sonnet", name: "Sonnet" }], efforts: [{ id: "high", name: "High" }] });
    expect(startingOptions(listed, "my/model", "high")).toEqual({ model: custom, customModel: "my/model", effort: "high" });
    const unlisted = harness("opencode", { caps: free });
    expect(startingOptions(unlisted, "my/model", "high")).toEqual({ model: "", customModel: "my/model", effort: "" });
  });
});
