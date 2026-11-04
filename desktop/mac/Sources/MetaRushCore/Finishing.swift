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

    /// otherCopy: MetaRush in the background is another copy than this app's,
    /// so the update doesn't reach it until the person lets this app run it
    /// (`service install` from the app's copy).
    public var otherCopy: Bool { error.code == FinishFailure.otherCopyCode }
    public static let otherCopyCode = "app:other-copy"
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
    /// one whose background service runs another copy is asked about first
    /// (the records would otherwise be brought up to date for a program that
    /// isn't the one running).
    public func run(progress: @escaping @Sendable (FinishStep, StepState) async -> Void) async -> Result<VersionInfo, FinishFailure> {
        let build: VersionInfo
        do {
            build = try await metarush.call(["version"], as: VersionInfo.self)
            let st = try await metarush.call(["service", "status"], as: ServiceStatus.self)
            if st.installed, let other = st.binary, !other.isEmpty, !st.sameBinary {
                return .failure(FinishFailure(step: nil, error: MetaRushError(code: FinishFailure.otherCopyCode, message: Words.otherCopyRuns,
                                                                              hint: Words.otherCopyHint, detail: "the background service runs \(other)")))
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
            let st = try await metarush.call(["service", "status"], as: ServiceStatus.self)
            if st.installed {
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

/// FinishMarker remembers the last build whose steps finished (or whose setup
/// did), so a new build after an update knows to finish, and one that didn't
/// finish tries again at the next start.
public struct FinishMarker {
    public static let key = "FinishedBuild"
    let defaults: UserDefaults

    public init(defaults: UserDefaults = .standard) { self.defaults = defaults }

    public var last: String? { defaults.string(forKey: FinishMarker.key) }

    /// needed: this build follows another that finished, and hasn't finished
    /// itself. A first start (nothing finished yet) is setup's, not this.
    public func needed(build: String) -> Bool {
        guard let last else { return false }
        return last != build
    }

    public func finished(build: String) {
        defaults.set(build, forKey: FinishMarker.key)
    }
}
