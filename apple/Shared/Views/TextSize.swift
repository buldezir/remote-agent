import CoreText
import SwiftUI
#if os(macOS)
import AppKit
#else
import UIKit
#endif

/// The text sizes chosen in Settings: one for the interface, one for messages.
///
/// iOS scales the interface with Dynamic Type. The Mac has no Dynamic Type, so
/// there the views set their fonts with `scaledFont` rather than `font`, and
/// the setting scales those; text that sets no font gets a scaled default.
enum TextSize {
    /// A step of `interfaceSizes`.
    static let interfaceKey = "interfaceTextSize"
    /// Points.
    static let messagesKey = "messageTextSize"
    #if os(iOS)
    /// The interface follows the system's text size, and `interfaceKey` is unused.
    static let followsSystemKey = "interfaceTextSizeFollowsSystem"
    #endif

    /// The body text size at each step of the interface setting, and the
    /// step of the system's size.
    #if os(macOS)
    static let interfaceSizes: [CGFloat] = [11, 12, 13, 14, 15, 16, 18]
    static let defaultInterface = 2
    #else
    static let interfaceSizes: [CGFloat] = [14, 15, 16, 17, 19, 21, 23]
    static let defaultInterface = 3
    /// The Dynamic Type size that gives each of `interfaceSizes`.
    static let dynamicTypeSizes: [DynamicTypeSize] = [.xSmall, .small, .medium, .large, .xLarge, .xxLarge, .xxxLarge]
    #endif

    #if os(macOS)
    static let messageSizes: ClosedRange<Double> = 10...24
    #else
    static let messageSizes: ClosedRange<Double> = 12...28
    #endif
    static let defaultMessages = Double(Metrics.textSize)

    static func step(_ stored: Int) -> Int { min(max(stored, 0), interfaceSizes.count - 1) }
    static func messages(_ stored: Double) -> CGFloat { CGFloat(min(max(stored, messageSizes.lowerBound), messageSizes.upperBound)) }

    #if os(macOS)
    /// The Mac's size for each text style.
    static let styleSizes: [Font.TextStyle: CGFloat] = {
        let styles: [(Font.TextStyle, NSFont.TextStyle)] = [
            (.largeTitle, .largeTitle), (.title, .title1), (.title2, .title2), (.title3, .title3),
            (.headline, .headline), (.subheadline, .subheadline), (.body, .body), (.callout, .callout),
            (.footnote, .footnote), (.caption, .caption1), (.caption2, .caption2),
        ]
        return Dictionary(uniqueKeysWithValues: styles.map { ($0, NSFont.preferredFont(forTextStyle: $1).pointSize) })
    }()
    #endif
}

/// A typeface for messages or code. The Mac's Settings offers a choice of
/// both; iOS uses the system font.
enum FontChoice: Hashable {
    /// The system font in one of its designs: SF Pro, New York, SF Pro Rounded or SF Mono.
    case system(Font.Design)
    /// An installed font family, by name.
    case family(String)

    static let message = FontChoice.system(.default)
    static let code = FontChoice.system(.monospaced)

    func font(size: CGFloat) -> Font {
        switch self {
        case .system(let design): .system(size: size, design: design)
        case .family(let name): .custom(name, fixedSize: size)
        }
    }

    /// The size of `code` in text set in this font, as a fraction of the
    /// text's: the size at which their lowercase letters stand as tall, so
    /// neither looks smaller. 1 when they are the same font.
    @MainActor func codeScale(_ code: FontChoice) -> CGFloat {
        guard self != code else { return 1 }
        if let scale = Self.codeScales[[self, code]] { return scale }
        var scale: CGFloat = 1
        if let text = xHeight, let codeX = code.xHeight, text > 0, codeX > 0 {
            scale = min(max(text / codeX, 0.85), 1.15)
        }
        Self.codeScales[[self, code]] = scale
        return scale
    }

    @MainActor private static var codeScales: [[FontChoice]: CGFloat] = [:]

    /// The height of a lowercase x at 100 pt, from the glyph: some fonts'
    /// x-height metric is wrong.
    private var xHeight: CGFloat? {
        guard let font = platformFont(size: 100) else { return nil }
        var char: UniChar = 0x78, glyph: CGGlyph = 0
        guard CTFontGetGlyphsForCharacters(font, &char, &glyph, 1) else { return nil }
        return CTFontGetBoundingRectsForGlyphs(font, .default, &glyph, nil, 1).height
    }

