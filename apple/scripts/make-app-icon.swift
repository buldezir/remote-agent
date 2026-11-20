// Renders the app icon into an .appiconset: a little robot, a handheld screen
// with a glowing face and two dish ears, on the accent colour. iOS rounds the
// corners itself; the Mac icons are drawn in the macOS shape, with its margin
// and shadow. --server draws only the Mac sizes, for Remote Agent Server: the
// same robot on graphite, so the two apps are easy to tell apart. --android
// draws the Android app's adaptive icon layers into its res folder.
//
//   cd apple && swift scripts/make-app-icon.swift Shared/Assets.xcassets/AppIcon.appiconset
//   cd apple && swift scripts/make-app-icon.swift --server ServerApp/Assets.xcassets/AppIcon.appiconset
//   cd apple && swift scripts/make-app-icon.swift --android ../android/app/src/main/res
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

func hex(_ v: UInt32, _ a: CGFloat = 1) -> CGColor {
    color(CGFloat(v >> 16 & 0xFF) / 255, CGFloat(v >> 8 & 0xFF) / 255, CGFloat(v & 0xFF) / 255, a)
}

func gradient(_ colors: [CGColor]) -> CGGradient {
    CGGradient(colorsSpace: srgb, colors: colors as CFArray, locations: nil)!
}

// The art is drawn in "tile space": the macOS icon's square, 824 points wide
// at (100, 100) in a 1024-point canvas, with the origin at the top left. The
// Mac draws it as it is; iOS and Android scale the square up to their canvas.
let tile = CGRect(x: 100, y: 100, width: 824, height: 824)

/// How many device pixels a point of user space covers, for shadows, which
/// Core Graphics measures in device space.
func deviceScale(_ ctx: CGContext) -> CGFloat {
    let t = ctx.userSpaceToDeviceSpaceTransform
    return sqrt(abs(t.a * t.d - t.b * t.c))
}

/// Draws only the blur of what `draw` paints, in `glow`: the shapes are drawn
/// far to the right, and their shadow is cast back into place.
func glow(_ ctx: CGContext, _ glow: CGColor, radius: CGFloat, _ draw: (CGContext) -> Void) {
    let s = deviceScale(ctx), away: CGFloat = 4096
    ctx.saveGState()
    ctx.setShadow(offset: CGSize(width: -away * s, height: 0), blur: radius * 2 * s, color: glow)
    ctx.translateBy(x: away, y: 0)
    draw(ctx)
    ctx.restoreGState()
}

func fill(_ ctx: CGContext, _ path: CGPath, _ c: CGColor) {
    ctx.addPath(path)
    ctx.setFillColor(c)
    ctx.fillPath()
}

func stroke(_ ctx: CGContext, _ path: CGPath, _ c: CGColor, width: CGFloat) {
    ctx.addPath(path)
    ctx.setStrokeColor(c)
    ctx.setLineWidth(width)
    ctx.setLineCap(.round)
    ctx.strokePath()
}

/// Fills a path with a vertical gradient from its top to its bottom.
func fillVertical(_ ctx: CGContext, _ path: CGPath, _ g: CGGradient) {
    let box = path.boundingBox
    ctx.saveGState()
    ctx.addPath(path)
    ctx.clip()
    ctx.drawLinearGradient(g, start: CGPoint(x: box.midX, y: box.minY), end: CGPoint(x: box.midX, y: box.maxY), options: [])
    ctx.restoreGState()
}

func circle(_ c: CGPoint, _ r: CGFloat) -> CGPath {
    CGPath(ellipseIn: CGRect(x: c.x - r, y: c.y - r, width: 2 * r, height: 2 * r), transform: nil)
}

func rounded(_ rect: CGRect, _ r: CGFloat) -> CGPath {
    CGPath(roundedRect: rect, cornerWidth: r, cornerHeight: r, transform: nil)
}

