import Foundation
import Testing
@testable import MetaRushCore

/// The steps after an update (D472), with the stand-in program: deploy.sh's
/// order, skills only where they are, each failure plain, and the check that
/// the build answering is the new one.
@Suite struct FinishingTests {
    final class Seen: @unchecked Sendable {
        var steps: [(FinishStep, StepState)] = []
        func final(_ s: FinishStep) -> StepState? { steps.last { $0.0 == s }?.1 }
    }

    func run(_ fake: FakeMetaRush, timeout: TimeInterval = 2) async -> (Result<VersionInfo, FinishFailure>, Seen) {
        let seen = Seen()
        let r = await Finishing(metarush: fake.metarush(), checkTimeout: timeout, checkEvery: 0.05).run { step, state in
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
        #expect(seen.final(.renderer) == .done("You left the renderer out, so it isn't set up."))
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
        #expect(f.otherCopy && f.step == nil && f.error.message == Words.otherCopyRuns)
        // Nothing was changed for a program that isn't the one running.
        #expect(words(fake) == ["version", "service status"])
        #expect(seen.steps.isEmpty)
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

    @Test func theMarker() throws {
        let defaults = UserDefaults(suiteName: "metarush-app-test-" + UUID().uuidString)!
        let m = FinishMarker(defaults: defaults)
        #expect(!m.needed(build: "0.2.0")) // a first start is setup's
        m.finished(build: "0.1.0")
        #expect(m.needed(build: "0.2.0") && !m.needed(build: "0.1.0"))
        m.finished(build: "0.2.0")
        #expect(!m.needed(build: "0.2.0") && m.last == "0.2.0")
    }
}