    private func platformFont(size: CGFloat) -> CTFont? {
        #if os(macOS)
        switch self {
        case .system(let design):
            let system = NSFont.systemFont(ofSize: size)
            let font = system.fontDescriptor.withDesign(design.systemDesign).flatMap { NSFont(descriptor: $0, size: size) }
            return (font ?? system) as CTFont
        case .family(let name):
            return NSFontManager.shared.font(withFamily: name, traits: [], weight: 5, size: size).map { $0 as CTFont }
        }
        #else
        switch self {
        case .system(let design):
            let system = UIFont.systemFont(ofSize: size)
            let font = system.fontDescriptor.withDesign(design.systemDesign).map { UIFont(descriptor: $0, size: size) }
            return (font ?? system) as CTFont
        case .family(let name):
            return UIFont(descriptor: UIFontDescriptor(fontAttributes: [.family: name]), size: size) as CTFont
        }
        #endif
    }

    #if os(macOS)
    /// Prompts and replies.
    static let messageKey = "messageFont"
    /// Code in messages, tool calls and diffs.
    static let codeKey = "codeFont"

    /// The setting as stored: empty for `fallback`, "system:serif" and the like
    /// for the system font's designs, or a family name. A family that is no
    /// longer installed gives `fallback`.
    @MainActor init(stored: String, fallback: FontChoice) {
        switch stored {
        case "system:default": self = .system(.default)
        case "system:serif": self = .system(.serif)
        case "system:rounded": self = .system(.rounded)
        case "system:monospaced": self = .system(.monospaced)
        case let name where !name.isEmpty && Self.installedFamilies.contains(name): self = .family(name)
        default: self = fallback
        }
    }

    func stored(fallback: FontChoice) -> String {
        switch self {
        case fallback: ""
        case .system(.serif): "system:serif"
        case .system(.rounded): "system:rounded"
        case .system(.monospaced): "system:monospaced"
        case .system: "system:default"
        case .family(let name): name
        }
    }

    /// The font families installed on this Mac, in alphabetical order.
    @MainActor static let installedFamilies = NSFontManager.shared.availableFontFamilies
        .sorted { $0.localizedStandardCompare($1) == .orderedAscending }

    /// The installed families with fixed-width faces.
    @MainActor static let monospacedFamilies = Set((NSFontManager.shared.availableFontNames(with: .fixedPitchFontMask) ?? [])
        .compactMap { NSFont(name: $0, size: 12)?.familyName })
        .sorted { $0.localizedStandardCompare($1) == .orderedAscending }
    #endif
}

private extension Font.Design {
    #if os(macOS)
    var systemDesign: NSFontDescriptor.SystemDesign {
        switch self {
        case .serif: .serif
        case .rounded: .rounded
        case .monospaced: .monospaced
        default: .default
        }
    }
    #else
    var systemDesign: UIFontDescriptor.SystemDesign {
        switch self {
        case .serif: .serif
        case .rounded: .rounded
        case .monospaced: .monospaced
        default: .default
        }
    }
    #endif
}

extension EnvironmentValues {
    /// The size of prompts and replies in the transcript.
    @Entry var messageTextSize: CGFloat = Metrics.textSize
    /// The typeface of prompts and replies.
    @Entry var messageFont: FontChoice = .message
    /// The typeface of code: in messages, tool calls and diffs.
    @Entry var codeFont: FontChoice = .code
    #if os(macOS)
    /// The interface text size as a multiple of the system's.
    @Entry var textScale: CGFloat = 1
    #endif
}

extension View {
    /// Applies the text sizes and fonts from Settings. Part of `appStyle()`.
    func textSettings() -> some View {
        modifier(AppTextSettings())
    }

    /// Prompts and replies: the message font and size from Settings.
    func messageFont() -> some View {
        modifier(MessageFontModifier())
    }

    /// A text style that follows the interface text size. Use it instead of
    /// `font(.footnote)` and the like: iOS scales those itself, the Mac doesn't.
    func scaledFont(_ style: Font.TextStyle, weight: Font.Weight? = nil, design: Font.Design? = nil) -> some View {
        modifier(ScaledFont(style: style, weight: weight, design: design))
    }

    /// A point size that follows the interface text size, as body text does.
    func scaledFont(size: CGFloat, design: Font.Design? = nil) -> some View {
        modifier(ScaledSizeFont(size: size, design: design))
    }

