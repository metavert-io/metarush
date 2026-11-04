import Foundation
import Testing
@testable import MetaRushCore

/// The command-line link (D475) and where the app runs from (D476), in
/// temporary folders only: never the person's own programs folder or
/// Applications.
@Suite struct PlaceTests {
    func setUp() throws -> (TempDir, CommandLink) {
        let d = try TempDir()
        let app = d.url.appendingPathComponent("MetaRush.app/Contents/Helpers")
        try FileManager.default.createDirectory(at: app, withIntermediateDirectories: true)
        try Data("#!/bin/sh\n".utf8).write(to: app.appendingPathComponent("metarush"))
        return (d, CommandLink(folder: d.url.appendingPathComponent("home/.local/bin"), target: app.appendingPathComponent("metarush")))
    }

    @Test func aLinkWhereNothingIs() throws {
        let (d, link) = try setUp()
        _ = d
        #expect(link.state() == .missing)
        #expect(try link.make(replacing: false) == nil)
        #expect(link.state() == .ours)
        #expect(try FileManager.default.destinationOfSymbolicLink(atPath: link.link.path) == link.target.path)
        #expect(try link.make(replacing: false) == nil) // again: nothing to do
    }

    @Test func anotherProgramIsAskedAboutAndKept() throws {
        let (d, link) = try setUp()
        _ = d
        try FileManager.default.createDirectory(at: link.folder, withIntermediateDirectories: true)
        try Data("another metarush".utf8).write(to: link.link)
        #expect(link.state() == .otherProgram)
        #expect(throws: CommandLink.LinkError.somethingThere) { try link.make(replacing: false) }
        #expect(link.state() == .otherProgram)
        let aside = try link.make(replacing: true)
        #expect(aside?.lastPathComponent == "metarush.before-app" && link.state() == .ours)
        #expect(try String(contentsOf: aside!, encoding: .utf8) == "another metarush")
    }

    @Test func anotherLinkIsKeptUnderTheNextName() throws {
        let (d, link) = try setUp()
        _ = d
        let fm = FileManager.default
        try fm.createDirectory(at: link.folder, withIntermediateDirectories: true)
        try Data("older".utf8).write(to: link.folder.appendingPathComponent("metarush.before-app"))
        try fm.createSymbolicLink(atPath: link.link.path, withDestinationPath: "/opt/homebrew/bin/metarush")
        #expect(link.state() == .otherLink("/opt/homebrew/bin/metarush"))
        let aside = try link.make(replacing: true)
        #expect(aside?.lastPathComponent == "metarush.before-app-2")
        #expect(try fm.destinationOfSymbolicLink(atPath: aside!.path) == "/opt/homebrew/bin/metarush")
        // A relative link to the app's copy is ours too.
        try fm.removeItem(at: link.link)
        let rel = "../../MetaRush.app/Contents/Helpers/metarush"
        try fm.createSymbolicLink(atPath: link.link.path, withDestinationPath: rel)
        #expect(link.state() == .ours)
    }

    @Test func placements() {
        let home = URL(fileURLWithPath: "/Users/you")
        #expect(Placement.of(URL(fileURLWithPath: "/Applications/MetaRush.app"), home: home) == .applications)
        #expect(Placement.of(URL(fileURLWithPath: "/Applications/Video/MetaRush.app"), home: home) == .applications)
        #expect(Placement.of(URL(fileURLWithPath: "/Users/you/Applications/MetaRush.app"), home: home) == .applications)
        #expect(Placement.of(URL(fileURLWithPath: "/Users/you/Downloads/MetaRush.app"), home: home) == .elsewhere)
        let moved = URL(fileURLWithPath: "/private/var/folders/q1/x7/T/AppTranslocation/3F2A9C1E-0B1D-4E57-9A43-1C6E7B9D2F10/d/MetaRush.app")
        #expect(Placement.of(moved, home: home) == .temporaryCopy && Placement.temporaryCopy.mustMove)
        #expect(Placement.diskImage.mustMove && !Placement.elsewhere.mustMove && !Placement.applications.mustMove)
    }

    @Test func theDownloadFlagIsCleared() throws {
        let d = try TempDir()
        let bundle = d.url.appendingPathComponent("MetaRush.app")
        let helper = bundle.appendingPathComponent("Contents/Helpers/metarush")
        try FileManager.default.createDirectory(at: helper.deletingLastPathComponent(), withIntermediateDirectories: true)
        try Data("x".utf8).write(to: helper)
        let flag = "0081;6aea9e73;Safari;"
        for u in [bundle, helper] {
            #expect(setxattr(u.path, Quarantine.attribute, flag, flag.utf8.count, 0, XATTR_NOFOLLOW) == 0)
        }
        #expect(Quarantine.has(helper))
        #expect(Quarantine.release(bundle) == 2)
        #expect(!Quarantine.has(helper) && !Quarantine.has(bundle))
        #expect(Quarantine.release(bundle) == 0)
    }

    @Test func movingToApplications() throws {
        let d = try TempDir()
        let src = d.url.appendingPathComponent("Downloads/MetaRush.app")
        try FileManager.default.createDirectory(at: src.appendingPathComponent("Contents"), withIntermediateDirectories: true)
        try Data("new".utf8).write(to: src.appendingPathComponent("Contents/Info.plist"))
        let flag = "0081;6aea9e73;Safari;"
        _ = setxattr(src.appendingPathComponent("Contents/Info.plist").path, Quarantine.attribute, flag, flag.utf8.count, 0, XATTR_NOFOLLOW)
        let apps = d.url.appendingPathComponent("Applications")
        try FileManager.default.createDirectory(at: apps.appendingPathComponent("MetaRush.app"), withIntermediateDirectories: true)
        var setAside: [URL] = []
        let copy = try Moving.copy(src, into: apps) { old in
            setAside.append(old)
            try FileManager.default.moveItem(at: old, to: d.url.appendingPathComponent("older.app"))
        }
        #expect(copy == apps.appendingPathComponent("MetaRush.app") && setAside.count == 1)
        #expect(try String(contentsOf: copy.appendingPathComponent("Contents/Info.plist"), encoding: .utf8) == "new")
        #expect(!Quarantine.has(copy.appendingPathComponent("Contents/Info.plist")))
        // The person's own Applications when the system's isn't writable.
        let home = d.url.appendingPathComponent("home")
        #expect(Moving.destinationFolder(system: URL(fileURLWithPath: "/System"), home: home) == home.appendingPathComponent("Applications"))
    }
}
