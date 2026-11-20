//! The live state of one session (its `session:<id>` stream): its turns and
//! transcript items, and the prompts on their way to rad. A port of RAKit's
//! `SessionStore` (and ra's `internal/client/sessionstore.go`).

use std::cmp::Ordering;
use std::collections::{HashMap, HashSet};
use std::rc::Rc;

use crate::protocol::{Event, ImageRef, Item, ItemKind, Session, Turn};

use super::sync::StreamSync;

/// A prompt on its way to rad, shown until the transcript has it.
#[derive(Debug, Clone, PartialEq)]
pub struct Outgoing {
    pub command_id: String,
    pub text: String,
    pub images: Vec<ImageRef>,
}

#[derive(Debug, Default)]
pub struct SessionState {
    pub id: String,
    pub sync: StreamSync,
    pub session: Option<Rc<Session>>,
    pub turns: HashMap<String, Rc<Turn>>,
    pub items: HashMap<String, Rc<Item>>,
    /// The top-level items in order; a tool call's children are under it.
    pub transcript: Vec<Rc<Item>>,
    children: HashMap<String, Vec<Rc<Item>>>,
    pub outbox: Vec<Outgoing>,
    /// The session was deleted on rad; its stream says so.
    pub removed: bool,
    /// Views showing the session; the stream is subscribed while any does.
    pub viewers: usize,
    /// Counts changes, for views that cache what they drew.
    pub version: u64,
}

impl SessionState {
    pub fn new(id: &str, session: Option<Rc<Session>>) -> Self {
        Self {
            id: id.into(),
            session,
            ..Default::default()
        }
    }

    pub fn synchronized(&mut self, seq: i64, reset: bool) {
        let sync = self.sync.synchronized(seq, reset);
        if sync.reset {
            self.turns.clear();
            self.items.clear();
        }
        for ev in &sync.apply {
            self.upsert(ev);
        }
        self.rebuild();
    }

    /// Applies live events; returns true when a gap needs a resubscribe.
    pub fn apply(&mut self, events: Vec<Event>) -> bool {
        let accepted = self.sync.accept(events);
        for ev in &accepted.apply {
            self.upsert(ev);
        }
        self.rebuild();
        accepted.resubscribe
    }

    /// The index's copy of the session, before the stream has its own.
    pub fn set_session(&mut self, session: Rc<Session>) {
        self.session = Some(session);
        self.version += 1;
    }

    fn upsert(&mut self, ev: &Event) {
        match ev.kind.as_str() {
            "session.upserted" => {
                if let Some(s) = &ev.session {
                    self.session = Some(Rc::new(s.clone()));
                }
            }
            "turn.upserted" => {
                if let Some(t) = &ev.turn {
                    self.turns.insert(t.id.clone(), Rc::new(t.clone()));
                }
            }
            "item.upserted" => {
                if let Some(it) = &ev.item {
                    self.items.insert(it.id.clone(), Rc::new(it.clone()));
                }
            }
            "session.removed" => {
                self.session = None;
                self.turns.clear();
                self.items.clear();
                self.outbox.clear();
                self.removed = true;
            }
            _ => {}
        }
    }

    fn rebuild(&mut self) {
        let mut top: Vec<Rc<Item>> = Vec::new();
        let mut kids: HashMap<String, Vec<Rc<Item>>> = HashMap::new();
        for it in self.items.values() {
            match &it.parent_item_id {
                Some(parent) if self.items.contains_key(parent) => {
                    kids.entry(parent.clone()).or_default().push(it.clone())
                }
                _ => top.push(it.clone()),
            }
        }
        let by_order = |a: &Rc<Item>, b: &Rc<Item>| -> Ordering { a.order.cmp(&b.order).then_with(|| a.id.cmp(&b.id)) };
        top.sort_by(by_order);
        for list in kids.values_mut() {
            list.sort_by(by_order);
        }
        // Drop the prompts rad has confirmed.
        let confirmed: HashSet<String> = top
            .iter()
            .filter(|it| it.kind == ItemKind::UserMessage)
            .map(|it| outgoing_key(it.text.as_deref().unwrap_or(""), &it.images))
            .collect();
        self.outbox
            .retain(|o| !confirmed.contains(&outgoing_key(&o.text, &o.images)));
        self.transcript = top;
        self.children = kids;
        self.version += 1;
    }

