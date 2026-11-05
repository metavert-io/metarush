import Foundation

/// CommandLink is the link that lets a terminal and Claude Code's skills find
/// the app's MetaRush by name (D475): `metarush` in the person's own programs
/// folder (~/.local/bin) pointing at the program inside the app. It is made
/// only when the person says yes; another `metarush` already there is replaced
/// only after asking, and then kept beside it under another name, never deleted.
public struct CommandLink: Sendable {
    /// The folder (~/.local/bin).
    public var folder: URL
    /// The app's program (MetaRush.app/Contents/Helpers/metarush).
    public var target: URL

    public init(folder: URL, target: URL) {
        self.folder = folder
        self.target = target
    }

    /// The person's own programs folder.
    public static func personalFolder(home: URL = FileManager.default.homeDirectoryForCurrentUser) -> URL {
        home.appendingPathComponent(".local/bin")
    }

    public var link: URL { folder.appendingPathComponent("metarush") }

    public enum State: Equatable, Sendable {
        /// Nothing is there.
        case missing
        /// The link points at this app's program.
        case ours
        /// A link to another program (where it points).
        case otherLink(String)
        /// A program of its own (installed another way).
        case otherProgram
    }

    public func state() -> State {
        let fm = FileManager.default
        if let dest = try? fm.destinationOfSymbolicLink(atPath: link.path) {
            let resolved = URL(fileURLWithPath: dest, relativeTo: folder).standardizedFileURL.resolvingSymlinksInPath()
            if resolved.path == target.standardizedFileURL.resolvingSymlinksInPath().path {
                return .ours
            }
            return .otherLink(dest)
        }
        // lstat: anything else there, even a broken file, is someone's.
        var st = stat()
        if lstat(link.path, &st) == 0 {
            return .otherProgram
        }
        return .missing
    }

    /// make makes the link. Another program or link there is only replaced
    /// with `replacing` (the person said yes): it is kept beside it as
    /// metarush.before-app (or .before-app-2, …), and that name is returned.
    @discardableResult
    public func make(replacing: Bool) throws -> URL? {
        let fm = FileManager.default
        switch state() {
        case .ours:
            return nil
        case .otherLink, .otherProgram:
            guard replacing else { throw LinkError.somethingThere }
            try fm.createDirectory(at: folder, withIntermediateDirectories: true)
            var aside = folder.appendingPathComponent("metarush.before-app")
            var n = 2
            while (try? aside.checkResourceIsReachable()) == true || fm.fileExists(atPath: aside.path) || isLink(aside) {
                aside = folder.appendingPathComponent("metarush.before-app-\(n)")
                n += 1
            }
            try fm.moveItem(at: link, to: aside)
            try fm.createSymbolicLink(at: link, withDestinationURL: target)
            return aside
        case .missing:
            try fm.createDirectory(at: folder, withIntermediateDirectories: true)
            try fm.createSymbolicLink(at: link, withDestinationURL: target)
            return nil
        }
    }

    /// What making the link means for MetaRush in the background (D475): a
    /// background service another copy set up may start MetaRush by this very
    /// path, and would then start this app's copy from its next start.
    public enum Effect: Equatable, Sendable {
        /// The background service doesn't start MetaRush from here.
        case none
        /// It does: from its next start it runs this app's copy instead of
        /// the one there (that one's version, when it says).
        case serviceFollows(running: String?)
        /// It does, and the copy there (or the one running from it) is newer
        /// than this app's, so the link isn't made.
        case refused(running: String?)
        /// It does, and the copy there doesn't say which version it is (it may
        /// be newer), so the link isn't made either.
        case unknown
    }

    /// There is the program at the link's path, as far as the person choosing
    /// needs to know: its version and whether it is newer than this app's.
    public enum There: Equatable, Sendable {
        /// Nothing there, or this app's own link.
        case nothing
        /// A program that doesn't say which version it is.
        case silent
        /// Its version, and whether it is newer than this app's (nil: the two
        /// can't be ordered).
        case version(String, newer: Bool?)

        public var version: String? {
            if case .version(let v, _) = self { return v }
            return nil
        }

        public var newer: Bool {
            if case .version(_, newer: true) = self { return true }
            return false
        }
    }

    /// there asks the program at the link's path its version (a few seconds
    /// at most), then this app's own program how the two are ordered (`version
    /// --compare`: MetaRush's order, never one written here).
    public func there(asking metarush: MetaRush, seconds: Double = 5) async -> There {
        switch state() {
        case .missing, .ours: return .nothing
        default: break
        }
        guard let v = await versionThere(seconds: seconds) else { return .silent }
        guard let c = (try? await metarush.call(["version", "--compare", v], as: VersionInfo.self))?.compared, c.known else {
            return .version(v, newer: nil)
        }
        return .version(v, newer: c.order < 0)
    }

    /// effect: what the link means for a service that starts MetaRush from
    /// its path. Refused when the copy there, or the copy running from it, is
    /// newer than this app's, or can't be told (whether MetaRush runs or not).
    public func effect(_ st: ServiceStatus?, there: There = .nothing) -> Effect {
        guard let st, st.installed, let bin = st.binary, !bin.isEmpty,
              URL(fileURLWithPath: bin).standardizedFileURL.path == link.standardizedFileURL.path else { return .none }
        let d = st.daemon
        let otherRunning = d != nil && d?.sameCopy != true
        if otherRunning && d?.newer == true { return .refused(running: d?.version) }
        switch there {
        case .version(let v, newer: true): return .refused(running: v)
        case .version(_, newer: nil), .silent: return .unknown
        default: break
        }
        return .serviceFollows(running: there.version ?? (otherRunning ? d?.version : nil))
    }

    /// The version the program there says it is (`version --json`, a few
    /// seconds at most); nil when nothing answers.
    public func versionThere(runner: CommandRunner = ProcessRunner(), seconds: Double = 5) async -> String? {
        guard state() != .missing else { return nil }
        let there = MetaRush(program: link, runner: runner)
        return await withTaskGroup(of: String?.self) { group in
            group.addTask { (try? await there.call(["version"], as: VersionInfo.self))?.version }
            group.addTask {
                try? await Task.sleep(nanoseconds: UInt64(seconds * 1_000_000_000))
                return nil
            }
            let first = await group.next() ?? nil
            group.cancelAll()
            return first
        }
    }

    private func isLink(_ u: URL) -> Bool {
        (try? FileManager.default.destinationOfSymbolicLink(atPath: u.path)) != nil
    }

    public enum LinkError: Error, Equatable {
        /// Another program is there and the person hasn't said to replace it.
        case somethingThere
    }
}
