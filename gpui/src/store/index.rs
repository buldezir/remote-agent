//! The `index` stream: projects, every session's summary, and the agents'
//! usage limits.

use std::collections::HashMap;
use std::rc::Rc;

use crate::protocol::{Event, Project, Session, UsageLimits};

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
                    self.sessions.insert(s.id.clone(), Rc::new(s.clone()));
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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::protocol::{Frame, Notification};

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
