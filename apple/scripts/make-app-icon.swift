// Renders the app icon into an .appiconset: a chat bubble with a terminal
// prompt cut out of it, on the accent colour. iOS rounds the corners itself;
// the Mac icons are drawn in the macOS shape, with its margin and shadow.
// --server draws only the Mac sizes, for Remote Agent Server: an accent
// bubble on graphite, so the two apps are easy to tell apart.
//
//   cd apple && swift scripts/make-app-icon.swift Shared/Assets.xcassets/AppIcon.appiconset
//   cd apple && swift scripts/make-app-icon.swift --server ServerApp/Assets.xcassets/AppIcon.appiconset
import CoreGraphics
import Foundation
import ImageIO
import SwiftUI
import UniformTypeIdentifiers

let size = 1024
let srgb = CGColorSpace(name: CGColorSpace.sRGB)!

func color(_ r: CGFloat, _ g: CGFloat, _ b: CGFloat, _ a: CGFloat = 1) -> CGColor {
    CGColor(srgbRed: r, green: g, blue: b, alpha: a)
}

// Light and deep ends of AccentColor (0.91, 0.45, 0.38).
let accent = CGGradient(colorsSpace: srgb, colors: [color(0.97, 0.60, 0.47), color(0.85, 0.32, 0.30)] as CFArray, locations: [0, 1])!

func fillAccent(_ ctx: CGContext) {
    ctx.drawLinearGradient(accent, start: .zero, end: CGPoint(x: size, y: size), options: [])
}

let graphite = CGGradient(colorsSpace: srgb, colors: [color(0.30, 0.32, 0.36), color(0.13, 0.14, 0.17)] as CFArray, locations: [0, 1])!

func fillGraphite(_ ctx: CGContext) {
    ctx.drawLinearGradient(graphite, start: .zero, end: CGPoint(x: size, y: size), options: [])
}

// Shapes, in a 1024-point space with the origin at the top left.

let body = CGPath(roundedRect: CGRect(x: 188, y: 226, width: 648, height: 472), cornerWidth: 144, cornerHeight: 144, transform: nil)

let tail: CGPath = {
    let p = CGMutablePath()
    p.move(to: CGPoint(x: 290, y: 640))
    p.addCurve(to: CGPoint(x: 222, y: 826), control1: CGPoint(x: 298, y: 748), control2: CGPoint(x: 268, y: 800))
    p.addCurve(to: CGPoint(x: 468, y: 680), control1: CGPoint(x: 330, y: 820), control2: CGPoint(x: 420, y: 768))
    p.addLine(to: CGPoint(x: 468, y: 640))
    p.closeSubpath()
    return p
}()

/// ">_", centred in the bubble body.
let prompt: CGPath = {
    let k: CGFloat = 0.74, w: CGFloat = 424, h: CGFloat = 280
    let o = CGPoint(x: 512 - w * k / 2, y: 462 - h * k / 2)
    func pt(_ x: CGFloat, _ y: CGFloat) -> CGPoint { CGPoint(x: o.x + x * k, y: o.y + y * k) }
    let p = CGMutablePath()
    p.move(to: pt(0, 0)); p.addLine(to: pt(160, 140)); p.addLine(to: pt(0, 280))
    p.move(to: pt(244, 280)); p.addLine(to: pt(424, 280))
    return p.copy(strokingWithWidth: 70, lineCap: .round, lineJoin: .round, miterLimit: 10)
}()

/// The bubble, painted by fill, with the prompt left see-through.
func drawBubble(_ ctx: CGContext, fill: (CGContext) -> Void) {
    ctx.beginTransparencyLayer(auxiliaryInfo: nil)
    for part in [body, tail] {
        ctx.saveGState()
        ctx.addPath(part)
        ctx.clip()
        fill(ctx)
        ctx.restoreGState()
    }
    ctx.setBlendMode(.clear)
    ctx.addPath(prompt)
    ctx.fillPath()
    ctx.endTransparencyLayer()
}

func white(_ ctx: CGContext) {
    ctx.setFillColor(color(1, 1, 1))
    ctx.fill(CGRect(x: 0, y: 0, width: size, height: size))
}

