//! What every view reaches: the app's prefs, and the window's main view,
//! through which a view opens a session or a dialog elsewhere in the window.

use gpui_kit::{App, Global, WeakEntity, px};

use crate::store::prefs::{Prefs, PrefsStore};
use crate::ui::app::AppView;

pub struct AppPrefs(pub PrefsStore);

impl Global for AppPrefs {}

pub fn prefs(cx: &App) -> &Prefs {
    &cx.global::<AppPrefs>().0.prefs
}

/// Changes the prefs, saves them, and redraws.
pub fn update_prefs(cx: &mut App, f: impl FnOnce(&mut Prefs)) {
    cx.global_mut::<AppPrefs>().0.update(f);
    apply_text_size(cx);
    cx.refresh_windows();
}

/// The interface's text size, and code's font and size, from the prefs. Code
/// in replies follows the theme's monospaced font, which the kit hands its
/// Markdown through the base theme.
pub fn apply_text_size(cx: &mut App) {
    use gpui_kit::component::Theme;
    let size = prefs(cx).text_size.clamp(10., 20.);
    crate::ui::fonts::apply(cx);
    let code_size = crate::ui::fonts::code_size();
    let code = crate::ui::fonts::code();
    let theme = Theme::global_mut(cx);
    let changed =
        theme.font_size != px(size) || theme.mono_font_size != px(code_size) || theme.mono_font_family != code;
    if changed {
        theme.font_size = px(size);
        theme.mono_font_size = px(code_size);
        theme.mono_font_family = code;
        Theme::sync_base(cx);
    }
}

/// The window's main view.
pub struct AppHandle(pub WeakEntity<AppView>);

impl Global for AppHandle {}

pub fn app_view(cx: &App) -> Option<gpui_kit::Entity<AppView>> {
    cx.try_global::<AppHandle>().and_then(|h| h.0.upgrade())
}

/// Running from the app bundle, whose Info.plist names why the app asks for
/// the microphone and speech recognition. A bare binary has none, and macOS
/// ends it as it asks, so dictation is offered only in the bundle.
pub fn in_app_bundle() -> bool {
    std::env::current_exe()
        .ok()
        .is_some_and(|p| p.to_string_lossy().contains(".app/Contents/MacOS/"))
}
