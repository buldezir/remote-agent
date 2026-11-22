//! The `index` stream: projects, every session's summary, and the agents'
//! usage limits.

use std::collections::HashMap;
use std::rc::Rc;

use crate::protocol::{Event, Project, Session, SessionStatus, UsageLimits};

use super::sync::StreamSync;

#[derive(Debug, Default)]
pub struct IndexState {
    pub sync: StreamSync,
    pub projects: HashMap<String, Rc<Project>>,
    pub sessions: HashMap<String, Rc<Session>>,
    pub limits: HashMap<String, Rc<UsageLimits>>,
}

impl IndexState {
    /// Applies `synchronized`: drops the cache on reset, then the replay.
    pub fn synchronized(&mut self, seq: i64, reset: bool) {
        let sync = self.sync.synchronized(seq, reset);
        if sync.reset {
            self.projects.clear();
            self.sessions.clear();
            self.limits.clear();
        }
        for ev in &sync.apply {
            self.upsert(ev);
        }
    }

    /// Applies live events; returns true when a gap needs a resubscribe.
    pub fn apply(&mut self, events: Vec<Event>) -> bool {
        let accepted = self.sync.accept(events);
        for ev in &accepted.apply {
            self.upsert(ev);
        }
        accepted.resubscribe
    }

    fn upsert(&mut self, ev: &Event) {
        match ev.kind.as_str() {
            "project.upserted" => {
                if let Some(p) = &ev.project {
                    self.projects.insert(p.id.clone(), Rc::new(p.clone()));
                }
            }
            "project.removed" => {
                if let Some(id) = &ev.id {
                    self.projects.remove(id);
                }
            }
            "session.upserted" => {
                if let Some(s) = &ev.session {
                    let mut s = s.clone();
                    if let Some(old) = self.sessions.get(&s.id)
                        && only_reports(old, &s)
                    {
                        s.updated_at = old.updated_at.clone();
                    }
                    self.sessions.insert(s.id.clone(), Rc::new(s));
                }
            }
            "session.removed" => {
                if let Some(id) = &ev.id {
                    self.sessions.remove(id);
                }
            }
            "limits.upserted" => {
                if let Some(l) = &ev.limits {
                    self.limits.insert(l.harness.clone(), Rc::new(l.clone()));
                }
            }
            _ => {}
        }
    }

    /// Unarchived sessions, the latest first.
    pub fn active_sessions(&self) -> Vec<Rc<Session>> {
        let mut out: Vec<Rc<Session>> = self.sessions.values().filter(|s| !s.archived).cloned().collect();
        out.sort_by(|a, b| b.updated_at.cmp(&a.updated_at).then_with(|| a.id.cmp(&b.id)));
        out
    }

    /// Archived sessions, the latest first.
    pub fn archived_sessions(&self) -> Vec<Rc<Session>> {
        let mut out: Vec<Rc<Session>> = self.sessions.values().filter(|s| s.archived).cloned().collect();
        out.sort_by(|a, b| b.updated_at.cmp(&a.updated_at).then_with(|| a.id.cmp(&b.id)));
        out
    }

    /// Projects by name.
    pub fn projects_by_name(&self) -> Vec<Rc<Project>> {
        let mut out: Vec<Rc<Project>> = self.projects.values().cloned().collect();
        out.sort_by(|a, b| {
            a.name
                .to_lowercase()
                .cmp(&b.name.to_lowercase())
                .then_with(|| a.id.cmp(&b.id))
        });
        out
    }
}

/// Whether an update to a running session only reports on its turn: rad
/// saves the agent's context usage, model and background tasks as it hears
/// them, several times a turn, each with a new `updatedAt`, so sessions
/// running side by side would swap places in the lists at every step. Such
/// an update keeps the session's place; a turn that starts or ends, an
/// approval, and archiving still move it. Until rad itself keeps
/// `updatedAt` for them.
fn only_reports(old: &Session, new: &Session) -> bool {
    old.status == SessionStatus::Running
        && new.status == SessionStatus::Running
        && old.unread == new.unread
        && old.archived == new.archived
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::protocol::{Frame, Notification};

    fn upsert(index: &mut IndexState, id: &str, status: SessionStatus, at: &str) {
        let mut session: Session = serde_json::from_value(serde_json::json!({
            "id": id, "projectId": "p", "harness": "fake", "title": "", "createdAt": "2026-11-22T09:00:00Z",
        }))
        .unwrap();
        session.status = status;
        session.updated_at = at.into();
        index.upsert(&Event {
            stream: "index".into(),
            seq: 0,
            kind: "session.upserted".into(),
            project: None,
            session: Some(session),
            turn: None,
            item: None,
            limits: None,
            id: None,
        });
    }

    fn order(index: &IndexState) -> Vec<String> {
        index.active_sessions().iter().map(|s| s.id.clone()).collect()
    }

    #[test]
    fn running_sessions_keep_their_places() {
        use SessionStatus::*;
        let mut index = IndexState::default();
        upsert(&mut index, "a", Running, "2026-11-22T10:00:00Z");
        upsert(&mut index, "b", Running, "2026-11-22T10:00:01Z");
        assert_eq!(order(&index), ["b", "a"]);
        // Context usage reported mid-turn: a's place stays.
        upsert(&mut index, "a", Running, "2026-11-22T10:00:05Z");
        assert_eq!(order(&index), ["b", "a"]);
        assert_eq!(index.sessions["a"].updated_at, "2026-11-22T10:00:00Z");
        // An approval, its answer and the turn's end move it.
        upsert(&mut index, "a", AwaitingApproval, "2026-11-22T10:00:06Z");
        assert_eq!(order(&index), ["a", "b"]);
        upsert(&mut index, "b", Running, "2026-11-22T10:00:07Z");
        assert_eq!(order(&index), ["a", "b"]);
        upsert(&mut index, "b", Idle, "2026-11-22T10:00:08Z");
        assert_eq!(order(&index), ["b", "a"]);
        upsert(&mut index, "b", Running, "2026-11-22T10:00:09Z");
        assert_eq!(index.sessions["b"].updated_at, "2026-11-22T10:00:09Z");
    }

    #[test]
    fn applies_the_index_fixture() {
        let text = std::fs::read_to_string(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../protocol/fixtures/notification_events_index.json"
        ))
        .unwrap();
        let Frame::Notification(Notification::Events { events, .. }) = Frame::parse(&text).unwrap() else {
            panic!("not events")
        };
        let last = events.iter().map(|e| e.seq).max().unwrap();
        let mut index = IndexState::default();
        index.sync.begin_replay();
        assert!(!index.apply(events));
        assert!(index.sessions.is_empty(), "the replay waits for synchronized");
        index.synchronized(last, true);
        assert!(!index.projects.is_empty());
        assert!(!index.sessions.is_empty());
        assert_eq!(index.sync.last_seq, last);
    }
}