    pub fn children_of(&self, id: &str) -> &[Rc<Item>] {
        self.children.get(id).map(Vec::as_slice).unwrap_or(&[])
    }

    /// Puts a prompt in the outbox, where it shows until the transcript has it.
    pub fn add_outgoing(&mut self, command_id: &str, text: &str, images: Vec<ImageRef>) {
        self.outbox.push(Outgoing {
            command_id: command_id.into(),
            text: text.into(),
            images,
        });
        self.version += 1;
    }

    /// Takes a prompt that didn't get through out of the outbox.
    pub fn drop_outgoing(&mut self, command_id: &str) {
        self.outbox.retain(|o| o.command_id != command_id);
        self.version += 1;
    }
}

fn outgoing_key(text: &str, images: &[ImageRef]) -> String {
    let mut key = text.to_string();
    for im in images {
        key.push('\0');
        key.push_str(&im.id);
    }
    key
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::protocol::{Frame, Notification};

    fn fixture_events() -> Vec<Event> {
        let text = std::fs::read_to_string(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../protocol/fixtures/notification_events_session.json"
        ))
        .unwrap();
        let Frame::Notification(Notification::Events { events, .. }) = Frame::parse(&text).unwrap() else {
            panic!("not events")
        };
        events
    }

    fn user_message(id: &str, order: i64, text: &str) -> Event {
        Event {
            stream: "session:s1".into(),
            seq: 0,
            kind: "item.upserted".into(),
            project: None,
            session: None,
            turn: None,
            item: Some(Item {
                id: id.into(),
                session_id: "s1".into(),
                order,
                kind: ItemKind::UserMessage,
                text: Some(text.into()),
                ..Default::default()
            }),
            limits: None,
            id: None,
        }
    }

    #[test]
    fn replays_the_fixture() {
        let events = fixture_events();
        let last = events.iter().map(|e| e.seq).max().unwrap();
        let mut s = SessionState::new("s1", None);
        s.sync.begin_replay();
        s.apply(events);
        assert!(s.transcript.is_empty(), "the replay waits for synchronized");
        s.synchronized(last, true);
        assert!(s.session.is_some());
        assert!(!s.transcript.is_empty());
        assert!(!s.turns.is_empty());
        // Sorted by order, children under their tool call.
        assert!(s.transcript.windows(2).all(|w| w[0].order <= w[1].order));
        for it in s.items.values() {
            if let Some(p) = &it.parent_item_id
                && s.items.contains_key(p)
            {
                assert!(s.children_of(p).iter().any(|c| c.id == it.id));
                assert!(!s.transcript.iter().any(|t| t.id == it.id));
            }
        }
    }

    #[test]
    fn the_outbox_empties_as_the_transcript_confirms() {
        let mut s = SessionState::new("s1", None);
        s.sync.begin_replay();
        s.synchronized(0, true);
        s.add_outgoing("c1", "hello", vec![]);
        s.add_outgoing("c2", "again", vec![]);
        let mut ev = user_message("u1", 1, "hello");
        ev.seq = 1;
        s.apply(vec![ev]);
        assert_eq!(
            s.outbox.iter().map(|o| o.command_id.as_str()).collect::<Vec<_>>(),
            ["c2"]
        );
        s.drop_outgoing("c2");
        assert!(s.outbox.is_empty());
    }

    #[test]
    fn removal_empties_the_session() {
        let mut s = SessionState::new("s1", None);
        s.sync.begin_replay();
        let mut ev = user_message("u1", 1, "hi");
        ev.seq = 1;
        s.apply(vec![ev]);
        s.synchronized(1, true);
        assert_eq!(s.transcript.len(), 1);
        let removed = Event {
            stream: "session:s1".into(),
            seq: 2,
            kind: "session.removed".into(),
            project: None,
            session: None,
            turn: None,
            item: None,
            limits: None,
            id: Some("s1".into()),
        };
        s.apply(vec![removed]);
        assert!(s.removed && s.transcript.is_empty() && s.session.is_none());
    }
}
