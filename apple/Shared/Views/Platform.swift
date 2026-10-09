import SwiftUI
#if os(iOS)
import UIKit
#else
import AppKit
#endif

/// The few things the iOS and Mac apps get from different frameworks.
enum Platform {
    /// The name a server lists this device under.
    @MainActor static var deviceName: String {
        #if os(iOS)
        UIDevice.current.name
        #else
        Host.current().localizedName ?? "Mac"
        #endif
    }

    @MainActor static var pasteboardString: String? {
        #if os(iOS)
        UIPasteboard.general.string
        #else
        NSPasteboard.general.string(forType: .string)
        #endif
    }
}

extension View {
    /// A small title bar on iOS. Mac windows and sheets have their own.
    func inlineTitle() -> some View {
        #if os(iOS)
        navigationBarTitleDisplayMode(.inline)
        #else
        self
        #endif
    }

    /// For ids, branch names and links: no capitals or corrections.
    func plainTextInput() -> some View {
        #if os(iOS)
        textInputAutocapitalization(.never).autocorrectionDisabled()
        #else
        autocorrectionDisabled()
        #endif
    }
}
