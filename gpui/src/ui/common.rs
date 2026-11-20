//! Small pieces the views share: captions, code blocks, Markdown, status
//! glyphs, file rows and the usage-limits gauge.

use chrono::Utc;
use gpui_kit::assets::IconName as L;
use gpui_kit::component::text::{MarkdownExtensions, TextView};
use gpui_kit::component::{Icon, IconName, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    AnyElement, Div, ElementId, FontWeight, InteractiveElement as _, IntoElement, ParentElement as _, SharedString,
    Stateful, StatefulInteractiveElement as _, Styled as _, div, px,
};

use crate::protocol::{FileStat, FileStatus, Session, SessionStatus, UsageLimits};
use crate::store::limits::{LimitLevel, limits_details, limits_gauge, limits_level};
use crate::store::sidebar::is_busy;
use crate::ui::palette::Palette;

pub fn caption(text: impl Into<SharedString>, p: &Palette) -> Div {
    div().text_xs().text_color(p.overlay).child(text.into())
}

/// Agent Markdown, in the message font; its code takes the theme's
/// monospaced font, the code font. As the other clients do: raw HTML in a
/// reply stays text.
pub fn markdown(id: impl Into<SharedString>, text: &str) -> Div {
    div().font_family(crate::ui::fonts::message()).child(
        TextView::markdown(
            ElementId::Name(SharedString::from(format!("md-{}", id.into()))),
            SharedString::from(text.to_string()),
        )
        .markdown_extensions(MarkdownExtensions::default().raw_html(false))
        .selectable(true),
    )
}

pub fn disclosure(id: impl Into<SharedString>, open: bool, title: &str, p: &Palette) -> Stateful<Div> {
    h_flex()
        .id(ElementId::Name(id.into()))
        .gap_1p5()
        .py_0p5()
        .cursor_pointer()
        .text_sm()
        .text_color(p.subtext)
        .hover(|d| d.text_color(p.text))
        .child(
            Icon::new(if open {
                IconName::ChevronDown
            } else {
                IconName::ChevronRight
            })
            .xsmall()
            .text_color(p.overlay),
        )
        .child(title.to_string())
}

/// Monospaced text on the crust colour, with an optional title.
pub fn code_block(title: Option<&str>, text: &str, p: &Palette) -> Div {
    v_flex()
        .w_full()
        .gap_1()
        .px_2()
        .py_1p5()
        .rounded_md()
        .bg(p.crust)
        .when_some(title, |d, t| {
            d.child(
                div()
                    .text_xs()
                    .font_weight(FontWeight::SEMIBOLD)
                    .text_color(p.overlay)
                    .child(t.to_string()),
            )
        })
        .child(
            div()
                .text_xs()
                .font_family(crate::ui::fonts::code())
                .text_color(p.subtext)
                .child(text.to_string()),
        )
}

/// The session's state at a glance: waiting for you, failed, working,
/// unread, or nothing.
pub fn status_dot(s: &Session, p: &Palette) -> AnyElement {
    let color = match s.status {
        SessionStatus::AwaitingApproval => Some(p.yellow),
        SessionStatus::Error => Some(p.red),
        _ if is_busy(s) => Some(p.blue),
        _ if s.unread => Some(p.accent),
        _ => None,
    };
    let dot = div().size(px(8.)).rounded_full().flex_shrink_0();
    match color {
        Some(c) => dot.bg(c).into_any_element(),
        None => dot.into_any_element(),
    }
}

pub fn file_icon(status: FileStatus, p: &Palette) -> Icon {
    match status {
        FileStatus::Added => Icon::new(IconName::Plus).text_color(p.green),
        FileStatus::Deleted => Icon::new(IconName::Minus).text_color(p.red),
        FileStatus::Renamed => Icon::new(IconName::ArrowRight).text_color(p.blue),
        _ => Icon::new(IconName::Asterisk).text_color(p.peach),
    }
}

/// A changed file: its name, its folder (or where it was), and its lines.
pub fn file_row(f: &FileStat, p: &Palette) -> Div {
    let path = std::path::Path::new(&f.path);
    let name = path
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_else(|| f.path.clone());
    let place = match &f.old_path {
        Some(old) => format!("{old} → {}", f.path),
        None => path
            .parent()
            .map(|d| d.to_string_lossy().into_owned())
            .unwrap_or_default(),
    };
    h_flex()
        .gap_2()
        .py_0p5()
        .text_sm()
        .child(file_icon(f.status, p).small())
        .child(div().flex_shrink_0().child(name))
        .child(
            div()
                .flex_1()
                .min_w_0()
                .truncate()
                .text_xs()
                .text_color(p.overlay)
                .child(place),
        )
        .child(if f.binary {
            caption("binary", p).into_any_element()
        } else {
            h_flex()
                .gap_1()
                .text_xs()
                .font_family(crate::ui::fonts::code())
                .child(div().text_color(p.green).child(format!("+{}", f.additions)))
                .child(div().text_color(p.red).child(format!("−{}", f.deletions)))
                .into_any_element()
        })
}

/// "N files  +a −d".
pub fn files_summary(files: &[FileStat]) -> String {
    let adds: i64 = files.iter().map(|f| f.additions).sum();
    let dels: i64 = files.iter().map(|f| f.deletions).sum();
    let n = files.len();
    format!("{n} file{}  +{adds} −{dels}", if n == 1 { "" } else { "s" })
}

pub fn plural(n: usize, one: &str, many: &str) -> String {
    format!("{n} {}", if n == 1 { one } else { many })
}

/// An agent's usage limits as a gauge, its details on hover; nothing when
/// there's nothing to show.
pub fn limits_gauge_view(id: impl Into<SharedString>, l: &UsageLimits, p: &Palette) -> Option<AnyElement> {
    let now = Utc::now();
    let text = limits_gauge(l, now);
    if text.is_empty() {
        return None;
    }
    let color = match limits_level(l, now) {
        LimitLevel::Normal => p.subtext,
        LimitLevel::Warning => p.yellow,
        LimitLevel::Limited => p.red,
    };
    let details = limits_details(l, now).join("\n");
    Some(
        h_flex()
            .id(ElementId::Name(id.into()))
            .gap_1()
            .text_xs()
            .text_color(color)
            .child(Icon::new(L::Gauge).xsmall().text_color(color))
            .child(text)
            .tooltip(move |window, cx| gpui_kit::component::tooltip::Tooltip::new(details.clone()).build(window, cx))
            .into_any_element(),
    )
}

/// The first line of a text, cut to `max` characters with an ellipsis.
pub fn first_line(text: &str, max: usize) -> String {
    let line = text.lines().find(|l| !l.trim().is_empty()).unwrap_or("").trim();
    if line.chars().count() > max {
        let cut: String = line.chars().take(max - 1).collect();
        format!("{}…", cut.trim_end())
    } else {
        line.to_string()
    }
}
