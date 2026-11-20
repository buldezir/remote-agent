//! What in a session the user wants to hear about while they look
//! elsewhere: the agent waits for them, finished its turn, or failed.
//! RAKit's SessionAlert.

use crate::protocol::{Session, SessionStatus};

#[derive(Debug, Clone, PartialEq)]
pub enum SessionAlert {
    NeedsYou,
    Finished,
    Failed(Option<String>),
}

/// The alert for a session that changed from `old` to `next`, if any. An
/// archived session has none.
pub fn alert(old: &Session, next: &Session) -> Option<SessionAlert> {
    if next.archived || old.status == next.status {
        return None;
    }
    match next.status {
        SessionStatus::AwaitingApproval => Some(SessionAlert::NeedsYou),
        SessionStatus::Idle if old.status == SessionStatus::Running => Some(SessionAlert::Finished),
        SessionStatus::Error => Some(SessionAlert::Failed(next.error.clone())),
        _ => None,
    }
}
