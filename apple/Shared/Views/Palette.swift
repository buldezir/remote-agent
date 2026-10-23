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
    /// The highlight over selected text in replies: the accent, see-through, so
    /// it shows on code blocks and inline code too. The Mac's own colour, the
    /// system's, is all but lost on Latte's crust.
    static let selection = Color(latte: 0x8839ef, frappe: 0xca9ee6, latteAlpha: 0.28, frappeAlpha: 0.4)

    // Text, from the most to the least prominent.
    static let text = Color(latte: 0x4c4f69, frappe: 0xc6d0f5)
    static let subtext = Color(latte: 0x6c6f85, frappe: 0xa5adce)
    /// Subtle text, such as placeholders and times: Overlay 1, as the style guide says.
    static let overlay = Color(latte: 0x8c8fa1, frappe: 0x838ba7)

    // Surfaces. In Frappé, from the lightest to the darkest.
    static let surface1 = Color(latte: 0xbcc0cc, frappe: 0x51576d)
    static let surface0 = Color(latte: 0xccd0da, frappe: 0x414559)
    /// Windows, the transcript and lists.
    static let base = Color(latte: 0xeff1f5, frappe: 0x303446)
    /// Sidebars, grouped forms, the composer, and cards in the transcript.
    static let mantle = Color(latte: 0xe6e9ef, frappe: 0x292c3c)
    /// Code and output, set into a card or the page.
    static let crust = Color(latte: 0xdce0e8, frappe: 0x232634)
    /// A card raised over `base`, such as the Mac's composer: surface0 in
    /// Frappé, and base in Latte, which has nothing lighter. Give it a border.
    static let raised = Color(latte: 0xeff1f5, frappe: 0x414559)

    /// A terminal's colours, as fixed values: a terminal view takes them
    /// rather than colours that follow the appearance. Catppuccin's own
    /// terminal themes, on the transcript's background.
    struct Terminal {
        var ansi: [UInt32]  // the 16 ANSI colours: black, red, green, yellow, blue, magenta, cyan, white, then bright
        var background, foreground, cursor, selection: UInt32
    }

    static func terminal(dark: Bool) -> Terminal {
        dark
            ? Terminal(ansi: [0x51576d, 0xe78284, 0xa6d189, 0xe5c890, 0x8caaee, 0xf4b8e4, 0x81c8be, 0xb5bfe2,
                              0x626880, 0xe78284, 0xa6d189, 0xe5c890, 0x8caaee, 0xf4b8e4, 0x81c8be, 0xa5adce],
                       background: 0x303446, foreground: 0xc6d0f5, cursor: 0xf2d5cf, selection: 0x626880)
            : Terminal(ansi: [0x5c5f77, 0xd20f39, 0x40a02b, 0xdf8e1d, 0x1e66f5, 0xea76cb, 0x179299, 0xacb0be,
                              0x6c6f85, 0xd20f39, 0x40a02b, 0xdf8e1d, 0x1e66f5, 0xea76cb, 0x179299, 0xbcc0cc],
                       background: 0xeff1f5, foreground: 0x4c4f69, cursor: 0xdc8a78, selection: 0xacb0be)
    }
}

private extension Color {
    /// A colour that follows the appearance: `latte` in light mode, `frappe` in dark.
    init(latte: UInt32, frappe: UInt32, latteAlpha: CGFloat = 1, frappeAlpha: CGFloat = 1) {
        #if os(iOS)
        self.init(uiColor: UIColor { traits in
            traits.userInterfaceStyle == .dark ? UIColor(rgb: frappe, alpha: frappeAlpha) : UIColor(rgb: latte, alpha: latteAlpha)
        })
        #else
        self.init(nsColor: NSColor(name: nil) { appearance in
            appearance.bestMatch(from: [.aqua, .darkAqua]) == .darkAqua
                ? NSColor(rgb: frappe, alpha: frappeAlpha) : NSColor(rgb: latte, alpha: latteAlpha)
        })
        #endif
    }
}

#if os(iOS)
extension UIColor {
    convenience init(rgb: UInt32, alpha: CGFloat = 1) {
        self.init(red: CGFloat(rgb >> 16 & 0xff) / 255, green: CGFloat(rgb >> 8 & 0xff) / 255,
                  blue: CGFloat(rgb & 0xff) / 255, alpha: alpha)
    }
}
#else
extension NSColor {
    convenience init(rgb: UInt32, alpha: CGFloat = 1) {
        self.init(srgbRed: CGFloat(rgb >> 16 & 0xff) / 255, green: CGFloat(rgb >> 8 & 0xff) / 255,
                  blue: CGFloat(rgb & 0xff) / 255, alpha: alpha)
    }
}
#endif

/// Light or dark, from Settings, or as the system is: Latte or Frappé.
enum Appearance: String, CaseIterable, Identifiable {
    case system, dark, light

    static let key = "appearance"

    var id: Self { self }

    var title: String {
        switch self {
        case .system: "System"
        case .dark: "Dark"
        case .light: "Light"
        }
    }
}

extension View {
    /// The app's look, for a window and again for each sheet: the appearance,
    /// text sizes and fonts from Settings, and the palette's accent and sheet
    /// background. iOS doesn't carry the Dynamic Type size into sheets.
    func appStyle() -> some View {
        textSettings()
            .modifier(AppAppearance())
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

private struct AppAppearance: ViewModifier {
    @AppStorage(Appearance.key) private var appearance = Appearance.system

    // The whole app's rather than `preferredColorScheme`, which leaves an
    // open sheet in the old scheme when set back to nil. This way sheets,
    // menus and alerts follow too, and the Mac's Settings window.
    func body(content: Content) -> some View {
        content.onChange(of: appearance, initial: true) { _, appearance in
            #if os(macOS)
            NSApp.appearance = switch appearance {
            case .system: nil
            case .dark: NSAppearance(named: .darkAqua)
            case .light: NSAppearance(named: .aqua)
            }
            #else
            let style: UIUserInterfaceStyle = switch appearance {
            case .system: .unspecified
            case .dark: .dark
            case .light: .light
            }
            for case let scene as UIWindowScene in UIApplication.shared.connectedScenes {
                for window in scene.windows { window.overrideUserInterfaceStyle = style }
            }
            #endif
        }
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