// Palette.
let coral = gradient([hex(0xF89A78), hex(0xD9474B)])
let graphite = gradient([color(0.30, 0.32, 0.36), color(0.13, 0.14, 0.17)])
let shell = gradient([hex(0xFAFAFA), hex(0xB9BBC3)])
let earShell = gradient([hex(0xFFFFFF), hex(0xB3B5BD)])
let screen = gradient([hex(0x3A2547), hex(0x140D1D)])
let ink = hex(0x1A1A20), earInk = hex(0x2A1D3A)
let face = hex(0xFFB59A), faceGlow = hex(0xFF7A5C), mouth = hex(0xFF9A7C)

// The robot's shapes, in tile space.
let device = CGRect(x: 212, y: 316, width: 600, height: 520)
let deviceRadius: CGFloat = 130
let bezel = device.insetBy(dx: 48 * 600 / 824, dy: 48 * 600 / 824)
let screenRect = device.insetBy(dx: 80 * 600 / 824, dy: 80 * 600 / 824)
let screenRadius = deviceRadius - 80 * 600 / 824 + 7
let ears = [CGPoint(x: 286, y: 290), CGPoint(x: 738, y: 290)]

let eyes: [CGPath] = {
    let k: CGFloat = 0.82, w = 100 * k, h = 184 * k, gap = 104 * k
    let y = 590 - h / 2 - 40 * k
    return [512 - gap / 2 - w, 512 + gap / 2].map { rounded(CGRect(x: $0, y: y, width: w, height: h), w / 2) }
}()

let smile: CGPath = {
    let k: CGFloat = 0.82, y = 590 + 110 * k, w = 62 * k
    let p = CGMutablePath()
    p.move(to: CGPoint(x: 512 - w, y: y))
    p.addQuadCurve(to: CGPoint(x: 512 + w, y: y), control: CGPoint(x: 512, y: y + 44 * k))
    return p
}()

/// The tile's background: the accent, or graphite for the Server app, with a
/// soft light from the top left.
func drawBackground(_ ctx: CGContext, server: Bool, in rect: CGRect = tile) {
    ctx.saveGState()
    ctx.clip(to: rect)
    ctx.drawLinearGradient(server ? graphite : coral, start: CGPoint(x: rect.minX, y: rect.minY), end: CGPoint(x: rect.maxX, y: rect.maxY), options: [])
    let light = gradient([color(1, 1, 1, 0.22), color(1, 1, 1, 0)])
    let centre = CGPoint(x: rect.minX + rect.width * 0.25, y: rect.minY + rect.height * 0.05)
    ctx.drawRadialGradient(light, startCenter: centre, startRadius: 0, endCenter: centre, endRadius: rect.width * 0.7, options: [])
    ctx.restoreGState()
}

/// A dish ear: a chrome disc with rings glowing in it.
func drawEar(_ ctx: CGContext, at c: CGPoint) {
    fillVertical(ctx, circle(c, 112.8), earShell)
    fill(ctx, circle(c, 79.2), earInk)
    glow(ctx, faceGlow.copy(alpha: 0.35)!, radius: 14) { stroke($0, circle(c, 67.2), faceGlow, width: 12) }
    stroke(ctx, circle(c, 52.8), face.copy(alpha: 0.55)!, width: 11)
    stroke(ctx, circle(c, 26.4), face.copy(alpha: 0.85)!, width: 11)
    fill(ctx, circle(c, 9.6), face)
}

