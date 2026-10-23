import { expect, it } from "vitest";
import { commandQuery, commandSuggestions } from "./commandSuggestions";
const commands = [{name:"compact"}, {name:"skill:prototype", description:"Answer a design question"}];
it("matches commands, namespaced skills and descriptions without changing prose or arguments", () => {
  expect(commandSuggestions("/", commands)).toEqual(commands);
  expect(commandSuggestions("/PROT", commands)).toEqual([commands[1]]);
  expect(commandSuggestions("/design", commands)).toEqual([commands[1]]);
  expect(commandSuggestions("/", [...commands, commands[0]])).toEqual(commands);
  expect(commandSuggestions("/missing", commands)).toEqual([]);
  for (const text of ["", "write /prot", " /prot", "/prototype ", "/prototype\n", "/prototype\targs"]) {
    expect(commandQuery(text)).toBeUndefined();
    expect(commandSuggestions(text, commands)).toEqual([]);
  }
});

it("ranks exact and prefix names before substring and description matches", () => {
  const catalog = [
    { name: "code-check", description: "Review code" },
    { name: "preview" }, { name: "review-long" }, { name: "review" },
  ];
  expect(commandSuggestions("/review", catalog).map((c) => c.name)).toEqual(["review", "review-long", "preview", "code-check"]);
  expect(commandSuggestions("/proto", [{name:"other",description:"prototype"},{name:"skill:prototype"}])[0].name).toBe("skill:prototype");
});
