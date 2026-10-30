package client

import (
	"context"
	"time"

	"remote-agent/internal/model"
)

// taskSource is where a ServerTaskProgress finds its task: the connection.
// Its methods are safe from any goroutine.
type taskSource interface {
	Task(ctx context.Context, id string) (model.Task, error)
	TaskInput(ctx context.Context, id, text string) error
	CancelTask(ctx context.Context, id string) error
	// ServerVersion is the version of rad last connected to.
	ServerVersion() string
	// TaskEnded is called once an agent's update or sign-in ended.
	TaskEnded(ctx context.Context, t model.Task)
	// ReviveConnection connects again if the connection gave up, as it may
	// while rad restarts.
	ReviveConnection()
}

type TaskPhaseKind int

const (
	TaskPhaseRunning TaskPhaseKind = iota
	// TaskPhaseRestarting: rad installed its update and restarts.
	TaskPhaseRestarting
	TaskPhaseDone
	TaskPhaseFailed
	TaskPhaseCancelled
)

type TaskPhase struct {
	Kind    TaskPhaseKind
	Message string // failed
}

// ServerTaskProgress follows a task on rad until it ends, polling it each
// second. A dropped connection doesn't end it: it asks again until rad
// comes back. An update of rad then waits for rad to come back as the new
// version. As RAKit's ServerTaskProgress.
//
// Task and Phase belong to the dispatcher; the poll loop keeps its own
// copies and posts them.
type ServerTaskProgress struct {
	ID    string
	Task  model.Task
	Phase TaskPhase
	// Interval is the time between polls.
	Interval time.Duration
	// RestartTimeout is how long an update of rad may take to come back as
	// the new version.
	RestartTimeout time.Duration
	// OnChange hears of each change, on the dispatcher.
	OnChange func()

	d            Dispatcher
	source       taskSource
	startVersion string
	cancel       context.CancelFunc
}

func newServerTaskProgress(d Dispatcher, t model.Task, source taskSource) *ServerTaskProgress {
	return &ServerTaskProgress{
		ID: t.ID, Task: t, Interval: time.Second, RestartTimeout: 120 * time.Second,
		d: d, source: source, startVersion: source.ServerVersion(),
	}
}

func (p *ServerTaskProgress) IsActive() bool {
	return p.Phase.Kind == TaskPhaseRunning || p.Phase.Kind == TaskPhaseRestarting
}

