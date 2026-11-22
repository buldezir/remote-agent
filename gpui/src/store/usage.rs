//! What a session's usage popup says besides its agent's plan limits
//! (`limits.rs`): how full its context is, and the tokens its turns used.

use std::rc::Rc;

use crate::protocol::{ContextUsage, Turn};
use crate::store::limits::LimitLevel;

/// A count of tokens, short: "950", "5k", "30.2k", "200k", "1.2M".
pub fn token_count(n: i64) -> String {
    let n = n.max(0);
    // One decimal, without a ".0".
    let short = |v: f64, unit: &str| {
        let s = format!("{v:.1}");
        format!("{}{unit}", s.strip_suffix(".0").unwrap_or(&s))
    };
    match n {
        n if n < 1000 => n.to_string(),
        n if n < 99_950 => short(n as f64 / 1000., "k"),
        n if n < 999_500 => format!("{}k", (n as f64 / 1000.).round()),
        n => short(n as f64 / 1_000_000., "M"),
    }
}

/// How full the context is, in percent, when the agent says how much it holds.
pub fn context_percent(c: &ContextUsage) -> Option<i64> {
    let window = c.window.filter(|w| *w > 0)?;
    Some(((c.used as f64 / window as f64).min(1.) * 100.).round() as i64)
}

/// A context of a million tokens or more: it gets worse to work in well
/// before it's full, so it warns earlier.
const LARGE_CONTEXT: i64 = 1_000_000;

/// How pressing the context's fullness is: amber from 80% and red from 90%,
/// or from 65% and 85% in a context of a million tokens.
pub fn context_level(c: &ContextUsage) -> LimitLevel {
    let Some(percent) = context_percent(c) else {
        return LimitLevel::Normal;
    };
    let (warning, limited) = if c.window.is_some_and(|w| w >= LARGE_CONTEXT) {
        (65, 85)
    } else {
        (80, 90)
    };
    if percent >= limited {
        LimitLevel::Limited
    } else if percent >= warning {
        LimitLevel::Warning
    } else {
        LimitLevel::Normal
    }
}

/// The context in tokens: "30k of 200k tokens", or "5k tokens" when the
/// agent doesn't say how much it holds.
pub fn context_line(c: &ContextUsage) -> String {
    match c.window.filter(|w| *w > 0) {
        Some(window) => format!("{} of {} tokens", token_count(c.used), token_count(window)),
        None => format!("{} tokens", token_count(c.used)),
    }
}

/// A gauge as an SVG icon, its needle at `percent` of the dial: Lucide's
/// `gauge`, whose needle only ever points at two thirds. The needle moves in
/// steps of a tenth, so there are eleven icons to draw and keep, not a
/// hundred.
pub fn gauge_svg(percent: i64) -> String {
    let (x, y) = gauge_needle(percent);
    format!(
        concat!(
            r#"<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" "#,
            r#"stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">"#,
            r#"<path d="M12 14 {x} {y}"/><path d="M3.34 19a10 10 0 1 1 17.32 0"/></svg>"#
        ),
        x = x,
        y = y
    )
}

/// Where the gauge's needle ends, from the dial's centre at (12, 14): the
/// dial opens downwards, from 210° when empty, over the top, to −30° when full.
fn gauge_needle(percent: i64) -> (f64, f64) {
    let step = (percent.clamp(0, 100) + 5) / 10;
    let angle = (210. - 24. * step as f64).to_radians();
    // As long as Lucide's: from (12, 14) to (16, 10).
    let length = 32f64.sqrt();
    let round = |v: f64| (v * 100.).round() / 100.;
    (round(12. + length * angle.cos()), round(14. - length * angle.sin()))
}

/// The tokens a session's turns used, as their agents counted them. Their
/// cost isn't here: Claude Code reports its process's running total with
/// each turn, not the turn's own, so the turns' costs don't add up.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct TokenTotals {
    pub input: i64,
    pub output: i64,
    pub cache_read: i64,
    pub cache_write: i64,
}

