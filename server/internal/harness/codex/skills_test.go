package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"remote-agent/internal/config"
	"remote-agent/internal/harness"
	"remote-agent/internal/harness/replaytest"
	"remote-agent/internal/model"
)

func TestReplaySkills(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "codex-rec/ws")
	if err := os.MkdirAll(cwd, 0755); err != nil {
		t.Fatal(err)
	}
	exe, env := replaytest.Command(t, "testdata/skills.ndjson", root)
	diag := filepath.Join(t.TempDir(), "diag.ndjson")
	rt, err := New(config.Command{Command: exe, Env: env}).Open(context.Background(), harness.OpenOptions{Cwd: cwd, DiagPath: diag})
	if err != nil {
		t.Fatal(err)
	}
	commands, err := rt.(harness.CommandLister).Commands(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 || commands[1] != (model.Command{Name: "prototype", Description: "Prototype a design", Kind: "skill", Scope: "user"}) {
		t.Fatalf("commands = %+v", commands)
	}
	if err := rt.Prompt(context.Background(), harness.Input{Text: "/prototype keep arguments", Images: []harness.Image{{Path: filepath.Join(root, "image.png")}}}); err != nil {
		t.Fatal(err)
	}
	replaytest.AwaitTurn(t, rt, nil)
	_, sent := replaytest.Diag(t, diag)
	lookups := 0
	for _, frame := range sent {
		if frame["method"] == "skills/list" {
			lookups++
		}
	}
	if lookups != 1 {
		t.Errorf("skill discovery calls = %d, want one picker lookup and none on Send", lookups)
	}
	for _, frame := range sent {
		if frame["method"] != "turn/start" {
			continue
		}
		input := frame["params"].(map[string]any)["input"].([]any)
		if len(input) != 3 {
			t.Fatalf("input = %+v", input)
		}
		selected := input[0].(map[string]any)
		if selected["type"] != "skill" || selected["name"] != "prototype" || selected["path"] != filepath.Join(root, "prototype/SKILL.md") {
			t.Errorf("selected = %+v", selected)
		}
		if image := input[1].(map[string]any); image["type"] != "localImage" || image["path"] != filepath.Join(root, "image.png") {
			t.Errorf("image = %+v", image)
		}
		if input[2].(map[string]any)["text"] != "$prototype keep arguments" {
			t.Errorf("input = %+v", input)
		}
		replaytest.CloseClean(t, rt)
		return
	}
	t.Fatal("turn/start missing")
}

func TestSkillInput(t *testing.T) {
	skills := []skill{{Name: "prototype", Path: "/skills/prototype/SKILL.md"}}
	for _, input := range []string{"/prototype details", "$prototype details", "  /prototype details"} {
		text, selected := skillInput(input, skills)
		if text != "$prototype details" || selected == nil {
			t.Errorf("%q -> %q, %v", input, text, selected)
		}
	}
	for _, input := range []string{"/prototypes details", "write /prototype", "/missing", "/prototype-other"} {
		text, selected := skillInput(input, skills)
		if text != input || selected != nil {
			t.Errorf("changed unrelated input %q", input)
		}
	}
}

func TestReplaySkillCatalogLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, transcript string
		refreshes        int
		lookupFails      bool
		wantLookups      int
		wantPath         string
	}{
		{name: "without picker", transcript: "skills.ndjson", wantLookups: 0},
		{name: "explicit refresh", transcript: "skills-refresh.ndjson", refreshes: 2, wantLookups: 2, wantPath: "refreshed/SKILL.md"},
		{name: "failed discovery", transcript: "skills-unavailable.ndjson", refreshes: 1, lookupFails: true, wantLookups: 1},
		{name: "empty catalog", transcript: "skills-empty.ndjson", refreshes: 1, wantLookups: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			cwd := filepath.Join(root, "codex-rec/ws")
			if err := os.MkdirAll(cwd, 0755); err != nil {
				t.Fatal(err)
			}
			exe, env := replaytest.Command(t, filepath.Join("testdata", tc.transcript), root)
			diag := filepath.Join(t.TempDir(), "diag.ndjson")
			rt, err := New(config.Command{Command: exe, Env: env}).Open(context.Background(), harness.OpenOptions{Cwd: cwd, DiagPath: diag})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < tc.refreshes; i++ {
				_, err := rt.(harness.CommandLister).Commands(context.Background())
				if (err != nil) != tc.lookupFails {
					t.Fatalf("discovery error=%v, want failure=%v", err, tc.lookupFails)
				}
			}
			replaytest.RunTurn(t, rt, "/prototype arguments", nil)
			_, sent := replaytest.Diag(t, diag)
			lookups := 0
			for _, f := range sent {
				if f["method"] == "skills/list" {
					lookups++
					if f["params"].(map[string]any)["forceReload"] != (tc.refreshes > 0) {
						t.Errorf("unexpected refresh mode: %+v", f)
					}
				}
				if f["method"] == "turn/start" {
					input := f["params"].(map[string]any)["input"].([]any)
					if tc.wantPath != "" {
						if len(input) != 2 || input[0].(map[string]any)["path"] != filepath.Join(root, tc.wantPath) || input[1].(map[string]any)["text"] != "$prototype arguments" {
							t.Errorf("skill input=%+v", input)
						}
					} else if len(input) != 1 || input[0].(map[string]any)["text"] != "/prototype arguments" {
						t.Errorf("ordinary fallback=%+v", input)
					}
				}
			}
			if lookups != tc.wantLookups {
				t.Errorf("lookups=%d, want %d", lookups, tc.wantLookups)
			}
			replaytest.CloseClean(t, rt)
		})
	}
}

func TestReplayNormalizedSkillWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "codex-rec/ws")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "workspace-link")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Fatal(err)
	}
	exe, env := replaytest.Command(t, "testdata/skills.ndjson", root)
	rt, err := New(config.Command{Command: exe, Env: env}).Open(context.Background(), harness.OpenOptions{Cwd: alias})
	if err != nil {
		t.Fatal(err)
	}
	commands, err := rt.(harness.CommandLister).Commands(context.Background())
	if err != nil || len(commands) != 2 {
		t.Fatalf("canonical workspace commands=%v err=%v", commands, err)
	}
	replaytest.RunTurn(t, rt, "/prototype", nil)
	replaytest.CloseClean(t, rt)
}
