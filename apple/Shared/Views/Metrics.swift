import SwiftUI

/// Spacing and corner sizes the views share, kept small for a dense layout.
enum Metrics {
    /// Space between transcript blocks.
    static let gap: CGFloat = 8
    /// The side margin of the transcript and the composer.
    static let margin: CGFloat = 12
    /// Padding inside cards: tool calls, plans, approvals.
    static let padding: CGFloat = 8
    /// The transcript stops widening here, so lines stay readable with the iPad
    /// sidebar hidden.
    static let readableWidth: CGFloat = 1000
    /// Rows of the server and session lists.
    static let rowInsets = EdgeInsets(top: 6, leading: 12, bottom: 6, trailing: 12)
    /// The default size of messages, prompts and replies (Settings can change
    /// it): a little under the body size.
    static let textSize: CGFloat = 15
    /// Tool calls' text, as a fraction of the message size: their titles,
    /// commands, outputs and diffs, a step below the messages around them.
    static let toolText: CGFloat = 0.9
    /// Labels in tool calls, such as "Output", as a fraction of the message size.
    static let toolLabel: CGFloat = 0.8
    /// The round buttons beside the prompt: add images, dictate, send and stop.
    static let roundButton: CGFloat = 26

    enum Corner {
        /// Snippets inside a card: code, outputs, diffs.
        static let inset: CGFloat = 4
        /// Cards and buttons.
        static let card: CGFloat = 6
        /// User messages and approvals.
        static let bubble: CGFloat = 8
        /// The composer's text field.
        static let field: CGFloat = 10
        /// The selected row in the sidebar and session list.
        static let selection: CGFloat = 12
    }
}

extension View {
    /// Grouped forms with compact section spacing and narrower side margins,
    /// on the palette's background. Their rows take `paletteRows()`.
    func compactForm() -> some View {
        listSectionSpacing(.compact)
            .contentMargins(.horizontal, Metrics.margin, for: .scrollContent)
            .listBackground(Palette.mantle)
    }
}
