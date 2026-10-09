import SwiftUI
#if os(macOS)
import AppKit
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

extension EnvironmentValues {
    /// The size of prompts and replies in the transcript.
    @Entry var messageTextSize: CGFloat = Metrics.textSize
    #if os(macOS)
    /// The interface text size as a multiple of the system's.
    @Entry var textScale: CGFloat = 1
    #endif
}

extension View {
    /// Applies the text sizes from Settings to a window, and to each sheet:
    /// iOS doesn't carry the Dynamic Type size into sheets.
    func appTextSizes() -> some View {
        modifier(AppTextSizes())
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

private struct AppTextSizes: ViewModifier {
    @AppStorage(TextSize.interfaceKey) private var interface = TextSize.defaultInterface
    @AppStorage(TextSize.messagesKey) private var messages = TextSize.defaultMessages
    #if os(iOS)
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
        #else
        let size = TextSize.dynamicTypeSizes[step]
        content
            .dynamicTypeSize(followsSystem ? DynamicTypeSize.xSmall...DynamicTypeSize.accessibility5 : size...size)
            .environment(\.messageTextSize, TextSize.messages(messages))
        #endif
    }
}

private struct ScaledFont: ViewModifier {
    let style: Font.TextStyle
    let weight: Font.Weight?
    let design: Font.Design?
    #if os(macOS)
    @Environment(\.textScale) private var scale
    #endif

    func body(content: Content) -> some View {
        #if os(macOS)
        // The Mac's headline is bold body text, and its caption2 is medium.
        let weight = weight ?? (style == .headline ? .bold : style == .caption2 ? .medium : nil)
        content.font(.system(size: (TextSize.styleSizes[style] ?? NSFont.systemFontSize) * scale, weight: weight, design: design))
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
    #endif

    init(size: CGFloat, design: Font.Design?) {
        _size = ScaledMetric(wrappedValue: size, relativeTo: .body)
        self.design = design
    }

    func body(content: Content) -> some View {
        #if os(macOS)
        content.font(.system(size: size * scale, design: design))
        #else
        content.font(.system(size: size, design: design))
        #endif
    }
}
