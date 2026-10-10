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
    /// sidebar hidden. On the Mac it fills the window, as wide as the user makes it.
    #if os(macOS)
    static let readableWidth: CGFloat = .infinity
    #else
    static let readableWidth: CGFloat = 1000
    #endif
    /// Rows of the server and session lists.
    static let rowInsets = EdgeInsets(top: 6, leading: 12, bottom: 6, trailing: 12)
    /// The default size of messages, prompts and replies (Settings can change
    /// it): a little under the body size on iOS, the body size on the Mac.
    #if os(macOS)
    static let textSize: CGFloat = 13
    #else
    static let textSize: CGFloat = 15
    #endif
    /// The round buttons beside the prompt: add images, dictate, send and stop.
    static let roundButton: CGFloat = 26

    enum Corner {
        /// Snippets inside a card: code, outputs, diffs.
        static let inset: CGFloat = 4
        /// Cards and buttons.
        static let card: CGFloat = 6
        /// User messages and approvals.
        static let bubble: CGFloat = 8
        /// The composer's text field on iOS.
        static let field: CGFloat = 10
        /// The composer on the Mac, a card around the field and its buttons.
        static let composer: CGFloat = 14
        /// The selected row in the iPad sidebar.
        static let selection: CGFloat = 12
    }
}

extension View {
    /// A round button beside the prompt, laid out at its symbol's size. Mac
    /// buttons and menus pad their labels, which would set the symbols in
    /// from the edges of the prompt.
    func roundButtonFrame() -> some View {
        #if os(macOS)
        frame(width: Metrics.roundButton, height: Metrics.roundButton)
        #else
        self
        #endif
    }

    /// Grouped forms with compact section spacing and narrower side margins,
    /// on the palette's background. Their rows take `paletteRows()`.
    func compactForm() -> some View {
        #if os(macOS)
        formStyle(.grouped)
            .listBackground(Palette.mantle)
        #else
        listSectionSpacing(.compact)
            .contentMargins(.horizontal, Metrics.margin, for: .scrollContent)
            .listBackground(Palette.mantle)
        #endif
    }
}
