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
        /// the one it runs now (that one's version, when it says).
        case serviceFollows(running: String?)
        /// It does, and the copy it runs now is newer than this app's, so the
        /// link isn't made.
        case refused(running: String?)
    }

    public func effect(_ st: ServiceStatus?) -> Effect {
        guard let st, st.installed, let bin = st.binary, !bin.isEmpty,
              URL(fileURLWithPath: bin).standardizedFileURL.path == link.standardizedFileURL.path else { return .none }
        let d = st.daemon
        let other = d != nil && d?.sameCopy != true
        if other && d?.newer == true { return .refused(running: d?.version) }
        return .serviceFollows(running: other ? d?.version : nil)
    }

    /// The version the program there says it is (`version --json`, a few
    /// seconds at most), so the person sees both before choosing; nil when
    /// nothing answers.
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