func image(opaque: Bool, draw: (CGContext) -> Void) -> CGImage {
    // The App Store rejects an alpha channel in the main iOS icon.
    let alpha: CGImageAlphaInfo = opaque ? .noneSkipLast : .premultipliedLast
    let ctx = CGContext(data: nil, width: size, height: size, bitsPerComponent: 8, bytesPerRow: 0,
                        space: srgb, bitmapInfo: alpha.rawValue)!
    ctx.translateBy(x: 0, y: CGFloat(size))
    ctx.scaleBy(x: 1, y: -1)
    draw(ctx)
    return ctx.makeImage()!
}

func write(_ image: CGImage, _ name: String, in dir: URL, pixels: Int = size) {
    var image = image
    if pixels != size {
        let ctx = CGContext(data: nil, width: pixels, height: pixels, bitsPerComponent: 8, bytesPerRow: 0,
                            space: srgb, bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
        ctx.interpolationQuality = .high
        ctx.draw(image, in: CGRect(x: 0, y: 0, width: pixels, height: pixels))
        image = ctx.makeImage()!
    }
    let url = dir.appendingPathComponent(name)
    let dest = CGImageDestinationCreateWithURL(url as CFURL, UTType.png.identifier as CFString, 1, nil)!
    CGImageDestinationAddImage(dest, image, nil)
    guard CGImageDestinationFinalize(dest) else { fatalError("could not write \(url.path)") }
    print(url.path)
}

func render(_ name: String, opaque: Bool, in dir: URL, draw: (CGContext) -> Void) {
    write(image(opaque: opaque, draw: draw), name, in: dir)
}

/// The iOS icon shrunk into the macOS shape: a rounded square 824 points
/// wide with continuous corners, centred, over a soft shadow. The server
/// app's icon swaps the colours: an accent bubble on graphite.
func drawMac(_ ctx: CGContext, server: Bool) {
    let square = CGRect(x: 100, y: 100, width: 824, height: 824)
    let shape = RoundedRectangle(cornerRadius: 185.4, style: .continuous).path(in: square).cgPath
    ctx.saveGState()
    ctx.setShadow(offset: CGSize(width: 0, height: -10), blur: 20, color: color(0, 0, 0, 0.3))
    ctx.addPath(shape)
    ctx.setFillColor(server ? color(0.13, 0.14, 0.17) : color(0.85, 0.32, 0.30))
    ctx.fillPath()
    ctx.restoreGState()
    ctx.addPath(shape)
    ctx.clip()
    ctx.translateBy(x: square.minX, y: square.minY)
    ctx.scaleBy(x: square.width / CGFloat(size), y: square.height / CGFloat(size))
    if server {
        fillGraphite(ctx)
        ctx.setShadow(offset: CGSize(width: 0, height: -8), blur: 22, color: color(0, 0, 0, 0.35))
        drawBubble(ctx, fill: fillAccent)
    } else {
        fillAccent(ctx)
        ctx.setShadow(offset: CGSize(width: 0, height: -8), blur: 22, color: color(0.45, 0.08, 0.06, 0.25))
        drawBubble(ctx, fill: white)
    }
}

/// The Mac takes a PNG per size.
func writeMac(server: Bool, in dir: URL) {
    let mac = image(opaque: false) { drawMac($0, server: server) }
    for points in [16, 32, 128, 256, 512] {
        write(mac, "AppIcon-Mac-\(points).png", in: dir, pixels: points)
        write(mac, "AppIcon-Mac-\(points)@2x.png", in: dir, pixels: points * 2)
    }
}

let args = CommandLine.arguments.dropFirst()
let server = args.first == "--server"
guard args.count == (server ? 2 : 1) else {
    print("usage: swift make-app-icon.swift [--server] <AppIcon.appiconset>")
    exit(2)
}
let dir = URL(fileURLWithPath: args.last!)
if server {
    writeMac(server: true, in: dir)
    exit(0)
}

render("AppIcon.png", opaque: true, in: dir) { ctx in
    fillAccent(ctx)
    ctx.setShadow(offset: CGSize(width: 0, height: -10), blur: 28, color: color(0.45, 0.08, 0.06, 0.25))
    drawBubble(ctx, fill: white)
}
// Dark and tinted icons are transparent: iOS draws the background.
render("AppIcon-Dark.png", opaque: false, in: dir) { ctx in
    drawBubble(ctx, fill: fillAccent)
}
render("AppIcon-Tinted.png", opaque: false, in: dir) { ctx in
    drawBubble(ctx, fill: white)
}
writeMac(server: false, in: dir)
