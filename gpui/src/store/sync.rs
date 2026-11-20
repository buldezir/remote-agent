//! A stream's sync rules (PROTOCOL.md, "Streams and sync"), as RAKit, the
//! web client and Android's core keep them:
//!
//! - After `subscribe`, the replay's `events` are buffered until
//!   `synchronized`, which says whether to drop the cache (`reset`) and
//!   then applies them in one step.
//! - Live events must follow on from the last seq; a gap resubscribes from
//!   what was applied.
//! - Subscriptions can overlap (a view subscribes as the connection
//!   resubscribes everything): only the last one's `synchronized` makes the
//!   stream live, so a newer replay is buffered rather than skipped, and its
//!   reset can't drop what it replays.

use crate::protocol::Event;

#[derive(Debug, Default)]
pub struct StreamSync {
    pub last_seq: i64,
    pub live: bool,
    /// A `synchronized` has come at least once.
    pub synced: bool,
    outstanding: usize,
    replay: Vec<Event>,
}

/// What to do with a batch of events.
#[derive(Debug, Default, PartialEq)]
pub struct Accepted {
    /// The events to apply now, in order.
    pub apply: Vec<Event>,
    /// A gap: subscribe again, from `last_seq`.
    pub resubscribe: bool,
}

/// What `synchronized` asks of the stream's state.
#[derive(Debug, Default, PartialEq)]
pub struct Synchronized {
    /// Drop the cached state first.
    pub reset: bool,
    /// Then apply these, the buffered replay.
    pub apply: Vec<Event>,
}

impl StreamSync {
    /// A subscribe is about to be sent; returns its `afterSeq`.
    pub fn begin_replay(&mut self) -> i64 {
        self.outstanding += 1;
        self.live = false;
        self.replay.clear();
        self.last_seq
    }

    /// A subscribe call failed, so its `synchronized` won't come.
    pub fn subscription_failed(&mut self) {
        self.outstanding = self.outstanding.saturating_sub(1);
    }

    /// The socket closed: no subscription is outstanding any more.
    pub fn closed(&mut self) {
        self.outstanding = 0;
        self.live = false;
    }

    pub fn synchronized(&mut self, seq: i64, reset: bool) -> Synchronized {
        self.outstanding = self.outstanding.saturating_sub(1);
        let apply = std::mem::take(&mut self.replay);
        self.last_seq = seq;
        self.live = self.outstanding == 0;
        self.synced = true;
        Synchronized { reset, apply }
    }

    pub fn accept(&mut self, events: Vec<Event>) -> Accepted {
        if !self.live {
            self.replay.extend(events);
            return Accepted::default();
        }
        let mut out = Accepted::default();
        for ev in events {
            if ev.seq <= self.last_seq {
                continue;
            }
            if ev.seq != self.last_seq + 1 {
                self.live = false;
                out.resubscribe = true;
                break;
            }
            self.last_seq = ev.seq;
            out.apply.push(ev);
        }
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ev(seq: i64) -> Event {
        Event {
            stream: "index".into(),
            seq,
            kind: "project.removed".into(),
            project: None,
            session: None,
            turn: None,
            item: None,
            limits: None,
            id: Some(format!("p{seq}")),
        }
    }

    fn seqs(events: &[Event]) -> Vec<i64> {
        events.iter().map(|e| e.seq).collect()
    }

    #[test]
    fn buffers_the_replay_until_synchronized() {
        let mut s = StreamSync::default();
        assert_eq!(s.begin_replay(), 0);
        assert_eq!(s.accept(vec![ev(3), ev(7)]), Accepted::default());
        let sync = s.synchronized(7, true);
        assert!(sync.reset);
        assert_eq!(seqs(&sync.apply), [3, 7]);
        assert!(s.live && s.synced);
        // Live events follow on.
        let a = s.accept(vec![ev(7), ev(8), ev(9)]);
        assert_eq!(seqs(&a.apply), [8, 9]);
        assert!(!a.resubscribe);
    }

    #[test]
    fn a_gap_resubscribes_from_what_was_applied() {
        let mut s = StreamSync::default();
        s.begin_replay();
        s.synchronized(5, true);
        let a = s.accept(vec![ev(6), ev(8), ev(9)]);
        assert_eq!(seqs(&a.apply), [6]);
        assert!(a.resubscribe && !s.live);
        assert_eq!(s.begin_replay(), 6);
    }

    #[test]
    fn only_the_last_overlapping_subscription_goes_live() {
        let mut s = StreamSync::default();
        s.begin_replay();
        s.begin_replay();
        s.accept(vec![ev(1)]);
        let first = s.synchronized(1, true);
        assert!(!s.live, "a second replay is on its way");
        assert_eq!(seqs(&first.apply), [1]);
        s.accept(vec![ev(1), ev(2)]);
        let second = s.synchronized(2, false);
        assert!(s.live);
        assert_eq!(seqs(&second.apply), [1, 2]);
    }

    #[test]
    fn a_failed_or_closed_subscription_stops_waiting() {
        let mut s = StreamSync::default();
        s.begin_replay();
        s.begin_replay();
        s.subscription_failed();
        s.synchronized(0, true);
        assert!(s.live);
        s.begin_replay();
        s.closed();
        assert!(!s.live);
        s.begin_replay();
        s.synchronized(0, false);
        assert!(s.live);
    }
}
