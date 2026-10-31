package client

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"remote-agent/internal/model"
)

// Runs of tool calls in a transcript, each folded into one row that sums it
// up ("Ran 4 commands, edited 3 files, read 2 files") and opens to show the
// calls, as ToolRuns does in the apps (web/src/store/toolRuns.ts, Android's
// core).
//
// A run is the tool calls and the agent's thinking between two pieces of
// conversation (a prompt, a reply, an approval, a plan, a notice, an error),
// within one turn. It folds with two tool calls or more. A call that waits
// for approval, failed, or shows images stands on its own, between runs. A
// run is live while its turn runs and nothing has come after it: it shows
// open, unless the user shut it, so that the user can watch the agent work,
// and folds once the agent replies or the turn ends. A prompt queued while
// the agent works isn't conversation yet: it doesn't end the run it was sent
// during, and waits after it.

// ToolRunMinimum is the fewest tool calls that fold: one on its own stays
// as it is.
const ToolRunMinimum = 2

// ToolRun is a run of tool calls.
type ToolRun struct {
	ID    string        // "tools:" and the first item's id, which stays as the run grows
	Items []*model.Item // its tool calls, and the reasoning between them
	Live  bool          // its turn runs and nothing has come after it
}

// ToolRunPart is the calls of one kind in a run: "ran 4 commands".
type ToolRunPart struct {
	Kind  model.ToolKind
	Count int
	Text  string
}

// TranscriptRow is a row of the transcript: an item, a run's summary
// (followed by its items when it's open), or the end of a turn that was
// interrupted or failed, after its last item. One of its pointers is set.
type TranscriptRow struct {
	Item     *model.Item
	Run      *ToolRun
	Expanded bool // the run is open
	TurnEnd  *model.Turn
}

// Tools is the run's tool calls, without the reasoning.
func (r *ToolRun) Tools() []*model.Item {
	var out []*model.Item
	for _, it := range r.Items {
		if it.Kind == model.ItemToolCall {
			out = append(out, it)
		}
	}
	return out
}

// Parts sums up the run's calls by kind.
func (r *ToolRun) Parts() []ToolRunPart { return ToolRunParts(r.Tools()) }

// Title is "Ran 4 commands, edited 3 files".
func (r *ToolRun) Title() string { return ToolRunTitle(r.Parts()) }

// Working reports whether the agent is at work in the run.
func (r *ToolRun) Working() bool {
	return r.Live || slices.ContainsFunc(r.Items, func(it *model.Item) bool { return it.Status == model.ItemInProgress })
}

// Contains reports whether the item is in the run.
func (r *ToolRun) Contains(id string) bool {
	return slices.ContainsFunc(r.Items, func(it *model.Item) bool { return it.ID == id })
}

// TranscriptRows is the transcript's rows. A run is open as expanded says,
// by its id, else while it's live. Without grouping, every item is a row.
func TranscriptRows(items []*model.Item, turns map[string]*model.Turn, expanded map[string]bool, grouping bool) []TranscriptRow {
	// A prompt queued during a turn sits among its items, so the last is looked up.
	last := map[string]string{}
	for _, it := range items {
		if it.TurnID != "" {
			last[it.TurnID] = it.ID
		}
	}
	var out []TranscriptRow
	end := func(it *model.Item) {
		t := turns[it.TurnID]
		if t != nil && last[it.TurnID] == it.ID && (t.Status == model.TurnInterrupted || t.Status == model.TurnFailed) {
			out = append(out, TranscriptRow{TurnEnd: t})
		}
	}
	segs := toolRunSegments(items, turns)
	if !grouping {
		segs = make([]toolRunSegment, len(items))
		for i, it := range items {
			segs[i] = toolRunSegment{item: it}
		}
	}
	for _, s := range segs {
		if s.run == nil {
			out = append(out, TranscriptRow{Item: s.item})
			end(s.item)
			continue
		}
		open, ok := expanded[s.run.ID]
		if !ok {
			open = s.run.Live
		}
		out = append(out, TranscriptRow{Run: s.run, Expanded: open})
		if open {
			for _, it := range s.run.Items {
				out = append(out, TranscriptRow{Item: it})
			}
		}
		end(s.run.Items[len(s.run.Items)-1])
	}
	return out
}

// ToolRuns is the runs that fold, in order.
func ToolRuns(items []*model.Item, turns map[string]*model.Turn) []*ToolRun {
	var out []*ToolRun
	for _, s := range toolRunSegments(items, turns) {
		if s.run != nil {
			out = append(out, s.run)
		}
	}
	return out
}

// ToolRunContaining is the run an item folds into, or nil.
func ToolRunContaining(id string, items []*model.Item, turns map[string]*model.Turn) *ToolRun {
	for _, r := range ToolRuns(items, turns) {
		if r.Contains(id) {
			return r
		}
	}
	return nil
}

