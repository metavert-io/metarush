import Foundation

/// The steps after an update (D472), as scripts/deploy.sh takes them, through
/// the program the new app carries: Claude Code's skills (only where they are
/// installed), this version's renderer, the projects' records, MetaRush
/// restarted in the background, then a check that what answers is this build.
public enum FinishStep: String, CaseIterable, Sendable {
    case skills, renderer, records, restart, check
}

/// Where a step is.
public enum StepState: Equatable, Sendable {
    case waiting
    case running
    case done(String?)
    case skipped(String)
    case failed(MetaRushError)
}

/// FinishFailure is why the steps stopped, and at which step (nil: before any).
public struct FinishFailure: Error, Equatable, Sendable {
    public var step: FinishStep?
    public var error: MetaRushError

    public init(step: FinishStep?, error: MetaRushError) {
        self.step = step
        self.error = error
    }

    /// otherCopy: MetaRush runs for this Mac from another copy than this
    /// app's (its background service, or one started by opening its
    /// dashboard), so the steps stopped before changing anything and ask
    /// first (D473). newerRunning: that copy is newer than this app, so the
    /// app doesn't offer to take its place.
    public var otherCopy: Bool { [Self.otherServiceCode, Self.otherRunningCode, Self.newerRunningCode].contains(error.code) }
    public var canTakeOver: Bool { error.code == Self.otherServiceCode || error.code == Self.otherRunningCode }
    public static let otherServiceCode = "app:other-copy"
    public static let otherRunningCode = "app:other-running"
    public static let newerRunningCode = "app:newer-running"
}

/// WhoRuns is which copy of MetaRush runs for this Mac's home, as far as an
/// update is concerned (D473): the steps go ahead by themselves only for this
/// app's copy, or when nothing runs.
public enum WhoRuns: Equatable, Sendable {
    case thisCopy
    case nothing
    /// The background service starts another copy (or this one only through
    /// a link, which could lead elsewhere later).
    case otherService(newer: Bool, workerOnly: Bool)
    /// Another copy runs, started by opening its dashboard (or a build that
    /// didn't say which copy it is).
    case otherDaemon(newer: Bool)

    public static func of(_ st: ServiceStatus) -> WhoRuns {
        let d = st.daemon
        let daemonIsThis = d?.sameCopy == true
        let newer = d != nil && !daemonIsThis && d?.newer == true
        if st.startsAnotherCopy {
            // A link that leads here today, with this copy running from it:
            // restarting the service starts this copy again.
            if st.sameBinary && daemonIsThis { return .thisCopy }
            return .otherService(newer: newer, workerOnly: st.workerOnly)
        }
        if d != nil && !daemonIsThis { return .otherDaemon(newer: newer) }
        return (st.installed || d != nil) ? .thisCopy : .nothing
    }

    /// The command that lets this app's copy run in the background in the
    /// other copy's place, once the person says yes: the service set up again
    /// as it ran (a worker stays a worker), never over a newer copy. nil: the
    /// steps themselves stop and start it (opened by the dashboard).
    public var takeOver: [String]? {
        switch self {
        case .otherService(_, let worker): return ["service", "install", "--keep-newer"] + (worker ? ["--worker-only"] : [])
        default: return nil
        }
    }

    public var newer: Bool {
        switch self {
        case .otherService(let n, _), .otherDaemon(let n): return n
        default: return false
        }
    }
}

/// Finishing runs the steps after an update.
public struct Finishing: Sendable {
    public var metarush: MetaRush
    /// How long the check waits for the new build to answer.
    public var checkTimeout: TimeInterval
    /// How often the check looks (tests make it short).
    public var checkEvery: TimeInterval

    public init(metarush: MetaRush, checkTimeout: TimeInterval = 60, checkEvery: TimeInterval = 1) {
        self.metarush = metarush
        self.checkTimeout = checkTimeout
        self.checkEvery = checkEvery
    }

