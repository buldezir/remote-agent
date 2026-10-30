package client

import "remote-agent/internal/model"

type AlertKind int

const (
	// AlertNone: nothing to tell.
	AlertNone AlertKind = iota
	// AlertNeedsYou: it waits for an approval or an answer.
	AlertNeedsYou
	// AlertFinished: its turn ended.
	AlertFinished
	// AlertFailed: the agent didn't start, or exited with an error.
	AlertFailed
)

// SessionAlert is a change in a session that the user wants to hear about
// while they look elsewhere: the agent waits for them, finished its turn,
// or failed. As RAKit's SessionAlert.
type SessionAlert struct {
	Kind  AlertKind
	Error string // failed
}

// Alert is the alert for a session that changed from old to next, if any.
// An archived session has none.
func Alert(old, next *model.Session) SessionAlert {
	if next.Archived || old.Status == next.Status {
		return SessionAlert{}
	}
	switch next.Status {
	case model.SessionAwaitingApproval:
		return SessionAlert{Kind: AlertNeedsYou}
	case model.SessionIdle:
		if old.Status == model.SessionRunning {
			return SessionAlert{Kind: AlertFinished}
		}
	case model.SessionError:
		return SessionAlert{Kind: AlertFailed, Error: next.Error}
	}
	return SessionAlert{}
}