// Start polls until the task ends, unless it already has.
func (p *ServerTaskProgress) Start() {
	if p.cancel != nil || !p.IsActive() {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go (&taskPoll{p: p, ctx: ctx, task: p.Task, phase: p.Phase}).run()
}

// Stop stops polling, as when the view goes away; the task goes on on rad.
func (p *ServerTaskProgress) Stop() {
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
}

// Send types a line into the task's command, such as a code to paste.
func (p *ServerTaskProgress) Send(ctx context.Context, text string) error {
	return p.source.TaskInput(ctx, p.ID, text)
}

func (p *ServerTaskProgress) Cancel(ctx context.Context) error {
	return p.source.CancelTask(ctx, p.ID)
}

// taskPoll is one poll loop's state, off the dispatcher.
type taskPoll struct {
	p        *ServerTaskProgress
	ctx      context.Context
	task     model.Task
	phase    TaskPhase
	deadline time.Time
}

func (t *taskPoll) publish() {
	task, phase, ctx, p := t.task, t.phase, t.ctx, t.p
	p.d.Post(func() {
		if ctx.Err() != nil {
			return
		}
		p.Task, p.Phase = task, phase
		if phase.Kind != TaskPhaseRunning && phase.Kind != TaskPhaseRestarting {
			p.cancel = nil
		}
		if p.OnChange != nil {
			p.OnChange()
		}
	})
}

func (t *taskPoll) active() bool {
	return t.phase.Kind == TaskPhaseRunning || t.phase.Kind == TaskPhaseRestarting
}

func (t *taskPoll) run() {
	if t.task.Status != model.TaskRunning {
		t.ended()
	}
	for t.ctx.Err() == nil && t.active() {
		switch t.phase.Kind {
		case TaskPhaseRunning:
			t.poll()
		case TaskPhaseRestarting:
			v := t.task.Version
			t.p.source.ReviveConnection()
			if v != "" && t.p.source.ServerVersion() == v {
				t.setPhase(TaskPhase{Kind: TaskPhaseDone})
			} else if time.Now().After(t.deadline) {
				if v == "" {
					v = "the new version"
				}
				t.setPhase(TaskPhase{Kind: TaskPhaseFailed, Message: "rad hasn't come back as " + v})
			}
		}
		if !t.active() {
			return
		}
		select {
		case <-t.ctx.Done():
		case <-time.After(t.p.Interval):
		}
	}
}

func (t *taskPoll) setPhase(ph TaskPhase) {
	t.phase = ph
	t.publish()
}

func (t *taskPoll) poll() {
	task, err := t.p.source.Task(t.ctx, t.p.ID)
	if t.ctx.Err() != nil {
		return
	}
	if err == nil {
		t.task = task
		t.publish()
		if task.Status != model.TaskRunning {
			t.ended()
		}
		return
	}
	switch ErrorCode(err) {
	case "not_found", "method_not_found":
		// rad forgot it: it restarted, maybe as a release from before tasks.
		v := t.p.source.ServerVersion()
		if t.task.Kind == model.TaskUpdateServer && v != "" && v != t.p.startVersion {
			t.task.Status = model.TaskSucceeded
			if t.task.Version == "" {
				t.task.Version = v
			}
			t.ended()
		} else {
			t.setPhase(TaskPhase{Kind: TaskPhaseFailed, Message: "rad restarted before the task finished"})
			if isAgentTask(t.task.Kind) {
				t.p.source.TaskEnded(t.ctx, t.task)
			}
		}
	}
	// Disconnected or slow: ask again.
}

func (t *taskPoll) ended() {
	switch t.task.Status {
	case model.TaskSucceeded:
		if t.task.Kind == model.TaskUpdateServer {
			t.deadline = time.Now().Add(t.p.RestartTimeout)
			t.setPhase(TaskPhase{Kind: TaskPhaseRestarting})
		} else {
			t.setPhase(TaskPhase{Kind: TaskPhaseDone})
		}
	case model.TaskCancelled:
		t.setPhase(TaskPhase{Kind: TaskPhaseCancelled})
	default:
		msg := t.task.Error
		if msg == "" {
			msg = "The task failed"
		}
		t.setPhase(TaskPhase{Kind: TaskPhaseFailed, Message: msg})
	}
	if isAgentTask(t.task.Kind) {
		t.p.source.TaskEnded(t.ctx, t.task)
	}
}

func isAgentTask(k model.TaskKind) bool {
	return k == model.TaskUpdateAgent || k == model.TaskLogin
}

// ServerTasks are the tasks the server settings follow while they're open,
// one progress each: those started there, and those that were running when
// they opened. Its methods belong to the dispatcher.
type ServerTasks struct {
	// OnChange hears of each change to any of them, on the dispatcher.
	OnChange func()

	d      Dispatcher
	source taskSource
	all    map[string]*ServerTaskProgress
	order  []string
	live   bool
}

func NewServerTasks(d Dispatcher, conn *ServerConnection) *ServerTasks {
	return newServerTasks(d, conn)
}

func newServerTasks(d Dispatcher, source taskSource) *ServerTasks {
	return &ServerTasks{d: d, source: source, all: map[string]*ServerTaskProgress{}, live: true}
}

// Follow returns the progress for a task, made and started the first time.
// Starting a task that already runs returns it, so it keeps its progress.
func (s *ServerTasks) Follow(t model.Task) *ServerTaskProgress {
	p := s.all[t.ID]
	if p == nil {
		p = newServerTaskProgress(s.d, t, s.source)
		p.OnChange = s.changed
		s.all[t.ID] = p
		s.order = append(s.order, t.ID)
		if s.live {
			p.Start()
		}
		s.changed()
	}
	return p
}

func (s *ServerTasks) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
}

func (s *ServerTasks) Get(id string) *ServerTaskProgress { return s.all[id] }

// Active is the latest task of this kind, for this agent, while it runs
// (or rad restarts).
func (s *ServerTasks) Active(kind model.TaskKind, harness string) *ServerTaskProgress {
	var found *ServerTaskProgress
	for _, id := range s.order {
		p := s.all[id]
		if p.Task.Kind == kind && p.Task.Harness == harness && p.IsActive() {
			found = p
		}
	}
	return found
}

// Start polls again after Stop.
func (s *ServerTasks) Start() {
	s.live = true
	for _, p := range s.all {
		p.Start()
	}
}

// Stop stops polling them all: polling doesn't outlive the screen.
func (s *ServerTasks) Stop() {
	s.live = false
	for _, p := range s.all {
		p.Stop()
	}
}