    /// Runs every step in order and stops at the first that fails; each change
    /// of a step is told to `progress`. Running it again is safe (each step is).
    /// Before any step: a MetaRush not set up here has nothing to finish, and
    /// another copy running for this Mac is asked about first (D473): nothing
    /// of it is changed or stopped until the person lets this app take its
    /// place (`takingOver`), and never when it is newer.
    public func run(takingOver: Bool = false, progress: @escaping @Sendable (FinishStep, StepState) async -> Void) async -> Result<VersionInfo, FinishFailure> {
        let build: VersionInfo
        do {
            build = try await metarush.call(["version"], as: VersionInfo.self)
            let st = try await metarush.call(["service", "status"], as: ServiceStatus.self)
            let who = WhoRuns.of(st)
            if who.newer {
                return .failure(FinishFailure(step: nil, error: MetaRushError(code: FinishFailure.newerRunningCode, message: Words.newerCopyRuns,
                                                                              hint: Words.newerCopyHint, detail: Finishing.detail(st))))
            }
            switch who {
            case .otherService where !takingOver:
                return .failure(FinishFailure(step: nil, error: MetaRushError(code: FinishFailure.otherServiceCode, message: Words.otherCopyRuns,
                                                                              hint: Words.otherCopyHint, detail: Finishing.detail(st))))
            case .otherService:
                if let words = who.takeOver { try await metarush.call(words) }
            case .otherDaemon where !takingOver:
                return .failure(FinishFailure(step: nil, error: MetaRushError(code: FinishFailure.otherRunningCode, message: Words.otherCopyOpen,
                                                                              hint: Words.otherCopyOpenHint, detail: Finishing.detail(st))))
            default:
                break
            }
        } catch let e as MetaRushError where e.notSetUp {
            for step in FinishStep.allCases { await progress(step, .skipped(Words.notSetUpYet)) }
            return .success(VersionInfo(version: "", release: "", commit: ""))
        } catch {
            return .failure(FinishFailure(step: nil, error: asError(error)))
        }
        var restarted = false
        for step in FinishStep.allCases {
            await progress(step, .running)
            do {
                let state = try await take(step, build: build, restarted: &restarted)
                await progress(step, state)
            } catch {
                let e = asError(error)
                await progress(step, .failed(e))
                return .failure(FinishFailure(step: step, error: e))
            }
        }
        return .success(build)
    }

    /// The technical part of an other-copy question, for Details.
    static func detail(_ st: ServiceStatus) -> String {
        var lines: [String] = []
        if st.installed, let b = st.binary, !b.isEmpty { lines.append("the background service runs \(b)") }
        if let d = st.daemon {
            lines.append("running: \(d.version ?? "-") (pid \(d.pid))" + (d.sameCopy == nil ? ", which copy not known" : ""))
        }
        return lines.joined(separator: "\n")
    }

    private func take(_ step: FinishStep, build: VersionInfo, restarted: inout Bool) async throws -> StepState {
        switch step {
        case .skills:
            let would = try await metarush.call(["skills", "install", "--user", "--dry-run"], as: SkillsInstall.self)
            guard would.there else { return .skipped(Words.skillsNotHere) }
            try await metarush.call(["skills", "install", "--user"])
            return .done(nil)
        case .renderer:
            // A renderer a person left out stays out: the command says so and succeeds.
            let out = try await metarush.call(["renderer", "install"])
            return .done(out["says"]?.string)
        case .records:
            try await metarush.call(["migrate"])
            return .done(nil)
        case .restart:
            // Only this app's copy, or one the person let it replace, gets here (run's check).
            let st = try await metarush.call(["service", "status"], as: ServiceStatus.self)
            if st.installed {
                if let d = st.daemon, d.sameCopy != true, (d.service ?? "").isEmpty {
                    // Another copy opened by its dashboard, which the person let this app replace.
                    try await metarush.call(["stop", "--keep-database"])
                }
                // It waits until the service's MetaRush answers as this build.
                try await metarush.call(["service", "restart"])
                restarted = true
                return .done(nil)
            }
            if st.daemon != nil {
                // Started by opening the dashboard: stopped and started again, the
                // database left running.
                try await metarush.call(["stop", "--keep-database"])
                _ = try await metarush.call(["dashboard"], as: DashboardLink.self)
                restarted = true
                return .done(nil)
            }
            return .skipped(Words.notRunningStartsLater)
        case .check:
            guard restarted else { return .skipped(Words.notRunningStartsLater) }
            try await check(build)
            return .done(nil)
        }
    }