impl TokenTotals {
    pub fn of<'a>(turns: impl IntoIterator<Item = &'a Rc<Turn>>) -> TokenTotals {
        let mut t = TokenTotals::default();
        for u in turns.into_iter().filter_map(|turn| turn.usage.as_ref()) {
            t.input += u.input_tokens.unwrap_or(0);
            t.output += u.output_tokens.unwrap_or(0);
            t.cache_read += u.cache_read_tokens.unwrap_or(0);
            t.cache_write += u.cache_write_tokens.unwrap_or(0);
        }
        t
    }

    /// What was used, each with its name; nothing for a count of none.
    pub fn lines(&self) -> Vec<(&'static str, String)> {
        [
            ("Input", self.input),
            ("Output", self.output),
            ("Cache read", self.cache_read),
            ("Cache written", self.cache_write),
        ]
        .into_iter()
        .filter(|(_, n)| *n > 0)
        .map(|(name, n)| (name, token_count(n)))
        .collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn counts_tokens_briefly() {
        for (n, text) in [
            (0, "0"),
            (950, "950"),
            (1000, "1k"),
            (5000, "5k"),
            (30_240, "30.2k"),
            (99_949, "99.9k"),
            (99_950, "100k"),
            (200_000, "200k"),
            (999_499, "999k"),
            (999_500, "1M"),
            (1_234_000, "1.2M"),
            (-5, "0"),
        ] {
            assert_eq!(token_count(n), text, "{n}");
        }
    }

    #[test]
    fn says_how_full_the_context_is() {
        let c = |used, window| ContextUsage { used, window };
        assert_eq!(context_line(&c(30_000, Some(200_000))), "30k of 200k tokens");
        assert_eq!(context_percent(&c(30_000, Some(200_000))), Some(15));
        assert_eq!(context_percent(&c(300_000, Some(200_000))), Some(100));
        // An agent that doesn't say how much its context holds.
        assert_eq!(context_line(&c(5000, None)), "5k tokens");
        assert_eq!(context_percent(&c(5000, None)), None);
        assert_eq!(context_percent(&c(5000, Some(0))), None);
    }

    #[test]
    fn a_large_context_warns_earlier() {
        let level = |percent: i64, window: i64| {
            context_level(&ContextUsage {
                used: window / 100 * percent,
                window: Some(window),
            })
        };
        use LimitLevel::*;
        for (percent, usual, large) in [
            (64, Normal, Normal),
            (65, Normal, Warning),
            (79, Normal, Warning),
            (80, Warning, Warning),
            (84, Warning, Warning),
            (85, Warning, Limited),
            (89, Warning, Limited),
            (90, Limited, Limited),
            (100, Limited, Limited),
        ] {
            assert_eq!(level(percent, 200_000), usual, "{percent}% of 200k");
            assert_eq!(level(percent, 1_000_000), large, "{percent}% of 1M");
        }
        // Without a size, there's no saying how full it is.
        assert_eq!(
            context_level(&ContextUsage {
                used: 900_000,
                window: None
            }),
            Normal
        );
    }

    #[test]
    fn the_gauges_needle_follows_the_percent() {
        // Empty: down to the left, where the dial starts. Half: straight up.
        // Full: down to the right, where it ends.
        assert_eq!(gauge_needle(0), (7.1, 16.83));
        assert_eq!(gauge_needle(50), (12., 8.34));
        assert_eq!(gauge_needle(100), (16.9, 16.83));
        // In steps of a tenth, and never off the dial.
        assert_eq!(gauge_needle(14), gauge_needle(10));
        assert_eq!(gauge_needle(15), gauge_needle(20));
        assert_eq!(gauge_needle(250), gauge_needle(100));
        assert_eq!(gauge_needle(-3), gauge_needle(0));
        assert!(gauge_svg(50).contains(r#"<path d="M12 14 12 8.34"/>"#));
    }

    #[test]
    fn sums_the_turns_tokens() {
        let turn = |usage: serde_json::Value| -> Rc<Turn> {
            Rc::new(
                serde_json::from_value(serde_json::json!({"id": "t", "sessionId": "s", "n": 1, "usage": usage}))
                    .unwrap(),
            )
        };
        let turns = [
            turn(serde_json::json!({"inputTokens": 4, "outputTokens": 300, "cacheReadTokens": 58_000, "costUsd": 0.1})),
            turn(serde_json::json!({"inputTokens": 2, "outputTokens": 1200})),
            // A turn that was stopped has none.
            turn(serde_json::Value::Null),
        ];
        let totals = TokenTotals::of(&turns);
        assert_eq!(
            totals,
            TokenTotals {
                input: 6,
                output: 1500,
                cache_read: 58_000,
                cache_write: 0
            }
        );
        assert_eq!(
            totals.lines(),
            [
                ("Input", "6".to_string()),
                ("Output", "1.5k".to_string()),
                ("Cache read", "58k".to_string())
            ]
        );
        assert!(TokenTotals::of(&[]).lines().is_empty());
    }
}
