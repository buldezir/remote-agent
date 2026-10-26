import RAKit
import SwiftUI

/// Where the terminal opens, from Settings: in place of the messages, below
/// them, or beside them. A narrow window always shows it in their place.
enum TerminalPlacement: String, CaseIterable, Identifiable {
    case over, below, right

    static let key = "terminalPlacement"

    var id: Self { self }

    var title: String {
        switch self {
        case .over: "Instead of the Messages"
        case .below: "Below the Messages"
        case .right: "Right Sidebar"
        }
    }
}

/// A session's shell on the server. It keeps running when this goes away;
/// showing it again resumes where it was.
struct TerminalPanel: View {
    let connection: ServerConnection
    let sessionID: String
    let folder: String
    @Binding var focused: Bool
    /// Hides the panel; the shell keeps running.
    let hide: () -> Void

    /// Whether text selected with the pointer goes to the clipboard, from the
    /// Mac's Settings. iOS selects with taps, and offers Copy in its menu.
    static let copyOnSelectKey = "terminalCopyOnSelect"

    @State private var session: TerminalSession?
    @Environment(\.colorScheme) private var scheme
    @Environment(\.codeFont) private var codeFont
    @ScaledMetric(relativeTo: .body) private var size: CGFloat = 12
    #if os(macOS)
    @Environment(\.textScale) private var scale
    @AppStorage(TerminalPanel.copyOnSelectKey) private var copyOnSelect = true
    #else
    private let copyOnSelect = false
    #endif

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            ZStack {
                if let session {
                    TerminalHost(session: session, font: codeFont, fontSize: fontSize, dark: scheme == .dark,
                                 copyOnSelect: copyOnSelect, focused: $focused)
                        .id(ObjectIdentifier(session))
                        .padding(4)
                }
                status
            }
            .background(Palette.base)
        }
        .onAppear {
            if session == nil { session = connection.terminal(sessionID: sessionID) }
        }
        .onDisappear {
            session?.detach()
            session = nil
            focused = false
        }
        .onChange(of: connection.state) { _, state in session?.connectionChanged(state) }
    }

    private var fontSize: CGFloat {
        #if os(macOS)
        size * scale
        #else
        size
        #endif
    }

    private var header: some View {
        HStack(spacing: 8) {
            Image(systemName: "apple.terminal")
                .foregroundStyle(Palette.subtext)
            Text(folder)
                .lineLimit(1)
                .truncationMode(.head)
                .foregroundStyle(Palette.subtext)
                .help(folder)
            Spacer()
            Menu {
                Button("Close Shell", systemImage: "xmark.octagon", role: .destructive, action: closeShell)
                    .disabled(session?.state != .open)
            } label: {
                Image(systemName: "ellipsis.circle")
            }
            .menuStyle(.button)
            .menuIndicator(.hidden)
            .buttonStyle(.borderless)
            .fixedSize()
            .accessibilityLabel("Terminal Menu")
            Button(action: hide) {
                Image(systemName: "xmark")
            }
            .buttonStyle(.borderless)
            .help("Hide the terminal (⌘J). The shell keeps running.")
            .accessibilityLabel("Hide Terminal")
        }
        .scaledFont(.caption)
        .padding(.horizontal, Metrics.margin)
        .padding(.vertical, 6)
        .background(Palette.mantle)
    }

    @ViewBuilder private var status: some View {
        switch session?.state {
        case .reconnecting?:
            Label("Reconnecting…", systemImage: "arrow.triangle.2.circlepath")
                .scaledFont(.caption)
                .foregroundStyle(Palette.subtext)
                .padding(.horizontal, 10)
                .padding(.vertical, 4)
                .background(Palette.mantle, in: Capsule())
                .frame(maxHeight: .infinity, alignment: .top)
                .padding(.top, 8)
        case .exited(let code)?:
            banner(session?.closedHere == true ? "The shell was closed."
                   : code == 0 ? "The shell exited." : "The shell exited with code \(code).", action: "Start New Shell")
        case .gone?:
            banner("The shell has ended.", action: "Start New Shell")
        case .failed(let message)?:
            banner(message, action: "Try Again")
        case .connecting?, .open?, nil:
            EmptyView()
        }
    }

    private func banner(_ message: String, action: String) -> some View {
        HStack(spacing: 12) {
            Text(message)
                .scaledFont(.callout)
                .foregroundStyle(Palette.text)
            Spacer(minLength: 0)
            Button {
                session?.restart()
            } label: {
                Text(action).scaledFont(.callout, weight: .semibold).foregroundStyle(Palette.onAccent)
            }
            .buttonStyle(.borderedProminent)
        }
        .padding(10)
        .background(Palette.mantle, in: RoundedRectangle(cornerRadius: Metrics.Corner.card))
        .overlay(RoundedRectangle(cornerRadius: Metrics.Corner.card).strokeBorder(Palette.surface1))
        .padding(Metrics.margin)
        .frame(maxHeight: .infinity, alignment: .bottom)
    }

    private func closeShell() {
        session?.closeShell()
    }
}


/// The bar above the terminal below the session: dragging it sets the
/// terminal's height.
struct PanelResizeHandle: View {
    @Binding var height: Double
    let range: ClosedRange<Double>
    @State private var start: Double?

    var body: some View {
        ZStack {
            Palette.mantle
            Capsule().fill(Palette.surface1).frame(width: 36, height: 4)
        }
        #if os(macOS)
        .frame(height: 8)
        .pointerStyle(.rowResize)
        #else
        .frame(height: 16)
        #endif
        .overlay(alignment: .top) { Divider() }
        .contentShape(Rectangle())
        .gesture(DragGesture(minimumDistance: 1, coordinateSpace: .global)
            .onChanged { drag in
                let from = start ?? height
                start = from
                height = min(max(from - drag.translation.height, range.lowerBound), range.upperBound)
            }
            .onEnded { _ in start = nil })
        .accessibilityHidden(true)
    }
}