    /// Form section headers. On the Mac, the scaled default font would
    /// otherwise make them regular body text.
    func formHeaderFont() -> some View {
        #if os(macOS)
        scaledFont(.headline)
        #else
        self
        #endif
    }

    /// Form section footers, like `formHeaderFont`.
    func formFooterFont() -> some View {
        #if os(macOS)
        scaledFont(.callout)
        #else
        self
        #endif
    }

    /// List section headers and footers, the sidebar's too. Mac lists keep
    /// their own fonts for rows, headers and footers, so these scale only with
    /// a font set on each: this one, or `scaledFont(.body)` for a row.
    func listHeaderFont() -> some View {
        #if os(macOS)
        scaledFont(.subheadline, weight: .bold)
        #else
        self
        #endif
    }
}

private struct AppTextSettings: ViewModifier {
    @AppStorage(TextSize.interfaceKey) private var interface = TextSize.defaultInterface
    @AppStorage(TextSize.messagesKey) private var messages = TextSize.defaultMessages
    #if os(macOS)
    @AppStorage(FontChoice.messageKey) private var messageFont = ""
    @AppStorage(FontChoice.codeKey) private var codeFont = ""
    #else
    @AppStorage(TextSize.followsSystemKey) private var followsSystem = true
    #endif

    func body(content: Content) -> some View {
        let step = TextSize.step(interface)
        #if os(macOS)
        let scale = TextSize.interfaceSizes[step] / TextSize.interfaceSizes[TextSize.defaultInterface]
        content
            // For text that sets no font: form rows, fields and labels. Left
            // alone at the system's size, so containers keep their own fonts.
            .font(scale == 1 ? nil : .system(size: NSFont.systemFontSize * scale))
            .environment(\.textScale, scale)
            .environment(\.messageTextSize, TextSize.messages(messages))
            .environment(\.messageFont, FontChoice(stored: messageFont, fallback: .message))
            .environment(\.codeFont, FontChoice(stored: codeFont, fallback: .code))
        #else
        let size = TextSize.dynamicTypeSizes[step]
        content
            .dynamicTypeSize(followsSystem ? DynamicTypeSize.xSmall...DynamicTypeSize.accessibility5 : size...size)
            .environment(\.messageTextSize, TextSize.messages(messages))
        #endif
    }
}

private struct MessageFontModifier: ViewModifier {
    @Environment(\.messageTextSize) private var size
    @Environment(\.messageFont) private var font

    func body(content: Content) -> some View {
        content.font(font.font(size: size))
    }
}

private struct ScaledFont: ViewModifier {
    let style: Font.TextStyle
    let weight: Font.Weight?
    let design: Font.Design?
    #if os(macOS)
    @Environment(\.textScale) private var scale
    @Environment(\.codeFont) private var codeFont
    #endif

    func body(content: Content) -> some View {
        #if os(macOS)
        // The Mac's headline is bold body text, and its caption2 is medium.
        let weight = weight ?? (style == .headline ? .bold : style == .caption2 ? .medium : nil)
        let size = (TextSize.styleSizes[style] ?? NSFont.systemFontSize) * scale
        content.font(codeFamily(design, codeFont, size: size, weight: weight)
            ?? .system(size: size, weight: weight, design: design))
        #else
        content.font(.system(style, design: design, weight: weight))
        #endif
    }
}

private struct ScaledSizeFont: ViewModifier {
    @ScaledMetric private var size: CGFloat
    let design: Font.Design?
    #if os(macOS)
    @Environment(\.textScale) private var scale
    @Environment(\.codeFont) private var codeFont
    #endif

    init(size: CGFloat, design: Font.Design?) {
        _size = ScaledMetric(wrappedValue: size, relativeTo: .body)
        self.design = design
    }

    func body(content: Content) -> some View {
        #if os(macOS)
        content.font(codeFamily(design, codeFont, size: size * scale, weight: nil)
            ?? .system(size: size * scale, design: design))
        #else
        content.font(.system(size: size, design: design))
        #endif
    }
}

#if os(macOS)
/// Monospaced text in the code font, when Settings names a family for it.
private func codeFamily(_ design: Font.Design?, _ codeFont: FontChoice, size: CGFloat, weight: Font.Weight?) -> Font? {
    guard design == .monospaced, case .family = codeFont else { return nil }
    let font = codeFont.font(size: size)
    return weight.map { font.weight($0) } ?? font
}
#endif
