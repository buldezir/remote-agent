package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"remote-agent/internal/model"
)

// The golden frames in protocol/fixtures, which rad's tests write, decode
// into ra's wire types as the apps' do (FixtureTests.swift,
// fixtures.test.ts, FixtureTests.kt).

func fixture[T any](t *testing.T, name string) T {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "protocol", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return v
}

type resultFrame[T any] struct {
	ID     int64     `json:"id"`
	Result T         `json:"result"`
	Error  *RPCError `json:"error"`
}

type notificationFrame[T any] struct {
	Method string `json:"method"`
	Params T      `json:"params"`
}

func TestFixtures(t *testing.T) {
	pair := fixture[PairResult](t, "http_pair_response.json")
	if pair.ServerID != "srv1" || pair.Token != "0123abcd" || pair.DeviceID != "dev1" || pair.ProtocolVersion != 1 {
		t.Errorf("pair response: %+v", pair)
	}

	info := fixture[resultFrame[ServerInfo]](t, "response_server_info.json").Result
	if info.Version != "0.4.0" || info.DeviceID != "dev1" || len(info.Roots) != 1 || !info.Drafts || !info.Terminal || !info.Forks || !info.Commit || !info.Search || !info.Rename {
		t.Errorf("server.info: %+v", info)
	}

	ls := fixture[resultFrame[FSListing]](t, "response_fs_list.json").Result
	if ls.Parent != "/Users/me" || len(ls.Entries) != 2 || !ls.Entries[0].IsGitRepo || ls.Entries[1].IsGitRepo {
		t.Errorf("fs.list: %+v", ls)
	}

	sub := fixture[resultFrame[subscribeResult]](t, "response_subscribe.json").Result
	if sub.Stream != "session:s1" || sub.Seq != 14 {
		t.Errorf("subscribe: %+v", sub)
	}

	cmds := fixture[resultFrame[commandList]](t, "response_session_commands.json").Result.Commands
	if len(cmds) != 2 || cmds[1].Kind != "skill" || cmds[1].Scope != "user" {
		t.Errorf("session.commands: %+v", cmds)
	}

	hs := fixture[resultFrame[harnessList]](t, "response_harness_list.json").Result.Harnesses
	if len(hs) == 0 || hs[0].ID != "claude" || len(hs[0].Models) == 0 || len(hs[0].Models[0].Efforts) != 2 {
		t.Errorf("harness.list: %+v", hs)
	}
	if len(hs) < 2 || !hs[0].Caps.Steer || hs[1].Caps.Steer || !hs[0].Caps.Import || hs[1].Caps.Import {
		t.Errorf("harness.list caps: %+v", hs)
	}
	importable := fixture[resultFrame[importableList]](t, "response_session_importable.json").Result.Sessions
	if len(importable) != 2 || importable[0].Title != "Fix the flaky login test" || importable[0].Messages != 12 || !importable[0].Active ||
		importable[1].Active || importable[1].UpdatedAt.IsZero() {
		t.Errorf("session.importable: %+v", importable)
	}

	search := fixture[resultFrame[model.SearchResults]](t, "response_session_search.json").Result
	if len(search.Results) != 2 || !search.More || !search.Indexing {
		t.Errorf("session.search: %+v", search)
	} else {
		r0, r1 := search.Results[0], search.Results[1]
		if r0.Session == nil || r0.Session.ID != "s1" || r0.ItemID != "i4" || r0.ItemKind != model.ItemAssistantMessage || r0.Matches != 2 ||
			len(r0.Snippet) != 3 || r0.Snippet[0].Match || r0.Snippet[1] != (model.SnippetPart{Text: "tests", Match: true}) {
			t.Errorf("session.search's first result: %+v", r0)
		}
		if r1.Session == nil || r1.Session.ID != "s2" || !r1.Session.Archived || r1.ItemID != "" || len(r1.Snippet) < 2 || !r1.Snippet[1].Match {
			t.Errorf("session.search's second result: %+v", r1)
		}
	}

	tasks := fixture[resultFrame[taskList]](t, "response_task_list.json").Result.Tasks
	if len(tasks) == 0 || tasks[0].ID == "" {
		t.Errorf("task.list: %+v", tasks)
	}
	if task := fixture[resultFrame[model.Task]](t, "response_task.json").Result; task.ID == "" || task.Kind == "" {
		t.Errorf("task.get: %+v", task)
	}
	commit := fixture[resultFrame[model.Task]](t, "response_task_commit.json").Result
	if commit.Kind != model.TaskCommit || commit.SessionID != "s1" || len(commit.Steps) != 3 || commit.Steps[0].Status != model.StepSkipped ||
		commit.Steps[2].ID != "pull_request" || commit.Steps[2].Detail != "#12 Fix the flaky tests" || commit.URL != "https://github.com/me/app/pull/12" {
		t.Errorf("commit task: %+v", commit)
	}
	preview := fixture[resultFrame[CommitPreview]](t, "response_commit_preview.json").Result
	if preview.Branch != "ra/fix-tests" || len(preview.Files) != 2 || preview.Files[1].Status != "added" || preview.Files[0].Additions != 4 ||
		preview.Remote != "origin" || preview.Base != "main" || preview.Ahead != 1 || !strings.HasPrefix(preview.Message, "Fix the flaky tests\n\n") ||
		preview.NoPullRequest != "" {
		t.Errorf("git.commitPreview: %+v", preview)
	}
	onMain := fixture[resultFrame[CommitPreview]](t, "response_commit_preview_default_branch.json").Result
	if onMain.Branch != "main" || onMain.Remote != "origin" || onMain.Base != "" ||
		onMain.NoPullRequest != "main is the repository's default branch: a pull request needs a branch of its own" {
		t.Errorf("git.commitPreview on the default branch: %+v", onMain)
	}
	if u := fixture[resultFrame[model.UpdateInfo]](t, "response_check_update.json").Result; u.Current == "" {
		t.Errorf("server.checkUpdate: %+v", u)
	}
	if s := fixture[resultFrame[model.ServerSettings]](t, "response_server_settings.json").Result; s.SystemAgent == nil || s.SystemAgent.Harness == "" {
		t.Errorf("server.settings: %+v", s)
	}
	if s := fixture[resultFrame[model.Session]](t, "response_session.json").Result; s.ID == "" || s.ForkedFrom == nil || s.ForkedFrom.SessionID != "s0" || s.ForkedFrom.Turn != 3 || !s.CustomTitle {
		t.Errorf("session.create: %+v", s)
	}

	if e := fixture[resultFrame[struct{}]](t, "response_error.json").Error; e == nil || e.Code != "conflict" || e.Message == "" {
		t.Errorf("error: %+v", e)
	}

	synced := fixture[notificationFrame[synchronizedParams]](t, "notification_synchronized.json")
	if synced.Method != "synchronized" || synced.Params.Seq != 14 || !synced.Params.Reset {
		t.Errorf("synchronized: %+v", synced)
	}
	if r := fixture[notificationFrame[resyncParams]](t, "notification_resync.json"); r.Method != "resync" || r.Params.Stream != "session:s1" {
		t.Errorf("resync: %+v", r)
	}

	ev := fixture[notificationFrame[eventsParams]](t, "notification_events_session.json").Params
	s := ev.Events[0].Session
	if s == nil || s.Workspace.AgentPath == "" || !s.Unread || s.Draft == nil || len(s.Draft.Images) != 1 || s.Context.Window == 0 || s.ModelInfo.Name == "" || len(s.BackgroundTasks) == 0 {
		t.Errorf("session event: %+v", s)
	}
	var questions []model.Question
	for _, e := range ev.Events {
		if it := e.Item; it != nil && it.Approval != nil && it.Approval.Special == model.SpecialQuestion {
			questions = it.Approval.Questions
		}
	}
	if len(questions) != 2 || questions[0].OptionsOnly || !questions[1].OptionsOnly {
		t.Errorf("questions: %+v", questions)
	}
	idx := fixture[notificationFrame[eventsParams]](t, "notification_events_index.json").Params
	if idx.Stream != "index" || len(idx.Events) == 0 {
		t.Errorf("index events: %+v", idx)
	}
}