/// The handheld: a chrome case, a dark bezel and a screen with a dot grid,
/// scanlines and the face.
func drawDevice(_ ctx: CGContext) {
    fillVertical(ctx, rounded(device, deviceRadius), shell)
    fill(ctx, rounded(bezel, deviceRadius - 48 * 600 / 824), ink)

    ctx.saveGState()
    ctx.addPath(rounded(screenRect, screenRadius))
    ctx.clip()
    let centre = CGPoint(x: device.midX, y: device.minY + device.height * 0.45)
    ctx.drawRadialGradient(screen, startCenter: centre, startRadius: 0, endCenter: centre, endRadius: 400, options: [.drawsAfterEndLocation])
    ctx.setFillColor(hex(0xF4A08A, 0.16))
    for y in stride(from: device.minY + 50, to: device.maxY, by: 24) {
        for x in stride(from: device.minX + 50, to: device.maxX, by: 24) {
            ctx.addPath(circle(CGPoint(x: x, y: y), 2.6))
        }
    }
    ctx.fillPath()

    glow(ctx, faceGlow, radius: 26) { c in eyes.forEach { fill(c, $0, faceGlow) } }
    eyes.forEach { fill(ctx, $0, face) }
    glow(ctx, faceGlow.copy(alpha: 0.8)!, radius: 14) { stroke($0, smile, faceGlow, width: 30 * 0.82) }
    stroke(ctx, smile, mouth, width: 24 * 0.82)

    ctx.setFillColor(color(0, 0, 0, 0.2))
    for y in stride(from: device.minY, to: device.maxY, by: 8) {
        ctx.fill(CGRect(x: device.minX, y: y, width: device.width, height: 3))
    }
    let sheen = CGMutablePath()
    sheen.move(to: CGPoint(x: device.minX, y: device.minY + device.height * 0.23))
    sheen.addQuadCurve(to: CGPoint(x: device.maxX, y: device.minY + device.height * 0.23), control: CGPoint(x: device.midX, y: device.minY + device.height * 0.12))
    sheen.addLine(to: CGPoint(x: device.maxX, y: device.minY))
    sheen.addLine(to: CGPoint(x: device.minX, y: device.minY))
    sheen.closeSubpath()
    fill(ctx, sheen, color(1, 1, 1, 0.05))
    ctx.restoreGState()
}

/// The robot, over a soft shadow of the given colour.
func drawRobot(_ ctx: CGContext, shadow: CGColor) {
    let s = deviceScale(ctx)
    ctx.saveGState()
    ctx.setShadow(offset: CGSize(width: 0, height: -22 * s), blur: 44 * s, color: shadow)
    ctx.beginTransparencyLayer(auxiliaryInfo: nil)
    ears.forEach { drawEar(ctx, at: $0) }
    drawDevice(ctx)
    ctx.endTransparencyLayer()
    ctx.restoreGState()
}

/// The robot in one colour, for tinted and themed icons: the case, the ears'
/// rims and rings and the face in white, the screen and the ears' insides
/// see-through.
func drawRobotMono(_ ctx: CGContext) {
    let white = color(1, 1, 1)
    ctx.beginTransparencyLayer(auxiliaryInfo: nil)
    for c in ears {
        fill(ctx, circle(c, 112.8), white)
        ctx.setBlendMode(.clear)
        fill(ctx, circle(c, 79.2), white)
        ctx.setBlendMode(.normal)
        stroke(ctx, circle(c, 52.8), color(1, 1, 1, 0.6), width: 11)
        stroke(ctx, circle(c, 26.4), white, width: 11)
        fill(ctx, circle(c, 9.6), white)
    }
    fill(ctx, rounded(device, deviceRadius), white)
    ctx.setBlendMode(.clear)
    fill(ctx, rounded(screenRect, screenRadius), white)
    ctx.setBlendMode(.normal)
    eyes.forEach { fill(ctx, $0, white) }
    stroke(ctx, smile, white, width: 24 * 0.82)
    ctx.endTransparencyLayer()
}

