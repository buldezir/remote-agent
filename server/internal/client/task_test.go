package client

import (
	"context"
	"sync"
	"testing"
	"time"

	"remote-agent/internal/model"
)

// As web/src/store/serverTask.test.ts.

type fakeTasks struct {
	mu      sync.Mutex
	replies []func() (model.Task, error)
	version string
	ended   int
	revived int
}

func (f *fakeTasks) Task(context.Context, string) (model.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.replies) == 0 {
		return model.Task{}, errTimeout()
	}
	r := f.replies[0]
	if len(f.replies) > 1 {
		f.replies = f.replies[1:]
	}
	return r()
}

func (f *fakeTasks) TaskInput(context.Context, string, string) error { return nil }
func (f *fakeTasks) CancelTask(context.Context, string) error        { return nil }

func (f *fakeTasks) ServerVersion() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.version
}

func (f *fakeTasks) setVersion(v string) {
	f.mu.Lock()
	f.version = v
	f.mu.Unlock()
}

func (f *fakeTasks) TaskEnded(context.Context, model.Task) {
	f.mu.Lock()
	f.ended++
	f.mu.Unlock()
}

func (f *fakeTasks) ReviveConnection() {
	f.mu.Lock()
	f.revived++
	f.mu.Unlock()
}

func task(kind model.TaskKind, status model.TaskStatus, version string) func() (model.Task, error) {
	return func() (model.Task, error) {
		return model.Task{ID: "t", Kind: kind, Status: status, Version: version, Error: map[bool]string{true: "boom"}[status == model.TaskFailed]}, nil
	}
}

func follow(t *testing.T, src *fakeTasks, kind model.TaskKind) (*ServerTaskProgress, *Queue) {
	q := NewQueue()
	p := newServerTaskProgress(q, model.Task{ID: "t", Kind: kind, Status: model.TaskRunning}, src)
	p.Interval = 5 * time.Millisecond
	p.Start()
	t.Cleanup(p.Stop)
	return p, q
}

func TestTaskPollsUntilItEnds(t *testing.T) {
	src := &fakeTasks{version: "1", replies: []func() (model.Task, error){
		task(model.TaskLogin, model.TaskRunning, ""), task(model.TaskLogin, model.TaskSucceeded, "2.0"),
	}}
	p, q := follow(t, src, model.TaskLogin)
	eventually(t, q, "done", func() bool { return p.Phase.Kind == TaskPhaseDone })
	src.mu.Lock()
	defer src.mu.Unlock()
	if src.ended != 1 || p.Task.Version != "2.0" {
		t.Fatalf("ended %d, task %+v", src.ended, p.Task)
	}
}

func TestTaskFailsWithItsError(t *testing.T) {
	src := &fakeTasks{replies: []func() (model.Task, error){task(model.TaskUpdateAgent, model.TaskFailed, "")}}
	p, q := follow(t, src, model.TaskUpdateAgent)
	eventually(t, q, "failed", func() bool { return p.Phase.Kind == TaskPhaseFailed })
	if p.Phase.Message != "boom" {
		t.Fatal(p.Phase)
	}
	src2 := &fakeTasks{replies: []func() (model.Task, error){task(model.TaskUpdateAgent, model.TaskCancelled, "")}}
	p2, q2 := follow(t, src2, model.TaskUpdateAgent)
	eventually(t, q2, "cancelled", func() bool { return p2.Phase.Kind == TaskPhaseCancelled })
}

func TestTaskWaitsForRadToComeBackAsTheNewVersion(t *testing.T) {
	src := &fakeTasks{version: "1", replies: []func() (model.Task, error){task(model.TaskUpdateServer, model.TaskSucceeded, "2")}}
	p, q := follow(t, src, model.TaskUpdateServer)
	eventually(t, q, "restarting", func() bool { return p.Phase.Kind == TaskPhaseRestarting })
	src.setVersion("2")
	eventually(t, q, "done", func() bool { return p.Phase.Kind == TaskPhaseDone })
}

func TestTaskFailsWhenRadDoesntComeBack(t *testing.T) {
	src := &fakeTasks{version: "1", replies: []func() (model.Task, error){task(model.TaskUpdateServer, model.TaskSucceeded, "2")}}
	q := NewQueue()
	p := newServerTaskProgress(q, model.Task{ID: "t", Kind: model.TaskUpdateServer, Status: model.TaskRunning}, src)
	p.Interval, p.RestartTimeout = 5*time.Millisecond, 30*time.Millisecond
	p.Start()
	defer p.Stop()
	eventually(t, q, "failed", func() bool { return p.Phase.Kind == TaskPhaseFailed })
	if p.Phase.Message != "rad hasn't come back as 2" {
		t.Fatal(p.Phase.Message)
	}
}

func TestTaskForgottenByARadThatCameBackNewIsDone(t *testing.T) {
	src := &fakeTasks{version: "1", replies: []func() (model.Task, error){func() (model.Task, error) { return model.Task{}, &RPCError{"not_found", "no task"} }}}
	q := NewQueue()
	p := newServerTaskProgress(q, model.Task{ID: "t", Kind: model.TaskUpdateServer, Status: model.TaskRunning}, src)
	p.Interval = 5 * time.Millisecond
	src.setVersion("2")
	p.Start()
	defer p.Stop()
	eventually(t, q, "done", func() bool { return p.Phase.Kind == TaskPhaseDone })

	// A sign-in it forgot failed.
	src2 := &fakeTasks{version: "1", replies: []func() (model.Task, error){func() (model.Task, error) { return model.Task{}, &RPCError{"not_found", "no task"} }}}
	p2, q2 := follow(t, src2, model.TaskLogin)
	eventually(t, q2, "failed", func() bool { return p2.Phase.Kind == TaskPhaseFailed })
	if p2.Phase.Message != "rad restarted before the task finished" {
		t.Fatal(p2.Phase.Message)
	}
}

func TestServerTasksKeepOneProgressPerTask(t *testing.T) {
	src := &fakeTasks{}
	q := NewQueue()
	tasks := newServerTasks(q, src)
	a := tasks.Follow(model.Task{ID: "a", Kind: model.TaskLogin, Harness: "claude", Status: model.TaskRunning})
	if tasks.Follow(model.Task{ID: "a", Kind: model.TaskLogin, Harness: "claude", Status: model.TaskRunning}) != a {
		t.Fatal("two progresses for one task")
	}
	if tasks.Active(model.TaskLogin, "claude") != a || tasks.Active(model.TaskLogin, "codex") != nil {
		t.Fatal("Active")
	}
	tasks.Stop()
	if a.cancel != nil {
		t.Fatal("still polling")
	}
}
