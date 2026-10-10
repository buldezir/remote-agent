import PhotosUI
import QuickLook
import RAKit
import SwiftUI
import UniformTypeIdentifiers
#if os(macOS)
import AppKit
#endif

// MARK: Attaching

/// The composer's + button: photos, files or the pasteboard. With a title,
/// it is a labeled row instead, for forms.
struct AttachMenu: View {
    let attachments: Attachments
    let connection: ServerConnection
    var title: String?
    @State private var pickingPhotos = false
    @State private var pickingFiles = false
    @State private var photos: [PhotosPickerItem] = []

    var body: some View {
        menu
            .photosPicker(isPresented: $pickingPhotos, selection: $photos, maxSelectionCount: 10, matching: .images)
            .onChange(of: photos) { _, items in
                guard !items.isEmpty else { return }
                photos = []
                Task { await attachments.add(items, via: connection) }
            }
            .fileImporter(isPresented: $pickingFiles, allowedContentTypes: [.image], allowsMultipleSelection: true) { result in
                if case .success(let urls) = result {
                    Task { await attachments.add(urls, via: connection) }
                }
            }
    }
}

extension AttachMenu {
    @ViewBuilder private var menu: some View {
        if let title {
            Menu { items } label: { Label(title, systemImage: "photo.badge.plus") }
        } else {
            Menu { items } label: { plus.accessibilityLabel("Add images") }
                .menuStyle(.button)
                .buttonStyle(.borderless)
                .menuIndicator(.hidden)
                .fixedSize()
                #if os(macOS)
                .tint(Palette.overlay) // The symbol's colour; iOS sets it on the image.
                #endif
                .roundButtonFrame()
        }
    }

    @ViewBuilder private var items: some View {
        Button("Photo Library", systemImage: "photo.on.rectangle") { pickingPhotos = true }
        Button("Files", systemImage: "folder") { pickingFiles = true }
        #if os(iOS)
        // A paste button doesn't make iOS ask before pasting.
        PasteButton(supportedContentTypes: [.image]) { providers in
            Task { await attachments.add(providers, via: connection) }
        }
        #else
        Button("Paste Image", systemImage: "doc.on.clipboard") {
            if let images = Platform.pasteboardImages() {
                Task { await attachments.add(images, via: connection) }
            }
        }
        .disabled(!Platform.pasteboardHasImages)
        #endif
    }

    /// The size of the round send and mic buttons beside it. A Mac menu draws
    /// its label as an image and drops SwiftUI's font and colour, so it gets a
    /// sized one, in the menu's tint.
    @ViewBuilder private var plus: some View {
        #if os(macOS)
        let config = NSImage.SymbolConfiguration(pointSize: Metrics.roundButton, weight: .regular)
        Image(nsImage: NSImage(systemSymbolName: "plus.circle.fill", accessibilityDescription: nil)!
            .withSymbolConfiguration(config)!)
        #else
        Image(systemName: "plus.circle.fill")
            .font(.system(size: Metrics.roundButton))
            .foregroundStyle(Palette.overlay)
        #endif
    }
}

