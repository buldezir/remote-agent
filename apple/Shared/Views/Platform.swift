import SwiftUI
import UniformTypeIdentifiers
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

    /// The pasteboard holds an image. Checking doesn't make iOS ask to paste.
    @MainActor static var pasteboardHasImages: Bool {
        #if os(iOS)
        UIPasteboard.general.hasImages
        #else
        NSPasteboard.general.canReadItem(withDataConformingToTypes: [UTType.image.identifier])
        #endif
    }

    #if os(macOS)
    /// The images on the pasteboard: image files first, else image data. Nil
    /// when it holds none. With `unlessText`, also nil when it holds text
    /// alongside them (copied cells, rich text), which ⌘V should paste as text.
    @MainActor static func pasteboardImages(_ pb: NSPasteboard = .general, unlessText: Bool = false) -> [Data]? {
        let files = pb.readObjects(forClasses: [NSURL.self], options: [
            .urlReadingFileURLsOnly: true,
            .urlReadingContentsConformToTypes: [UTType.image.identifier],
        ]) as? [URL] ?? []
        if !files.isEmpty {
            return files.compactMap { try? Data(contentsOf: $0) }
        }
        if pb.types?.contains(.fileURL) == true || (unlessText && pb.types?.contains(.string) == true) { return nil }
        for type in [NSPasteboard.PasteboardType.png, .tiff, .init(UTType.jpeg.identifier), .init(UTType.heic.identifier)] {
            if let data = pb.data(forType: type) { return [data] }
        }
        return nil
    }
    #endif
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

    /// For prompts. On the Mac, Return ends a multiline field and Option-Return
    /// starts a new line; this makes Shift-Return start one too. On iOS, Return
    /// already starts a new line.
    func shiftReturnNewline() -> some View {
        #if os(macOS)
        onKeyPress(.return, phases: .down) { press in
            guard press.modifiers.contains(.shift) else { return .ignored }
            NSApp.sendAction(#selector(NSResponder.insertNewlineIgnoringFieldEditor(_:)), to: nil, from: nil)
            return .handled
        }
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
