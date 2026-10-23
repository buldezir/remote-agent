import RAKit
import SwiftUI
import Textual

/// An agent's Markdown, in the message and code fonts and the message size
/// from Settings. Textual draws it, so a selection runs across its
/// paragraphs, lists, tables and code blocks, which wrap rather than scroll
/// to stay part of it. The images rad keeps for a reply (`rad-image:<id>`)
/// stand between the texts, where a tap opens them; one in a line of text is
/// drawn small and can't be tapped.
struct AgentMarkdown: View {
    let text: String
    var images: [ImageRef] = []
    var store: SessionStore?

    @Environment(\.messageTextSize) private var size
    @Environment(\.messageFont) private var font
    @Environment(\.codeFont) private var codeFont
    @Environment(\.displayScale) private var scale

    var body: some View {
        let parts = MarkdownParts.of(text)
        VStack(alignment: .leading, spacing: 8) {
            ForEach(parts.indices, id: \.self) { i in
                switch parts[i] {
                case .text(let content):
                    // Textual parses when the markup changes, so the key
                    // changes with the reply.
                    StructuredText("\(i):\(text)", parser: Parsed(content: content))
                case .image(let id):
                    if let store {
                        ReplyImage(ref: images.first { $0.id == id } ?? ImageRef(id: id, mimeType: "", size: 0),
                                   images: images, store: store)
                    }
                }
            }
        }
        .font(font.font(size: size))
        .foregroundStyle(Palette.text)
        .textual.structuredTextStyle(AgentTextStyle(size: size, codeFont: codeFont, codeScale: font.codeScale(codeFont)))
        .textual.highlighterTheme(.init(foregroundColor: DynamicColor(Palette.text), backgroundColor: DynamicColor(Palette.crust)))
        .textual.overflowMode(.wrap)
        .textual.imageAttachmentLoader(InlineImageLoader(store: store, scale: scale))
        .textual.textSelection(.enabled)
        .textual.textSelectionColor(Palette.selection)
    }
}

/// A reply split at the paragraphs that hold only images rad keeps.
private enum MarkdownPart {
    case text(AttributedString)
    case image(String)
}

private enum MarkdownParts {
    /// Parsed replies, so rows that scroll back into view don't parse again.
    /// A streaming reply adds one for each word, so the limit is on their length.
    @MainActor private static let cache: NSCache<NSString, Box> = {
        let c = NSCache<NSString, Box>()
        c.totalCostLimit = 1_000_000
        return c
    }()

    private final class Box {
        let parts: [MarkdownPart]
        init(_ parts: [MarkdownPart]) { self.parts = parts }
    }

    @MainActor static func of(_ markdown: String) -> [MarkdownPart] {
        if let box = cache.object(forKey: markdown as NSString) { return box.parts }
        var doc = (try? AttributedStringMarkdownParser.markdown().attributedString(for: markdown)) ?? AttributedString(markdown)
        showHTMLBlocks(&doc)
        markTasks(&doc)
        var parts: [MarkdownPart] = []
        var start = doc.startIndex
        for (intent, range) in doc.runs[\.presentationIntent] {
            guard let intent, intent.components.count == 1, intent.components[0].kind == .paragraph,
                  let ids = imageIDs(doc[range]) else { continue }
            if start < range.lowerBound { parts.append(.text(AttributedString(doc[start..<range.lowerBound]))) }
            parts += ids.map(MarkdownPart.image)
            start = range.upperBound
        }
        if start < doc.endIndex { parts.append(.text(AttributedString(doc[start...]))) }
        cache.setObject(Box(parts), forKey: markdown as NSString, cost: markdown.utf16.count)
        return parts
    }

    /// The images in a paragraph that holds nothing else, when rad keeps them all.
    private static func imageIDs(_ paragraph: AttributedSubstring) -> [String]? {
        var ids: [String] = []
        for (url, range) in paragraph.runs[\.imageURL] {
            if let url {
                guard let id = ImageRef.id(linkedBy: url) else { return nil }
                ids.append(id)
            } else if !paragraph[range].characters.allSatisfy(\.isWhitespace) {
                return nil
            }
        }
        return ids.isEmpty ? nil : ids
    }

