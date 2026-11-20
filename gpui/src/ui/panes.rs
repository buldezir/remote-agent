//! The sidebar's width, and the Columns layout's middle column's: a drag on
//! a column's right edge sets it, a double click there restores it, and the
//! prefs keep both between launches.

use std::time::Duration;

use gpui_kit::{
    AnyElement, App, AppContext as _, ClickEvent, Context, DragMoveEvent, Empty, InteractiveElement as _, IntoElement,
    Render, StatefulInteractiveElement as _, Styled as _, Window, div, px,
};

use crate::ui::app::AppView;
use crate::ui::palette::Palette;
use crate::ui::state::{AppPrefs, prefs};

pub const SIDEBAR: Limits = Limits {
    default: 290.,
    min: 200.,
    max: 480.,
};
pub const MIDDLE: Limits = Limits {
    default: 330.,
    min: 240.,
    max: 560.,
};
/// What a drag leaves the session beside them, at the least.
const DETAIL_MIN: f32 = 360.;

pub struct Limits {
    pub default: f32,
    pub min: f32,
    pub max: f32,
}

impl Limits {
    /// `width` within the limits, and within `room` when that's less, but
    /// never under the minimum.
    fn fit(&self, width: f32, room: f32) -> f32 {
        width.min(self.max).min(room).max(self.min)
    }
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Pane {
    Sidebar,
    Middle,
}

/// What a drag on an edge carries; it draws nothing under the pointer.
#[derive(Clone, Copy)]
pub struct PaneResize(pub Pane);

impl Render for PaneResize {
    fn render(&mut self, _: &mut Window, _: &mut Context<Self>) -> impl IntoElement {
        Empty
    }
}

/// The columns' widths as the prefs keep them, within the limits.
pub fn saved(cx: &App) -> (f32, f32) {
    let p = prefs(cx);
    (
        SIDEBAR.fit(p.sidebar_width, f32::INFINITY),
        MIDDLE.fit(p.column_width, f32::INFINITY),
    )
}

/// A column's right edge, to drag: it sits over the column's border.
pub fn edge(pane: Pane, p: &Palette, cx: &mut Context<AppView>) -> AnyElement {
    let hover = p.overlay;
    div()
        .id(match pane {
            Pane::Sidebar => "sidebar-edge",
            Pane::Middle => "middle-edge",
        })
        .absolute()
        .top_0()
        .right_0()
        .h_full()
        .w(px(5.))
        .cursor_col_resize()
        .hover(move |s| s.bg(hover.opacity(0.5)))
        .on_drag(PaneResize(pane), |d, _, _, cx| cx.new(|_| *d))
        .on_click(cx.listener(move |this, e: &ClickEvent, _, cx| {
            if e.click_count() >= 2 {
                this.reset_pane(pane, cx);
            }
        }))
        .into_any_element()
}

impl AppView {
    /// Follows a drag of a column's edge to `x`, the pointer's place in the window.
    pub(crate) fn drag_pane(&mut self, e: &DragMoveEvent<PaneResize>, window: &Window, cx: &mut Context<Self>) {
        let x = e.event.position.x.as_f32();
        let total = window.viewport_size().width.as_f32();
        let middle = if self.layout(cx) == crate::store::sidebar::Layout::Columns {
            self.middle_width
        } else {
            0.
        };
        match e.drag(cx).0 {
            Pane::Sidebar => self.sidebar_width = SIDEBAR.fit(x, total - middle - DETAIL_MIN),
            Pane::Middle => {
                self.middle_width = MIDDLE.fit(x - self.sidebar_width, total - self.sidebar_width - DETAIL_MIN)
            }
        }
        cx.notify();
        self.save_widths_soon(cx);
    }

    fn reset_pane(&mut self, pane: Pane, cx: &mut Context<Self>) {
        match pane {
            Pane::Sidebar => self.sidebar_width = SIDEBAR.default,
            Pane::Middle => self.middle_width = MIDDLE.default,
        }
        cx.notify();
        self.save_widths_soon(cx);
    }

    /// Saves the widths half a second after the last change, not at every
    /// step of a drag.
    fn save_widths_soon(&mut self, cx: &mut Context<Self>) {
        self.widths_save = Some(cx.spawn(async move |this, cx| {
            cx.background_executor().timer(Duration::from_millis(500)).await;
            this.update(cx, |this, cx| {
                let (sidebar, middle) = (this.sidebar_width, this.middle_width);
                cx.global_mut::<AppPrefs>().0.update(|p| {
                    p.sidebar_width = sidebar;
                    p.column_width = middle;
                });
            })
            .ok();
        }));
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn keeps_a_width_within_its_limits_and_the_room() {
        assert_eq!(SIDEBAR.fit(320., f32::INFINITY), 320.);
        assert_eq!(SIDEBAR.fit(90., f32::INFINITY), 200.);
        assert_eq!(SIDEBAR.fit(900., f32::INFINITY), 480.);
        // A narrow window leaves less room than the limit.
        assert_eq!(SIDEBAR.fit(450., 400.), 400.);
        // Never under the minimum, even with no room.
        assert_eq!(MIDDLE.fit(300., 100.), 240.);
    }
}
