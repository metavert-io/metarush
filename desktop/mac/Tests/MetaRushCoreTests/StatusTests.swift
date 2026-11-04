import Foundation
import Testing
@testable import MetaRushCore

/// What the menu says (D474), from the program's answers alone.
@Suite struct StatusTests {
    func data<T: Decodable>(_ name: String, _ type: T.Type) throws -> T {
        try JSONDecoder().decode(DataOf<T>.self, from: try fixture(name)).data
    }

    let app = VersionInfo(version: "0.2.0", release: "0.2.0", commit: "bbbbbbb")

    @Test func runningTheNewBuild() throws {
        let s = MenuStatus.build(app: app, service: .success(try data("service-status-new.json", ServiceStatus.self)), doctor: try data("doctor.json", DoctorReport.self))
        #expect(s.running == .running && s.headline == Words.running && s.version == "Version 0.2.0" && s.serviceInstalled && !s.otherCopy)
        #expect(s.problems.map(\.says) == ["The storage folder isn't there", "The database needs 1 update for this version of MetaRush"])
        #expect(s.problems.first?.todo == "Connect the drive that holds it.")
        #expect(s.log == "/Users/you/.metarush/logs/daemon.log")
    }

    @Test func stillRunningTheOldBuild() throws {
        let s = MenuStatus.build(app: app, service: .success(try data("service-status-old.json", ServiceStatus.self)), doctor: nil)
        #expect(s.version == Words.versionRunsOther("0.2.0", running: "0.1.0"))
    }

    @Test func stoppedAndNotSetUp() throws {
        let stopped = MenuStatus.build(app: app, service: .success(try data("service-status-stopped.json", ServiceStatus.self)), doctor: nil)
        #expect(stopped.running == .stopped && stopped.headline == Words.stopped)
        let none = MenuStatus.build(app: app, service: .success(try data("service-status-none.json", ServiceStatus.self)), doctor: nil)
        #expect(none.running == .stopped && none.headline == Words.stoppedStartsWhenOpened)
        let notSetUp = MetaRushError(code: "E_NOT_CONFIGURED", message: "MetaRush isn't set up on this machine yet")
        let s = MenuStatus.build(app: app, service: .failure(notSetUp), doctor: try data("doctor.json", DoctorReport.self))
        #expect(s.running == .notSetUp && s.headline == Words.notSetUp && s.problems.isEmpty)
    }

    @Test func otherCopyAndWorker() throws {
        let other = MenuStatus.build(app: app, service: .success(try data("service-status-other-copy.json", ServiceStatus.self)), doctor: nil)
        #expect(other.otherCopy)
        let worker = MenuStatus.build(app: app, service: .success(try data("service-status-worker-new.json", ServiceStatus.self)), doctor: nil)
        #expect(worker.running == .running && worker.headline == Words.runningWorkOnly)
    }

    @Test func manyProblemsAreCounted() throws {
        var d = try data("doctor.json", DoctorReport.self)
        d.checks = (1...5).map { DoctorReport.Check(area: "a", name: "n\($0)", status: "warn", say: "Problem \($0)", do: nil) }
        let s = MenuStatus.build(app: app, service: .success(try data("service-status-new.json", ServiceStatus.self)), doctor: d)
        #expect(s.problems.count == MenuStatus.shownProblems + 1 && s.problems.last?.says == Words.moreProblems(2))
    }

    @Test func anErrorItCantPlace() {
        let s = MenuStatus.build(app: nil, service: .failure(MetaRushError(code: "", message: Words.programMissing, hint: Words.programMissingHint)), doctor: nil)
        #expect(s.running == .unknown && s.headline == Words.cantTell && s.problems.first?.says == Words.programMissing && s.version == nil)
    }
}
