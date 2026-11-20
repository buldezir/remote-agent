//! How the sidebar groups sessions, as `SidebarLayout` does in the other
//! clients (`web/src/ui/sidebarLayout.ts`, ra's `internal/client/sidebar.go`).
//! This client has the Inbox layout only, for now.

use chrono::{DateTime, Local, Utc};

use crate::protocol::{Session, SessionStatus};

/// Waiting for an answer, or failed: what a person has to look at.
pub fn needs_you(s: &Session) -> bool {
    matches!(s.status, SessionStatus::AwaitingApproval | SessionStatus::Error)
}

/// Needs you or is unread: its title is bold in the lists.
pub fn stands_out(s: &Session) -> bool {
    needs_you(s) || s.unread
}

/// Working, or done with tasks left running in the background.
pub fn is_busy(s: &Session) -> bool {
    s.status == SessionStatus::Running || (s.status == SessionStatus::Idle && !s.background_tasks.is_empty())
}

/// The open session, and whether it stood out when it was opened.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct OpenedSession {
    pub id: String,
    pub stood_out: bool,
}

/// Splits sessions into the Inbox's groups, each in the order given: what
/// needs you or is unread, then the rest. The open session counts as unread
/// if it was when it was opened: reading it leaves its row where it is, and
/// so does a turn that ends in view.
pub fn inbox_groups<T: AsRef<Session> + Clone>(active: &[T], opened: &OpenedSession) -> (Vec<T>, Vec<T>) {
    let mut needs = Vec::new();
    let mut recent = Vec::new();
    for s in active {
        let session = s.as_ref();
        let stands = needs_you(session)
            || if !opened.id.is_empty() && session.id == opened.id {
                opened.stood_out
            } else {
                session.unread
            };
        if stands {
            needs.push(s.clone())
        } else {
            recent.push(s.clone())
        }
    }
    (needs, recent)
}

/// A session's age where a row has little room: "now", "4m", "3h", "2d",
/// then the date.
pub fn short_age(t: DateTime<Utc>, now: DateTime<Utc>) -> String {
    let s = (now - t).num_seconds().max(0);
    match s {
        s if s < 60 => "now".into(),
        s if s < 3600 => format!("{}m", s / 60),
        s if s < 86_400 => format!("{}h", s / 3600),
        s if s < 7 * 86_400 => format!("{}d", s / 86_400),
        _ => t.with_timezone(&Local).format("%b %-d").to_string(),
    }
}

/// `short_age` of an RFC 3339 time, or "" when it doesn't parse.
pub fn short_age_of(time: &str, now: DateTime<Utc>) -> String {
    DateTime::parse_from_rfc3339(time)
        .map(|t| short_age(t.with_timezone(&Utc), now))
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::protocol::BackgroundTask;
    use chrono::TimeZone;
    use std::rc::Rc;

    fn sess(id: &str, status: SessionStatus, unread: bool, background: bool) -> Rc<Session> {
        let mut s: Session = serde_json::from_value(serde_json::json!({
            "id": id, "projectId": "p", "harness": "fake", "title": id
        }))
        .unwrap();
        s.status = status;
        s.unread = unread;
        if background {
            s.background_tasks = vec![BackgroundTask {
                id: "b".into(),
                kind: "command".into(),
                description: "".into(),
            }];
        }
        Rc::new(s)
    }

    fn ids(list: &[Rc<Session>]) -> Vec<&str> {
        list.iter().map(|s| s.id.as_str()).collect()
    }

    #[test]
    fn groups_sessions_by_what_they_need() {
        use SessionStatus::*;
        assert!(needs_you(&sess("s", AwaitingApproval, false, false)));
        assert!(needs_you(&sess("s", Error, false, false)));
        assert!(!needs_you(&sess("s", Running, false, false)));
        assert!(is_busy(&sess("s", Running, false, false)));
        assert!(is_busy(&sess("s", Idle, false, true)));
        assert!(!is_busy(&sess("s", Idle, false, false)));
        assert!(!is_busy(&sess("s", Stopped, false, true)));
    }

    #[test]
    fn inbox_puts_what_needs_you_or_is_unread_first() {
        use SessionStatus::*;
        let items = vec![
            sess("running", Running, false, false),
            sess("unread", Idle, true, false),
            sess("background", Idle, false, true),
            sess("waiting", AwaitingApproval, false, false),
            sess("idle", Idle, false, false),
            sess("failed", Error, false, false),
        ];
        assert!(stands_out(&sess("s", Idle, true, false)));
        assert!(!stands_out(&sess("s", Running, false, false)));
        let (needs, recent) = inbox_groups(&items, &OpenedSession::default());
        assert_eq!(ids(&needs), ["unread", "waiting", "failed"]);
        assert_eq!(ids(&recent), ["running", "background", "idle"]);
        // The open session stays where it was when it was opened.
        let read: Vec<Rc<Session>> = items
            .iter()
            .map(|s| {
                let mut c = (**s).clone();
                c.unread = false;
                Rc::new(c)
            })
            .collect();
        let (needs, _) = inbox_groups(
            &read,
            &OpenedSession {
                id: "unread".into(),
                stood_out: true,
            },
        );
        assert_eq!(ids(&needs), ["unread", "waiting", "failed"]);
    }

    #[test]
    fn short_ages() {
        let now = Utc.with_ymd_and_hms(2026, 10, 9, 12, 0, 0).unwrap();
        let ago = |s: i64| short_age(now - chrono::Duration::seconds(s), now);
        assert_eq!(ago(10), "now");
        assert_eq!(ago(4 * 60), "4m");
        assert_eq!(ago(3 * 3600), "3h");
        assert_eq!(ago(2 * 86_400), "2d");
        assert!(ago(30 * 86_400).starts_with("Sep"));
        assert_eq!(short_age_of("2026-10-09T11:56:00Z", now), "4m");
        assert_eq!(short_age_of("bogus", now), "");
    }
}
