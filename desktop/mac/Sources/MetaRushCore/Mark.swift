import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

/// Mark is the MetaRush mark — a clapperboard on the amber tile, the same
/// drawing as web/public/favicon.svg — drawn at any size: the app's icon
/// (scripts/mac-app.sh makes AppIcon.icns from it) and the menu bar's template
/// image. Nothing is kept as a picture file.
public enum Mark {
    static let amber = CGColor(srgbRed: 0xE0 / 255.0, green: 0xA4 / 255.0, blue: 0x58 / 255.0, alpha: 1)
    static let ink = CGColor(srgbRed: 0x1C / 255.0, green: 0x13 / 255.0, blue: 0x07 / 255.0, alpha: 1)

    /// The app icon: the tile on Apple's icon grid (824 of 1024, centred).
    public static func icon(pixels: Int) -> CGImage? {
        draw(pixels: pixels) { ctx, px in
            let inset = px * 100 / 1024, side = px * 824 / 1024
            ctx.translateBy(x: inset, y: inset + side)
            ctx.scaleBy(x: side / 32, y: -side / 32) // the drawing's 32 units, y down as in the SVG
            ctx.addPath(CGPath(roundedRect: CGRect(x: 0, y: 0, width: 32, height: 32), cornerWidth: 8, cornerHeight: 8, transform: nil))
            ctx.setFillColor(amber)
            ctx.fillPath()
            clapper(ctx, stripes: amber)
        }
    }

    /// The menu bar's: the clapperboard alone in black, its stripes cut out,
    /// for a template image macOS tints.
    /// With `dot`, a dot at the top right says something waits for the person
    /// (an update, a problem), cut clear of the clapperboard.
    public static func menuBar(pixels: Int, dot: Bool = false) -> CGImage? {
        draw(pixels: pixels) { ctx, px in
            ctx.saveGState()
            // The clapperboard with its outline spans x 4.7…26.0 and y 3.0…26.6.
            let box = CGRect(x: 4.7, y: 3.0, width: 21.3, height: 23.6)
            let k = px / max(box.width, box.height)
            ctx.translateBy(x: (px - box.width * k) / 2 - box.minX * k, y: px - ((px - box.height * k) / 2 - box.minY * k))
            ctx.scaleBy(x: k, y: -k)
            clapper(ctx, stripes: nil)
            ctx.restoreGState()
            if dot {
                let r = px * 0.2, centre = CGPoint(x: px - r, y: px - r)
                ctx.setBlendMode(.clear)
                ctx.fillEllipse(in: CGRect(x: centre.x - r * 1.45, y: centre.y - r * 1.45, width: r * 2.9, height: r * 2.9))
                ctx.setBlendMode(.normal)
                ctx.setFillColor(ink)
                ctx.fillEllipse(in: CGRect(x: centre.x - r, y: centre.y - r, width: r * 2, height: r * 2))
            }
        }
    }

    /// PNG bytes of an image.
    public static func png(_ image: CGImage) -> Data? {
        let data = NSMutableData()
        guard let dest = CGImageDestinationCreateWithData(data, UTType.png.identifier as CFString, 1, nil) else { return nil }
        CGImageDestinationAddImage(dest, image, nil)
        return CGImageDestinationFinalize(dest) ? data as Data : nil
    }

    /// The icon set iconutil turns into AppIcon.icns: name → pixels, each size
    /// at 1x and 2x (icon_16x16.png, then the same name with "@2x" before ".png").
    public static let iconSet: [(String, Int)] = [16, 32, 128, 256, 512].flatMap { side in
        [("icon_\(side)x\(side).png", side), ("icon_\(side)x\(side)" + retina + ".png", side * 2)]
    }

    static let retina = "@2x"

    private static func draw(pixels: Int, _ body: (CGContext, CGFloat) -> Void) -> CGImage? {
        guard let space = CGColorSpace(name: CGColorSpace.sRGB),
              let ctx = CGContext(data: nil, width: pixels, height: pixels, bitsPerComponent: 8, bytesPerRow: 0, space: space,
                                  bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return nil }
        ctx.setShouldAntialias(true)
        body(ctx, CGFloat(pixels))
        return ctx.makeImage()
    }

    /// The clapperboard: its stick and slate in ink with a rounded outline, and
    /// two stripes on the stick (in `stripes`, or cut out when nil).
    private static func clapper(_ ctx: CGContext, stripes: CGColor?) {
        ctx.setLineWidth(1.2)
        ctx.setLineJoin(.round)
        ctx.setFillColor(ink)
        ctx.setStrokeColor(ink)
        for path in [polygon([(6.6, 13.3), (25, 8), (23.7, 3.6), (5.3, 8.9)]),
                     CGPath(roundedRect: CGRect(x: 6.6, y: 15, width: 18.8, height: 11), cornerWidth: 1.6, cornerHeight: 1.6, transform: nil)] {
            ctx.addPath(path)
            ctx.drawPath(using: .fillStroke)
        }
        if let stripes {
            ctx.setFillColor(stripes)
        } else {
            ctx.setBlendMode(.clear)
        }
        let stripeShapes: [[(CGFloat, CGFloat)]] = [[(11.2, 12), (13.8, 11.2), (14.4, 6.2), (11.8, 7)], [(17.4, 10.2), (20, 9.5), (20.6, 4.5), (18, 5.2)]]
        for p in stripeShapes {
            ctx.addPath(polygon(p))
            ctx.fillPath()
        }
        ctx.setBlendMode(.normal)
    }

    private static func polygon(_ points: [(CGFloat, CGFloat)]) -> CGPath {
        let p = CGMutablePath()
        p.addLines(between: points.map { CGPoint(x: $0.0, y: $0.1) })
        p.closeSubpath()
        return p
    }
}
