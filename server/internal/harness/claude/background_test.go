package claude

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"remote-agent/internal/harness"
)

// A command run in the background without a description has the command as
// its description: the task and its notice keep only its first line.
func TestBackgroundTaskDescriptionIsOneLine(t *testing.T) {
	script := "cd /work/app && python3 - <<'EOF'\nprint('hi')\nEOF"
	r := &runtime{events: make(chan harness.Event, 4)}
	var f frame
	if err := json.Unmarshal([]byte(`{"type":"system","subtype":"background_tasks_changed","tasks":[`+
		`{"task_id":"b1","task_type":"local_bash","description":`+quote(script)+`}]}`), &f); err != nil {
		t.Fatal(err)
	}
	r.onBackgroundTasks(f)
	want := "cd /work/app && python3 - <<'EOF' …"
	if ev := (<-r.events).(harness.BackgroundTasks); len(ev.Tasks) != 1 || ev.Tasks[0].Description != want {
		t.Errorf("tasks = %+v, want %q", ev.Tasks, want)
	}
	r.onTaskNotification(frame{TaskID: "b1", Status: "completed"})
	if n := []string{"Background task finished: " + want}; !slices.Equal(r.notices, n) {
		t.Errorf("notices = %q, want %q", r.notices, n)
	}
}

func TestFirstLineCutsLongLinesByRune(t *testing.T) {
	got := firstLine(strings.Repeat("é", 200))
	if want := strings.Repeat("é", 160) + "…"; got != want {
		t.Errorf("firstLine = %q, want %q", got, want)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
