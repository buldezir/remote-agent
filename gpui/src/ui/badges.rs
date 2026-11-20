//! Small marks the sidebar, the session bar and the composer share: a
//! project's badge, an agent's icon, a session's model, and the pull request
//! placeholder.

use gpui_kit::assets::IconName as L;
use gpui_kit::component::Icon;
use gpui_kit::{Div, FontWeight, Hsla, ParentElement as _, Styled as _, div, px};

use crate::protocol::{HarnessInfo, Session, WorkspaceKind};
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

/// A session's pull request.
pub struct PullRequest {
    pub number: u32,
    pub title: String,
}

/// Sessions don't carry their pull request yet. With `RA_GPUI_FAKE_PR=1`,
/// every worktree session gets a made-up one, to try the rows and cards
/// with it; otherwise none.
pub fn pull_request(s: &Session) -> Option<PullRequest> {
    if std::env::var("RA_GPUI_FAKE_PR").ok().as_deref() != Some("1") || s.workspace.kind != WorkspaceKind::Worktree {
        return None;
    }
    let mut h: u32 = 0x811c_9dc5;
    for b in s.id.bytes() {
        h ^= b as u32;
        h = h.wrapping_mul(0x0100_0193);
    }
    let title = if s.title.is_empty() {
        "Placeholder pull request".to_string()
    } else {
        format!("Placeholder: {}", s.title)
    };
    Some(PullRequest {
        number: 100 + h % 900,
        title,
    })
}
