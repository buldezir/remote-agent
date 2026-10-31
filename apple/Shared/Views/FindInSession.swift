import RAKit
import SwiftUI

/// A session to open at one of its items, as a search result asks: its view
/// scrolls the item to the middle and flashes it, once. Another request for
/// the same item, a new `id`, flashes it again. Kept apart from the route, so
/// the sidebars' selection stays the session's.
struct ItemFocus: Equatable {
    let route: SessionRoute
    let itemID: String
    let id = UUID()
}

/// What the Mac's Edit › Find menu asks of the open session's find bar.
struct SessionFindCommands {
    var open: () -> Void
    var next: () -> Void
    var previous: () -> Void
    /// The find bar is open, with matches to step through.
    var canStep: Bool
}

/// What the Mac's Session menu asks of the open session: the sheets and the
/// alert of its ⋯ menu. Nil where the session can't do one now.
struct SessionMenuCommands {
    var rename: (() -> Void)?
    var changes: (() -> Void)?
    var commit: (() -> Void)?
    var fork: (() -> Void)?
}

extension EnvironmentValues {
    /// The find bar's query, on the transcript's items that hold it, which
    /// mark it in their text. Nil elsewhere, so the other items don't change.
    @Entry var findHighlight: String?
}

/// The find bar's and search's colours, and the occurrences of a query
/// marked in a text.
enum FindHighlight {
    /// Behind an item that holds the query.
    static let match = Palette.yellow.opacity(0.12)
    /// Behind the match the find bar is on.
    static let current = Palette.yellow.opacity(0.28)
    /// Behind the query's words in a text.
    static let occurrence = Palette.yellow.opacity(0.4)
    /// Behind an item a search result opened, fading.
    static let flash = Palette.accent.opacity(0.22)

    /// The text with the query's occurrences marked.
    static func text(_ s: String, _ query: String?) -> AttributedString {
        var a = AttributedString(s)
        mark(&a, query)
        return a
    }

    /// Marks the query's occurrences, ignoring case, as find matches them.
    static func mark(_ a: inout AttributedString, _ query: String?) {
        guard let q = query?.trimmingCharacters(in: .whitespacesAndNewlines), !q.isEmpty else { return }
        var start = a.startIndex
        while start < a.endIndex, let r = a[start...].range(of: q, options: .caseInsensitive) {
            a[r].swiftUI.backgroundColor = occurrence
            start = r.upperBound
        }
    }
}

/// The bar over the transcript that finds text in it: the query, which match
/// it is on, and the way to the others. Return goes to the next match,
/// Shift-Return to the previous one, and Esc closes it.
struct FindBar: View {
    @Binding var query: String
    /// "2 of 5", "No matches", or nil while there is no query.
    let count: String?
    let canStep: Bool
    var focused: FocusState<Bool>.Binding
    let next: () -> Void
    let previous: () -> Void
    let close: () -> Void

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "magnifyingglass")
                .foregroundStyle(Palette.overlay)
                .accessibilityHidden(true)
            TextField("Find in session", text: $query, prompt: Text("Find in session").foregroundStyle(Palette.overlay))
                .textFieldStyle(.plain)
                .plainTextInput()
                .foregroundStyle(Palette.text)
                .focused(focused)
                .submitLabel(.next)
                .onKeyPress(.return, phases: .down) { press in
                    guard press.modifiers.contains(.shift) else { return .ignored }
                    previous()
                    return .handled
                }
                .onSubmit {
                    next()
                    // iOS lets go of the field on Return.
                    focused.wrappedValue = true
                }
                .onKeyPress(.escape) {
                    close()
                    return .handled
                }
            if let count {
                Text(count)
                    .monospacedDigit()
                    .foregroundStyle(Palette.subtext)
                    .lineLimit(1)
                    .fixedSize()
            }
            Group {
                Button("Previous match", systemImage: "chevron.up", action: previous)
                    .help("Previous match (⇧Return)")
                Button("Next match", systemImage: "chevron.down", action: next)
                    .help("Next match (Return)")
            }
            .labelStyle(.iconOnly)
            .buttonStyle(.borderless)
            .foregroundStyle(canStep ? Palette.subtext : Palette.overlay)
            .disabled(!canStep)
            Button(action: close) {
                Text("Done").fontWeight(.medium).foregroundStyle(Palette.accent)
            }
            .buttonStyle(.borderless)
        }
        .scaledFont(.callout)
        .padding(.horizontal, Metrics.margin)
        .padding(.vertical, 7)
        .background(Palette.mantle)
        .overlay(alignment: .bottom) {
            Rectangle().fill(Palette.surface0).frame(height: 1)
        }
        // The field takes the keyboard once it's there.
        .onAppear { focused.wrappedValue = true }
    }
}
