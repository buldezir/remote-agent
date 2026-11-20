//! The prompt box a session and a new session share: the box with its
//! images, the text, a row under it (the + menu at its start; the agent,
//! dictation and Send at its end), and a row of small pickers under the box,
//! for the server, where the agent works, the branch and the permissions. A
//! session greys out what it can't change once started.

use gpui_kit::component::button::{Button, ButtonRounded, ButtonVariants as _};
use gpui_kit::component::menu::{DropdownMenu as _, PopupMenu, PopupMenuItem};
use gpui_kit::component::{Disableable as _, Icon, IconName, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    Anchor, AnyElement, App, ClickEvent, Context, IntoElement, ParentElement as _, Styled as _, Window, div, px, rems,
};

use crate::ui::palette::Palette;

/// The widest the box gets: a centred column in a wide window.
pub const MAX_WIDTH: f32 = 760.;

#[derive(Default)]
pub struct PromptBox {
    /// Over the box, such as the slash commands' suggestions.
    pub above: Option<AnyElement>,
    pub attachments: Option<AnyElement>,
    pub input: Option<AnyElement>,
    /// The start of the row under the text: the + menu, a state.
    pub leading: Vec<AnyElement>,
    /// Its end: the agent, Stop, Steer, dictation, Send.
    pub trailing: Vec<AnyElement>,
    /// The pickers under the box, at its start and at its end.
    pub footer_start: Vec<AnyElement>,
    pub footer_end: Vec<AnyElement>,
    /// As wide as its column, as under a transcript, rather than a centred
    /// column of its own.
    pub wide: bool,
}

impl PromptBox {
    pub fn render(self, p: &Palette) -> AnyElement {
        let footer = !self.footer_start.is_empty() || !self.footer_end.is_empty();
        v_flex()
            .w_full()
            .when(!self.wide, |d| d.max_w(px(MAX_WIDTH)))
            .gap_1()
            .when_some(self.above, |d, a| d.child(a))
            .child(
                v_flex()
                    .w_full()
                    .gap_2()
                    .px_3()
                    .pt_2p5()
                    .pb_2()
                    .rounded_xl()
                    .border_1()
                    .border_color(p.surface0)
                    .bg(p.mantle)
                    .when_some(self.attachments, |d, a| d.child(a))
                    .when_some(self.input, |d, i| {
                        d.child(div().font_family(crate::ui::fonts::message()).child(i))
                    })
                    .child(
                        h_flex()
                            .gap_1()
                            .children(self.leading)
                            .child(div().flex_1())
                            .children(self.trailing),
                    ),
            )
            .when(footer, |d| {
                d.child(
                    h_flex()
                        .w_full()
                        .flex_wrap()
                        .gap_x_1()
                        .px_1()
                        .child(h_flex().gap_1().children(separated(self.footer_start, p)))
                        .child(div().flex_1())
                        .child(h_flex().gap_1().children(self.footer_end)),
                )
            })
            .into_any_element()
    }
}

/// The start's pickers with a thin rule between them, as T3 has.
fn separated(items: Vec<AnyElement>, p: &Palette) -> Vec<AnyElement> {
    let mut out = Vec::new();
    for (i, item) in items.into_iter().enumerate() {
        if i > 0 {
            out.push(div().w(px(1.)).h(px(12.)).bg(p.surface0).into_any_element());
        }
        out.push(item);
    }
    out
}

/// A small, quiet button: an icon and a label.
/// The pills' text, against the interface's size: a little smaller than
/// the rest, larger than a caption.
pub const PILL_TEXT: f32 = 0.9;

/// A small, quiet button: an icon and a label, at `PILL_TEXT` of the
/// interface's size, which the kit's sizes can't give its own label.
pub fn pill(id: impl Into<gpui_kit::ElementId>, icon: Option<Icon>, label: impl Into<String>) -> Button {
    let label: String = label.into();
    Button::new(id).ghost().xsmall().h(rems(1.5)).px_1p5().child(
        h_flex()
            .gap_1()
            .text_size(rems(PILL_TEXT))
            .line_height(rems(1.2))
            .when_some(icon, |d, i| d.child(i.size(rems(PILL_TEXT))))
            .child(label),
    )
}

/// A pill that opens a menu upward, as it sits near the window's foot.
pub fn pill_menu(
    id: &'static str,
    icon: Option<Icon>,
    label: impl Into<String>,
    tooltip: Option<String>,
    build: impl Fn(PopupMenu, &mut Window, &mut Context<PopupMenu>) -> PopupMenu + 'static,
) -> AnyElement {
    let mut b = pill(id, icon, label);
    if let Some(t) = tooltip {
        b = b.tooltip(t);
    }
    b.dropdown_menu_with_anchor(Anchor::BottomLeft, move |menu, window, cx| {
        build(menu.max_w(px(360.)), window, cx)
    })
    .into_any_element()
}

/// The same pill, greyed out: a setting a started session keeps.
pub fn fixed_pill(id: &'static str, icon: Option<Icon>, label: impl Into<String>) -> AnyElement {
    pill(id, icon, label).disabled(true).into_any_element()
}

/// What a menu does with the choice picked.
pub type OnPick = std::rc::Rc<dyn Fn(String, &mut Window, &mut App)>;

