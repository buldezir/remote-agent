import CoreText
import SwiftUI
import UIKit

/// The text sizes chosen in Settings: one for the interface, one for messages.
/// The interface follows Dynamic Type, at the system's size or a size of its own.
enum TextSize {
    /// A step of `interfaceSizes`.
    static let interfaceKey = "interfaceTextSize"
    /// Points.
    static let messagesKey = "messageTextSize"
    /// The interface follows the system's text size, and `interfaceKey` is unused.
    static let followsSystemKey = "interfaceTextSizeFollowsSystem"

    /// The body text size at each step of the interface setting, and the
    /// step of the system's size.
    static let interfaceSizes: [CGFloat] = [14, 15, 16, 17, 19, 21, 23]
    static let defaultInterface = 3
    /// The Dynamic Type size that gives each of `interfaceSizes`.
    static let dynamicTypeSizes: [DynamicTypeSize] = [.xSmall, .small, .medium, .large, .xLarge, .xxLarge, .xxxLarge]

    static let messageSizes: ClosedRange<Double> = 12...28
    static let defaultMessages = Double(Metrics.textSize)

    static func step(_ stored: Int) -> Int { min(max(stored, 0), interfaceSizes.count - 1) }
    static func messages(_ stored: Double) -> CGFloat { CGFloat(min(max(stored, messageSizes.lowerBound), messageSizes.upperBound)) }

    /// The size of code in messages, as a fraction of the text's: the size at
    /// which the system's monospaced lowercase letters stand as tall as its
    /// own, so neither looks smaller.
    @MainActor static let codeScale: CGFloat = {
        guard let text = xHeight(.default), let code = xHeight(.monospaced), text > 0, code > 0 else { return 1 }
        return min(max(text / code, 0.85), 1.15)
    }()

    /// The height of a lowercase x at 100 pt in the system font's design, from
    /// the glyph: some fonts' x-height metric is wrong.
    private static func xHeight(_ design: UIFontDescriptor.SystemDesign) -> CGFloat? {
        let system = UIFont.systemFont(ofSize: 100)
        let font = (system.fontDescriptor.withDesign(design).map { UIFont(descriptor: $0, size: 100) } ?? system) as CTFont
        var char: UniChar = 0x78, glyph: CGGlyph = 0
        guard CTFontGetGlyphsForCharacters(font, &char, &glyph, 1) else { return nil }
        return CTFontGetBoundingRectsForGlyphs(font, .default, &glyph, nil, 1).height
    }
}

extension EnvironmentValues {
    /// The size of prompts and replies in the transcript.
    @Entry var messageTextSize: CGFloat = Metrics.textSize
}

extension View {
    /// Applies the text sizes and fonts from Settings. Part of `appStyle()`.
    func textSettings() -> some View {
        modifier(AppTextSettings())
    }

    /// Prompts and replies: the message size from Settings.
    func messageFont() -> some View {
        modifier(MessageFontModifier())
    }

    /// A text style that follows the interface text size.
    func scaledFont(_ style: Font.TextStyle, weight: Font.Weight? = nil, design: Font.Design? = nil) -> some View {
        modifier(ScaledFont(style: style, weight: weight, design: design))
    }

    /// A size relative to the messages', for what sits among them, such as
    /// tool calls. Monospaced text takes the size code has in messages.
    func messageRelativeFont(_ scale: CGFloat, weight: Font.Weight? = nil, design: Font.Design? = nil) -> some View {
        modifier(MessageRelativeFont(scale: scale, weight: weight, design: design))
    }

    /// A point size that follows the interface text size, as body text does.
    func scaledFont(size: CGFloat, design: Font.Design? = nil) -> some View {
        modifier(ScaledSizeFont(size: size, design: design))
    }
}

private struct AppTextSettings: ViewModifier {
    @AppStorage(TextSize.interfaceKey) private var interface = TextSize.defaultInterface
    @AppStorage(TextSize.messagesKey) private var messages = TextSize.defaultMessages
    @AppStorage(TextSize.followsSystemKey) private var followsSystem = true

    func body(content: Content) -> some View {
        let size = TextSize.dynamicTypeSizes[TextSize.step(interface)]
        content
            .dynamicTypeSize(followsSystem ? DynamicTypeSize.xSmall...DynamicTypeSize.accessibility5 : size...size)
            .environment(\.messageTextSize, TextSize.messages(messages))
    }
}

private struct MessageFontModifier: ViewModifier {
    @Environment(\.messageTextSize) private var size

    func body(content: Content) -> some View {
        content.font(.system(size: size))
    }
}

private struct MessageRelativeFont: ViewModifier {
    let scale: CGFloat
    let weight: Font.Weight?
    let design: Font.Design?
    @Environment(\.messageTextSize) private var size

    func body(content: Content) -> some View {
        if design == .monospaced {
            let font = Font.system(size: size * scale * TextSize.codeScale, design: .monospaced)
            content.font(weight.map { font.weight($0) } ?? font)
        } else {
            content.font(.system(size: size * scale, weight: weight, design: design))
        }
    }
}

private struct ScaledFont: ViewModifier {
    let style: Font.TextStyle
    let weight: Font.Weight?
    let design: Font.Design?

    func body(content: Content) -> some View {
        content.font(.system(style, design: design, weight: weight))
    }
}

private struct ScaledSizeFont: ViewModifier {
    @ScaledMetric private var size: CGFloat
    let design: Font.Design?

    init(size: CGFloat, design: Font.Design?) {
        _size = ScaledMetric(wrappedValue: size, relativeTo: .body)
        self.design = design
    }

    func body(content: Content) -> some View {
        content.font(.system(size: size, design: design))
    }
}
