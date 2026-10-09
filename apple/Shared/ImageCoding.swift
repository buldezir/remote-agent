import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

/// Readies images for agents, whose APIs reject large ones (Claude: 5 MB once
/// base64-encoded, 8000 px on a side), and decodes the ones shown.
enum ImageCoding {
    /// The long side of an image sent with a prompt.
    static let maxPixels = 2048
    /// Under 5 MB once base64-encoded.
    static let maxBytes = 3_500_000

    struct Prepared: Sendable {
        let data: Data
        let thumbnail: CGImage
    }

    /// Scales an image down to `maxPixels`, applies its orientation and
    /// re-encodes it without metadata (no location): PNG when it was a PNG or
    /// has transparency, so screenshots stay sharp, else JPEG.
    nonisolated static func prepare(_ data: Data) -> Prepared? {
        guard let source = CGImageSourceCreateWithData(data as CFData, nil),
              let image = thumbnail(source, maxPixels: maxPixels),
              let small = thumbnail(source, maxPixels: 240) else { return nil }
        let type = CGImageSourceGetType(source).map { UTType($0 as String) } ?? nil
        let alpha = ![.none, .noneSkipFirst, .noneSkipLast].contains(image.alphaInfo)
        if type == .png || alpha, let png = encode(image, as: .png), png.count <= maxBytes {
            return Prepared(data: png, thumbnail: small)
        }
        let opaque = alpha ? flattened(image) ?? image : image
        for quality in [0.85, 0.6] {
            if let jpeg = encode(opaque, as: .jpeg, quality: quality), jpeg.count <= maxBytes {
                return Prepared(data: jpeg, thumbnail: small)
            }
        }
        return nil
    }

    /// The image at most `maxPixels` on its long side, upright.
    nonisolated static func thumbnail(_ data: Data, maxPixels: Int) -> CGImage? {
        CGImageSourceCreateWithData(data as CFData, nil).flatMap { thumbnail($0, maxPixels: maxPixels) }
    }

    nonisolated private static func thumbnail(_ source: CGImageSource, maxPixels: Int) -> CGImage? {
        CGImageSourceCreateThumbnailAtIndex(source, 0, [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceThumbnailMaxPixelSize: maxPixels,
        ] as CFDictionary)
    }

    nonisolated private static func encode(_ image: CGImage, as type: UTType, quality: Double = 1) -> Data? {
        let out = NSMutableData()
        guard let dest = CGImageDestinationCreateWithData(out, type.identifier as CFString, 1, nil) else { return nil }
        CGImageDestinationAddImage(dest, image, [kCGImageDestinationLossyCompressionQuality: quality] as CFDictionary)
        return CGImageDestinationFinalize(dest) ? out as Data : nil
    }

    /// The image on white, for JPEG, which has no transparency.
    nonisolated private static func flattened(_ image: CGImage) -> CGImage? {
        guard let ctx = CGContext(data: nil, width: image.width, height: image.height, bitsPerComponent: 8, bytesPerRow: 0,
                                  space: CGColorSpace(name: CGColorSpace.sRGB)!,
                                  bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue) else { return nil }
        let rect = CGRect(x: 0, y: 0, width: image.width, height: image.height)
        ctx.setFillColor(CGColor(gray: 1, alpha: 1))
        ctx.fill(rect)
        ctx.draw(image, in: rect)
        return ctx.makeImage()
    }
}