/// Thumbnails of the images attached to a prompt being written.
struct AttachmentStrip: View {
    let attachments: Attachments
    let connection: ServerConnection

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                ForEach(attachments.entries) { entry in
                    thumbnail(entry)
                }
            }
            .padding(.top, 6)
            .padding(.trailing, 6)
        }
    }

    private func thumbnail(_ entry: Attachments.Entry) -> some View {
        Image(decorative: entry.thumbnail, scale: 1)
            .resizable()
            .scaledToFill()
            .frame(width: 56, height: 56)
            .clipShape(RoundedRectangle(cornerRadius: Metrics.Corner.card))
            .overlay {
                switch entry.state {
                case .uploading:
                    ProgressView().controlSize(.small)
                case .uploaded:
                    EmptyView()
                case .failed(let message):
                    Button {
                        attachments.retry(entry.id, via: connection)
                    } label: {
                        Image(systemName: "exclamationmark.arrow.circlepath")
                            .font(.title3)
                            .foregroundStyle(.white, Palette.red)
                            .frame(maxWidth: .infinity, maxHeight: .infinity)
                            .background(.black.opacity(0.35))
                    }
                    .buttonStyle(.plain)
                    .help("\(message) Click to try again.")
                    .accessibilityLabel("Upload failed: \(message). Try again")
                }
            }
            .overlay(alignment: .topTrailing) {
                Button {
                    attachments.remove(entry.id)
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .symbolRenderingMode(.palette)
                        .foregroundStyle(.white, .black.opacity(0.6))
                }
                .buttonStyle(.plain)
                .offset(x: 6, y: -6)
                .accessibilityLabel("Remove image")
            }
    }
}

extension View {
    /// Attaches images dropped on the view, and, on the Mac, images pasted
    /// with ⌘V while `pasting` (the prompt field has focus).
    func acceptsImages(_ attachments: Attachments, via connection: ServerConnection, pasting: Bool) -> some View {
        self
            .onDrop(of: [.image, .fileURL], isTargeted: nil) { providers in
                Task { await attachments.add(providers, via: connection) }
                return true
            }
        #if os(macOS)
            .background {
                PasteCatcher(enabled: pasting) { images in
                    Task { await attachments.add(images, via: connection) }
                }
            }
        #endif
    }
}

#if os(macOS)
/// Catches ⌘V when the pasteboard holds only images. A text field ignores
/// those, and it handles the paste before any SwiftUI view could see it.
private struct PasteCatcher: NSViewRepresentable {
    var enabled: Bool
    var onPaste: ([Data]) -> Void

    func makeNSView(context: Context) -> CatcherView { CatcherView() }

    func updateNSView(_ view: CatcherView, context: Context) {
        view.enabled = enabled
        view.onPaste = onPaste
    }

    final class CatcherView: NSView {
        var enabled = false
        var onPaste: (([Data]) -> Void)?
        private var monitor: Any?

        override func viewDidMoveToWindow() {
            if let monitor { NSEvent.removeMonitor(monitor) }
            monitor = nil
            guard window != nil else { return }
            monitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { [weak self] event in
                guard let self, enabled, event.window === window,
                      event.modifierFlags.intersection(.deviceIndependentFlagsMask) == .command,
                      event.charactersIgnoringModifiers == "v",
                      let images = Platform.pasteboardImages(unlessText: true) else { return event }
                onPaste?(images)
                return nil
            }
        }
    }
}
#endif

// MARK: Showing

/// An item's images as thumbnails that wrap onto more lines; a tap opens one
/// in Quick Look, which zooms and shares it: full screen on iOS, and in a
/// window that resizes on the Mac. The item's other images are a swipe or an
/// arrow key away.
struct ImageGallery: View {
    let images: [ImageRef]
    let store: SessionStore
    var height: CGFloat = 140
    var alignment: HorizontalAlignment = .leading
    @State private var files: [URL] = []
    @State private var shown: URL?
    @State private var opening = false

    var body: some View {
        FlowLayout(spacing: 6, alignment: alignment) {
            ForEach(images) { ref in
                Button {
                    Task { await open(ref) }
                } label: {
                    RemoteImage(ref: ref, store: store, height: height)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Image")
                .accessibilityHint("Shows the image")
            }
        }
        .quickLookPreview($shown, in: files)
    }

    /// Quick Look takes files, so the images are saved first; they are
    /// usually cached already, since their thumbnails are showing.
    private func open(_ ref: ImageRef) async {
        guard !opening else { return }
        opening = true
        defer { opening = false }
        var files: [URL] = []
        var target: URL?
        for image in images {
            guard let url = try? await store.imageFile(image.id) else { continue }
            files.append(url)
            if image.id == ref.id { target = url }
        }
        guard let target else { return }
        self.files = files
        shown = target
    }
}

/// Decoded thumbnails, so rows that scroll back into view don't flicker.
@MainActor
private enum Thumbnails {
    static let cache: NSCache<NSString, CGImage> = {
        let c = NSCache<NSString, CGImage>()
        c.countLimit = 200
        return c
    }()
}

/// One image from the server, at a fixed height and its own width.
struct RemoteImage: View {
    let ref: ImageRef
    let store: SessionStore
    let height: CGFloat
    @Environment(\.displayScale) private var scale
    @State private var image: CGImage?
    @State private var failed = false

