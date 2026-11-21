//! Small marks the sidebar, the session bar and the composer share: a
//! project's badge, an agent's icon, a session's model, and its pull request.

use gpui_kit::assets::IconName as L;
use gpui_kit::component::{Icon, Sizable as _};
use gpui_kit::{Div, FontWeight, Hsla, ParentElement as _, Styled as _, div, px};

use crate::protocol::{HarnessInfo, PullRequest, PullRequestChecks, PullRequestState, Session};
use crate::ui::palette::Palette;

/// A project's badge: its initial on a colour its name picks.
pub fn project_badge(name: &str, size: f32, p: &Palette) -> Div {
    let color = badge_color(name, p);
    let initial = name
        .chars()
        .find(|c| c.is_alphanumeric())
        .map(|c| c.to_uppercase().collect::<String>())
        .unwrap_or_else(|| "·".into());
    div()
        .size(px(size))
        .flex_shrink_0()
        .flex()
        .items_center()
        .justify_center()
        .rounded(px((size / 4.).round()))
        .bg(color.opacity(0.22))
        .text_color(color)
        .text_size(px((size * 0.62).round()))
        .font_weight(FontWeight::BOLD)
        .child(initial)
}

/// The same colour for a name on every launch and every machine.
fn badge_color(name: &str, p: &Palette) -> Hsla {
    let colors = [p.red, p.peach, p.yellow, p.green, p.blue, p.mauve];
    // FNV-1a: std's hasher may change between Rust versions.
    let mut h: u32 = 0x811c_9dc5;
    for b in name.bytes() {
        h ^= b as u32;
        h = h.wrapping_mul(0x0100_0193);
    }
    colors[h as usize % colors.len()]
}

/// An agent's icon, as the Swift client's `HarnessIcon` has them.
pub fn harness_icon(id: &str, p: &Palette) -> Icon {
    let (icon, color) = match id {
        "claude" => (L::Sparkle, p.peach),
        "codex" => (L::CodeXml, p.text),
        "fake" => (L::Drama, p.subtext),
        "acp:cursor" => (L::MousePointer, p.subtext),
        "acp:opencode" => (L::Braces, p.subtext),
        "acp:gemini" => (L::Diamond, p.blue),
        "pi" | "acp:omp" => (L::Pi, p.subtext),
        _ => (L::Cpu, p.subtext),
    };
    Icon::new(icon).text_color(color)
}

/// The session's model by name, else its agent's.
pub fn model_name(s: &Session, harness: Option<&HarnessInfo>) -> String {
    let info = s.model_info.clone().unwrap_or_default();
    info.name
        .or_else(|| {
            let id = info.id.or_else(|| s.model.clone())?;
            Some(harness.map(|h| h.model_name(&id)).unwrap_or(id))
        })
        .or_else(|| harness.map(|h| h.name.clone()))
        .unwrap_or_else(|| s.harness.clone())
}

/// How full the session's context is, in percent.
pub fn context_percent(s: &Session) -> Option<f64> {
    let c = s.context.as_ref()?;
    let w = c.window.filter(|w| *w > 0)?;
    Some(((c.used as f64 / w as f64).min(1.) * 100.).round())
}

/// A pull request's glyph, by its state.
pub fn pull_request_icon(pr: &PullRequest, p: &Palette) -> Icon {
    let (icon, color) = match pr.shown_state() {
        PullRequestState::Draft => (L::GitPullRequestDraft, p.subtext),
        PullRequestState::Merged => (L::GitMerge, p.mauve),
        PullRequestState::Closed => (L::GitPullRequestClosed, p.red),
        PullRequestState::Open | PullRequestState::Unknown => (L::GitPullRequest, p.green),
    };
    Icon::new(icon).text_color(color)
}

/// The colour of a pull request's state.
pub fn pull_request_color(pr: &PullRequest, p: &Palette) -> Hsla {
    match pr.shown_state() {
        PullRequestState::Draft => p.subtext,
        PullRequestState::Merged => p.mauve,
        PullRequestState::Closed => p.red,
        PullRequestState::Open | PullRequestState::Unknown => p.green,
    }
}

/// A small mark for how its checks went: a green check, a red ✕, a yellow dot.
pub fn checks_mark(pr: &PullRequest, p: &Palette) -> Option<Div> {
    let mark = match pr.shown_checks()? {
        PullRequestChecks::Passing => div().child(Icon::new(L::Check).xsmall().text_color(p.green)),
        PullRequestChecks::Failing => div().child(Icon::new(L::X).xsmall().text_color(p.red)),
        PullRequestChecks::Pending | PullRequestChecks::Unknown => {
            div().size(px(6.)).mx(px(3.)).rounded_full().bg(p.yellow)
        }
    };
    Some(mark.flex_shrink_0())
}
