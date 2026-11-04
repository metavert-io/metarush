import Foundation
import Testing
@testable import MetaRushCore

/// The app reads the program's answers as the Go types write them (the
/// fixtures), and says its failures in the program's own plain words.
@Suite struct AnswersTests {
    func canned(_ name: String, status: Int32 = 0, stderr: String = "") throws -> MetaRush {
        MetaRush(program: URL(fileURLWithPath: "/nowhere/metarush"), home: "/tmp/h",
                 runner: Canned(output: CommandOutput(status: status, stdout: try fixture(name), stderr: Data(stderr.utf8))))
    }

    @Test func everyCommandGetsJSONAndTheHome() {
        let m = MetaRush(program: URL(fileURLWithPath: "/x/metarush"), home: "/tmp/h")
        #expect(m.arguments(["service", "status"]) == ["service", "status", "--json", "--home", "/tmp/h"])
        let own = MetaRush(program: URL(fileURLWithPath: "/x/metarush"))
        #expect(own.arguments(["version"]) == ["version", "--json"])
        #expect(MetaRush.inBundle(URL(fileURLWithPath: "/Applications/MetaRush.app")).path == "/Applications/MetaRush.app/Contents/Helpers/metarush")
    }

    @Test func theModelsReadWhatTheGoTypesWrite() async throws {
        let v = try await canned("version-2.json").call(["version"], as: VersionInfo.self)
        #expect(v == VersionInfo(version: "0.2.0", release: "0.2.0", commit: "bbbbbbb", dev: false))
        let q = try await canned("setup-questions.json").call(["setup", "questions"], as: SetupQuestions.self)
        #expect(!q.setUp && q.storage.suggested == "folder" && q.database.here && q.database.downloadMB == 85 && q.port == 4845)
        #expect(q.components.map(\.name) == ["ffmpeg", "node", "renderer", "studio-voice"])
        #expect(q.components.first { $0.name == "renderer" }?.licence?.contains("Remotion") == true)
        let r = try await canned("setup.json").call(["setup"], as: SetupResult.self)
        #expect(r.fresh && r.service.installed && r.whatWorks.noKeys.count == 2 && r.components.needsYou?.first?.name == "node")
        let s = try await canned("service-status-new.json").call(["service", "status"], as: ServiceStatus.self)
        #expect(s.installed && s.sameBinary && s.daemon?.version == "0.2.0" && s.daemon?.commit == "bbbbbbb" && s.daemon?.answering == true)
        let none = try await canned("service-status-none.json").call(["service", "status"], as: ServiceStatus.self)
        #expect(!none.installed && none.daemon == nil && none.log.hasSuffix("daemon.log"))
        let d = try await canned("doctor.json", status: 1).call(["doctor"], as: DoctorReport.self)
        #expect(!d.ok && d.problems.map(\.status) == ["fail", "warn"] && d.problems.first?.say == "The storage folder isn't there")
        let link = try await canned("dashboard.json").call(["dashboard"], as: DashboardLink.self)
        #expect(link.url.hasPrefix("http://127.0.0.1:4845/auth?t="))
        let skills = try await canned("skills-dry-none.json").call(["skills", "install", "--user", "--dry-run"], as: SkillsInstall.self)
        #expect(!skills.there)
        #expect(try await canned("skills-dry-there.json").call(["skills"], as: SkillsInstall.self).there)
        let left = try await canned("renderer-left-out.json").call(["renderer", "install"])
        #expect(left["setUp"]?.bool == false && left["says"]?.string?.contains("left out") == true)
        let ready = try await canned("renderer-install.json").call(["renderer", "install"])
        #expect(ready["ready"]?.bool == true && ready["says"] == nil)
        let other = try await canned("service-status-other-newer.json").call(["service", "status"], as: ServiceStatus.self)
        #expect(other.otherCopy == true && other.startsAnotherCopy && other.daemon?.sameCopy == false && other.daemon?.newer == true && other.daemon?.version == "0.3.0")
        let compared = try await canned("version-2-compared-0.3.0.json").call(["version", "--compare", "0.3.0"], as: VersionInfo.self)
        #expect(compared.version == "0.2.0" && compared.compared == VersionInfo.Compared(other: "0.3.0", order: -1, known: true))
        let unordered = try await canned("version-2-compared-unknown.json").call(["version"], as: VersionInfo.self)
        #expect(unordered.compared?.known == false)
        let worker = try await canned("setup-questions-other-worker.json").call(["setup", "questions"], as: SetupQuestions.self)
        #expect(worker.service.otherCopy == true && worker.service.workerOnly == true && worker.service.otherVersion == "0.1.0" && !worker.service.suggested)
    }

    @Test func aFailureIsTheProgramsOwnWords() async throws {
        do {
            _ = try await canned("not-set-up.json", status: 3).call(["service", "status"], as: ServiceStatus.self)
            Issue.record("no error")
        } catch let e as MetaRushError {
            #expect(e.notSetUp && e.message == "MetaRush isn't set up on this machine yet" && e.command == "metarush doctor" && e.status == 3)
            #expect(e.details.contains("code: E_NOT_CONFIGURED") && e.details.contains("command: metarush doctor"))
        }
    }

    @Test func aProgramThatSaysNothing() throws {
        do {
            _ = try MetaRush.answer(CommandOutput(status: 137, stdout: Data(), stderr: Data()))
            Issue.record("no error")
        } catch let e as MetaRushError {
            #expect(e.message == Words.programStopped && e.hint == Words.programStoppedHint)
        }
        do {
            _ = try MetaRush.answer(CommandOutput(status: 2, stdout: Data("panic".utf8), stderr: Data("it broke\n".utf8)))
            Issue.record("no error")
        } catch let e as MetaRushError {
            #expect(e.message == Words.failedWithoutSaying && e.detail == "it broke")
        }
    }

    @Test func aProgramThatIsntThere() async throws {
        let m = MetaRush(program: URL(fileURLWithPath: "/nowhere/at/all/metarush"))
        do {
            try await m.call(["version"])
            Issue.record("no error")
        } catch let e as MetaRushError {
            #expect(e.message == Words.programMissing)
        }
    }

    @Test func theRunnerReadsBothStreamsAndTheStatus() async throws {
        let out = try await ProcessRunner().run(URL(fileURLWithPath: "/bin/sh"), ["-c", "printf out; printf err >&2; exit 3"], environment: nil)
        #expect(out.status == 3 && String(decoding: out.stdout, as: UTF8.self) == "out" && String(decoding: out.stderr, as: UTF8.self) == "err")
        // A lot of output never stalls the program.
        let big = try await ProcessRunner().run(URL(fileURLWithPath: "/bin/sh"), ["-c", "head -c 2000000 /dev/zero; head -c 300000 /dev/zero >&2"], environment: nil)
        #expect(big.status == 0 && big.stdout.count == 2_000_000 && big.stderr.count == 300_000)
    }

    @Test func stoppingTheTaskStopsTheProgram() async throws {
        let started = Date()
        let task = Task {
            try await ProcessRunner().run(URL(fileURLWithPath: "/bin/sleep"), ["30"], environment: nil)
        }
        try await Task.sleep(nanoseconds: 300_000_000)
        task.cancel()
        let out = try await task.value
        #expect(out.status != 0 && Date().timeIntervalSince(started) < 10)
    }
}