    /// Very wide images are cropped to twice the height.
    private var width: CGFloat {
        let ratio = ref.aspectRatio ?? image.map { Double($0.width) / Double($0.height) } ?? 1
        return height * min(max(ratio, 0.5), 2)
    }

    private var key: NSString { "\(ref.id)@\(Int(height * scale))" as NSString }

    var body: some View {
        ZStack {
            if let image = image ?? Thumbnails.cache.object(forKey: key) {
                Image(decorative: image, scale: scale)
                    .resizable()
                    .scaledToFill()
            } else {
                Rectangle().fill(.quaternary)
                if failed {
                    Image(systemName: "photo").foregroundStyle(.secondary)
                } else {
                    ProgressView().controlSize(.small)
                }
            }
        }
        .frame(width: width, height: height)
        .clipShape(RoundedRectangle(cornerRadius: Metrics.Corner.card))
        .contentShape(Rectangle())
        .task(id: store.isConnected) { await load() }
    }

    private func load() async {
        if let cached = Thumbnails.cache.object(forKey: key) {
            image = cached
            return
        }
        guard image == nil, store.isConnected else { return }
        do {
            let data = try await store.imageData(ref.id)
            // Twice the frame, so cropped wide or tall images still fill it.
            let pixels = Int(max(width, height) * scale * 2)
            let thumb = await Task.detached { ImageCoding.thumbnail(data, maxPixels: pixels) }.value
            if let thumb { Thumbnails.cache.setObject(thumb, forKey: key) }
            image = thumb
            failed = thumb == nil
        } catch {
            failed = true
        }
    }
}

/// Lays views out in rows, wrapping when a row is full.
struct FlowLayout: Layout {
    var spacing: CGFloat
    var alignment: HorizontalAlignment = .leading

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let rows = rows(subviews, width: proposal.width ?? .infinity)
        let width = rows.map(\.width).max() ?? 0
        let height = rows.map(\.height).reduce(0, +) + spacing * CGFloat(max(rows.count - 1, 0))
        return CGSize(width: proposal.width.map { min($0, width) } ?? width, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var y = bounds.minY
        for row in rows(subviews, width: bounds.width) {
            var x = switch alignment {
            case .trailing: bounds.maxX - row.width
            case .center: bounds.midX - row.width / 2
            default: bounds.minX
            }
            for i in row.indices {
                let size = subviews[i].sizeThatFits(.unspecified)
                subviews[i].place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(size))
                x += size.width + spacing
            }
            y += row.height + spacing
        }
    }

    private struct Row {
        var indices: [Int] = []
        var width: CGFloat = 0
        var height: CGFloat = 0
    }

    private func rows(_ subviews: Subviews, width: CGFloat) -> [Row] {
        var rows: [Row] = []
        var row = Row()
        for (i, view) in subviews.enumerated() {
            let size = view.sizeThatFits(.unspecified)
            if !row.indices.isEmpty && row.width + spacing + size.width > width {
                rows.append(row)
                row = Row()
            }
            row.width += (row.indices.isEmpty ? 0 : spacing) + size.width
            row.height = max(row.height, size.height)
            row.indices.append(i)
        }
        if !row.indices.isEmpty { rows.append(row) }
        return rows
    }
}