    /// check waits until the MetaRush running for the home is this build.
    private func check(_ build: VersionInfo) async throws {
        let deadline = Date().addingTimeInterval(checkTimeout)
        var seen = ""
        while true {
            let st = try await metarush.call(["service", "status"], as: ServiceStatus.self)
            if let d = st.daemon {
                if d.mode == "worker-only" {
                    if d.version == build.version { return }
                } else if d.answering && d.version == build.version && d.commit == build.commit {
                    return
                }
                seen = d.version ?? ""
            }
            if Date() >= deadline { break }
            try await Task.sleep(nanoseconds: UInt64(checkEvery * 1_000_000_000))
        }
        if !seen.isEmpty, seen != build.version {
            throw MetaRushError(code: "", message: Words.stillRuns(seen, build.version), hint: Words.restartHint,
                                detail: "running \(seen), this build \(build.version) (\(build.commit))")
        }
        throw MetaRushError(code: "", message: Words.notAnswering(build.version), hint: Words.restartHint,
                            detail: "this build \(build.version) (\(build.commit))")
    }

    private func asError(_ error: Error) -> MetaRushError {
        (error as? MetaRushError) ?? MetaRushError(code: "", message: Words.failedWithoutSaying, detail: "\(error)")
    }
}

/// MarkerStore keeps the marker's two values: UserDefaults in the app, a
/// dictionary in the tests (which never write the person's preferences).
public protocol MarkerStore: AnyObject {
    func string(forKey key: String) -> String?
    func set(_ value: String?, forKey key: String)
}

/// The app's preferences.
public final class DefaultsStore: MarkerStore {
    let defaults: UserDefaults
    public init(_ defaults: UserDefaults = .standard) { self.defaults = defaults }
    public func string(forKey key: String) -> String? { defaults.string(forKey: key) }
    public func set(_ value: String?, forKey key: String) { defaults.set(value, forKey: key) }
}

/// FinishMarker remembers the last build whose steps finished (or whose setup
/// did) and the build whose steps were last started by themselves (D472), so
/// that a newer build after an update finishes by itself once; an older one
/// never does; and an update that didn't finish is offered again ("Try
/// Again" in the menu) instead of restarting MetaRush at every start.
public struct FinishMarker {
    public static let finishedKey = "FinishedBuild"
    public static let triedKey = "FinishTriedBuild"
    let store: MarkerStore
    /// newer(a, b): build a is newer than build b (Sparkle's own comparator in
    /// the app, so "newer" means what it means to updates).
    let newer: (String, String) -> Bool

    public init(store: MarkerStore, newer: @escaping (String, String) -> Bool) {
        self.store = store
        self.newer = newer
    }

    public var last: String? { store.string(forKey: FinishMarker.finishedKey) }
    public var tried: String? { store.string(forKey: FinishMarker.triedKey) }

    /// What a start of the app does about the steps.
    public enum AtStart: Equatable, Sendable {
        /// Nothing finished yet: setup's to do (the app opens it only when
        /// MetaRush isn't set up here).
        case firstStart
        /// The steps run, by themselves (this build is newer than the last
        /// that finished, and they weren't tried for it yet).
        case finish
        /// They were tried for this build and didn't finish: offered, not run.
        case offerAgain
        /// This build finished, or it is older than the one that did.
        case nothing
    }

    public func atStart(build: String) -> AtStart {
        guard let last else { return .firstStart }
        guard newer(build, last) else { return .nothing }
        return tried == build ? .offerAgain : .finish
    }

    /// trying is said before the steps run by themselves.
    public func trying(build: String) {
        store.set(build, forKey: FinishMarker.triedKey)
    }

    public func finished(build: String) {
        store.set(build, forKey: FinishMarker.finishedKey)
        store.set(nil, forKey: FinishMarker.triedKey)
    }
}
