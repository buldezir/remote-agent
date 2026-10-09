import PhotosUI
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
    /// its label as an image and drops SwiftUI's font, so it gets a sized one.
    @ViewBuilder private var plus: some View {
        #if os(macOS)
        let config = NSImage.SymbolConfiguration(pointSize: 22, weight: .regular)
            .applying(.init(paletteColors: [.white, .tertiaryLabelColor]))
        Image(nsImage: NSImage(systemSymbolName: "plus.circle.fill", accessibilityDescription: nil)!
            .withSymbolConfiguration(config)!)
        #else
        Image(systemName: "plus.circle.fill")
            .font(.system(size: 26))
            .foregroundStyle(.secondary)
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
                            .foregroundStyle(.white, .red)
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

/// An item's images as thumbnails that wrap onto more lines; a tap opens one.
struct ImageGallery: View {
    let images: [ImageRef]
    let store: SessionStore
    var height: CGFloat = 140
    var alignment: HorizontalAlignment = .leading
    @State private var shown: ImageRef?

    var body: some View {
        FlowLayout(spacing: 6, alignment: alignment) {
            ForEach(images) { ref in
                Button {
                    shown = ref
                } label: {
                    RemoteImage(ref: ref, store: store, height: height)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Image")
                .accessibilityHint("Shows the image")
            }
        }
        .sheet(item: $shown) { ref in
            ImageViewer(ref: ref, store: store)
        }
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

/// An image at full size: pinch or double-click to zoom, and share it.
struct ImageViewer: View {
    let ref: ImageRef
    let store: SessionStore
    @Environment(\.dismiss) private var dismiss
    @State private var image: CGImage?
    @State private var failed: String?
    @State private var zoom: CGFloat = 1
    @GestureState private var pinch: CGFloat = 1

    var body: some View {
        NavigationStack {
            GeometryReader { geo in
                ScrollView([.horizontal, .vertical]) {
                    content
                        .frame(width: geo.size.width * zoom * pinch, height: geo.size.height * zoom * pinch)
                }
                .scrollIndicators(zoom > 1 ? .automatic : .hidden)
                .gesture(MagnifyGesture()
                    .updating($pinch) { value, state, _ in state = value.magnification }
                    .onEnded { value in zoom = min(max(zoom * value.magnification, 1), 6) })
                .onTapGesture(count: 2) { withAnimation(.snappy) { zoom = zoom > 1 ? 1 : 2.5 } }
            }
            .background(.black)
            .navigationTitle(title)
            .inlineTitle()
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Done") { dismiss() } }
                if let image {
                    ToolbarItem(placement: .primaryAction) {
                        let shared = Image(decorative: image, scale: 1)
                        ShareLink(item: shared, preview: SharePreview("Image", image: shared))
                    }
                }
            }
        }
        #if os(macOS)
        .frame(minWidth: 480, idealWidth: idealSize.width, minHeight: 360, idealHeight: idealSize.height)
        #endif
        .task { await load() }
    }

    @ViewBuilder private var content: some View {
        if let image {
            Image(decorative: image, scale: 1)
                .resizable()
                .scaledToFit()
        } else if let failed {
            Label(failed, systemImage: "photo").foregroundStyle(.secondary)
        } else {
            ProgressView()
        }
    }

    private var title: String {
        guard let w = ref.width, let h = ref.height else { return "Image" }
        return "\(w) × \(h)"
    }

    #if os(macOS)
    /// The image at its point size, within what fits on a laptop screen.
    private var idealSize: CGSize {
        let w = CGFloat(ref.width ?? 900) / 2, h = CGFloat(ref.height ?? 700) / 2 + 52
        return CGSize(width: min(max(w, 480), 1200), height: min(max(h, 360), 860))
    }
    #endif

    private func load() async {
        do {
            let data = try await store.imageData(ref.id)
            image = await Task.detached { ImageCoding.thumbnail(data, maxPixels: 8192) }.value
            if image == nil { failed = "This image can't be shown." }
        } catch {
            failed = error.localizedDescription
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
