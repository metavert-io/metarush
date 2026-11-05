import CoreGraphics
import Foundation
import Testing
@testable import MetaRushCore

/// Where updates come from and whether a build may update (D471); the mark.
@Suite struct FeedAndMarkTests {
    @Test func theFeedAddress() {
        let built = "https://github.com/example/project/releases/latest/download/appcast.xml"
        #expect(UpdateFeed.choose(built: built, override: nil)?.absoluteString == built)
        #expect(UpdateFeed.choose(built: built, override: "https://updates.example.com/appcast.xml")?.host == "updates.example.com")
        // A setting that isn't a safe address is ignored, not used.
        #expect(UpdateFeed.choose(built: built, override: "http://updates.example.com/appcast.xml")?.absoluteString == built)
        #expect(UpdateFeed.choose(built: built, override: "not an address")?.absoluteString == built)
        #expect(UpdateFeed.acceptable("http://127.0.0.1:5801/appcast.xml") != nil)
        #expect(UpdateFeed.acceptable("http://localhost:5801/appcast.xml") != nil)
        #expect(UpdateFeed.acceptable("ftp://example.com/appcast.xml") == nil)
        #expect(UpdateFeed.choose(built: "__FEED_URL__", override: nil) == nil)
    }

    @Test func onlyARealKeyUpdates() {
        #expect(!UpdateFeed.keyIsReal(nil))
        #expect(!UpdateFeed.keyIsReal(UpdateFeed.keyPlaceholder))
        #expect(!UpdateFeed.keyIsReal("c2hvcnQ="))
        #expect(UpdateFeed.keyIsReal(Data(repeating: 7, count: 32).base64EncodedString()))
    }

    func pixel(_ image: CGImage, _ x: Int, _ y: Int) -> (r: UInt8, g: UInt8, b: UInt8, a: UInt8) {
        let space = CGColorSpace(name: CGColorSpace.sRGB)!
        var px = [UInt8](repeating: 0, count: 4)
        let ctx = CGContext(data: &px, width: 1, height: 1, bitsPerComponent: 8, bytesPerRow: 4, space: space,
                            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
        ctx.draw(image, in: CGRect(x: -x, y: -(image.height - 1 - y), width: image.width, height: image.height))
        return (px[0], px[1], px[2], px[3])
    }

    @Test func theIconIsTheAmberTile() throws {
        let icon = try #require(Mark.icon(pixels: 256))
        #expect(icon.width == 256)
        // Outside the tile: clear. Near its top-left edge, inside: amber.
        #expect(pixel(icon, 2, 2).a == 0)
        let amber = pixel(icon, 40, 128)
        #expect(amber.a == 255 && amber.r > 200 && amber.g > 140 && amber.g < 190 && amber.b < 120)
        let png = try #require(Mark.png(icon))
        #expect(png.starts(with: [0x89, 0x50, 0x4E, 0x47]))
        #expect(Mark.iconSet.count == 10 && Mark.iconSet.contains { $0 == ("icon_512x512" + Mark.retina + ".png", 1024) && $0.1 == 1024 })
    }

    @Test func theMenuBarMarkIsATemplate() throws {
        let plain = try #require(Mark.menuBar(pixels: 36))
        let dot = try #require(Mark.menuBar(pixels: 36, dot: true))
        // Its slate is ink, its background clear; the dot shows at the top right.
        #expect(pixel(plain, 18, 30).a > 200 && pixel(plain, 1, 1).a == 0)
        #expect(pixel(plain, 33, 3).a == 0 && pixel(dot, 33, 3).a > 200)
    }
}
