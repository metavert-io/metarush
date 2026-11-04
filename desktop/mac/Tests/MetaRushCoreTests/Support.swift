import Foundation
@testable import MetaRushCore

/// The fixtures: the program's answers as the Go types write them, and the
/// stand-in program.
let fixtures = URL(fileURLWithPath: #filePath).deletingLastPathComponent().appendingPathComponent("Fixtures")

func fixture(_ name: String) throws -> Data {
    try Data(contentsOf: fixtures.appendingPathComponent(name))
}

/// TempDir is a temporary folder, removed when the test lets go of it.
final class TempDir {
    let url: URL

    init() throws {
        url = FileManager.default.temporaryDirectory.appendingPathComponent("metarush-app-test-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
    }

    deinit {
        try? FileManager.default.removeItem(at: url)
    }
}

/// FakeMetaRush is the stand-in program (Fixtures/fake-metarush.sh) in a folder
/// of its own, answering what a test puts beside it.
final class FakeMetaRush {
    let dir: TempDir
    var folder: URL { dir.url }
    var program: URL { folder.appendingPathComponent("metarush") }

    init() throws {
        dir = try TempDir()
        try FileManager.default.copyItem(at: fixtures.appendingPathComponent("fake-metarush.sh"), to: program)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: program.path)
    }

    /// answer makes `command` (setup-questions, service-status, …) answer a fixture.
    func answer(_ command: String, _ fixtureName: String, status: Int32 = 0) throws {
        try fixture(fixtureName).write(to: folder.appendingPathComponent(command + ".json"))
        try String(status).write(to: folder.appendingPathComponent(command + ".status"), atomically: true, encoding: .utf8)
    }

    /// later keeps a fixture beside the program for a hook to switch to.
    func keep(_ fixtureName: String, as name: String) throws {
        try fixture(fixtureName).write(to: folder.appendingPathComponent(name))
    }

    /// hook runs before `command` answers (it sees $D, the folder).
    func hook(_ command: String, _ script: String) throws {
        try script.write(to: folder.appendingPathComponent(command + ".sh"), atomically: true, encoding: .utf8)
    }

    /// The command lines it was run with.
    var calls: [String] {
        ((try? String(contentsOf: folder.appendingPathComponent("calls.log"), encoding: .utf8)) ?? "")
            .split(separator: "\n").map(String.init)
    }

    func metarush(home: String? = "/tmp/metarush-app-test-home") -> MetaRush {
        MetaRush(program: program, home: home)
    }
}

/// A runner that answers with given output, without running anything.
struct Canned: CommandRunner {
    var output: CommandOutput
    func run(_ program: URL, _ arguments: [String], environment: [String: String]?) async throws -> CommandOutput { output }
}

/// Answers: the fixtures every finishing test starts from (an update from 0.1.0 to 0.2.0).
func finishingFake() throws -> FakeMetaRush {
    let f = try FakeMetaRush()
    try f.answer("version", "version-2.json")
    try f.answer("service-status", "service-status-old.json")
    try f.answer("skills-dry", "skills-dry-there.json")
    try f.answer("skills-install", "skills-install.json")
    try f.answer("renderer-install", "renderer-install.json")
    try f.answer("migrate", "migrate.json")
    try f.answer("service-restart", "service-restart.json")
    try f.answer("stop", "stop.json")
    try f.answer("dashboard", "dashboard.json")
    try f.keep("service-status-new.json", as: "after-restart.json")
    // MetaRush restarted runs the new build.
    try f.hook("service-restart", #"cp "$D/after-restart.json" "$D/service-status.json""#)
    return f
}

/// DataOf reads an answer's data.
struct DataOf<T: Decodable>: Decodable {
    var data: T
}
