import Foundation
import Testing
@testable import MetaRushCore

/// The steps after an update (D472), with the stand-in program: deploy.sh's
/// order, skills only where they are, each failure plain, the check that the
/// build answering is the new one, and another copy running here asked about
/// before anything of it changes (D473).
@Suite struct FinishingTests {
    final class Seen: @unchecked Sendable {
        var steps: [(FinishStep, StepState)] = []
        func final(_ s: FinishStep) -> StepState? { steps.last { $0.0 == s }?.1 }
    }

    func run(_ fake: FakeMetaRush, timeout: TimeInterval = 2, takingOver: Bool = false) async -> (Result<VersionInfo, FinishFailure>, Seen) {
        let seen = Seen()
        let r = await Finishing(metarush: fake.metarush(), checkTimeout: timeout, checkEvery: 0.05).run(takingOver: takingOver) { step, state in
            seen.steps.append((step, state))
        }
        return (r, seen)
    }

    /// The command words the stand-in got, without --json and --home.
    func words(_ fake: FakeMetaRush) -> [String] {
        fake.calls.map { $0.replacingOccurrences(of: " --json --home /tmp/metarush-app-test-home", with: "") }
    }

    @Test func everyStepInDeploysOrder() async throws {
        let fake = try finishingFake()
        let (r, seen) = await run(fake)
        guard case .success(let v) = r else { Issue.record("\(r)"); return }
        #expect(v.version == "0.2.0" && v.commit == "bbbbbbb")
        #expect(words(fake) == ["version", "service status", "skills install --user --dry-run", "skills install --user", "renderer install",
                                "migrate", "service status", "service restart", "service status"])
        #expect(fake.calls.allSatisfy { $0.hasSuffix("--json --home /tmp/metarush-app-test-home") })
        for step in FinishStep.allCases {
            guard case .done = seen.final(step) else { Issue.record("\(step): \(String(describing: seen.final(step)))"); continue }
        }
    }

    @Test func skillsOnlyWhereTheyAre() async throws {
        let fake = try finishingFake()
        try fake.answer("skills-dry", "skills-dry-none.json")
        let (r, seen) = await run(fake)
        guard case .success = r else { Issue.record("\(r)"); return }
        #expect(!words(fake).contains("skills install --user"))
        #expect(seen.final(.skills) == .skipped(Words.skillsNotHere))
    }

    @Test func aLeftOutRendererStaysOut() async throws {
        let fake = try finishingFake()
        try fake.answer("renderer-install", "renderer-left-out.json")
        let (r, seen) = await run(fake)
        guard case .success = r else { Issue.record("\(r)"); return }
        let said = try JSONDecoder().decode(DataOf<JSONValue>.self, from: try fixture("renderer-left-out.json")).data["says"]?.string
        #expect(said?.contains("left out") == true && seen.final(.renderer) == .done(said))
    }

    @Test func aFailedStepStopsThere() async throws {
        let fake = try finishingFake()
        try fake.answer("migrate", "migrate-failed.json", status: 1)
        let (r, seen) = await run(fake)
        guard case .failure(let f) = r else { Issue.record("\(r)"); return }
        #expect(f.step == .records && f.error.message == "MetaRush can't reach its database." && f.error.command == "metarush doctor")
        #expect(!words(fake).contains("service restart"))
        #expect(seen.final(.restart) == nil)
    }

    @Test func aRestartThatFails() async throws {
        let fake = try finishingFake()
        try fake.answer("service-restart", "service-restart-failed.json", status: 1)
        let (r, _) = await run(fake)
        guard case .failure(let f) = r else { Issue.record("\(r)"); return }
        #expect(f.step == .restart && f.error.message == "MetaRush did not start again.")
    }

    @Test func theOldBuildStillAnswering() async throws {
        let fake = try finishingFake()
        try FileManager.default.removeItem(at: fake.folder.appendingPathComponent("service-restart.sh"))
        let (r, _) = await run(fake, timeout: 0.3)
        guard case .failure(let f) = r else { Issue.record("\(r)"); return }
        #expect(f.step == .check && f.error.message == Words.stillRuns("0.1.0", "0.2.0") && f.error.hint == Words.restartHint)
    }

    @Test func anotherCopyInTheBackgroundIsAskedAboutFirst() async throws {
        let fake = try finishingFake()
        try fake.answer("service-status", "service-status-other-copy.json")
        let (r, seen) = await run(fake)
        guard case .failure(let f) = r else { Issue.record("\(r)"); return }
        #expect(f.otherCopy && f.canTakeOver && f.step == nil && f.error.message == Words.otherCopyRuns && f.error.code == FinishFailure.otherServiceCode)
        // Nothing was changed for a program that isn't the one running.
        #expect(words(fake) == ["version", "service status"])
        #expect(seen.steps.isEmpty)
    }

    @Test func aYesSetsTheServiceUpAgainForThisCopyFirst() async throws {
        let fake = try finishingFake()
        try fake.answer("service-status", "service-status-other-copy.json")
        try fake.answer("service-install", "service-install.json")
        // Set up again, the service runs this copy's older build until it restarts.
        try fake.keep("service-status-old.json", as: "taken-over.json")
        try fake.hook("service-install", #"cp "$D/taken-over.json" "$D/service-status.json""#)
        let (r, _) = await run(fake, takingOver: true)
        guard case .success = r else { Issue.record("\(r)"); return }
        let w = words(fake)
        #expect(Array(w.prefix(3)) == ["version", "service status", "service install --keep-newer"])
        #expect(w.contains("service restart") && !w.contains { $0.hasPrefix("stop") })
    }

    @Test func aWorkerStaysAWorkerWhenTakenOver() async throws {
        let fake = try finishingFake()
        try fake.answer("service-status", "service-status-other-worker.json")
        try fake.answer("service-install", "service-install.json")
        let (r, _) = await run(fake)
        guard case .failure(let f) = r, f.canTakeOver else { Issue.record("\(r)"); return }
        try fake.keep("service-status-worker-new.json", as: "after-restart.json")
        try fake.keep("service-status-old.json", as: "taken-over.json")
        try fake.hook("service-install", #"cp "$D/taken-over.json" "$D/service-status.json""#)
        _ = await run(fake, takingOver: true)
        #expect(words(fake).contains("service install --keep-newer --worker-only"))
    }

    @Test func aNewerCopyIsNeverTakenOver() async throws {
        let fake = try finishingFake()
        try fake.answer("service-status", "service-status-other-newer.json")
        for takingOver in [false, true] {
            let (r, seen) = await run(fake, takingOver: takingOver)
            guard case .failure(let f) = r else { Issue.record("\(r)"); return }
            #expect(f.otherCopy && !f.canTakeOver && f.error.code == FinishFailure.newerRunningCode && f.error.message == Words.newerCopyRuns)
            #expect(seen.steps.isEmpty)
        }
        #expect(words(fake) == ["version", "service status", "version", "service status"])
    }

    @Test func theProgramRefusesANewerCopyItself() async throws {
        // The daemon isn't running, so only the program knows the other copy is newer.
        let fake = try finishingFake()
        try fake.answer("service-status", "service-status-other-copy.json")
        try fake.answer("service-install", "service-install-newer.json", status: 1)
        let (r, seen) = await run(fake, takingOver: true)
        guard case .failure(let f) = r else { Issue.record("\(r)"); return }
        #expect(f.step == nil && f.error.code == "E_CONFLICT" && f.error.message.contains("newer copy"))
        #expect(seen.steps.isEmpty && !words(fake).contains("migrate"))
    }

    @Test func anotherCopyOpenedByItsDashboardIsNeverStoppedWithoutAsking() async throws {
        for name in ["service-status-dashboard-other.json", "service-status-dashboard-unknown.json"] {
            let fake = try finishingFake()
            try fake.answer("service-status", name)
            let (r, seen) = await run(fake)
            guard case .failure(let f) = r else { Issue.record("\(name): \(r)"); continue }
            #expect(f.canTakeOver && f.error.code == FinishFailure.otherRunningCode && f.error.message == Words.otherCopyOpen)
            #expect(words(fake) == ["version", "service status"] && seen.steps.isEmpty)
            // Asked and answered yes: stopped and started again as this app's copy.
            try fake.keep("service-status-dashboard-new.json", as: "after-restart.json")
            try fake.hook("dashboard", #"cp "$D/after-restart.json" "$D/service-status.json""#)
            let (again, _) = await run(fake, takingOver: true)
            guard case .success = again else { Issue.record("\(name): \(again)"); continue }
            #expect(words(fake).contains("stop --keep-database") && words(fake).contains("dashboard"))
        }
    }

    @Test func aServiceThroughALinkThatRunsThisCopyGoesAhead() async throws {
        let fake = try finishingFake()
        try fake.answer("service-status", "service-status-via-link.json")
        let (r, _) = await run(fake)
        guard case .success = r else { Issue.record("\(r)"); return }
        #expect(words(fake).contains("service restart"))
    }

    @Test func aDashboardStartedByHandIsRestartedTheSameWay() async throws {
        let fake = try finishingFake()
        try fake.answer("service-status", "service-status-dashboard-old.json")
        try fake.keep("service-status-dashboard-new.json", as: "after-restart.json")
        try fake.hook("dashboard", #"cp "$D/after-restart.json" "$D/service-status.json""#)
        let (r, _) = await run(fake)
        guard case .success = r else { Issue.record("\(r)"); return }
        let w = words(fake)
        #expect(w.contains("stop --keep-database") && w.contains("dashboard") && !w.contains("service restart"))
    }

    @Test func nothingRunningStartsLater() async throws {
        let fake = try finishingFake()
        try fake.answer("service-status", "service-status-none.json")
        let (r, seen) = await run(fake)
        guard case .success = r else { Issue.record("\(r)"); return }
        #expect(seen.final(.restart) == .skipped(Words.notRunningStartsLater) && seen.final(.check) == .skipped(Words.notRunningStartsLater))
    }

    @Test func aWorkerWithoutTheDashboard() async throws {
        let fake = try finishingFake()
        try fake.keep("service-status-worker-new.json", as: "after-restart.json")
        let (r, _) = await run(fake)
        guard case .success = r else { Issue.record("\(r)"); return }
    }

    @Test func notSetUpHasNothingToFinish() async throws {
        let fake = try finishingFake()
        try fake.answer("service-status", "not-set-up.json", status: 3)
        let (r, seen) = await run(fake)
        guard case .success = r else { Issue.record("\(r)"); return }
        #expect(seen.steps.count == FinishStep.allCases.count && seen.steps.allSatisfy { $0.1 == .skipped(Words.notSetUpYet) })
        #expect(words(fake) == ["version", "service status"])
    }

    /// The marker, in memory (the tests never write the person's preferences).
    final class Memory: MarkerStore {
        var values: [String: String] = [:]
        func string(forKey key: String) -> String? { values[key] }
        func set(_ value: String?, forKey key: String) { values[key] = value }
    }

    /// Dotted numbers, enough for these builds (the app uses Sparkle's comparator).
    static func newer(_ a: String, _ b: String) -> Bool {
        let x = a.split(separator: ".").map { Int($0) ?? 0 }, y = b.split(separator: ".").map { Int($0) ?? 0 }
        for i in 0..<max(x.count, y.count) where (i < x.count ? x[i] : 0) != (i < y.count ? y[i] : 0) {
            return (i < x.count ? x[i] : 0) > (i < y.count ? y[i] : 0)
        }
        return false
    }

    @Test func theMarkerFinishesOnlyANewerBuildAndOnlyOnce() throws {
        let store = Memory()
        let m = FinishMarker(store: store, newer: FinishingTests.newer)
        #expect(m.atStart(build: "0.2.0") == .firstStart) // a first start is setup's
        m.finished(build: "0.1.0")
        #expect(m.atStart(build: "0.2.0") == .finish && m.atStart(build: "0.1.0") == .nothing)
        // Tried by itself and not finished (it failed, or the app quit): offered, never run again by itself.
        m.trying(build: "0.2.0")
        #expect(m.atStart(build: "0.2.0") == .offerAgain)
        m.finished(build: "0.2.0")
        #expect(m.atStart(build: "0.2.0") == .nothing && m.last == "0.2.0" && m.tried == nil)
        // An older build put back in place (a downgrade) never runs the steps by itself.
        #expect(m.atStart(build: "0.1.0") == .nothing && m.atStart(build: "0.1.9.4") == .nothing)
        #expect(m.atStart(build: "0.2.0.1") == .finish)
    }
}
