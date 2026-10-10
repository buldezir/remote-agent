import { describe, expect, it } from "vitest";
import { patchLines, splitPatch } from "./patch";

const patch = `diff --git a/main.go b/main.go
index 1..2 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,3 @@
 package main
-func a() {}
+func b() {}
\\ No newline at end of file
diff --git a/old.go b/new.go
similarity index 100%
rename from old.go
rename to new.go
diff --git a/gone.go b/gone.go
deleted file mode 100644
--- a/gone.go
+++ /dev/null
@@ -1 +0,0 @@
-package gone
`;

describe("patches", () => {
  it("splits by file", () => {
    const sections = splitPatch(patch);
    expect([...sections.keys()]).toEqual(["main.go", "new.go", "gone.go"]);
    expect(sections.get("main.go")?.startsWith("diff --git a/main.go")).toBe(true);
  });

  it("lists a file's lines from its first hunk", () => {
    const lines = patchLines(splitPatch(patch).get("main.go")!);
    expect(lines.map((l) => l.kind)).toEqual(["hunk", "context", "del", "add", "meta"]);
    expect(patchLines(splitPatch(patch).get("new.go")!)).toEqual([]);
    expect(patchLines(splitPatch(patch).get("gone.go")!).map((l) => l.kind)).toEqual(["hunk", "del"]);
  });
});