    /// HTML blocks: Foundation's parser leaves them outside any block, where
    /// Textual would drop them, so they become paragraphs of text, as inline
    /// HTML is.
    private static func showHTMLBlocks(_ doc: inout AttributedString) {
        var blocks: [(offset: Int, length: Int)] = []
        for (intent, range) in doc.runs[\.presentationIntent] where intent == nil {
            blocks.append((doc.characters.distance(from: doc.startIndex, to: range.lowerBound), doc[range].characters.count))
        }
        // From the end, so the offsets before each stay valid.
        for (n, block) in blocks.enumerated().reversed() {
            let start = doc.characters.index(doc.startIndex, offsetBy: block.offset)
            let end = doc.characters.index(start, offsetBy: block.length)
            var text = AttributedString(String(doc[start..<end].characters).trimmingCharacters(in: .newlines))
            text.presentationIntent = PresentationIntent(.paragraph, identity: Int.max - n)
            doc.replaceSubrange(start..<end, with: text)
        }
    }

    /// Task list items: Foundation's parser leaves their `[ ]` and `[x]` as
    /// text, so they become boxes.
    private static func markTasks(_ doc: inout AttributedString) {
        var tasks: [(offset: Int, done: Bool)] = []
        var items: Set<Int> = []
        for (intent, range) in doc.runs[\.presentationIntent] {
            // The first paragraph of each item.
            guard let components = intent?.components, components.count >= 2,
                  components[0].kind == .paragraph, case .listItem = components[1].kind,
                  items.insert(components[1].identity).inserted else { continue }
            let text = doc[range].characters
            let done = text.starts(with: "[x] ") || text.starts(with: "[X] ")
            guard done || text.starts(with: "[ ] ") else { continue }
            tasks.append((doc.characters.distance(from: doc.startIndex, to: range.lowerBound), done))
        }
        // From the end, so the offsets before each stay valid.
        for task in tasks.reversed() {
            let start = doc.characters.index(doc.startIndex, offsetBy: task.offset)
            let end = doc.characters.index(start, offsetBy: 3)
            var box = AttributedString(task.done ? "☑" : "☐", attributes: doc[start..<end].runs.first?.attributes ?? .init())
            box.swiftUI.foregroundColor = task.done ? Palette.green : Palette.overlay
            doc.replaceSubrange(start..<end, with: box)
        }
    }
}

/// Hands Textual a part already parsed.
private struct Parsed: MarkupParser {
    let content: AttributedString

    func attributedString(for _: String) -> AttributedString { content }
}

/// Images in a line of a reply's text, which can't be tapped or resized, so
/// they are drawn small. Other images load from their URLs.
private struct InlineImageLoader: AttachmentLoader {
    let store: SessionStore?
    let scale: CGFloat
    static let height: CGFloat = 120

    func attachment(for url: URL, text: String, environment: ColorEnvironmentValues) async throws -> AnyAttachment {
        guard let id = ImageRef.id(linkedBy: url) else {
            return Self.erase(try await URLAttachmentLoader.image().attachment(for: url, text: text, environment: environment))
        }
        guard let store else { throw URLError(.resourceUnavailable) }
        let data = try await store.imageData(id)
        let pixels = Int(Self.height * scale)
        guard let image = await Task.detached(operation: { ImageCoding.thumbnail(data, maxPixels: pixels) }).value else {
            throw URLError(.cannotDecodeContentData)
        }
        return Self.erase(InlineImage(image: image, scale: scale, text: text))
    }

    /// `AnyAttachment` has an initializer for `some` and one for `any`, which
    /// Swift can't choose between.
    private static func erase(_ attachment: any Textual.Attachment) -> AnyAttachment {
        let erase: (any Textual.Attachment) -> AnyAttachment = AnyAttachment.init
        return erase(attachment)
    }
}

private struct InlineImage: Attachment {
    let image: CGImage
    let scale: CGFloat
    let text: String

    var description: String { text }

    var body: some View {
        Image(decorative: image, scale: scale)
            .resizable()
            .clipShape(RoundedRectangle(cornerRadius: Metrics.Corner.card))
    }

    private var size: CGSize { CGSize(width: CGFloat(image.width) / scale, height: CGFloat(image.height) / scale) }

    func sizeThatFits(_ proposal: ProposedViewSize, in _: TextEnvironmentValues) -> CGSize {
        guard let width = proposal.width, width < size.width else { return size }
        return CGSize(width: width, height: size.height * width / size.width)
    }
}

/// GitHub-like, sized for a transcript and in the palette's colours.
private struct AgentTextStyle: StructuredText.Style {
    let size: CGFloat
    let codeFont: FontChoice
    let codeScale: CGFloat

    var inlineStyle: InlineStyle { inlineStyle(scale: 1) }

