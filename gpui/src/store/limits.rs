//! Usage limits: the gauge for an agent's plan ("5h 34% · week 61%"), its
//! colour, the details behind it, and the notice for a turn that hit a
//! limit. A port of RAKit's `UsageLimitsFormat`; the tests share
//! `protocol/vectors/usage_limits.json` with every other client.

use chrono::{DateTime, Utc};

use crate::protocol::{UsageLimits, UsageWindow};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum LimitLevel {
    Normal,
    /// Amber: `WARNING_PERCENT` or more used.
    Warning,
    /// Red: spent.
    Limited,
}

pub const WARNING_PERCENT: i64 = 80;

fn parse(t: &Option<String>) -> Option<DateTime<Utc>> {
    t.as_deref()
        .and_then(|s| DateTime::parse_from_rfc3339(s).ok())
        .map(|t| t.with_timezone(&Utc))
}

/// A window's use now: 0 once it has reset since the agent said.
pub fn window_percent(w: &UsageWindow, now: DateTime<Utc>) -> i64 {
    if parse(&w.resets_at).is_some_and(|r| r <= now) {
        return 0;
    }
    w.used_percent.clamp(0, 100)
}

fn percent_level(p: i64) -> LimitLevel {
    if p >= 100 {
        LimitLevel::Limited
    } else if p >= WARNING_PERCENT {
        LimitLevel::Warning
    } else {
        LimitLevel::Normal
    }
}

pub fn window_level(w: &UsageWindow, now: DateTime<Utc>) -> LimitLevel {
    percent_level(window_percent(w, now))
}

/// Whether the limit the agent hit hasn't reset yet.
pub fn limited_now(l: &UsageLimits, now: DateTime<Utc>) -> bool {
    l.limited && parse(&l.resets_at).is_none_or(|r| r > now)
}

/// The gauge's colour: its most used window's, or red while a limit holds.
pub fn limits_level(l: &UsageLimits, now: DateTime<Utc>) -> LimitLevel {
    if limited_now(l, now) {
        return LimitLevel::Limited;
    }
    let mut level = LimitLevel::Normal;
    for w in &l.windows {
        match window_level(w, now) {
            LimitLevel::Limited => return LimitLevel::Limited,
            LimitLevel::Warning => level = LimitLevel::Warning,
            LimitLevel::Normal => {}
        }
    }
    level
}

/// Each window's label and use, "5h 34% · week 61%"; "" with nothing to show.
pub fn limits_gauge(l: &UsageLimits, now: DateTime<Utc>) -> String {
    let parts: Vec<String> = l
        .windows
        .iter()
        .map(|w| format!("{} {}%", w.label, window_percent(w, now)))
        .collect();
    if parts.is_empty() && limited_now(l, now) {
        return "Limit reached".into();
    }
    parts.join(" · ")
}

/// The lines behind the gauge, on hover.
pub fn limits_details(l: &UsageLimits, now: DateTime<Utc>) -> Vec<String> {
    let mut lines = Vec::new();
    if limited_now(l, now) {
        lines.push(format!("Limit reached{}", resets_in(parse(&l.resets_at), now)));
    }
    for w in &l.windows {
        let resets = parse(&w.resets_at);
        if let Some(r) = resets.filter(|r| *r <= now) {
            lines.push(format!("{}: reset {} ago", w.label, limit_duration(now - r, false)));
            continue;
        }
        lines.push(format!(
            "{}: {}% used{}",
            w.label,
            window_percent(w, now),
            resets_in(resets, now)
        ));
    }
    if l.overage {
        lines.push("Using extra usage".into());
    }
    if let Some(plan) = l.plan.as_deref().filter(|p| !p.is_empty()) {
        lines.push(format!("{plan} plan"));
    }
    let updated = parse(&Some(l.updated_at.clone())).unwrap_or(now);
    let age = now - updated;
    if age < chrono::Duration::minutes(1) {
        lines.push("Updated just now".into());
    } else {
        lines.push(format!("Updated {} ago", limit_duration(age, false)));
    }
    lines
}

