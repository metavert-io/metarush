import Foundation

/// MetaRush is the `metarush` program the app carries, run with --json (D470).
/// The app asks it everything (what setup asks, whether MetaRush runs, what
/// doctor found) and has it do everything (setup, the background service, the
/// steps after an update), so nothing MetaRush knows is written twice here.
public struct MetaRush: Sendable {
    /// The program: MetaRush.app/Contents/Helpers/metarush.
    public var program: URL
    /// MetaRush's home (--home); nil is MetaRush's own choice (METARUSH_HOME,
    /// else ~/.metarush). Tests always give a temporary one.
    public var home: String?
    public var runner: CommandRunner
    /// The environment it runs with; nil is the app's own.
    public var environment: [String: String]?

    public init(program: URL, home: String? = nil, runner: CommandRunner = ProcessRunner(), environment: [String: String]? = nil) {
        self.program = program
        self.home = home
        self.runner = runner
        self.environment = environment
    }

    /// The program inside an app bundle.
    public static func inBundle(_ bundle: URL) -> URL {
        bundle.appendingPathComponent("Contents/Helpers/metarush")
    }

    /// arguments: the command line for `words` (--json and --home added).
    public func arguments(_ words: [String]) -> [String] {
        var args = words + ["--json"]
        if let home { args += ["--home", home] }
        return args
    }

    /// Runs a command and returns its answer's data. A command that says it
    /// failed throws its own error (code, plain message, what to do).
    @discardableResult
    public func call(_ words: [String]) async throws -> JSONValue {
        try MetaRush.answer(try await output(words))
    }

    /// Runs a command and decodes its answer's data.
    public func call<T: Decodable>(_ words: [String], as type: T.Type) async throws -> T {
        let out = try await output(words)
        do {
            let env = try JSONDecoder().decode(Typed<T>.self, from: out.stdout)
            if env.ok, let data = env.data { return data }
        } catch {
            _ = try MetaRush.answer(out) // its own error, when it said one
            throw MetaRushError.unexpected(Words.unexpectedAnswer, detail: "\(words.joined(separator: " ")): \(error)")
        }
        _ = try MetaRush.answer(out)
        throw MetaRushError.unexpected(Words.unexpectedAnswer, detail: "\(words.joined(separator: " ")): no data")
    }

    private func output(_ words: [String]) async throws -> CommandOutput {
        do {
            return try await runner.run(program, arguments(words), environment: environment)
        } catch {
            throw MetaRushError.couldNotRun(error)
        }
    }

    private struct Typed<T: Decodable>: Decodable {
        var ok: Bool
        var data: T?
    }

    /// answer reads what a command printed: {"ok": true, "data": …} or
    /// {"ok": false, "error": {code, message, hint, details}}. A command may
    /// answer and still end with a non-zero status (doctor does when something
    /// needs fixing): the answer counts.
    public static func answer(_ out: CommandOutput) throws -> JSONValue {
        if let env = try? JSONDecoder().decode(Envelope.self, from: out.stdout) {
            if env.ok { return env.data ?? .null }
            if let e = env.error {
                throw MetaRushError(code: e.code ?? "", message: e.message ?? Words.failedWithoutSaying, hint: e.hint,
                                    command: e.details?["command"]?.string, status: out.status, detail: nil)
            }
        }
        // No answer at all: the program stopped before it could say anything.
        let said = String(decoding: out.stderr, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
        if out.status == 137 || out.status == 9 {
            throw MetaRushError(code: "", message: Words.programStopped, hint: Words.programStoppedHint, command: nil, status: out.status, detail: said)
        }
        throw MetaRushError(code: "", message: Words.failedWithoutSaying, hint: nil, command: nil, status: out.status, detail: said.isEmpty ? nil : said)
    }

    private struct Envelope: Decodable {
        var ok: Bool
        var data: JSONValue?
        var error: ErrorBody?
    }

    private struct ErrorBody: Decodable {
        var code: String?
        var message: String?
        var hint: String?
        var details: JSONValue?
    }
}

/// MetaRushError is what went wrong, in MetaRush's own plain words: message
/// and hint are for people; code, command and detail go behind Details.
public struct MetaRushError: Error, Equatable, LocalizedError, Sendable {
    public var code: String
    public var message: String
    public var hint: String?
    /// The command that fixes it (details.command), for Details.
    public var command: String?
    public var status: Int32
    /// Anything else technical (what the program printed, the cause).
    public var detail: String?

    public init(code: String, message: String, hint: String? = nil, command: String? = nil, status: Int32 = 1, detail: String? = nil) {
        self.code = code
        self.message = message
        self.hint = hint
        self.command = command
        self.status = status
        self.detail = detail
    }

    public var errorDescription: String? { message }

    /// Details: the technical part, one fact a line.
    public var details: String {
        var lines: [String] = []
        if !code.isEmpty { lines.append("code: \(code)") }
        if let command, !command.isEmpty { lines.append("command: \(command)") }
        if status != 0 && status != 1 { lines.append("exit status: \(status)") }
        if let detail, !detail.isEmpty { lines.append(detail) }
        return lines.joined(separator: "\n")
    }

    static func couldNotRun(_ error: Error) -> MetaRushError {
        MetaRushError(code: "", message: Words.programMissing, hint: Words.programMissingHint, command: nil, status: -1, detail: "\(error)")
    }

    static func unexpected(_ message: String, detail: String) -> MetaRushError {
        MetaRushError(code: "", message: message, hint: nil, command: nil, status: 0, detail: detail)
    }

    /// notSetUp: MetaRush isn't set up in this home yet (E_NOT_CONFIGURED).
    public var notSetUp: Bool { code == "E_NOT_CONFIGURED" }
}

/// JSONValue is any JSON, for answers read a field at a time.
public enum JSONValue: Codable, Equatable, Sendable {
    case null
    case bool(Bool)
    case number(Double)
    case string(String)
    case array([JSONValue])
    case object([String: JSONValue])

    public init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() {
            self = .null
        } else if let b = try? c.decode(Bool.self) {
            self = .bool(b)
        } else if let n = try? c.decode(Double.self) {
            self = .number(n)
        } else if let s = try? c.decode(String.self) {
            self = .string(s)
        } else if let a = try? c.decode([JSONValue].self) {
            self = .array(a)
        } else {
            self = .object(try c.decode([String: JSONValue].self))
        }
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .null: try c.encodeNil()
        case .bool(let b): try c.encode(b)
        case .number(let n): try c.encode(n)
        case .string(let s): try c.encode(s)
        case .array(let a): try c.encode(a)
        case .object(let o): try c.encode(o)
        }
    }

    public subscript(key: String) -> JSONValue? {
        if case .object(let o) = self { return o[key] }
        return nil
    }

    public var string: String? {
        if case .string(let s) = self { return s }
        return nil
    }

    public var bool: Bool? {
        if case .bool(let b) = self { return b }
        return nil
    }

    public var array: [JSONValue]? {
        if case .array(let a) = self { return a }
        return nil
    }
}