    /// Inline code takes the code font, as large as the text around it.
    func inlineStyle(scale: CGFloat) -> InlineStyle {
        InlineStyle()
            .code(.font(codeFont.font(size: size * scale * codeScale)), .backgroundColor(Palette.surface0))
            .strong(.fontWeight(.semibold))
            .link(.foregroundColor(Palette.blue))
    }

    var headingStyle: AgentHeadingStyle { AgentHeadingStyle(text: self) }
    var paragraphStyle: AgentParagraphStyle { AgentParagraphStyle() }
    var blockQuoteStyle: AgentBlockQuoteStyle { AgentBlockQuoteStyle() }
    var codeBlockStyle: AgentCodeBlockStyle { AgentCodeBlockStyle(font: codeFont.font(size: size * codeScale)) }
    var listItemStyle: StructuredText.DefaultListItemStyle { .default }
    var unorderedListMarker: StructuredText.HierarchicalSymbolListMarker { .hierarchical(.disc, .circle, .square) }
    var orderedListMarker: StructuredText.DecimalListMarker { .decimal }
    var tableStyle: AgentTableStyle { AgentTableStyle() }
    var tableCellStyle: StructuredText.GitHubTableCellStyle { .gitHub }
    var thematicBreakStyle: AgentThematicBreakStyle { AgentThematicBreakStyle() }
}

// GitHub leaves 16pt after paragraphs and 24pt above headings; a transcript
// reads better tighter.

private struct AgentParagraphStyle: StructuredText.ParagraphStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .textual.lineSpacing(.fontScaled(0.2))
            .textual.blockSpacing(.init(top: 0, bottom: 8))
    }
}

private struct AgentHeadingStyle: StructuredText.HeadingStyle {
    let text: AgentTextStyle
    private static let scales: [CGFloat] = [1.4, 1.25, 1.1, 1, 0.9, 0.85]

    func makeBody(configuration: Configuration) -> some View {
        let scale = Self.scales[min(max(configuration.headingLevel, 1), 6) - 1]
        configuration.label
            .textual.fontScale(scale)
            .fontWeight(.semibold)
            .textual.inlineStyle(text.inlineStyle(scale: scale))
            .textual.lineSpacing(.fontScaled(0.125))
            .textual.blockSpacing(.init(top: 12, bottom: 6))
    }
}

private struct AgentBlockQuoteStyle: StructuredText.BlockQuoteStyle {
    func makeBody(configuration: Configuration) -> some View {
        HStack(spacing: 0) {
            RoundedRectangle(cornerRadius: 2)
                .fill(Palette.surface1)
                .textual.frame(width: .fontScaled(0.2))
            configuration.label
                .foregroundStyle(Palette.subtext)
                .textual.padding(.horizontal, .fontScaled(1))
        }
        .textual.blockSpacing(.init(top: 0, bottom: 8))
    }
}

/// A fenced code block, in the code font.
private struct AgentCodeBlockStyle: StructuredText.CodeBlockStyle {
    let font: Font

    func makeBody(configuration: Configuration) -> some View {
        Overflow {
            configuration.label
                .font(font)
                .textual.lineSpacing(.fontScaled(0.2))
                .fixedSize(horizontal: false, vertical: true)
                .padding(8)
        }
        .background(Palette.crust, in: RoundedRectangle(cornerRadius: Metrics.Corner.inset))
        .textual.blockSpacing(.init(top: 0, bottom: 8))
    }
}

private struct AgentTableStyle: StructuredText.TableStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .textual.tableCellSpacing(horizontal: 1, vertical: 1)
            .textual.tableBackground { layout in
                Canvas { context, _ in
                    for row in layout.rowIndices.dropFirst() where row.isMultiple(of: 2) {
                        context.fill(Path(layout.rowBounds(row).integral), with: .color(Palette.mantle))
                    }
                }
            }
            .textual.tableOverlay { layout in
                Canvas { context, _ in
                    for divider in layout.dividers() {
                        context.fill(Path(divider), with: .color(Palette.surface1))
                    }
                }
            }
            .padding(1)
            .border(Palette.surface1, width: 1)
            .textual.blockSpacing(.init(top: 0, bottom: 8))
    }
}

private struct AgentThematicBreakStyle: StructuredText.ThematicBreakStyle {
    func makeBody(configuration _: Configuration) -> some View {
        Rectangle()
            .fill(Palette.surface1)
            .frame(height: 1)
            .textual.blockSpacing(.init(top: 8, bottom: 8))
    }
}
