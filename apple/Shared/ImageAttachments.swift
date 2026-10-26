import Foundation
import Observation
import PhotosUI
import RAKit
import SwiftUI
import UniformTypeIdentifiers

/// Images attached to a prompt being written. Each uploads as soon as it is
/// added, so sending doesn't wait.
@MainActor @Observable
final class Attachments {
    struct Entry: Identifiable {
        let id = UUID()
        /// nil while an image that was on the server already loads.
        var thumbnail: CGImage?
        var state: State

        var ref: ImageRef? { if case .uploaded(let r) = state { r } else { nil } }
    }

    enum State {
        case uploading
        case uploaded(ImageRef)
        case failed(String)
    }

    private(set) var entries: [Entry] = []
    /// Something couldn't be attached, e.g. it wasn't an image.
    var error: String?
    @ObservationIgnored private var data: [UUID: Data] = [:]

    var isEmpty: Bool { entries.isEmpty }
    var isUploading: Bool { entries.contains { if case .uploading = $0.state { true } else { false } } }
    /// Every image is on the server, so the prompt can go.
    var ready: Bool { entries.allSatisfy { if case .uploaded = $0.state { true } else { false } } }
    var refs: [ImageRef] { entries.compactMap(\.ref) }

    func clear() {
        entries = []
        data = [:]
    }

    /// Puts back the images of a prompt that couldn't be sent, ahead of any
    /// added since. They are on the server already.
    func restore(_ sent: [Entry]) {
        entries.insert(contentsOf: sent, at: 0)
    }

    /// Puts back images that are on the server already, such as a draft's,
    /// ahead of any added since. Their thumbnails come from the server.
    func restore(_ refs: [ImageRef], via connection: ServerConnection) {
        let restored = refs.map { Entry(state: .uploaded($0)) }
        entries.insert(contentsOf: restored, at: 0)
        for (entry, ref) in zip(restored, refs) {
            Task {
                guard let data = try? await connection.imageData(ref.id),
                      let thumbnail = await Task.detached(operation: { ImageCoding.thumbnail(data, maxPixels: 240) }).value,
                      let i = entries.firstIndex(where: { $0.id == entry.id }) else { return }
                entries[i].thumbnail = thumbnail
            }
        }
    }

    func remove(_ id: UUID) {
        entries.removeAll { $0.id == id }
        data[id] = nil
    }

    /// Adds images given as encoded bytes in any format ImageIO reads.
    func add(_ images: [Data], via connection: ServerConnection) async {
        for raw in images {
            guard let prepared = await Task.detached(operation: { ImageCoding.prepare(raw) }).value else {
                error = "That isn't an image this app can read."
                continue
            }
            let entry = Entry(thumbnail: prepared.thumbnail, state: .uploading)
            entries.append(entry)
            data[entry.id] = prepared.data
            upload(entry.id, via: connection)
        }
    }

    func add(_ providers: sending [NSItemProvider], via connection: ServerConnection) async {
        await add(await Self.imageData(providers), via: connection)
    }

    func add(_ items: [PhotosPickerItem], via connection: ServerConnection) async {
        var images: [Data] = []
        for item in items {
            if let d = try? await item.loadTransferable(type: Data.self) { images.append(d) }
        }
        await add(images, via: connection)
    }

    /// Files from the file importer, which may be outside the sandbox.
    func add(_ urls: [URL], via connection: ServerConnection) async {
        let images = urls.compactMap { url -> Data? in
            let scoped = url.startAccessingSecurityScopedResource()
            defer { if scoped { url.stopAccessingSecurityScopedResource() } }
            return try? Data(contentsOf: url)
        }
        await add(images, via: connection)
    }

    func retry(_ id: UUID, via connection: ServerConnection) {
        guard let i = entries.firstIndex(where: { $0.id == id }) else { return }
        entries[i].state = .uploading
        upload(id, via: connection)
    }

    private func upload(_ id: UUID, via connection: ServerConnection) {
        guard let bytes = data[id] else { return }
        Task {
            let state: State
            do {
                state = .uploaded(try await connection.uploadImage(bytes))
            } catch {
                state = .failed(error.localizedDescription)
            }
            if let i = entries.firstIndex(where: { $0.id == id }) { entries[i].state = state }
        }
    }

    /// The images from a paste or drop: image data, or image files.
    nonisolated private static func imageData(_ providers: sending [NSItemProvider]) async -> [Data] {
        var images: [Data] = []
        for p in providers {
            if let d = await imageData(p) { images.append(d) }
        }
        return images
    }

    nonisolated private static func imageData(_ p: NSItemProvider) async -> Data? {
        if let type = p.registeredContentTypes.first(where: { $0.conforms(to: .image) }) {
            return await withCheckedContinuation { done in
                _ = p.loadDataRepresentation(for: type) { data, _ in done.resume(returning: data) }
            }
        }
        guard p.hasItemConformingToTypeIdentifier(UTType.fileURL.identifier) else { return nil }
        let url: URL? = await withCheckedContinuation { done in
            _ = p.loadDataRepresentation(for: .fileURL) { data, _ in
                done.resume(returning: data.flatMap { URL(dataRepresentation: $0, relativeTo: nil) })
            }
        }
        guard let url, UTType(filenameExtension: url.pathExtension)?.conforms(to: .image) == true else { return nil }
        return try? Data(contentsOf: url)
    }
}
