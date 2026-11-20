//! A message's images, large, one at a time; the arrow keys page.

use std::cell::Cell;
use std::rc::Rc;

use gpui_kit::component::WindowExt as _;
use gpui_kit::component::button::{Button, ButtonVariants as _};
use gpui_kit::component::spinner::Spinner;
use gpui_kit::component::{Disableable as _, IconName, Sizable as _, h_flex, v_flex};
use gpui_kit::{
    App, ClickEvent, Entity, InteractiveElement as _, IntoElement as _, KeyDownEvent, ObjectFit, ParentElement as _,
    Styled as _, StyledImage as _, Window, div, img, px,
};

use crate::conn::ServerConn;
use crate::images::Loaded;
use crate::protocol::ImageRef;
use crate::ui::common::caption;
use crate::ui::palette::Palette;

pub fn open(conn: Entity<ServerConn>, images: Vec<ImageRef>, start: usize, window: &mut Window, cx: &mut App) {
    let current = Rc::new(Cell::new(start.min(images.len().saturating_sub(1))));
    let images = Rc::new(images);
    let size = window.viewport_size();
    window.open_dialog(cx, move |dialog, _, cx| {
        let p = Palette::of(cx);
        let n = images.len();
        let i = current.get();
        let Some(r) = images.get(i) else { return dialog };
        let loaded = conn.update(cx, |c, cx| c.image(&r.id, cx));
        let picture = match loaded {
            Loaded::Ready(image) => img(image).size_full().object_fit(ObjectFit::Contain).into_any_element(),
            Loaded::Loading => Spinner::new().into_any_element(),
            Loaded::Failed(e) => caption(e, &p).into_any_element(),
        };
        let (prev, next) = (current.clone(), current.clone());
        let keys = current.clone();
        dialog.w(size.width * 0.85).child(
            v_flex()
                .gap_2()
                .on_key_down(move |ev: &KeyDownEvent, window, cx| match ev.keystroke.key.as_str() {
                    "left" if keys.get() > 0 => {
                        keys.set(keys.get() - 1);
                        window.refresh();
                        cx.stop_propagation();
                    }
                    "right" if keys.get() + 1 < n => {
                        keys.set(keys.get() + 1);
                        window.refresh();
                        cx.stop_propagation();
                    }
                    _ => {}
                })
                .child(
                    div()
                        .h(size.height * 0.7)
                        .w_full()
                        .flex()
                        .items_center()
                        .justify_center()
                        .child(picture),
                )
                .child(
                    h_flex()
                        .gap_2()
                        .justify_center()
                        .child(
                            Button::new("prev")
                                .small()
                                .ghost()
                                .icon(IconName::ChevronLeft)
                                .disabled(i == 0)
                                .on_click(move |_: &ClickEvent, window, _| {
                                    prev.set(prev.get().saturating_sub(1));
                                    window.refresh();
                                }),
                        )
                        .child(caption(format!("{} of {n}", i + 1), &p).min_w(px(60.)).text_center())
                        .child(
                            Button::new("next")
                                .small()
                                .ghost()
                                .icon(IconName::ChevronRight)
                                .disabled(i + 1 >= n)
                                .on_click(move |_: &ClickEvent, window, _| {
                                    if next.get() + 1 < n {
                                        next.set(next.get() + 1);
                                    }
                                    window.refresh();
                                }),
                        ),
                ),
        )
    });
}
