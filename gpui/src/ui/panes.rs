//! The columns' widths: a drag on a column's right edge sets it, a double
//! click there restores it, and the prefs keep them between launches. There
//! are two: the session list's, which is the sidebar in most layouts and the
//! middle column in Columns, so the list is as wide in every layout; and the
//! Columns layout's first column's, the servers and their projects.

use std::time::Duration;

use gpui_kit::{
    AnyElement, App, AppContext as _, ClickEvent, Context, DragMoveEvent, Empty, InteractiveElement as _, IntoElement,
    Render, StatefulInteractiveElement as _, Styled as _, Window, div, px,
};

use crate::ui::app::AppView;
use crate::ui::palette::Palette;
use crate::ui::state::{AppPrefs, prefs};

/// The session list's.
pub const SESSIONS: Limits = Limits {
    default: 290.,
    min: 230.,
    max: 480.,
};
/// The Columns layout's first column's. Its bar holds the traffic lights
/// and the list's buttons, which set the least.
pub const COLLECTIONS: Limits = Limits {
    default: 240.,
    min: 220.,
    max: 400.,
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

/// The widths the prefs keep.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum Width {
    Sessions,
    Collections,
}

impl Pane {
    /// Which width the column takes: the session list's, unless it's the
    /// sidebar beside the list, as in Columns.
    fn width(self, columns: bool) -> Width {
        match self {
            Pane::Sidebar if columns => Width::Collections,
            _ => Width::Sessions,
        }
    }
}

/// What a drag on an edge carries; it draws nothing under the pointer.
#[derive(Clone, Copy)]
pub struct PaneResize(pub Pane);

impl Render for PaneResize {
    fn render(&mut self, _: &mut Window, _: &mut Context<Self>) -> impl IntoElement {
        Empty
    }
}

/// The session list's width and the collections' as the prefs keep them,
/// within the limits.
pub fn saved(cx: &App) -> (f32, f32) {
    let p = prefs(cx);
    (
        SESSIONS.fit(p.sidebar_width, f32::INFINITY),
        COLLECTIONS.fit(p.collections_width, f32::INFINITY),
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
    fn columns(&self, cx: &App) -> bool {
        self.layout(cx) == crate::store::sidebar::Layout::Columns
    }

    /// How wide a column is now.
    pub(crate) fn pane_width(&self, pane: Pane, cx: &App) -> f32 {
        match pane.width(self.columns(cx)) {
            Width::Sessions => self.sidebar_width,
            Width::Collections => self.collections_width,
        }
    }

    /// Follows a drag of a column's edge to `x`, the pointer's place in the window.
    pub(crate) fn drag_pane(&mut self, e: &DragMoveEvent<PaneResize>, window: &Window, cx: &mut Context<Self>) {
        let x = e.event.position.x.as_f32();
        let total = window.viewport_size().width.as_f32();
        let columns = self.columns(cx);
        let pane = e.drag(cx).0;
        // What the other columns take: the one before it, and the one after.
        let (before, after) = match (pane, columns) {
            (Pane::Sidebar, true) => (0., self.sidebar_width),
            (Pane::Sidebar, false) => (0., 0.),
            (Pane::Middle, _) => (self.collections_width, 0.),
        };
        let room = total - before - after - DETAIL_MIN;
        match pane.width(columns) {
            Width::Sessions => self.sidebar_width = SESSIONS.fit(x - before, room),
            Width::Collections => self.collections_width = COLLECTIONS.fit(x - before, room),
        }
        cx.notify();
        self.save_widths_soon(cx);
    }

    fn reset_pane(&mut self, pane: Pane, cx: &mut Context<Self>) {
        match pane.width(self.columns(cx)) {
            Width::Sessions => self.sidebar_width = SESSIONS.default,
            Width::Collections => self.collections_width = COLLECTIONS.default,
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
                let (sidebar, collections) = (this.sidebar_width, this.collections_width);
                cx.global_mut::<AppPrefs>().0.update(|p| {
                    p.sidebar_width = sidebar;
                    p.collections_width = collections;
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
        assert_eq!(SESSIONS.fit(320., f32::INFINITY), 320.);
        assert_eq!(SESSIONS.fit(90., f32::INFINITY), 230.);
        assert_eq!(SESSIONS.fit(900., f32::INFINITY), 480.);
        // A narrow window leaves less room than the limit.
        assert_eq!(SESSIONS.fit(450., 400.), 400.);
        // Never under the minimum, even with no room.
        assert_eq!(COLLECTIONS.fit(300., 100.), 220.);
    }

    #[test]
    fn the_session_list_keeps_its_width_in_every_layout() {
        // The sidebar is the list, except in Columns, where the list is
        // the middle column and the sidebar has a width of its own.
        assert_eq!(Pane::Sidebar.width(false), Width::Sessions);
        assert_eq!(Pane::Middle.width(true), Width::Sessions);
        assert_eq!(Pane::Sidebar.width(true), Width::Collections);
    }
}