// Folds reports whether an item can be part of a run: reasoning, and tool
// calls that neither wait for approval, failed, nor show images.
func Folds(it *model.Item) bool {
	switch it.Kind {
	case model.ItemReasoning:
		return true
	case model.ItemToolCall:
		return it.Status != model.ItemPending && it.Status != model.ItemFailed && len(it.Images) == 0
	}
	return false
}

type toolRunSegment struct {
	item *model.Item
	run  *ToolRun
}

func toolRunSegments(items []*model.Item, turns map[string]*model.Turn) []toolRunSegment {
	lastOfTurn := map[string]int{}
	for i, it := range items {
		lastOfTurn[it.TurnID] = i
	}
	var out []toolRunSegment
	for i := 0; i < len(items); {
		first := items[i]
		if !Folds(first) {
			out = append(out, toolRunSegment{item: first})
			i++
			continue
		}
		// The run's last item is j; k looks past the prompts queued meanwhile.
		j := i
		for k := i; k+1 < len(items); {
			next := items[k+1]
			if next.Status == model.ItemQueued {
				k++
			} else if Folds(next) && next.TurnID == first.TurnID {
				k++
				j = k
			} else {
				break
			}
		}
		span := items[i : j+1 : j+1]
		var run, queued []*model.Item
		tools := 0
		for _, it := range span {
			if it.Status == model.ItemQueued {
				queued = append(queued, it)
				continue
			}
			run = append(run, it)
			if it.Kind == model.ItemToolCall {
				tools++
			}
		}
		if tools >= ToolRunMinimum {
			t := turns[first.TurnID]
			live := first.TurnID != "" && t != nil && t.Status == model.TurnRunning && lastOfTurn[first.TurnID] == j
			out = append(out, toolRunSegment{run: &ToolRun{ID: "tools:" + first.ID, Items: run, Live: live}})
			for _, it := range queued {
				out = append(out, toolRunSegment{item: it})
			}
		} else {
			for _, it := range span {
				out = append(out, toolRunSegment{item: it})
			}
		}
		i = j + 1
	}
	return out
}

// toolRunKinds is the order the kinds are summed up in.
var toolRunKinds = []model.ToolKind{model.ToolExecute, model.ToolEdit, model.ToolRead, model.ToolSearch, model.ToolFetch, model.ToolThink, model.ToolOther}

func toolKindOf(it *model.Item) model.ToolKind {
	if it.Tool != nil && slices.Contains(toolRunKinds, it.Tool.Kind) {
		return it.Tool.Kind
	}
	return model.ToolOther
}

// ToolRunParts is the calls of each kind: files edited and read count once
// each, by their paths (a call without paths is a file of its own); the
// rest count by calls.
func ToolRunParts(tools []*model.Item) []ToolRunPart {
	var out []ToolRunPart
	for _, kind := range toolRunKinds {
		var calls []*model.Item
		for _, it := range tools {
			if toolKindOf(it) == kind {
				calls = append(calls, it)
			}
		}
		if len(calls) == 0 {
			continue
		}
		n := len(calls)
		if kind == model.ToolEdit || kind == model.ToolRead {
			files := map[string]bool{}
			for _, c := range calls {
				if len(c.Tool.Paths) == 0 {
					files["\x00"+c.ID] = true
				}
				for _, p := range c.Tool.Paths {
					files[p] = true
				}
			}
			n = len(files)
		}
		out = append(out, ToolRunPart{Kind: kind, Count: n, Text: toolRunPhrase(kind, n)})
	}
	return out
}

func toolRunPhrase(kind model.ToolKind, n int) string {
	of := func(one, many string) string {
		if n == 1 {
			return fmt.Sprintf("%d %s", n, one)
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	switch kind {
	case model.ToolExecute:
		return "ran " + of("command", "commands")
	case model.ToolEdit:
		return "edited " + of("file", "files")
	case model.ToolRead:
		return "read " + of("file", "files")
	case model.ToolSearch:
		return "searched " + of("time", "times")
	case model.ToolFetch:
		return "fetched " + of("page", "pages")
	case model.ToolThink:
		return "started " + of("agent", "agents")
	}
	return "used " + of("tool", "tools")
}

// ToolRunTitle is the parts as a sentence: "Ran 4 commands, edited 3 files".
func ToolRunTitle(parts []ToolRunPart) string {
	texts := make([]string, len(parts))
	for i, p := range parts {
		texts[i] = p.Text
	}
	return Capitalized(strings.Join(texts, ", "))
}

// Capitalized is the text with its first letter in capitals, as a part
// that starts the summary.
func Capitalized(text string) string {
	r, n := utf8.DecodeRuneInString(text)
	if n == 0 {
		return text
	}
	return string(unicode.ToUpper(r)) + text[n:]
}