/// The choices of a menu, the current one checked. A choice with the id "-"
/// is a separator.
pub fn choices(mut menu: PopupMenu, choices: &[(String, String)], current: &str, on_pick: OnPick) -> PopupMenu {
    if choices.len() > 16 {
        menu = menu.scrollable(true).max_h(px(420.));
    }
    for (id, name) in choices {
        if id == "-" {
            menu = menu.separator();
            continue;
        }
        let (f, id2) = (on_pick.clone(), id.clone());
        menu = menu.item(
            PopupMenuItem::new(name.clone())
                .checked(id == current)
                .on_click(move |_, window, cx| f(id2.clone(), window, cx)),
        );
    }
    menu
}

/// Send: a round button with an arrow.
pub fn send_button(
    enabled: bool,
    tooltip: &str,
    on_click: impl Fn(&ClickEvent, &mut Window, &mut App) + 'static,
) -> AnyElement {
    Button::new("send")
        .primary()
        .small()
        .icon(IconName::ArrowUp)
        .rounded(ButtonRounded::Size(px(14.)))
        .size(px(28.))
        .tooltip(tooltip.to_string())
        .disabled(!enabled)
        .on_click(on_click)
        .into_any_element()
}

/// Dictation: a microphone, red while it listens.
pub fn mic_button(
    listening: bool,
    enabled: bool,
    on_click: impl Fn(&ClickEvent, &mut Window, &mut App) + 'static,
) -> AnyElement {
    let b = Button::new("dictate")
        .small()
        .rounded(ButtonRounded::Size(px(14.)))
        .disabled(!enabled)
        .on_click(on_click);
    if listening {
        b.danger()
            .icon(gpui_kit::assets::IconName::AudioLines)
            .tooltip("Stop dictation")
            .into_any_element()
    } else {
        b.ghost()
            .icon(gpui_kit::assets::IconName::Mic)
            .tooltip("Dictate")
            .into_any_element()
    }
}

/// The icon for a permission mode: open for one that asks nothing.
pub fn mode_icon(mode: &str) -> Icon {
    let m = mode.to_ascii_lowercase();
    if ["bypass", "full", "yolo", "danger"].iter().any(|w| m.contains(w)) {
        Icon::new(gpui_kit::assets::IconName::ShieldOff)
    } else {
        Icon::new(gpui_kit::assets::IconName::ShieldCheck)
    }
}

/// Images on the pasteboard, as a paste takes them.
#[derive(Debug, PartialEq)]
pub enum Pasted {
    Images(Vec<Vec<u8>>),
    Files(Vec<std::path::PathBuf>),
}

/// The images to attach from what was copied: image files, all of them
/// images, or pictures with no text beside them; else none, and the paste
/// is the text's.
pub fn pasted_images(item: &gpui_kit::ClipboardItem) -> Option<Pasted> {
    use gpui_kit::ClipboardEntry;
    let mut images: Vec<Vec<u8>> = Vec::new();
    let mut paths: Vec<std::path::PathBuf> = Vec::new();
    let mut text = false;
    for e in item.entries() {
        match e {
            ClipboardEntry::Image(i) => images.push(i.bytes().to_vec()),
            ClipboardEntry::ExternalPaths(p) => paths.extend(p.paths().iter().cloned()),
            ClipboardEntry::String(s) => text |= !s.text().is_empty(),
        }
    }
    if !paths.is_empty() && paths.iter().all(|p| is_image_path(p)) {
        return Some(Pasted::Files(paths));
    }
    if images.is_empty() || text || !paths.is_empty() {
        return None;
    }
    Some(Pasted::Images(images))
}

pub fn is_image_path(p: &std::path::Path) -> bool {
    matches!(
        p.extension()
            .and_then(|e| e.to_str())
            .map(|e| e.to_ascii_lowercase())
            .as_deref(),
        Some("png" | "jpg" | "jpeg" | "gif" | "webp" | "tiff" | "tif" | "bmp")
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use gpui_kit::{ClipboardEntry, ClipboardItem, ClipboardString, ExternalPaths, Image, ImageFormat};

    fn item(entries: Vec<ClipboardEntry>) -> ClipboardItem {
        ClipboardItem { entries }
    }

    fn picture() -> ClipboardEntry {
        ClipboardEntry::Image(Image::from_bytes(ImageFormat::Png, vec![1, 2, 3]))
    }

    fn files(names: &[&str]) -> ClipboardEntry {
        ClipboardEntry::ExternalPaths(ExternalPaths(names.iter().map(std::path::PathBuf::from).collect()))
    }

    #[test]
    fn a_picture_alone_is_attached() {
        assert_eq!(
            pasted_images(&item(vec![picture()])),
            Some(Pasted::Images(vec![vec![1, 2, 3]]))
        );
    }

    #[test]
    fn text_with_a_picture_is_pasted_as_text() {
        let text = ClipboardEntry::String(ClipboardString::new("hello".into()));
        assert_eq!(pasted_images(&item(vec![text, picture()])), None);
        assert_eq!(
            pasted_images(&item(vec![ClipboardEntry::String(ClipboardString::new("x".into()))])),
            None
        );
    }

    #[test]
    fn image_files_are_attached_and_others_are_not() {
        assert_eq!(
            pasted_images(&item(vec![files(&["/a/shot.PNG", "/a/b.jpg"]), picture()])),
            Some(Pasted::Files(vec!["/a/shot.PNG".into(), "/a/b.jpg".into()]))
        );
        assert_eq!(
            pasted_images(&item(vec![files(&["/a/shot.png", "/a/notes.txt"]), picture()])),
            None
        );
    }
}
