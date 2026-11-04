import Foundation

/// What one run of a program gave back.
public struct CommandOutput: Sendable {
    public var status: Int32
    public var stdout: Data
    public var stderr: Data

    public init(status: Int32, stdout: Data, stderr: Data) {
        self.status = status
        self.stdout = stdout
        self.stderr = stderr
    }
}

/// Runs a program: the app runs the `metarush` it carries through one, tests a
/// stand-in.
public protocol CommandRunner: Sendable {
    func run(_ program: URL, _ arguments: [String], environment: [String: String]?) async throws -> CommandOutput
}

/// ProcessRunner runs a program with typed arguments and no shell. Cancelling
/// the task asks the program to stop the way Ctrl-C does (setup then says it can
/// carry on where it stopped).
public struct ProcessRunner: CommandRunner {
    public init() {}

    public func run(_ program: URL, _ arguments: [String], environment: [String: String]?) async throws -> CommandOutput {
        let process = Process()
        process.executableURL = program
        process.arguments = arguments
        if let environment { process.environment = environment }
        let out = Pipe(), err = Pipe()
        process.standardOutput = out
        process.standardError = err
        process.standardInput = FileHandle.nullDevice
        let box = OutputBox()
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (k: CheckedContinuation<CommandOutput, Error>) in
                // Both pipes are read while the program runs, so a full one never
                // stalls it; the answer waits for both (entered before the start, so
                // a program that ends at once still gives all it wrote).
                let reading = DispatchGroup()
                reading.enter()
                reading.enter()
                process.terminationHandler = { p in
                    reading.notify(queue: .global()) {
                        k.resume(returning: CommandOutput(status: p.terminationStatus, stdout: box.out, stderr: box.err))
                    }
                }
                do {
                    try process.run()
                } catch {
                    reading.leave()
                    reading.leave()
                    k.resume(throwing: error)
                    return
                }
                DispatchQueue.global().async {
                    box.out = out.fileHandleForReading.readDataToEndOfFile()
                    reading.leave()
                }
                DispatchQueue.global().async {
                    box.err = err.fileHandleForReading.readDataToEndOfFile()
                    reading.leave()
                }
            }
        } onCancel: {
            if process.isRunning { process.interrupt() }
        }
    }
}

private final class OutputBox: @unchecked Sendable {
    var out = Data()
    var err = Data()
}