/// Maps tile space onto a square `side` points wide at `origin`.
func fitTile(_ ctx: CGContext, side: CGFloat = CGFloat(size), origin: CGPoint = .zero) {
    ctx.translateBy(x: origin.x, y: origin.y)
    ctx.scaleBy(x: side / tile.width, y: side / tile.height)
    ctx.translateBy(x: -tile.minX, y: -tile.minY)
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

/// The macOS shape: a rounded square 824 points wide with continuous
/// corners, centred, over a soft shadow, with the tile drawn in it.
func drawMac(_ ctx: CGContext, server: Bool) {
    let shape = RoundedRectangle(cornerRadius: 185.4, style: .continuous).path(in: tile).cgPath
    ctx.saveGState()
    ctx.setShadow(offset: CGSize(width: 0, height: -10), blur: 20, color: color(0, 0, 0, 0.3))
    ctx.addPath(shape)
    ctx.setFillColor(server ? color(0.13, 0.14, 0.17) : hex(0xD9474B))
    ctx.fillPath()
    ctx.restoreGState()
    ctx.addPath(shape)
    ctx.clip()
    drawBackground(ctx, server: server)
    drawRobot(ctx, shadow: server ? color(0, 0, 0, 0.45) : hex(0x5A1010, 0.4))
}

/// The Mac takes a PNG per size.
func writeMac(server: Bool, in dir: URL) {
    let mac = image(opaque: false) { drawMac($0, server: server) }
    for points in [16, 32, 128, 256, 512] {
        write(mac, "AppIcon-Mac-\(points).png", in: dir, pixels: points)
        write(mac, "AppIcon-Mac-\(points)@2x.png", in: dir, pixels: points * 2)
    }
}

/// Android's adaptive icon: layers 108 dp square, of which the launcher shows
/// the middle 72 dp or less, and only a 66 dp circle for certain. The tile
/// maps onto those 72 dp, and the robot shrinks a little about its centre so
/// that its ears stay inside the circle. The background is the accent, edge
/// to edge. The monochrome layer is for themed icons.
func writeAndroid(in res: URL) {
    let margin = CGFloat(size) * 18 / 108
    func layer(_ draw: @escaping (CGContext) -> Void) -> CGImage {
        image(opaque: false) { ctx in
            fitTile(ctx, side: CGFloat(size) - 2 * margin, origin: CGPoint(x: margin, y: margin))
            ctx.translateBy(x: tile.midX, y: tile.midY)
            ctx.scaleBy(x: 0.86, y: 0.86)
            ctx.translateBy(x: -tile.midX, y: -tile.midY)
            draw(ctx)
        }
    }
    let foreground = layer { drawRobot($0, shadow: hex(0x5A1010, 0.4)) }
    let monochrome = layer { drawRobotMono($0) }
    let background = image(opaque: true) { drawBackground($0, server: false, in: CGRect(x: 0, y: 0, width: size, height: size)) }
    for (density, pixels) in [("mdpi", 108), ("hdpi", 162), ("xhdpi", 216), ("xxhdpi", 324), ("xxxhdpi", 432)] {
        let dir = res.appendingPathComponent("mipmap-\(density)")
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        write(foreground, "ic_launcher_foreground.png", in: dir, pixels: pixels)
        write(monochrome, "ic_launcher_monochrome.png", in: dir, pixels: pixels)
        write(background, "ic_launcher_background.png", in: dir, pixels: pixels)
    }
}

let args = CommandLine.arguments.dropFirst()
let server = args.first == "--server"
let android = args.first == "--android"
guard args.count == (server || android ? 2 : 1) else {
    print("usage: swift make-app-icon.swift [--server | --android] <AppIcon.appiconset | res folder>")
    exit(2)
}
let dir = URL(fileURLWithPath: args.last!)
if server {
    writeMac(server: true, in: dir)
    exit(0)
}
if android {
    writeAndroid(in: dir)
    exit(0)
}

render("AppIcon.png", opaque: true, in: dir) { ctx in
    fitTile(ctx)
    drawBackground(ctx, server: false)
    drawRobot(ctx, shadow: hex(0x5A1010, 0.4))
}
// Dark and tinted icons are transparent: iOS draws the background.
render("AppIcon-Dark.png", opaque: false, in: dir) { ctx in
    fitTile(ctx)
    drawRobot(ctx, shadow: color(0, 0, 0, 0.5))
}
render("AppIcon-Tinted.png", opaque: false, in: dir) { ctx in
    fitTile(ctx)
    drawRobotMono(ctx)
}
writeMac(server: false, in: dir)
