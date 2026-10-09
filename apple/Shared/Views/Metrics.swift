import SwiftUI

/// Spacing and corner sizes the views share, kept small for a dense layout.
enum Metrics {
    /// Space between transcript blocks.
    static let gap: CGFloat = 8
    /// The side margin of the transcript and the composer.
    static let margin: CGFloat = 12
    /// Padding inside cards: tool calls, plans, approvals.
    static let padding: CGFloat = 8
    /// The transcript stops widening here, so lines stay readable with the iPad sidebar hidden.
    static let readableWidth: CGFloat = 1000
    /// Rows of the server and session lists.
    static let rowInsets = EdgeInsets(top: 6, leading: 12, bottom: 6, trailing: 12)
    /// Assistant text: a little under the body size on iOS, the body size on the Mac.
    #if os(macOS)
    static let textSize: CGFloat = 13
    #else
    static let textSize: CGFloat = 15
    #endif

    enum Corner {
        /// Snippets inside a card: code, outputs, diffs.
        static let inset: CGFloat = 4
        /// Cards and buttons.
        static let card: CGFloat = 6
        /// User messages and approvals.
        static let bubble: CGFloat = 8
        /// The composer's text field.
        static let field: CGFloat = 10
    }
}

extension View {
    /// Grouped forms with compact section spacing and narrower side margins.
    func compactForm() -> some View {
        #if os(macOS)
        formStyle(.grouped)
        #else
        listSectionSpacing(.compact)
            .contentMargins(.horizontal, Metrics.margin, for: .scrollContent)
        #endif
    }
}
