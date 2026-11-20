//! The app's colours: Catppuccin, Frappé in dark mode and Latte in light
//! mode, as `apple/Shared/Views/Palette.swift` and `web/src/ui/styles.css`
//! have them. The kit's components take theirs from `assets/theme.json`,
//! made from the same values; this client's own views take them from here.

use gpui_kit::component::{ActiveTheme as _, Theme, ThemeMode, ThemeRegistry};
use gpui_kit::{App, Hsla, Window, px, rgb, rgba};

#[derive(Clone, Copy)]
#[allow(dead_code)] // the whole palette, as the other clients have it
pub struct Palette {
    pub red: Hsla,
    pub peach: Hsla,
    pub yellow: Hsla,
    pub green: Hsla,
    pub blue: Hsla,
    pub mauve: Hsla,
    /// Buttons, controls and the user's messages.
    pub accent: Hsla,
    /// Text on an accent colour.
    pub on_accent: Hsla,
    pub text: Hsla,
    pub subtext: Hsla,
    /// Subtle text, such as placeholders and times.
    pub overlay: Hsla,
    pub surface1: Hsla,
    pub surface0: Hsla,
    /// Windows, the transcript and lists.
    pub base: Hsla,
    /// Sidebars, the composer, and cards in the transcript.
    pub mantle: Hsla,
    /// Code and output, set into a card or the page.
    pub crust: Hsla,
    /// The user's messages: the accent, faint.
    pub user_bubble: Hsla,
}

impl Palette {
    pub fn latte() -> Self {
        Self {
            red: rgb(0xd20f39).into(),
            peach: rgb(0xfe640b).into(),
            yellow: rgb(0xdf8e1d).into(),
            green: rgb(0x40a02b).into(),
            blue: rgb(0x1e66f5).into(),
            mauve: rgb(0x8839ef).into(),
            accent: rgb(0x8839ef).into(),
            on_accent: rgb(0xeff1f5).into(),
            text: rgb(0x4c4f69).into(),
            subtext: rgb(0x6c6f85).into(),
            overlay: rgb(0x8c8fa1).into(),
            surface1: rgb(0xbcc0cc).into(),
            surface0: rgb(0xccd0da).into(),
            base: rgb(0xeff1f5).into(),
            mantle: rgb(0xe6e9ef).into(),
            crust: rgb(0xdce0e8).into(),
            user_bubble: rgba(0x8839ef1f).into(),
        }
    }

    pub fn frappe() -> Self {
        Self {
            red: rgb(0xe78284).into(),
            peach: rgb(0xef9f76).into(),
            yellow: rgb(0xe5c890).into(),
            green: rgb(0xa6d189).into(),
            blue: rgb(0x8caaee).into(),
            mauve: rgb(0xca9ee6).into(),
            accent: rgb(0xca9ee6).into(),
            on_accent: rgb(0x303446).into(),
            text: rgb(0xc6d0f5).into(),
            subtext: rgb(0xa5adce).into(),
            overlay: rgb(0x838ba7).into(),
            surface1: rgb(0x51576d).into(),
            surface0: rgb(0x414559).into(),
            base: rgb(0x303446).into(),
            mantle: rgb(0x292c3c).into(),
            crust: rgb(0x232634).into(),
            user_bubble: rgba(0xca9ee62e).into(),
        }
    }

    pub fn of(cx: &App) -> Self {
        if cx.theme().is_dark() {
            Self::frappe()
        } else {
            Self::latte()
        }
    }
}

/// Registers the two themes and picks the one for the system's appearance.
pub fn install_theme(cx: &mut App) {
    if let Err(e) = ThemeRegistry::global_mut(cx).load_themes_from_str(include_str!("../../assets/theme.json")) {
        eprintln!("theme: {e}");
        return;
    }
    let themes = ThemeRegistry::global(cx).themes();
    let (Some(light), Some(dark)) = (
        themes.get("Remote Agent Latte").cloned(),
        themes.get("Remote Agent Frappé").cloned(),
    ) else {
        return;
    };
    Theme::update(cx, |theme| {
        theme.light_theme = light;
        theme.dark_theme = dark;
        theme.font_size = px(14.);
        theme.mono_font_size = px(12.5);
    });
    let mode = forced_mode().unwrap_or_else(|| cx.window_appearance().into());
    Theme::change(mode, None, cx);
}

/// `RA_GPUI_APPEARANCE=light|dark` tries a theme without changing the Mac's appearance.
fn forced_mode() -> Option<ThemeMode> {
    match std::env::var("RA_GPUI_APPEARANCE").ok()?.as_str() {
        "light" => Some(ThemeMode::Light),
        "dark" => Some(ThemeMode::Dark),
        _ => None,
    }
}

/// Follows the system's appearance as it changes.
pub fn follow_appearance(window: &mut Window, cx: &mut App) {
    let mode = forced_mode().unwrap_or_else(|| window.appearance().into());
    if cx.theme().mode != mode {
        Theme::change(mode, None, cx);
    }
}
