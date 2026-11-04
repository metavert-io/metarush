import Foundation

/// MenuStatus is what the menu says about MetaRush on this Mac: whether it is
/// set up and running, its version, and doctor's problems in its own plain
/// words (D474). It is built from the program's answers only.
public struct MenuStatus: Equatable, Sendable {
    public enum Running: Equatable, Sendable {
        case unknown, notSetUp, running, stopped
    }

    public var running: Running = .unknown
    public var headline: String = Words.checking
    /// The version line ("Version 0.16.0"), with a note when MetaRush in the
    /// background still runs another one.
    public var version: String?
    /// The service is set up (MetaRush starts at login).
    public var serviceInstalled = false
    /// Another copy than this app's runs MetaRush here (its service, or one
    /// opened by its dashboard): the menu says so.
    public var otherCopy = false
    /// Problems: doctor's, in plain words (what it means; what to do).
    public var problems: [Problem] = []
    /// The background log, for Show Logs.
    public var log: String?

    public struct Problem: Equatable, Sendable {
        public var says: String
        public var todo: String?
    }

    public init() {}

    /// build makes the status from what the program answered: this app's
    /// build, the service and the daemon (or the error saying it isn't set
    /// up), and doctor's report when there is one.
    public static func build(app: VersionInfo?, service: Result<ServiceStatus, MetaRushError>?, doctor: DoctorReport?) -> MenuStatus {
        var s = MenuStatus()
        if let app { s.version = Words.version(app.version) }
        switch service {
        case .none:
            break
        case .failure(let e) where e.notSetUp:
            (s.running, s.headline) = (.notSetUp, Words.notSetUp)
        case .failure(let e):
            (s.running, s.headline) = (.unknown, Words.cantTell)
            s.problems.append(Problem(says: e.message, todo: e.hint))
        case .success(let st):
            (s.serviceInstalled, s.log) = (st.installed, st.log)
            switch WhoRuns.of(st) {
            case .otherService, .otherDaemon: s.otherCopy = true
            default: break
            }
            if let d = st.daemon, d.answering || d.mode == "worker-only" {
                s.running = .running
                s.headline = d.mode == "worker-only" ? Words.runningWorkOnly : Words.running
                if let app, let v = d.version, !v.isEmpty, v != app.version {
                    s.version = Words.versionRunsOther(app.version, running: v)
                }
            } else {
                s.running = .stopped
                s.headline = st.installed ? Words.stopped : Words.stoppedStartsWhenOpened
            }
        }
        if let doctor, s.running != .notSetUp {
            for c in doctor.problems.prefix(MenuStatus.shownProblems) {
                s.problems.append(Problem(says: c.say ?? "", todo: c.do))
            }
            if doctor.problems.count > MenuStatus.shownProblems {
                s.problems.append(Problem(says: Words.moreProblems(doctor.problems.count - MenuStatus.shownProblems), todo: nil))
            }
        }
        return s
    }

    /// The menu shows at most this many of doctor's problems.
    public static let shownProblems = 3
}
