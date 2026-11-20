import SwiftUI
import UIKit

/// Shorthands for the UIKit calls the views make.
enum Platform {
    /// The name a server lists this device under.
    @MainActor static var deviceName: String { UIDevice.current.name }

    @MainActor static var pasteboardString: String? { UIPasteboard.general.string }

    /// Puts text on the pasteboard.
    @MainActor static func copy(_ text: String) {
        UIPasteboard.general.string = text
    }
}

extension View {
    /// A small title bar.
    func inlineTitle() -> some View {
        navigationBarTitleDisplayMode(.inline)
    }

    /// For ids, branch names and links: no capitals or corrections.
    func plainTextInput() -> some View {
        textInputAutocapitalization(.never).autocorrectionDisabled()
    }
}
