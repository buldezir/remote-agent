package client

import (
	"encoding/json"
	"os"
)

// Drafts are prompts written and not sent yet, by session. They stay on
// this machine; rad never sees them (unlike a session's draft, which
// session.saveDraft keeps on rad). Each change reads the file first, so two
// ra's keep each other's drafts. Errors are dropped: a draft is a
// convenience.
type Drafts struct {
	path string
}

// Get returns the session's draft, or "" for none.
func (d *Drafts) Get(sessionID string) string {
	if d == nil {
		return ""
	}
	return d.load()[sessionID]
}

// Set sets the session's draft; "" forgets it.
func (d *Drafts) Set(sessionID, text string) {
	if d == nil {
		return
	}
	texts := d.load()
	if texts[sessionID] == text {
		return
	}
	if text == "" {
		delete(texts, sessionID)
	} else {
		texts[sessionID] = text
	}
	d.save(texts)
}

// Keep forgets the drafts of sessions that are gone from rad.
func (d *Drafts) Keep(kept func(sessionID string) bool) {
	if d == nil {
		return
	}
	texts := d.load()
	n := len(texts)
	for id := range texts {
		if !kept(id) {
			delete(texts, id)
		}
	}
	if len(texts) != n {
		d.save(texts)
	}
}

func (d *Drafts) load() map[string]string {
	texts := map[string]string{}
	if b, err := os.ReadFile(d.path); err == nil {
		if json.Unmarshal(b, &texts) != nil || texts == nil {
			texts = map[string]string{}
		}
	}
	return texts
}

func (d *Drafts) save(texts map[string]string) {
	if len(texts) == 0 {
		os.Remove(d.path)
		return
	}
	b, err := json.Marshal(texts)
	if err == nil {
		writeFileAtomic(d.path, b, 0o600)
	}
}
