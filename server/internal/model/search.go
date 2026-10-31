package model

import (
	"encoding/json"
	"strings"
)

// SearchResults is session.search's result.
type SearchResults struct {
	Results  []SearchResult `json:"results"`
	More     bool           `json:"more,omitempty"`     // more sessions match than the limit
	Indexing bool           `json:"indexing,omitempty"` // rad is still indexing sessions an older rad wrote, which may be missing
}

// SearchResult is a session that session.search found, with the item in it
// that matches best: else its title or branch matched.
type SearchResult struct {
	Session  *Session      `json:"session"`
	ItemID   string        `json:"itemId,omitempty"`   // absent: only the title or the branch matched
	ItemKind ItemKind      `json:"itemKind,omitempty"` // the item's kind
	Snippet  []SnippetPart `json:"snippet"`            // the match in its context, its words marked
	Matches  int           `json:"matches"`            // the session's items that match
}

// SnippetPart is a run of a snippet's text: Match marks the words the query
// found.
type SnippetPart struct {
	Text  string `json:"text"`
	Match bool   `json:"match,omitempty"`
}

// SearchText is the text of an item that search finds, on rad and in the
// clients' find in a transcript: prompts, replies, notices and errors, a tool
// call's title and command, a plan, and a proposed plan's or a question's
// text. Reasoning, tool output, approvals of tool calls (their tool call has
// the same title) and sub-agents' items, which the clients fold away, aren't
// searched: they're long, and they would bury the conversation.
func SearchText(it *Item) string {
	if it.ParentItemID != "" {
		return ""
	}
	switch it.Kind {
	case ItemUserMessage, ItemAssistantMessage, ItemNotice, ItemError:
		return it.Text
	case ItemToolCall:
		if it.Tool == nil {
			return ""
		}
		s := it.Tool.Title
		if cmd := ToolCommand(it.Tool.Input); cmd != "" && !strings.Contains(s, cmd) {
			s += "\n" + cmd
		}
		return s
	case ItemPlan:
		if it.Plan == nil {
			return ""
		}
		parts := make([]string, 0, len(it.Plan.Entries)+1)
		for _, e := range it.Plan.Entries {
			parts = append(parts, e.Content)
		}
		if it.Plan.Text != "" {
			parts = append(parts, it.Plan.Text)
		}
		return strings.Join(parts, "\n")
	case ItemApproval:
		if it.Approval == nil {
			return ""
		}
		switch it.Approval.Special {
		case SpecialPlan:
			return it.Approval.PlanText
		case SpecialQuestion:
			parts := make([]string, 0, len(it.Approval.Questions))
			for _, q := range it.Approval.Questions {
				parts = append(parts, q.Question)
			}
			return strings.Join(parts, "\n")
		}
	}
	return ""
}

// ToolCommand is the command a tool call ran, from its input's "command": a
// string, or a list of arguments, which it joins with spaces.
func ToolCommand(input json.RawMessage) string {
	if len(input) == 0 || input[0] != '{' {
		return ""
	}
	var in struct {
		Command json.RawMessage `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil || len(in.Command) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(in.Command, &s) == nil {
		return s
	}
	var args []string
	if json.Unmarshal(in.Command, &args) == nil {
		return strings.Join(args, " ")
	}
	return ""
}
