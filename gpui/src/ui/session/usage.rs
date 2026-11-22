//! The session's usage, in a popup from the composer's context gauge: how
//! full its context is and with which model, the tokens its turns used,
//! and its agent's plan limits.

use chrono::Utc;
use gpui_kit::assets::IconName as L;
use gpui_kit::component::popover::Popover;
use gpui_kit::component::progress::Progress;
use gpui_kit::component::{Icon, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    Anchor, AnyElement, App, Div, Entity, FontWeight, Hsla, IntoElement, ParentElement as _, SharedString, Styled as _,
    div, px,
};

use crate::conn::ServerConn;
use crate::protocol::ContextUsage;
use crate::store::limits::{LimitLevel, limit_reached, limits_notes, window_detail, window_level, window_percent};
use crate::store::usage::{TokenTotals, context_level, context_line, context_percent, gauge_svg};
use crate::ui::badges::{harness_icon, model_name};
use crate::ui::common::caption;
use crate::ui::palette::Palette;
use crate::ui::prompt_box;

/// How full the session's context is, under the composer: a gauge with its
/// needle there and the percent, both amber and then red as it fills up
/// (`context_level`). It opens the popup above it. None when the agent
/// doesn't say how much its context holds.
pub fn context_pill(
    conn: Entity<ServerConn>,
    session_id: String,
    usage: &ContextUsage,
    cx: &App,
) -> Option<AnyElement> {
    let p = Palette::of(cx);
    let percent = context_percent(usage)?;
    let color = match context_level(usage) {
        LimitLevel::Normal => None,
        level => Some(level_color(level, &p)),
    };
    let gauge = Icon::default().data(gauge_svg(percent).as_bytes());
    Some(
        Popover::new("usage-popover")
            .anchor(Anchor::BottomRight)
            .trigger(
                prompt_box::tinted_pill("context-pill", Some(gauge), format!("{percent}%"), color)
                    .tooltip(format!("{percent}% of the context used")),
            )
            .content(move |_, _, cx| content(&conn, &session_id, cx))
            .into_any_element(),
    )
}

fn level_color(level: LimitLevel, p: &Palette) -> Hsla {
    match level {
        LimitLevel::Normal => p.accent,
        LimitLevel::Warning => p.yellow,
        LimitLevel::Limited => p.red,
    }
}

/// A part's heading, with a note at its end.
fn heading(title: impl Into<SharedString>, note: Option<String>, p: &Palette) -> Div {
    h_flex()
        .gap_2()
        .child(
            div()
                .flex_1()
                .min_w_0()
                .truncate()
                .text_xs()
                .font_weight(FontWeight::SEMIBOLD)
                .text_color(p.overlay)
                .child(title.into()),
        )
        .when_some(note, |d, note| d.child(caption(note, p).flex_shrink_0()))
}

/// What's measured, how much of it is used, and a bar of that in its
/// level's colour.
fn gauge(id: SharedString, name: String, detail: String, percent: i64, level: LimitLevel, p: &Palette) -> Div {
    v_flex()
        .gap_1()
        .child(
            h_flex()
                .gap_2()
                .text_sm()
                .child(div().flex_1().min_w_0().truncate().text_color(p.text).child(name))
                .child(caption(detail, p).flex_shrink_0()),
        )
        .child(
            Progress::new(id)
                .xsmall()
                .value(percent as f32)
                .color(level_color(level, p)),
        )
}

fn content(conn: &Entity<ServerConn>, session_id: &str, cx: &App) -> AnyElement {
    let p = Palette::of(cx);
    let c = conn.read(cx);
    let Some(s) = c.session(session_id) else {
        return div().into_any_element();
    };
    let now = Utc::now();
    let harness = c.harness(&s.harness);
    let agent = harness.map(|h| h.name.clone()).unwrap_or_else(|| s.harness.clone());
    let mut parts = v_flex().w(px(300.)).gap_3().p_1();

    // The context, and the model that fills it.
    let effort = s
        .model_info
        .as_ref()
        .and_then(|m| m.effort.clone())
        .or_else(|| s.effort.clone())
        .filter(|e| !e.is_empty());
    let model = match effort {
        Some(effort) => format!("{} · {effort}", model_name(&s, harness)),
        None => model_name(&s, harness),
    };
    let mut context = v_flex().gap_1p5().child(heading("Context", None, &p));
    if let Some(usage) = &s.context {
        context = context.child(match context_percent(usage) {
            Some(percent) => gauge(
                "usage-context".into(),
                context_line(usage),
                format!("{percent}% used"),
                percent,
                context_level(usage),
                &p,
            ),
            None => div().text_sm().text_color(p.text).child(context_line(usage)),
        });
    }
    context = context.child(
        h_flex()
            .gap_1p5()
            .text_xs()
            .text_color(p.subtext)
            .child(harness_icon(&s.harness, &p).xsmall())
            .child(div().min_w_0().truncate().child(model)),
    );
    parts = parts.child(context);

    // The tokens its turns used.
    let tokens = c
        .stores
        .get(session_id)
        .map(|store| TokenTotals::of(store.turns.values()).lines())
        .unwrap_or_default();
    if !tokens.is_empty() {
        let mut list = v_flex().gap_1().child(heading("Tokens this session", None, &p));
        for (name, count) in tokens {
            list = list.child(
                h_flex()
                    .gap_2()
                    .text_sm()
                    .child(div().flex_1().text_color(p.subtext).child(name))
                    .child(div().text_color(p.text).child(count)),
            );
        }
        parts = parts.child(list);
    }

    // Its agent's plan limits, which every session of that agent shares.
    let mut plan = v_flex().gap_1p5();
    match c.index.limits.get(&s.harness) {
        Some(l) => {
            let mut notes = limits_notes(l, now);
            // The report's age, which is the last of the notes, sits by the heading.
            let updated = notes.pop();
            plan = plan.child(heading(format!("{agent} limits"), updated, &p));
            if let Some(reached) = limit_reached(l, now) {
                plan = plan.child(
                    h_flex()
                        .gap_1p5()
                        .text_sm()
                        .text_color(p.red)
                        .child(Icon::new(L::Gauge).xsmall().text_color(p.red))
                        .child(reached),
                );
            }
            for (i, w) in l.windows.iter().enumerate() {
                plan = plan.child(gauge(
                    SharedString::from(format!("usage-window-{i}")),
                    w.label.clone(),
                    window_detail(w, now),
                    // None once the window has reset, whatever was said of it.
                    window_percent(w, now),
                    window_level(w, now),
                    &p,
                ));
            }
            if !notes.is_empty() {
                plan = plan.child(caption(notes.join(" · "), &p));
            }
        }
        None => {
            plan = plan
                .child(heading(format!("{agent} limits"), None, &p))
                .child(caption(format!("{agent} hasn't reported its plan's limits."), &p));
        }
    }
    parts.child(plan).into_any_element()
}
