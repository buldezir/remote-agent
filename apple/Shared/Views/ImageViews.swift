import PhotosUI
import QuickLook
import RAKit
import SwiftUI
import UniformTypeIdentifiers

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
        // A paste button doesn't make iOS ask before pasting.
        PasteButton(supportedContentTypes: [.image]) { providers in
            Task { await attachments.add(providers, via: connection) }
        }
    }

    /// The size of the round send and mic buttons beside it.
    private var plus: some View {
        Image(systemName: "plus.circle.fill")
            .font(.system(size: Metrics.roundButton))
            .foregroundStyle(Palette.overlay)
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
        picture(entry)
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

    /// The thumbnail, or a placeholder while one on the server loads.
    @ViewBuilder private func picture(_ entry: Attachments.Entry) -> some View {
        if let thumbnail = entry.thumbnail {
            Image(decorative: thumbnail, scale: 1)
                .resizable()
                .scaledToFill()
        } else {
            ZStack {
                Rectangle().fill(.quaternary)
                Image(systemName: "photo").foregroundStyle(.secondary)
            }
        }
    }
}

extension View {
    /// Attaches images dropped on the view.
    func acceptsImages(_ attachments: Attachments, via connection: ServerConnection) -> some View {
        onDrop(of: [.image, .fileURL], isTargeted: nil) { providers in
            Task { await attachments.add(providers, via: connection) }
            return true
        }
    }
}

// MARK: Showing

/// An item's images as thumbnails that wrap onto more lines; a tap opens one
/// in Quick Look, which zooms and shares it, full screen, where a swipe down
/// closes it. The item's other images are a swipe or an arrow key away.
struct ImageGallery: View {
    let images: [ImageRef]
    let store: SessionStore
    var height: CGFloat = 140
    var alignment: HorizontalAlignment = .leading
    /// The images Quick Look pages through, when the gallery shows only some
    /// of the item's: a reply shows each where its text links to it.
    var browsing: [ImageRef]?
    @Environment(\.imagePreview) private var preview
    @State private var opening = false

    var body: some View {
        FlowLayout(spacing: 6, alignment: alignment) {
            ForEach(images) { ref in
                Button {
                    Task { await open(ref) }
                } label: {
                    RemoteImage(ref: ref, store: store, height: height)
                        .onGeometryChange(for: CGRect.self) { $0.frame(in: .global) } action: { preview?.thumbnails[ref.id] = $0 }
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Image")
                .accessibilityHint("Shows the image")
            }
        }
    }

    /// Quick Look takes files, so the images are saved first; they are
    /// usually cached already, since their thumbnails are showing.
    private func open(_ ref: ImageRef) async {
        guard !opening else { return }
        opening = true
        defer { opening = false }
        var files: [URL] = []
        var ids: [String] = []
        var target: URL?
        for image in browsing ?? images {
            guard let url = try? await store.imageFile(image.id) else { continue }
            files.append(url)
            ids.append(image.id)
            if image.id == ref.id { target = url }
        }
        guard let target else { return }
        preview?.show(target, among: files, ids: ids)
    }
}

/// The image Quick Look shows for a transcript's galleries. They share one
/// preview, which a tap on another image points elsewhere, so there is one
/// preview to close.
@MainActor @Observable
final class ImagePreview {
    /// Where each image's thumbnail is in the window, by id, for Quick Look to
    /// zoom from and back to. Not observed: it changes as the transcript scrolls.
    @ObservationIgnored var thumbnails: [String: CGRect] = [:]
    @ObservationIgnored private let presenter = QuickLookPresenter()

    /// Shows `url` among `files`, the images with those `ids`.
    func show(_ url: URL, among files: [URL], ids: [String]) {
        var sources: [URL: CGRect] = [:]
        for (file, id) in zip(files, ids) { sources[file] = thumbnails[id] }
        presenter.show(url, among: files, from: sources)
    }
}

/// Presents Quick Look from UIKit, zooming from the image's thumbnail and back
/// to it. The zoom also lets a swipe down close it; SwiftUI's
/// `quickLookPreview` has neither.
@MainActor
private final class QuickLookPresenter: NSObject, @MainActor QLPreviewControllerDataSource, @MainActor QLPreviewControllerDelegate {
    private var files: [URL] = []
    private var sources: [URL: CGRect] = [:]
    private weak var window: UIWindow?

    func show(_ url: URL, among files: [URL], from sources: [URL: CGRect]) {
        guard let window = UIApplication.shared.connectedScenes.lazy.compactMap({ ($0 as? UIWindowScene)?.keyWindow }).first,
              var top = window.rootViewController else { return }
        // Over a sheet, when one is open.
        while let presented = top.presentedViewController { top = presented }
        self.files = files
        self.sources = sources
        self.window = window
        let controller = QLPreviewController()
        controller.dataSource = self
        controller.delegate = self
        controller.currentPreviewItemIndex = files.firstIndex(of: url) ?? 0
        top.present(controller, animated: true)
    }

    func numberOfPreviewItems(in controller: QLPreviewController) -> Int { files.count }

    func previewController(_ controller: QLPreviewController, previewItemAt index: Int) -> any QLPreviewItem { files[index] as NSURL }

    /// The thumbnail, while it's on screen. Without one Quick Look fades.
    func previewController(_ controller: QLPreviewController, frameFor item: any QLPreviewItem,
                           inSourceView view: AutoreleasingUnsafeMutablePointer<UIView?>) -> CGRect {
        guard let window, let url = item.previewItemURL, let frame = sources[url], window.bounds.intersects(frame) else { return .zero }
        view.pointee = window
        return frame
    }

    func previewController(_ controller: QLPreviewController, transitionImageFor item: any QLPreviewItem,
                           contentRect: UnsafeMutablePointer<CGRect>) -> UIImage? {
        item.previewItemURL.flatMap { UIImage(contentsOfFile: $0.path) }
    }
}

extension EnvironmentValues {
    @Entry var imagePreview: ImagePreview? = nil
}

extension View {
    /// Shows the images that the galleries inside open, in one Quick Look
    /// preview, which a swipe down closes.
    func imagePreviews() -> some View { modifier(ImagePreviewHost()) }
}

private struct ImagePreviewHost: ViewModifier {
    @State private var preview = ImagePreview()

    func body(content: Content) -> some View {
        content
            .environment(\.imagePreview, preview)
    }
}

/// An image an agent's reply links to: as large as fits the transcript's
/// width, up to a height that leaves the text around it in view.
struct ReplyImage: View {
    let ref: ImageRef
    let images: [ImageRef]
    let store: SessionStore

    var body: some View {
        ViewThatFits(in: .horizontal) {
            gallery(height: 320)
            gallery(height: 240)
            gallery(height: 180)
            gallery(height: 120)
        }
    }

    private func gallery(height: CGFloat) -> some View {
        ImageGallery(images: [ref], store: store, height: height, browsing: images)
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
