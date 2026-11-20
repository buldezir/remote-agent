//! The Dock icon, as the SwiftUI Mac app's `Notifier` had it: a badge with
//! the number of sessions, on every server, that need the user, and a bounce
//! when one needs them, finishes or fails while the app is in the
//! background. With `Bounce::UntilActive`, a session that waits for an answer
//! or failed keeps the icon bouncing until the user comes to the app, or
//! until none of those sessions needs them, such as when they answered on
//! another device. Free of AppKit: `ui::dock` asks macOS.

use std::collections::HashSet;

use crate::protocol::Session;
use crate::store::alert::SessionAlert;
use crate::store::sidebar::stands_out;

/// How the Dock icon bounces when a session needs the user.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Bounce {
    Off,
    Once,
    /// Until the user comes to the app, for a session that's stuck.
    UntilActive,
}

impl Bounce {
    pub const ALL: [Bounce; 3] = [Bounce::Off, Bounce::Once, Bounce::UntilActive];

    /// The pref's value; anything else is `Off`.
    pub fn from_key(key: &str) -> Self {
        match key {
            "once" => Self::Once,
            "untilActive" => Self::UntilActive,
            _ => Self::Off,
        }
    }

    pub fn key(self) -> &'static str {
        match self {
            Self::Off => "off",
            Self::Once => "once",
            Self::UntilActive => "untilActive",
        }
    }

    pub fn title(self) -> &'static str {
        match self {
            Self::Off => "Off",
            Self::Once => "Once",
            Self::UntilActive => "Keep bouncing when stuck",
        }
    }
}

/// The badge's number: the sessions that wait for an answer, failed or have
/// a reply nobody has read, as the Inbox's bold rows. Archived ones don't count.
pub fn badge_count<'a>(sessions: impl IntoIterator<Item = &'a Session>) -> usize {
    sessions.into_iter().filter(|s| !s.archived && stands_out(s)).count()
}

/// What to ask macOS for.
#[derive(Debug, PartialEq, Eq)]
pub enum Attention {
    None,
    /// One bounce.
    Once,
    /// Bouncing until the user comes to the app, or it's cancelled: the
    /// caller hands its request back with `Bouncer::started`.
    UntilActive,
}

/// The bounce that lasts until the user comes to the app, and the sessions
/// it is for, by "<serverId> <sessionId>".
#[derive(Debug, Default)]
pub struct Bouncer {
    request: Option<isize>,
    routes: HashSet<String>,
}

impl Bouncer {
    /// What a session's alert asks of the Dock. A turn that finished bounces
    /// once, even with `UntilActive`: only a session that is stuck keeps it
    /// bouncing, and only while another app is in front.
    pub fn alert(&mut self, mode: Bounce, alert: &SessionAlert, route: &str, active: bool) -> Attention {
        match mode {
            Bounce::Off => Attention::None,
            Bounce::Once => Attention::Once,
            Bounce::UntilActive if *alert == SessionAlert::Finished => Attention::Once,
            Bounce::UntilActive if active => Attention::None,
            Bounce::UntilActive => {
                let first = self.routes.is_empty() && self.request.is_none();
                self.routes.insert(route.to_string());
                if first { Attention::UntilActive } else { Attention::None }
            }
        }
    }

    /// The request macOS gave for `Attention::UntilActive`.
    pub fn started(&mut self, request: isize) {
        self.request = Some(request);
    }

    /// The sessions the bounce is for.
    pub fn routes(&self) -> impl Iterator<Item = &String> {
        self.routes.iter()
    }

    /// A session that no longer needs the user: the request to cancel, once
    /// none of the bounce's sessions does.
    pub fn resolved(&mut self, route: &str) -> Option<isize> {
        if !self.routes.remove(route) || !self.routes.is_empty() {
            return None;
        }
        self.request.take()
    }

    /// Stops the bounce at once, such as when the setting changes: the
    /// request to cancel.
    pub fn stop(&mut self) -> Option<isize> {
        self.routes.clear();
        self.request.take()
    }

    /// The user came to the app, and macOS stopped the bounce itself.
    pub fn activated(&mut self) {
        self.routes.clear();
        self.request = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::protocol::SessionStatus;

    fn session(status: SessionStatus, unread: bool, archived: bool) -> Session {
        let mut s: Session = serde_json::from_value(serde_json::json!({
            "id": "s", "projectId": "p", "harness": "fake", "title": "", "status": "idle",
            "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z",
        }))
        .unwrap();
        s.status = status;
        s.unread = unread;
        s.archived = archived;
        s
    }

    #[test]
    fn the_badge_counts_what_stands_out() {
        let sessions = [
            session(SessionStatus::AwaitingApproval, false, false),
            session(SessionStatus::Error, false, false),
            session(SessionStatus::Idle, true, false),
            session(SessionStatus::Idle, false, false),
            session(SessionStatus::Running, false, false),
            session(SessionStatus::AwaitingApproval, false, true),
        ];
        assert_eq!(badge_count(&sessions), 3);
    }

    #[test]
    fn bounce_keys_round_trip() {
        for b in Bounce::ALL {
            assert_eq!(Bounce::from_key(b.key()), b);
        }
        assert_eq!(Bounce::from_key(""), Bounce::Off);
    }

    #[test]
    fn off_and_once() {
        let mut b = Bouncer::default();
        assert_eq!(
            b.alert(Bounce::Off, &SessionAlert::NeedsYou, "a", false),
            Attention::None
        );
        assert_eq!(
            b.alert(Bounce::Once, &SessionAlert::NeedsYou, "a", false),
            Attention::Once
        );
        assert_eq!(
            b.alert(Bounce::Once, &SessionAlert::Finished, "a", false),
            Attention::Once
        );
        assert_eq!(b.routes().count(), 0);
    }

    #[test]
    fn a_finished_turn_bounces_once_even_until_active() {
        let mut b = Bouncer::default();
        assert_eq!(
            b.alert(Bounce::UntilActive, &SessionAlert::Finished, "a", false),
            Attention::Once
        );
        assert_eq!(b.routes().count(), 0);
    }

    #[test]
    fn stuck_sessions_keep_it_bouncing_until_none_needs_you() {
        let mut b = Bouncer::default();
        assert_eq!(
            b.alert(Bounce::UntilActive, &SessionAlert::NeedsYou, "a", true),
            Attention::None
        );
        assert_eq!(
            b.alert(Bounce::UntilActive, &SessionAlert::NeedsYou, "a", false),
            Attention::UntilActive
        );
        b.started(7);
        assert_eq!(
            b.alert(Bounce::UntilActive, &SessionAlert::Failed(None), "b", false),
            Attention::None
        );
        assert_eq!(b.resolved("c"), None);
        assert_eq!(b.resolved("a"), None);
        assert_eq!(b.resolved("b"), Some(7));
        assert_eq!(b.resolved("b"), None);
    }

    #[test]
    fn coming_to_the_app_or_a_new_setting_ends_it() {
        let mut b = Bouncer::default();
        b.alert(Bounce::UntilActive, &SessionAlert::NeedsYou, "a", false);
        b.started(3);
        b.activated();
        assert_eq!(b.resolved("a"), None);
        assert_eq!(
            b.alert(Bounce::UntilActive, &SessionAlert::NeedsYou, "a", false),
            Attention::UntilActive
        );
        b.started(4);
        assert_eq!(b.stop(), Some(4));
        assert_eq!(b.stop(), None);
    }
}
