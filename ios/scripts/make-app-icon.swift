// Renders the app icon into an .appiconset: a chat bubble with a terminal
// prompt cut out of it, on the accent colour. iOS rounds the corners itself.
//
//   cd ios && swift scripts/make-app-icon.swift RemoteAgent/Assets.xcassets/AppIcon.appiconset
import CoreGraphics
import Foundation
import ImageIO
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

func render(_ name: String, opaque: Bool, in dir: URL, draw: (CGContext) -> Void) {
    // The App Store rejects an alpha channel in the main icon.
    let alpha: CGImageAlphaInfo = opaque ? .noneSkipLast : .premultipliedLast
    let ctx = CGContext(data: nil, width: size, height: size, bitsPerComponent: 8, bytesPerRow: 0,
                        space: srgb, bitmapInfo: alpha.rawValue)!
    ctx.translateBy(x: 0, y: CGFloat(size))
    ctx.scaleBy(x: 1, y: -1)
    draw(ctx)
    let url = dir.appendingPathComponent(name)
    let dest = CGImageDestinationCreateWithURL(url as CFURL, UTType.png.identifier as CFString, 1, nil)!
    CGImageDestinationAddImage(dest, ctx.makeImage()!, nil)
    guard CGImageDestinationFinalize(dest) else { fatalError("could not write \(url.path)") }
    print(url.path)
}

guard CommandLine.arguments.count == 2 else {
    print("usage: swift make-app-icon.swift <AppIcon.appiconset>")
    exit(2)
}
let dir = URL(fileURLWithPath: CommandLine.arguments[1])

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
