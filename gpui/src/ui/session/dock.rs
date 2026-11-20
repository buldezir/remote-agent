//! The terminal docked below the messages or on their right, with an edge to
//! drag: a drag sets its height or width, a double click restores it, and the
//! prefs keep both between launches.

use std::time::Duration;

use gpui_kit::component::{h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    AnyElement, AppContext as _, ClickEvent, Context, DragMoveEvent, Empty, InteractiveElement as _, IntoElement,
    ParentElement as _, Render, StatefulInteractiveElement as _, Styled as _, Window, div, px,
};

use super::SessionView;
use crate::store::prefs::Prefs;
use crate::ui::palette::Palette;
use crate::ui::state::{prefs, update_prefs};

/// The terminal's least height below the messages, and width beside them.
const MIN_HEIGHT: f32 = 120.;
const MIN_WIDTH: f32 = 240.;
/// What a drag leaves the messages, at the least.
const MESSAGES_MIN_HEIGHT: f32 = 200.;
const MESSAGES_MIN_WIDTH: f32 = 360.;

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Dock {
    Below,
    Right,
}

/// What a drag on the terminal's edge carries; it draws nothing under the pointer.
#[derive(Clone, Copy)]
pub struct TerminalResize(Dock);

impl Render for TerminalResize {
    fn render(&mut self, _: &mut Window, _: &mut Context<Self>) -> impl IntoElement {
        Empty
    }
}

/// `size` within the least and the room a drag leaves, but never under the least.
fn fit(size: f32, min: f32, room: f32) -> f32 {
    size.min(room).max(min)
}

impl SessionView {
    /// The terminal's height below the messages, or its width beside them:
    /// the one a drag is setting, else the saved one.
    fn dock_size(&self, dock: Dock, prefs: &Prefs) -> f32 {
        match dock {
            Dock::Below => self.terminal_height.unwrap_or(prefs.terminal_height).max(MIN_HEIGHT),
            Dock::Right => self.terminal_width.unwrap_or(prefs.terminal_width).max(MIN_WIDTH),
        }
    }

    /// The messages with the terminal docked below or beside them.
    pub(crate) fn render_docked(
        &mut self,
        dock: Dock,
        messages: AnyElement,
        terminal: AnyElement,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let p = Palette::of(cx);
        let size = self.dock_size(dock, prefs(cx));
        let edge = self.terminal_edge(dock, &p, cx);
        // The terminal gives way, down to its least, when the window is too
        // small for it and the messages' least.
        let pane = div()
            .relative()
            .flex_shrink(1.)
            .border_color(p.surface0)
            .child(terminal)
            .child(edge);
        let pane = match dock {
            Dock::Below => pane.h(px(size)).min_h(px(MIN_HEIGHT)).w_full().border_t_1(),
            Dock::Right => pane.w(px(size)).min_w(px(MIN_WIDTH)).h_full().border_l_1(),
        };
        let drag = cx.listener(|this, e: &DragMoveEvent<TerminalResize>, _, cx| this.drag_terminal(e, cx));
        match dock {
            Dock::Below => v_flex()
                .id("terminal-dock")
                .flex_1()
                .min_h_0()
                .child(div().flex_1().min_h(px(MESSAGES_MIN_HEIGHT)).flex().child(messages))
                .child(pane)
                .on_drag_move(drag)
                .into_any_element(),
            Dock::Right => h_flex()
                .id("terminal-dock")
                .flex_1()
                .min_h_0()
                .items_start()
                .child(
                    div()
                        .flex_1()
                        .h_full()
                        .min_w(px(MESSAGES_MIN_WIDTH))
                        .flex()
                        .child(messages),
                )
                .child(pane)
                .on_drag_move(drag)
                .into_any_element(),
        }
    }

    /// The terminal's edge toward the messages, to drag: it sits over the border.
    fn terminal_edge(&self, dock: Dock, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let hover = p.overlay;
        div()
            .id("terminal-edge")
            .absolute()
            .top_0()
            .left_0()
            .map(|d| match dock {
                Dock::Below => d.w_full().h(px(5.)).cursor_row_resize(),
                Dock::Right => d.h_full().w(px(5.)).cursor_col_resize(),
            })
            .hover(move |s| s.bg(hover.opacity(0.5)))
            .on_drag(TerminalResize(dock), |d, _, _, cx| cx.new(|_| *d))
            .on_click(cx.listener(move |this, e: &ClickEvent, _, cx| {
                if e.click_count() >= 2 {
                    this.reset_terminal_size(dock, cx);
                }
            }))
            .into_any_element()
    }

    /// Follows a drag of the terminal's edge to the pointer, within the dock.
    fn drag_terminal(&mut self, e: &DragMoveEvent<TerminalResize>, cx: &mut Context<Self>) {
        let pos = e.event.position;
        let b = e.bounds;
        match e.drag(cx).0 {
            Dock::Below => {
                let room = b.size.height.as_f32() - MESSAGES_MIN_HEIGHT;
                self.terminal_height = Some(fit(b.bottom().as_f32() - pos.y.as_f32(), MIN_HEIGHT, room));
            }
            Dock::Right => {
                let room = b.size.width.as_f32() - MESSAGES_MIN_WIDTH;
                self.terminal_width = Some(fit(b.right().as_f32() - pos.x.as_f32(), MIN_WIDTH, room));
            }
        }
        cx.notify();
        self.save_terminal_size_soon(cx);
    }

    fn reset_terminal_size(&mut self, dock: Dock, cx: &mut Context<Self>) {
        let d = Prefs::default();
        match dock {
            Dock::Below => self.terminal_height = Some(d.terminal_height),
            Dock::Right => self.terminal_width = Some(d.terminal_width),
        }
        cx.notify();
        self.save_terminal_size_soon(cx);
    }

    /// Saves the size half a second after the last change, not at every
    /// step of a drag.
    fn save_terminal_size_soon(&mut self, cx: &mut Context<Self>) {
        self.terminal_size_save = Some(cx.spawn(async move |this, cx| {
            cx.background_executor().timer(Duration::from_millis(500)).await;
            this.update(cx, |this, cx| {
                let (height, width) = (this.terminal_height.take(), this.terminal_width.take());
                update_prefs(cx, |p| {
                    if let Some(h) = height {
                        p.terminal_height = h;
                    }
                    if let Some(w) = width {
                        p.terminal_width = w;
                    }
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
    fn keeps_a_size_within_its_least_and_the_room() {
        assert_eq!(fit(300., MIN_HEIGHT, 500.), 300.);
        assert_eq!(fit(600., MIN_HEIGHT, 500.), 500.);
        assert_eq!(fit(40., MIN_HEIGHT, 500.), MIN_HEIGHT);
        // Never under the least, even with no room.
        assert_eq!(fit(300., MIN_WIDTH, 100.), MIN_WIDTH);
    }
}
