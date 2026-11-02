package tui

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"remote-agent/internal/model"
)

// Renaming a session, as the apps' Rename Session prompt: a title of the
// user's, which rad keeps, or none, which hands it back to rad's names.

const renameNote = "Leave it empty to name it automatically."

// canRename: rad renames sessions (ServerInfo.Rename). Archived sessions
// are renamed too.
func canRename(m *Model) bool {
	return m.conn.Info != nil && m.conn.Info.Rename
}

// canUnarchive: rad brings archived sessions back (ServerInfo.Unarchive).
func canUnarchive(m *Model) bool {
	return m.conn.Info != nil && m.conn.Info.Unarchive
}

// unarchive brings the session back from the archive; the index moves it.
func unarchive(m *Model, s *model.Session) tea.Cmd {
	id := s.ID
	return m.do(func(ctx context.Context) error {
		_, err := m.conn.Unarchive(ctx, id)
		return err
	}, nil)
}

// rename asks for the session's new title, starting from the one it has,
// all of it selected, so that typing replaces it. The same title changes
// nothing; the index brings the new one, as any change.
func rename(m *Model, s *model.Session) tea.Cmd {
	if !m.conn.IsConnected() {
		m.fail(errors.New("Not connected to rad."))
		return nil
	}
	id, title := s.ID, s.Title
	if is := m.conn.Sessions[id]; is != nil {
		title = is.Title // a search result's may be older
	}
	p := newInput(m, "Rename Session", "", title, func(m *Model, v string) tea.Cmd {
		if strings.TrimSpace(v) == title {
			return nil
		}
		return m.do(func(ctx context.Context) error {
			_, err := m.conn.RenameSession(ctx, id, v)
			return err
		}, nil)
	})
	p.note, p.action, p.selected = renameNote, "rename", true
	m.push(p)
	return nil
}
