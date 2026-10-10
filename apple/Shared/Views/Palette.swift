import SwiftUI
#if os(iOS)
import UIKit
#else
import AppKit
#endif

/// The app's colours: Catppuccin (catppuccin.com/palette), Frappé in dark
/// mode and Latte in light mode. Views take their colours from here rather
/// than the system's `.red`, `.green` and backgrounds.
enum Palette {
    // Accents. Catppuccin's style guide: errors red, warnings yellow,
    // success green, links blue.
    static let red = Color(latte: 0xd20f39, frappe: 0xe78284)
    static let peach = Color(latte: 0xfe640b, frappe: 0xef9f76)
    static let yellow = Color(latte: 0xdf8e1d, frappe: 0xe5c890)
    static let green = Color(latte: 0x40a02b, frappe: 0xa6d189)
    static let blue = Color(latte: 0x1e66f5, frappe: 0x8caaee)
    static let mauve = Color(latte: 0x8839ef, frappe: 0xca9ee6)

    /// Buttons, controls and the user's messages. AccentColor in the asset
    /// catalog matches it, for the controls SwiftUI leaves to the system.
    static let accent = mauve
    /// Text on an accent colour.
    static let onAccent = base

    // Text, from the most to the least prominent.
    static let text = Color(latte: 0x4c4f69, frappe: 0xc6d0f5)
    static let subtext = Color(latte: 0x6c6f85, frappe: 0xa5adce)
    static let overlay = Color(latte: 0x9ca0b0, frappe: 0x737994)

    // Surfaces. In Frappé, from the lightest to the darkest.
    static let surface1 = Color(latte: 0xbcc0cc, frappe: 0x51576d)
    static let surface0 = Color(latte: 0xccd0da, frappe: 0x414559)
    /// Windows, the transcript and lists.
    static let base = Color(latte: 0xeff1f5, frappe: 0x303446)
    /// Sidebars, grouped forms, the composer, and cards in the transcript.
    static let mantle = Color(latte: 0xe6e9ef, frappe: 0x292c3c)
    /// Code and output, set into a card or the page.
    static let crust = Color(latte: 0xdce0e8, frappe: 0x232634)
}

private extension Color {
    /// A colour that follows the appearance: `latte` in light mode, `frappe` in dark.
    init(latte: UInt32, frappe: UInt32) {
        #if os(iOS)
        self.init(uiColor: UIColor { traits in
            UIColor(rgb: traits.userInterfaceStyle == .dark ? frappe : latte)
        })
        #else
        self.init(nsColor: NSColor(name: nil) { appearance in
            NSColor(rgb: appearance.bestMatch(from: [.aqua, .darkAqua]) == .darkAqua ? frappe : latte)
        })
        #endif
    }
}

#if os(iOS)
private extension UIColor {
    convenience init(rgb: UInt32) {
        self.init(red: CGFloat(rgb >> 16 & 0xff) / 255, green: CGFloat(rgb >> 8 & 0xff) / 255,
                  blue: CGFloat(rgb & 0xff) / 255, alpha: 1)
    }
}
#else
private extension NSColor {
    convenience init(rgb: UInt32) {
        self.init(srgbRed: CGFloat(rgb >> 16 & 0xff) / 255, green: CGFloat(rgb >> 8 & 0xff) / 255,
                  blue: CGFloat(rgb & 0xff) / 255, alpha: 1)
    }
}
#endif

extension View {
    /// The app's look, for a window and again for each sheet: the text sizes
    /// and fonts from Settings, and the palette's accent and sheet background.
    /// iOS doesn't carry the Dynamic Type size into sheets.
    func appStyle() -> some View {
        textSettings()
            .tint(Palette.accent)
            .presentationBackground(Palette.mantle)
    }

    /// A list or form on a palette colour rather than the system's background.
    func listBackground(_ color: Color) -> some View {
        scrollContentBackground(.hidden).background(color)
    }

    /// The rows of a list or form on a palette colour: iOS otherwise draws
    /// each row on the system's background. A list ignores a row background
    /// set on itself, so this goes on the rows, or on a Group around them.
    func paletteRows(_ color: Color = Palette.base) -> some View {
        #if os(iOS)
        listRowBackground(color)
        #else
        self
        #endif
    }

    /// Text in the palette's colours: the primary, secondary and tertiary
    /// styles become text, subtext and overlay. For the transcript and list
    /// rows, not whole screens: it also overrides the tint of buttons inside.
    func paletteText() -> some View {
        modifier(PaletteText())
    }
}

private struct PaletteText: ViewModifier {
    @Environment(\.backgroundProminence) private var prominence

    func body(content: Content) -> some View {
        // A selected row keeps the system's styles, which contrast with the selection.
        let system = prominence == .increased
        content.foregroundStyle(
            system ? AnyShapeStyle(.primary) : AnyShapeStyle(Palette.text),
            system ? AnyShapeStyle(.secondary) : AnyShapeStyle(Palette.subtext),
            system ? AnyShapeStyle(.tertiary) : AnyShapeStyle(Palette.overlay))
    }
}
