//! A message's images, large, one at a time; the arrow keys page, ⌘C
//! copies the image and ⌘S saves it.

use std::cell::{Cell, RefCell};
use std::path::PathBuf;
use std::rc::Rc;
use std::sync::Arc;

use gpui_kit::assets::IconName as L;
use gpui_kit::component::WindowExt as _;
use gpui_kit::component::button::{Button, ButtonVariants as _};
use gpui_kit::component::spinner::Spinner;
use gpui_kit::component::{Disableable as _, Icon, IconName, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    App, AppContext as _, ClickEvent, ClipboardItem, Entity, Image, InteractiveElement as _, IntoElement as _,
    KeyDownEvent, ObjectFit, ParentElement as _, SharedString, Styled as _, StyledImage as _, Window, div, img, px,
};

use crate::conn::ServerConn;
use crate::images::{self, Loaded};
use crate::protocol::ImageRef;
use crate::ui::common::caption;
use crate::ui::palette::Palette;

/// What the last Copy or Save did, shown under the image until it pages.
type Status = Rc<RefCell<Option<SharedString>>>;

pub fn open(conn: Entity<ServerConn>, images: Vec<ImageRef>, start: usize, window: &mut Window, cx: &mut App) {
    let current = Rc::new(Cell::new(start.min(images.len().saturating_sub(1))));
    let images = Rc::new(images);
    let status: Status = Rc::default();
    let size = window.viewport_size();
    // The dialog focuses its own root, which its content's key handler is
    // under, not on the way to: the content takes focus of its own instead,
    // and Escape still reaches the dialog from there.
    let focus = cx.focus_handle();
    let keys_focus = focus.clone();
    window.open_dialog(cx, move |dialog, _, cx| {
        let p = Palette::of(cx);
        let n = images.len();
        let i = current.get();
        let Some(r) = images.get(i) else { return dialog };
        let loaded = conn.update(cx, |c, cx| c.image(&r.id, cx));
        let ready = match &loaded {
            Loaded::Ready(image) => Some((r.id.clone(), image.clone())),
            _ => None,
        };
        let picture = match loaded {
            Loaded::Ready(image) => img(image).size_full().object_fit(ObjectFit::Contain).into_any_element(),
            Loaded::Loading => Spinner::new().into_any_element(),
            Loaded::Failed(e) => caption(e, &p).into_any_element(),
        };
        let (prev, next) = (current.clone(), current.clone());
        let keys = current.clone();
        let (prev_status, next_status, key_status) = (status.clone(), status.clone(), status.clone());
        let key_image = ready.clone();
        let note = status.borrow().clone();
        dialog.w(size.width * 0.85).child(
            v_flex()
                .gap_2()
                .key_context("ImageViewer")
                .track_focus(&keys_focus)
                .on_key_down(move |ev: &KeyDownEvent, window, cx| {
                    let k = &ev.keystroke;
                    let cmd = k.modifiers.platform && !k.modifiers.shift && !k.modifiers.control && !k.modifiers.alt;
                    match k.key.as_str() {
                        "left" if keys.get() > 0 => {
                            keys.set(keys.get() - 1);
                            key_status.take();
                            window.refresh();
                            cx.stop_propagation();
                        }
                        "right" if keys.get() + 1 < n => {
                            keys.set(keys.get() + 1);
                            key_status.take();
                            window.refresh();
                            cx.stop_propagation();
                        }
                        "c" if cmd => {
                            if let Some((_, image)) = &key_image {
                                copy(image, &key_status, window, cx);
                            }
                            cx.stop_propagation();
                        }
                        "s" if cmd => {
                            if let Some((id, image)) = &key_image {
                                save(id, image.clone(), key_status.clone(), cx);
                            }
                            cx.stop_propagation();
                        }
                        _ => {}
                    }
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
                        .child(
                            div()
                                .flex_1()
                                .min_w_0()
                                .when_some(note, |d, note| d.child(caption(note, &p).truncate())),
                        )
                        .child(
                            Button::new("prev")
                                .small()
                                .ghost()
                                .icon(IconName::ChevronLeft)
                                .disabled(i == 0)
                                .on_click(move |_: &ClickEvent, window, _| {
                                    prev.set(prev.get().saturating_sub(1));
                                    prev_status.take();
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
                                    next_status.take();
                                    window.refresh();
                                }),
                        )
                        .child(
                            h_flex()
                                .flex_1()
                                .justify_end()
                                .gap_1()
                                .child({
                                    let (image, status) = (ready.clone(), status.clone());
                                    Button::new("copy-image")
                                        .small()
                                        .ghost()
                                        .icon(Icon::new(L::Copy))
                                        .label("Copy")
                                        .tooltip("Copy the image (⌘C)")
                                        .disabled(ready.is_none())
                                        .on_click(move |_: &ClickEvent, window, cx| {
                                            if let Some((_, image)) = &image {
                                                copy(image, &status, window, cx);
                                            }
                                        })
                                })
                                .child({
                                    let (image, status) = (ready.clone(), status.clone());
                                    Button::new("save-image")
                                        .small()
                                        .ghost()
                                        .icon(Icon::new(L::Download))
                                        .label("Save…")
                                        .tooltip("Save the image to a file (⌘S)")
                                        .disabled(ready.is_none())
                                        .on_click(move |_: &ClickEvent, _, cx| {
                                            if let Some((id, image)) = &image {
                                                save(id, image.clone(), status.clone(), cx);
                                            }
                                        })
                                }),
                        ),
                ),
        )
    });
    focus.focus(window, cx);
}

/// ⌘C and ⌘S are the viewer's own while it has focus: without these, the
/// app's bindings (Copy, Save Draft) take them before its key handler does.
pub fn bind_keys(cx: &mut App) {
    use gpui_kit::{KeyBinding, NoAction};
    cx.bind_keys([
        KeyBinding::new("cmd-c", NoAction, Some("ImageViewer")),
        KeyBinding::new("cmd-s", NoAction, Some("ImageViewer")),
    ]);
}

/// Puts the image on the pasteboard.
fn copy(image: &Image, status: &Status, window: &mut Window, cx: &mut App) {
    let note = match images::for_clipboard(image) {
        Ok(image) => {
            cx.write_to_clipboard(ClipboardItem::new_image(&image));
            "Copied".to_string()
        }
        Err(e) => e,
    };
    status.replace(Some(note.into()));
    window.refresh();
}

/// Asks where to save the image, in Downloads first, and writes its bytes
/// there as rad sent them.
fn save(id: &str, image: Arc<Image>, status: Status, cx: &mut App) {
    let dir = std::env::var_os("HOME")
        .map(PathBuf::from)
        .unwrap_or_default()
        .join("Downloads");
    let name = images::file_name(id, image.format);
    let rx = cx.prompt_for_new_path(&dir, Some(&name));
    cx.spawn(async move |cx| {
        let Ok(Ok(Some(path))) = rx.await else { return };
        let bytes = image.bytes.clone();
        let target = path.clone();
        let written = cx.background_spawn(async move { std::fs::write(&target, bytes) }).await;
        let note = match written {
            Ok(()) => format!(
                "Saved to {}",
                path.file_name()
                    .map(|n| n.to_string_lossy().into_owned())
                    .unwrap_or_default()
            ),
            Err(e) => format!("Couldn't save the image: {e}"),
        };
        status.replace(Some(note.into()));
        cx.update(|cx| cx.refresh_windows());
    })
    .detach();
}
