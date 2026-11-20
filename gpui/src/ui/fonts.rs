//! The message and code fonts Settings picks, as the Mac app's `FontChoice`.
//!
//! GPUI panics on the first line it lays out in a family it can't load, so a
//! family the prefs name is used only while it's installed; else the default.
//! GPUI loads fonts by family name, which leaves out the system font's other
//! designs (New York, SF Pro Rounded, SF Mono), so the defaults are the
//! system font and Menlo.

use std::cell::RefCell;
use std::sync::OnceLock;

use core_foundation::base::TCFType;
use core_foundation::dictionary::CFDictionary;
use core_foundation::string::CFString;
use core_text::font_descriptor::{SymbolicTraitAccessors, kCTFontFamilyNameAttribute};
use gpui_kit::{App, SharedString};

use crate::ui::state::prefs;

/// The interface's font, and the messages' unless Settings picks another.
pub const SYSTEM: &str = ".SystemUIFont";
/// Code's font unless Settings picks another: every Mac has it.
pub const MONO: &str = "Menlo";

/// The font families installed on this Mac that GPUI can load, in
/// alphabetical order. Listing them takes a while, so it's done once.
pub fn installed_families(cx: &App) -> &'static [String] {
    static NAMES: OnceLock<Vec<String>> = OnceLock::new();
    NAMES.get_or_init(|| {
        let mut names: Vec<String> = cx
            .text_system()
            .all_font_names()
            .into_iter()
            .filter(|n| !n.is_empty() && !n.starts_with('.'))
            .collect();
        names.sort_by_key(|n| n.to_lowercase());
        names.dedup();
        names
    })
}

/// The installed families whose faces are fixed-width.
pub fn monospaced_families(cx: &App) -> &'static [String] {
    static NAMES: OnceLock<Vec<String>> = OnceLock::new();
    NAMES.get_or_init(|| {
        installed_families(cx)
            .iter()
            .filter(|n| is_monospaced(n))
            .cloned()
            .collect()
    })
}

fn is_monospaced(family: &str) -> bool {
    let key = unsafe { CFString::wrap_under_get_rule(kCTFontFamilyNameAttribute) };
    let attrs = CFDictionary::from_CFType_pairs(&[(key, CFString::new(family).as_CFType())]);
    let desc = core_text::font_descriptor::new_from_attributes(&attrs);
    let font = core_text::font::new_from_descriptor(&desc, 12.);
    // CoreText falls back to another family when it has none of that name.
    font.family_name() == family && font.symbolic_traits().is_monospace()
}

/// `stored` when it names an installed family, else `fallback`.
fn resolve(stored: &str, fallback: &'static str, families: &[String]) -> SharedString {
    if !stored.is_empty() && families.iter().any(|f| f == stored) {
        SharedString::from(stored.to_string())
    } else {
        SharedString::new_static(fallback)
    }
}

/// Prompts and replies.
pub fn message_font(cx: &App) -> SharedString {
    resolve(&prefs(cx).message_font, SYSTEM, installed_families(cx))
}

/// Code in replies, tool calls, diffs and the terminal.
pub fn code_font(cx: &App) -> SharedString {
    resolve(&prefs(cx).code_font, MONO, installed_families(cx))
}

thread_local! {
    /// The fonts as last applied, for views that draw without the app at hand.
    static CURRENT: RefCell<(SharedString, SharedString)> =
        const { RefCell::new((SharedString::new_static(SYSTEM), SharedString::new_static(MONO))) };
}

/// Takes the prefs' fonts for `message()` and `code()`.
pub fn apply(cx: &App) {
    let fonts = (message_font(cx), code_font(cx));
    CURRENT.with(|c| *c.borrow_mut() = fonts);
}

/// The message font, as last applied.
pub fn message() -> SharedString {
    CURRENT.with(|c| c.borrow().0.clone())
}

/// The code font, as last applied.
pub fn code() -> SharedString {
    CURRENT.with(|c| c.borrow().1.clone())
}

/// The setting as the picker shows it: empty for the default, or for a
/// family that's no longer installed.
pub fn shown(stored: &str, families: &[String]) -> String {
    if families.iter().any(|f| f == stored) {
        stored.to_string()
    } else {
        String::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn falls_back_when_a_family_is_gone() {
        let families = vec!["Georgia".to_string(), "Menlo".to_string()];
        assert_eq!(resolve("Georgia", SYSTEM, &families).as_ref(), "Georgia");
        assert_eq!(resolve("", SYSTEM, &families).as_ref(), SYSTEM);
        assert_eq!(resolve("Gone Sans", MONO, &families).as_ref(), MONO);
        assert_eq!(shown("Gone Sans", &families), "");
        assert_eq!(shown("Georgia", &families), "Georgia");
    }

    #[test]
    fn tells_fixed_width_families() {
        assert!(is_monospaced("Menlo"));
        assert!(!is_monospaced("Helvetica"));
        assert!(!is_monospaced("No Such Family Here"));
    }
}
