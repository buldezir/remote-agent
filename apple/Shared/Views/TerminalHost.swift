// The only file that imports SwiftTerm: its Color and TerminalView would
// clash with SwiftUI's elsewhere.
@preconcurrency import SwiftTerm
import RAKit
import SwiftUI
import UIKit

/// SwiftTerm's terminal, showing a session's shell.
@MainActor
struct TerminalHost {
    let session: TerminalSession
    let fontSize: CGFloat
    let dark: Bool
    /// Whether the terminal has the keyboard, so the composer's Return
    /// shortcut leaves it alone.
    @Binding var focused: Bool

    func makeCoordinator() -> Coordinator { Coordinator(session: session) }

    private func make(_ coordinator: Coordinator) -> HostTerminalView {
        let view = HostTerminalView(frame: .zero)
        view.terminalDelegate = coordinator
        view.optionAsMetaKey = false  // Option types @, { and [ on many keyboards
        view.changeScrollback(5000)
        coordinator.view = view
        view.onLayout = { [weak coordinator] in coordinator?.laidOut() }
        view.onFocus = { [weak coordinator] in coordinator?.focused?.wrappedValue = $0 }
        session.onOutput = { [weak view] data in view?.show([UInt8](data)[...]) }
        // CAN ends any sequence the last connection left half-sent; ESC c resets.
        session.onReset = { [weak view] in view?.show([0x18, 0x1b, 0x63]) }
        update(view, coordinator)
        return view
    }

    private func update(_ view: HostTerminalView, _ coordinator: Coordinator) {
        coordinator.focused = $focused
        // Setting it clears the selection and redraws, so only on a change.
        if coordinator.fontSize != fontSize {
            coordinator.fontSize = fontSize
            view.font = .monospacedSystemFont(ofSize: fontSize, weight: .regular)
        }
        if coordinator.dark != dark {
            coordinator.dark = dark
            let colors = Palette.terminal(dark: dark)
            view.installColors(colors.ansi.map {
                SwiftTerm.Color(red8: UInt16($0 >> 16 & 0xff), green8: UInt16($0 >> 8 & 0xff), blue8: UInt16($0 & 0xff))
            })
            view.nativeForegroundColor = .init(rgb: colors.foreground)
            view.caretColor = .init(rgb: colors.cursor)
            view.selectedTextBackgroundColor = .init(rgb: colors.selection)
            // iOS paints the background on the layer, under clear cells.
            view.layer.backgroundColor = UIColor(rgb: colors.background).cgColor
            view.keyboardAppearance = dark ? .dark : .light
        }
    }

    @MainActor final class Coordinator: @preconcurrency TerminalViewDelegate {
        let session: TerminalSession
        weak var view: HostTerminalView?
        var focused: Binding<Bool>?
        var fontSize: CGFloat = 0
        var dark: Bool?
        private var started = false

        init(session: TerminalSession) {
            self.session = session
        }

        private var starting: Task<Void, Never>?

        /// Connects once the view's size settles, so the shell starts at it
        /// rather than at a size it passes through while a panel opens.
        func laidOut() {
            guard !started else { return }
            starting?.cancel()
            starting = Task { [weak self] in
                try? await Task.sleep(for: .milliseconds(150))
                guard !Task.isCancelled, let self, !self.started, let view = self.view,
                      view.bounds.width > 0, view.bounds.height > 0 else { return }
                let t = view.getTerminal()
                guard t.cols > 1, t.rows > 1 else { return }
                self.started = true
                self.session.start(cols: t.cols, rows: t.rows, create: true)
            }
        }

        func sizeChanged(source: TerminalView, newCols: Int, newRows: Int) {
            if started { session.resize(cols: newCols, rows: newRows) } else { laidOut() }
        }

        func send(source: TerminalView, data: ArraySlice<UInt8>) {
            session.send(Data(data))
        }

        func requestOpenLink(source: TerminalView, link: String, params: [String: String]) {
            guard let url = URL(string: link), ["http", "https"].contains(url.scheme) else { return }
            UIApplication.shared.open(url)
        }

        /// A program in the shell copies text (OSC 52), as tmux and vim can.
        func clipboardCopy(source: TerminalView, content: Data) {
            guard let text = String(data: content, encoding: .utf8) else { return }
            UIPasteboard.general.string = text
        }

        func setTerminalTitle(source: TerminalView, title: String) {}
        func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) {}
        func scrolled(source: TerminalView, position: Double) {}
        func rangeChanged(source: TerminalView, startY: Int, endY: Int) {}
    }
}

extension TerminalHost: UIViewRepresentable {
    func makeUIView(context: Context) -> HostTerminalView { make(context.coordinator) }
    func updateUIView(_ view: HostTerminalView, context: Context) { update(view, context.coordinator) }

    static func dismantleUIView(_ view: HostTerminalView, coordinator: Coordinator) {
        view.updateUiClosed()  // its display link would keep it alive
    }
}

/// The terminal view, telling its host about its focus and its first size.
final class HostTerminalView: SwiftTerm.TerminalView {
    var onFocus: ((Bool) -> Void)?
    var onLayout: (() -> Void)?

    override func becomeFirstResponder() -> Bool {
        let ok = super.becomeFirstResponder()
        if ok { onFocus?(true) }
        return ok
    }

    override func resignFirstResponder() -> Bool {
        let ok = super.resignFirstResponder()
        if ok { onFocus?(false) }
        return ok
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        onLayout?()
    }

    /// Takes the keyboard when it first appears.
    override func didMoveToWindow() {
        super.didMoveToWindow()
        guard window != nil, !tookFocus else { return }
        tookFocus = true
        DispatchQueue.main.async { [weak self] in self?.becomeFirstResponder() }
    }
    private var tookFocus = false

    func show(_ bytes: ArraySlice<UInt8>) {
        feed(byteArray: bytes)
        // SwiftTerm leaves its scroll view where it was when output clears
        // the scrollback (clear, a reset), past the end of what's left, so
        // the screen looks empty. Settling on the current row resyncs it.
        if !isTracking && !isDecelerating {
            scrollTo(row: getTerminal().buffer.yDisp, notifyAccessibility: false)
        }
    }
}