fn resets_in(t: Option<DateTime<Utc>>, now: DateTime<Utc>) -> String {
    match t {
        Some(t) if t > now => format!(" · resets in {}", limit_duration(t - now, true)),
        _ => String::new(),
    }
}

/// What the session shows for an error whose turn hit the limit `w`:
/// "5h limit reached · resets in 2h 14m".
pub fn limit_notice(w: &UsageWindow, now: DateTime<Utc>) -> String {
    let mut s = format!("{} limit reached", w.label);
    match parse(&w.resets_at) {
        None => {}
        Some(r) if r > now => s += &format!(" · resets in {}", limit_duration(r - now, true)),
        Some(r) => s += &format!(" · reset {} ago", limit_duration(now - r, false)),
    }
    let mut chars = s.chars();
    match chars.next() {
        Some(c) => c.to_uppercase().collect::<String>() + chars.as_str(),
        None => s,
    }
}

/// A span to a reset, or since one: "45m", "2h 14m", "3d 4h". A span to come
/// rounds its minutes up, one past down; either is a minute at least.
pub fn limit_duration(d: chrono::Duration, future: bool) -> String {
    let secs = d.num_seconds().max(0);
    let mut m = secs / 60;
    if future && (secs % 60 != 0 || d.subsec_nanos() != 0) {
        m += 1;
    }
    let m = m.max(1);
    if m < 60 {
        return format!("{m}m");
    }
    if m < 24 * 60 {
        return if m % 60 == 0 {
            format!("{}h", m / 60)
        } else {
            format!("{}h {}m", m / 60, m % 60)
        };
    }
    let h = m % (24 * 60) / 60;
    if h > 0 {
        format!("{}d {}h", m / (24 * 60), h)
    } else {
        format!("{}d", m / (24 * 60))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde::Deserialize;

    #[derive(Deserialize)]
    #[serde(rename_all = "camelCase")]
    struct Case {
        name: String,
        limits: UsageLimits,
        gauge: String,
        level: String,
        window_levels: Vec<String>,
        window_percents: Vec<i64>,
        details: Vec<String>,
    }
    #[derive(Deserialize)]
    struct Notice {
        window: UsageWindow,
        text: String,
    }
    #[derive(Deserialize)]
    struct Duration {
        seconds: i64,
        future: String,
        past: String,
    }
    #[derive(Deserialize)]
    struct Vectors {
        now: String,
        cases: Vec<Case>,
        notices: Vec<Notice>,
        durations: Vec<Duration>,
    }

    fn level_name(l: LimitLevel) -> &'static str {
        match l {
            LimitLevel::Normal => "normal",
            LimitLevel::Warning => "warning",
            LimitLevel::Limited => "limited",
        }
    }

    #[test]
    fn shared_vectors() {
        let text = std::fs::read_to_string(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../protocol/vectors/usage_limits.json"
        ))
        .unwrap();
        let v: Vectors = serde_json::from_str(&text).unwrap();
        let now = DateTime::parse_from_rfc3339(&v.now).unwrap().with_timezone(&Utc);
        for c in &v.cases {
            assert_eq!(limits_gauge(&c.limits, now), c.gauge, "{}", c.name);
            assert_eq!(level_name(limits_level(&c.limits, now)), c.level, "{}", c.name);
            let levels: Vec<&str> = c
                .limits
                .windows
                .iter()
                .map(|w| level_name(window_level(w, now)))
                .collect();
            assert_eq!(levels, c.window_levels, "{}", c.name);
            let percents: Vec<i64> = c.limits.windows.iter().map(|w| window_percent(w, now)).collect();
            assert_eq!(percents, c.window_percents, "{}", c.name);
            assert_eq!(limits_details(&c.limits, now), c.details, "{}", c.name);
        }
        for n in &v.notices {
            assert_eq!(limit_notice(&n.window, now), n.text);
        }
        for d in &v.durations {
            let span = chrono::Duration::seconds(d.seconds);
            assert_eq!(limit_duration(span, true), d.future, "{}s", d.seconds);
            assert_eq!(limit_duration(span, false), d.past, "{}s", d.seconds);
        }
    }
}
