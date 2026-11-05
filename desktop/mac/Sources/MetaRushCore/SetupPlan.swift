import Foundation

/// SetupAnswers are a person's answers in the first-run panels (D473): they
/// start as setup's own suggestions (`setup questions`) and go back to setup
/// as flags, every one of them, with --yes for anything not asked.
public struct SetupAnswers: Equatable, Sendable {
    /// The folder for footage and finished videos (a folder on this Mac).
    public var folder: String
    /// cloudStorage: the person wants a cloud bucket. Its key is asked in
    /// Terminal only, never by the app, so setup then runs there.
    public var cloudStorage: Bool
    /// databaseElsewhere: a database the person already has. Its address holds
    /// a password, so setup then runs in Terminal.
    public var databaseElsewhere: Bool
    public var port: Int
    public var service: Bool
    /// takeOver: when another copy of MetaRush runs the background service,
    /// have it run this app's copy instead. A question of its own, its answer
    /// no until a person says yes, and never offered when that copy is newer.
    public var takeOver = false
    public var skills: Bool
    /// The optional components ticked (D324).
    public var components: Set<String>
    /// openAtLogin: the app itself opens when the person logs in (it checks
    /// for updates only while it runs). Not one of setup's flags: the app
    /// sets it (LoginItem). Suggested yes.
    public var openAtLogin = true

    public init(_ q: SetupQuestions) {
        folder = q.storage.folder ?? ""
        cloudStorage = q.storage.suggested != "folder"
        databaseElsewhere = q.database.suggested != "here"
        port = q.port
        service = q.service.suggested
        skills = q.skills.suggested
        components = Set(q.components.filter { $0.ticked && !$0.ready }.map(\.name))
    }

    /// The service question this home gets: whether this copy starts at
    /// login, or (another copy runs the service) whether it takes that
    /// copy's place, or nothing to ask (that copy is newer).
    public enum ServiceQuestion: Equatable, Sendable {
        case startAtLogin
        case takeOver(otherVersion: String?)
        case otherIsNewer(otherVersion: String?)
        /// That copy doesn't say which version it is, so it may be newer: not offered either.
        case otherUnknown
    }

    public static func serviceQuestion(_ q: SetupQuestions) -> ServiceQuestion {
        guard q.service.otherCopy == true else { return .startAtLogin }
        if q.service.newer == true { return .otherIsNewer(otherVersion: q.service.otherVersion) }
        if q.service.versionUnknown == true { return .otherUnknown }
        return .takeOver(otherVersion: q.service.otherVersion)
    }

    /// inTerminal: the answers need something the app never asks for (a
    /// bucket's key, a database's password): setup runs in Terminal instead.
    public func inTerminal(_ q: SetupQuestions) -> Bool {
        !q.setUp && (cloudStorage || databaseElsewhere)
    }

    /// The ticked components that aren't on this Mac, in setup's own order.
    public func toAdd(_ q: SetupQuestions) -> [String] {
        q.components.filter { components.contains($0.name) && !$0.ready }.map(\.name)
    }

    /// The download and the time the ticked components take.
    public func size(_ q: SetupQuestions) -> (megabytes: Int, minutes: Int) {
        let rows = q.components.filter { components.contains($0.name) && !$0.ready }
        return (rows.reduce(0) { $0 + $1.downloadMB }, rows.reduce(0) { $0 + $1.minutes })
    }

    /// The licences of the ticked components a person must know about (the
    /// renderer's says Remotion's).
    public func licences(_ q: SetupQuestions) -> [String] {
        q.components.filter { components.contains($0.name) && !$0.ready }.compactMap { $0.licence }.filter { !$0.isEmpty }
    }

    /// The command words for setup. A home set up already keeps where things
    /// are kept (setup refuses those flags there): only the port when it
    /// changed, the service, the skills and the components go.
    public func arguments(_ q: SetupQuestions) -> [String] {
        var a = ["setup", "--yes"]
        if !q.setUp {
            a += ["--storage", "folder", "--media-root", folder, "--database", "here", "--port", String(port)]
        } else if port != q.port {
            a += ["--port", String(port)]
        }
        switch SetupAnswers.serviceQuestion(q) {
        case .startAtLogin:
            a.append("--service=\(service)")
        case .takeOver:
            // Another copy's service stays as it is unless the person said yes.
            a += takeOver ? ["--service=true", "--take-over-service"] : ["--service=false"]
        case .otherIsNewer, .otherUnknown:
            a.append("--service=false")
        }
        if q.skills.claudeInstalled {
            a.append("--skills=\(skills)")
        }
        let add = toAdd(q)
        if add.isEmpty {
            a.append("--no-components")
        } else {
            a += ["--components", add.joined(separator: ",")]
        }
        return a
    }
}

extension SetupAnswers {
    /// The command for one more try at the background service when setup
    /// couldn't set it up: never over another copy's (setup left that one as
    /// it is), never over a newer copy (--keep-newer), and a worker stays a
    /// worker. nil: nothing to try.
    public static func retryService(_ r: SetupResult) -> [String]? {
        guard r.service.wanted, !r.service.installed, r.service.otherCopy != true else { return nil }
        return ["service", "install", "--keep-newer"] + (r.service.workerOnly == true ? ["--worker-only"] : [])
    }
}
