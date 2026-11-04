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

    func status(_ name: String) throws -> ServiceStatus {
        try JSONDecoder().decode(DataOf<ServiceStatus>.self, from: try fixture(name)).data
    }

    /// The link and MetaRush in the background (D475): when the service starts
    /// MetaRush from the link's own path, making the link changes what it runs
    /// from its next start, which the panel says; it isn't made over a newer copy.
    @Test func whatTheLinkMeansForTheService() throws {
        let link = CommandLink(folder: URL(fileURLWithPath: "/Users/you/.local/bin"), target: URL(fileURLWithPath: "/Applications/MetaRush.app/Contents/Helpers/metarush"))
        // The service starts another path (Homebrew's): the link changes nothing in the background.
        #expect(link.effect(try status("service-status-other-copy.json")) == .none)
        let none = try status("service-status-none.json")
        #expect(link.effect(nil) == .none && link.effect(none) == .none)
        // The service starts this very path.
        var st = try status("service-status-other-copy.json")
        st.binary = "/Users/you/.local/bin/metarush"
        #expect(link.effect(st) == .serviceFollows(running: "0.1.0"))
        st = try status("service-status-other-newer.json")
        st.binary = "/Users/you/.local/bin/metarush"
        #expect(link.effect(st) == .refused(running: "0.3.0"))
        // Through the link, already this app's copy: nothing new to say about what runs.
        #expect(link.effect(try status("service-status-via-link.json")) == .serviceFollows(running: nil))
        // MetaRush stopped: the program there says what it is (D475).
        var stopped = try status("service-status-stopped.json")
        stopped.binary = "/Users/you/.local/bin/metarush"
        #expect(link.effect(stopped, there: .version("0.3.0", newer: true)) == .refused(running: "0.3.0"))
        #expect(link.effect(stopped, there: .version("0.1.0", newer: false)) == .serviceFollows(running: "0.1.0"))
        #expect(link.effect(stopped, there: .version("0.2.0-dev", newer: nil)) == .unknown)
        #expect(link.effect(stopped, there: .silent) == .unknown)
        // Where the service doesn't start MetaRush from the link, the link changes nothing in the background.
        #expect(link.effect(try status("service-status-other-copy.json"), there: .version("0.3.0", newer: true)) == .none)
    }

    /// The program at the link's path: its version, and whether it is newer
    /// than this app's, in MetaRush's own order (`version --compare` from the
    /// app's program); one that says nothing is `silent`.
    @Test func theProgramThereAndHowItOrders() async throws {
        let (d, _) = try setUp()
        let app = try FakeMetaRush()
        let link = CommandLink(folder: d.url.appendingPathComponent("home/.local/bin"), target: app.program)
        #expect(await link.there(asking: app.metarush()) == .nothing)
        let other = try FakeMetaRush()
        try FileManager.default.createDirectory(at: link.folder, withIntermediateDirectories: true)
        try FileManager.default.createSymbolicLink(at: link.link, withDestinationURL: other.program)
        // The stand-in at the link answers from the link's folder.
        let answer = { (name: String) throws in try fixture(name).write(to: link.folder.appendingPathComponent("version.json")) }
        #expect(await link.there(asking: app.metarush()) == .silent)
        try answer("version-3.json")
        try app.answer("version", "version-2-compared-0.3.0.json")
        #expect(await link.there(asking: app.metarush()) == .version("0.3.0", newer: true))
        #expect(app.calls.last?.hasPrefix("version --compare 0.3.0 --json") == true)
        try answer("version-1.json")
        try app.answer("version", "version-2-compared-0.1.0.json")
        #expect(await link.there(asking: app.metarush()) == .version("0.1.0", newer: false))
        try app.answer("version", "version-2-compared-unknown.json")
        #expect(await link.there(asking: app.metarush()) == .version("0.1.0", newer: nil))
    }

    @Test func theVersionOfTheProgramThere() async throws {
        let (d, link) = try setUp()
        _ = d
        #expect(await link.versionThere() == nil) // nothing there
        let other = try FakeMetaRush()
        try FileManager.default.createDirectory(at: link.folder, withIntermediateDirectories: true)
        try FileManager.default.createSymbolicLink(at: link.link, withDestinationURL: other.program)
        // The stand-in answers from the folder it is called in: the link's.
        try fixture("version-1.json").write(to: link.folder.appendingPathComponent("version.json"))
        #expect(await link.versionThere() == "0.1.0")
        // A program that doesn't answer is given a few seconds, then nothing is said.
        try "exec sleep 5".write(to: link.folder.appendingPathComponent("version.sh"), atomically: true, encoding: .utf8)
        let started = Date()
        #expect(await link.versionThere(seconds: 0.5) == nil)
        #expect(Date().timeIntervalSince(started) < 10)
    }

    /// Moving to Applications (D476): an older copy there goes to the Trash; a
    /// newer one, or one that is open, is asked about first.
    @Test func whatMovingDoesWithTheCopyThere() {
        let this = AppVersion(build: "0.2.0", shown: "0.2.0")
        let older = AppVersion(build: "0.1.0", shown: "0.1.0"), newer = AppVersion(build: "0.3.0", shown: "0.3.0")
        let n = FinishingTests.newer
        #expect(MovePlan.plan(this: this, there: nil, thereExists: false, thereOpen: false, newer: n) == .copy)
        #expect(MovePlan.plan(this: this, there: older, thereExists: true, thereOpen: false, newer: n) == .replace(older))
        #expect(MovePlan.plan(this: this, there: this, thereExists: true, thereOpen: false, newer: n) == .replace(this))
        #expect(MovePlan.plan(this: this, there: newer, thereExists: true, thereOpen: false, newer: n) == .askNewer(newer))
        #expect(MovePlan.plan(this: this, there: older, thereExists: true, thereOpen: true, newer: n) == .askOpen(older))
        #expect(MovePlan.plan(this: this, there: newer, thereExists: true, thereOpen: true, newer: n) == .askOpen(newer))
        // Something there that doesn't say its version: replaced (it goes to the Trash).
        #expect(MovePlan.plan(this: this, there: nil, thereExists: true, thereOpen: false, newer: n) == .replace(AppVersion(build: "", shown: "")))
    }

    @Test func anAppsVersionFromItsInfoPlist() throws {
        let d = try TempDir()
        let app = d.url.appendingPathComponent("MetaRush.app")
        try FileManager.default.createDirectory(at: app.appendingPathComponent("Contents"), withIntermediateDirectories: true)
        let plist: [String: Any] = ["CFBundleVersion": "0.16.0.3", "CFBundleShortVersionString": "0.16.0-dev.3"]
        try PropertyListSerialization.data(fromPropertyList: plist, format: .xml, options: 0).write(to: app.appendingPathComponent("Contents/Info.plist"))
        #expect(AppVersion.of(app) == AppVersion(build: "0.16.0.3", shown: "0.16.0-dev.3"))
        #expect(AppVersion.of(d.url.appendingPathComponent("Nothing.app")) == nil)
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
