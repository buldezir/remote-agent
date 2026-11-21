//! What the app says of sessions' shells: a session's in its hover card, and
//! how many run in Server Settings, with the timeout's choices.

use chrono::{DateTime, Utc};

use crate::protocol::TerminalInfo;

/// The choices for the terminal timeout, in days (PROTOCOL.md, "Terminal").
pub const TIMEOUT_DAYS: std::ops::RangeInclusive<i64> = 1..=14;
pub const DEFAULT_TIMEOUT_DAYS: i64 = 7;

/// "1 day", "7 days".
pub fn days(n: i64) -> String {
    if n == 1 { "1 day".into() } else { format!("{n} days") }
}

/// A span in its largest unit: "5m", "3h", "6d". Days round to the nearest,
/// so a shell that just started with a week to go says "7d", not "6d".
fn span(secs: i64) -> String {
    let secs = secs.max(0);
    match secs {
        s if s < 3600 => format!("{}m", (s / 60).max(1)),
        s if s < 86_400 => format!("{}h", s / 3600),
        s => format!("{}d", (s + 43_200) / 86_400),
    }
}

fn parse(time: &str) -> Option<DateTime<Utc>> {
    DateTime::parse_from_rfc3339(time).ok().map(|t| t.with_timezone(&Utc))
}

/// A session's shell, for its hover card: "Terminal running · closes in 7d",
/// with how long nobody has typed once that's a minute or more.
pub fn state_line(info: Option<&TerminalInfo>, now: DateTime<Utc>) -> String {
    let Some(t) = info else {
        return "No terminal".into();
    };
    let mut parts = vec!["Terminal running".to_string()];
    if let Some(last) = parse(&t.last_input_at) {
        let idle = (now - last).num_seconds();
        if idle >= 60 {
            parts.push(format!("idle {}", span(idle)));
        }
    }
    if let Some(end) = parse(&t.expires_at) {
        let left = (end - now).num_seconds();
        parts.push(if left > 0 {
            format!("closes in {}", span(left))
        } else {
            "closing".into()
        });
    }
    parts.join(" · ")
}

/// How many shells run, for Server Settings.
pub fn count_line(n: usize) -> String {
    match n {
        0 => "No terminals running".into(),
        1 => "1 terminal running".into(),
        n => format!("{n} terminals running"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn info(last: &str, expires: &str) -> TerminalInfo {
        TerminalInfo {
            session_id: "s1".into(),
            started_at: "2026-11-01T12:00:00Z".into(),
            last_input_at: last.into(),
            expires_at: expires.into(),
        }
    }

    #[test]
    fn state() {
        let now = parse("2026-11-01T12:00:30Z").unwrap();
        assert_eq!(state_line(None, now), "No terminal");
        let fresh = info("2026-11-01T12:00:00Z", "2026-11-08T12:00:00Z");
        assert_eq!(state_line(Some(&fresh), now), "Terminal running · closes in 7d");
        let now = parse("2026-11-01T15:10:00Z").unwrap();
        assert_eq!(
            state_line(Some(&fresh), now),
            "Terminal running · idle 3h · closes in 7d"
        );
        let now = parse("2026-11-08T11:58:00Z").unwrap();
        assert_eq!(
            state_line(Some(&fresh), now),
            "Terminal running · idle 7d · closes in 2m"
        );
        let now = parse("2026-11-08T12:00:20Z").unwrap();
        assert_eq!(state_line(Some(&fresh), now), "Terminal running · idle 7d · closing");
    }

    #[test]
    fn spans() {
        assert_eq!(span(-5), "1m");
        assert_eq!(span(59), "1m");
        assert_eq!(span(3599), "59m");
        assert_eq!(span(3600), "1h");
        assert_eq!(span(86_399), "23h");
        assert_eq!(span(86_400), "1d");
        assert_eq!(span(7 * 86_400 - 30), "7d");
        assert_eq!(span(6 * 86_400 + 11 * 3600), "6d");
    }

    #[test]
    fn counts() {
        assert_eq!(count_line(0), "No terminals running");
        assert_eq!(count_line(1), "1 terminal running");
        assert_eq!(count_line(3), "3 terminals running");
        assert_eq!(days(1), "1 day");
        assert_eq!(days(14), "14 days");
    }
}
