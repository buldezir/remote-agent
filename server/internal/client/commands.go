package client

import (
	"cmp"
	"slices"
	"strings"
	"unicode"

	"remote-agent/internal/model"
)

// CommandQuery is what a composer's text asks to complete: only a leading
// command, before any whitespace, so arguments and prose stay as they are.
func CommandQuery(draft string) (string, bool) {
	if !strings.HasPrefix(draft, "/") || strings.IndexFunc(draft, unicode.IsSpace) >= 0 {
		return "", false
	}
	return strings.ToLower(draft[1:]), true
}

// CommandSuggestions ranks the commands that match the draft's query: an
// exact name, then a prefix (of the name or of its part after the last
// ":"), then a part of the name, then a part of the description. The first
// of each name wins, so put the server's before the harness's.
func CommandSuggestions(draft string, commands []model.Command) []model.Command {
	query, ok := CommandQuery(draft)
	if !ok {
		return nil
	}
	type ranked struct {
		cmd         model.Command
		index, rank int
	}
	var out []ranked
	seen := map[string]bool{}
	for i, c := range commands {
		if seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		name := strings.ToLower(c.Name)
		short := name[strings.LastIndex(name, ":")+1:]
		rank := -1
		switch {
		case name == query || short == query:
			rank = 0
		case strings.HasPrefix(name, query) || strings.HasPrefix(short, query):
			rank = 1
		case strings.Contains(name, query):
			rank = 2
		case strings.Contains(strings.ToLower(c.Description), query):
			rank = 3
		}
		if rank >= 0 {
			out = append(out, ranked{c, i, rank})
		}
	}
	slices.SortFunc(out, func(a, b ranked) int {
		return cmp.Or(cmp.Compare(a.rank, b.rank), cmp.Compare(a.index, b.index))
	})
	cmds := make([]model.Command, len(out))
	for i, r := range out {
		cmds[i] = r.cmd
	}
	return cmds
}
