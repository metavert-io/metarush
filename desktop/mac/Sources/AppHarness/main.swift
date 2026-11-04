import Foundation
import MetaRushCore
import Sparkle

// AppHarness: MetaRush.app's tool for its tests and its build, never shipped
// (D478). Each command prints one JSON line on stdout:
//
//   AppHarness apply-update <bundle> <feed>   the real Sparkle updates <bundle> from <feed>, accepting every step
//                                             (as a person pressing Install would): {"installed": true} or
//                                             {"error": code, "message": …}
//   AppHarness finish <bundle> [--home H]     the steps after an update with <bundle>'s own metarush
//   AppHarness compare <a> <b> [<a> <b>…]     Sparkle's order of each pair of bundle versions: -1, 0 or 1
//   AppHarness icon <folder.iconset>          the app's icon at every size (scripts/mac-app.sh)

func out(_ value: [String: Any]) {
    if let d = try? JSONSerialization.data(withJSONObject: value, options: [.sortedKeys]) {
        FileHandle.standardOutput.write(d)
        FileHandle.standardOutput.write(Data("\n".utf8))
    }
}

func say(_ s: String) {
    FileHandle.standardError.write(Data((s + "\n").utf8))
}

/// Accepts every step of an update, the way a person pressing Install does.
final class AcceptingDriver: NSObject, SPUUserDriver {
    func show(_ request: SPUUpdatePermissionRequest, reply: @escaping (SUUpdatePermissionResponse) -> Void) {
        reply(SUUpdatePermissionResponse(automaticUpdateChecks: false, sendSystemProfile: false))
    }
    func showUserInitiatedUpdateCheck(cancellation: @escaping () -> Void) { say("checking") }
    func showUpdateFound(with appcastItem: SUAppcastItem, state: SPUUserUpdateState, reply: @escaping (SPUUserUpdateChoice) -> Void) {
        say("found \(appcastItem.displayVersionString) (\(appcastItem.versionString))")
        reply(.install)
    }
    func showUpdateReleaseNotes(with downloadData: SPUDownloadData) {}
    func showUpdateReleaseNotesFailedToDownloadWithError(_ error: Error) {}
    func showUpdateNotFoundWithError(_ error: Error, acknowledgement: @escaping () -> Void) {
        out(["notFound": true, "message": error.localizedDescription])
        acknowledgement()
        exit(3)
    }
    func showUpdaterError(_ error: Error, acknowledgement: @escaping () -> Void) {
        let e = error as NSError
        var underlying: [Int] = []
        var u = e.userInfo[NSUnderlyingErrorKey] as? NSError
        while let x = u {
            underlying.append(x.code)
            u = x.userInfo[NSUnderlyingErrorKey] as? NSError
        }
        out(["error": e.code, "underlying": underlying, "message": e.localizedDescription])
        acknowledgement()
        exit(2)
    }
    func showDownloadInitiated(cancellation: @escaping () -> Void) { say("downloading") }
    func showDownloadDidReceiveExpectedContentLength(_ expectedContentLength: UInt64) {}
    func showDownloadDidReceiveData(ofLength length: UInt64) {}
    func showDownloadDidStartExtractingUpdate() { say("extracting") }
    func showExtractionReceivedProgress(_ progress: Double) {}
    func showReady(toInstallAndRelaunch reply: @escaping (SPUUserUpdateChoice) -> Void) { reply(.install) }
    func showInstallingUpdate(withApplicationTerminated applicationTerminated: Bool, retryTerminatingApplication: @escaping () -> Void) {
        say("installing")
    }
    func showUpdateInstalledAndRelaunched(_ relaunched: Bool, acknowledgement: @escaping () -> Void) {
        out(["installed": true])
        acknowledgement()
        exit(0)
    }
    func showUpdateInFocus() {}
    func dismissUpdateInstallation() {}
}

/// The feed the update is read from: the same choice the app makes (UpdateFeed).
final class FeedDelegate: NSObject, SPUUpdaterDelegate {
    let feed: String
    init(feed: String) { self.feed = feed }
    func feedURLString(for updater: SPUUpdater) -> String? {
        UpdateFeed.choose(built: feed, override: nil)?.absoluteString
    }
}

let args = Array(CommandLine.arguments.dropFirst())
switch args.first {
case "apply-update" where args.count == 3:
    guard let target = Bundle(path: args[1]) else {
        out(["error": -1, "message": "no bundle at \(args[1])"])
        exit(64)
    }
    let driver = AcceptingDriver()
    let delegate = FeedDelegate(feed: args[2])
    let updater = SPUUpdater(hostBundle: target, applicationBundle: target, userDriver: driver, delegate: delegate)
    do {
        try updater.start()
    } catch {
        out(["error": (error as NSError).code, "message": error.localizedDescription])
        exit(4)
    }
    updater.checkForUpdates()
    DispatchQueue.main.asyncAfter(deadline: .now() + 120) {
        out(["error": -2, "message": "timed out"])
        exit(5)
    }
    RunLoop.main.run() // Sparkle answers on the main thread

case "finish" where args.count >= 2:
    var home: String?
    if let i = args.firstIndex(of: "--home"), i + 1 < args.count { home = args[i + 1] }
    let program = MetaRush.inBundle(URL(fileURLWithPath: args[1]))
    let finishing = Finishing(metarush: MetaRush(program: program, home: home), checkTimeout: 10, checkEvery: 0.2)
    let done = DispatchSemaphore(value: 0)
    Task {
        let result = await finishing.run { step, state in
            switch state {
            case .waiting, .running: break
            case .done(let says): out(["step": step.rawValue, "state": "done", "says": says ?? ""])
            case .skipped(let why): out(["step": step.rawValue, "state": "skipped", "says": why])
            case .failed(let e): out(["step": step.rawValue, "state": "failed", "says": e.message, "code": e.code])
            }
        }
        switch result {
        case .success(let v):
            out(["finished": true, "version": v.version, "commit": v.commit])
            done.signal()
        case .failure(let f):
            out(["finished": false, "step": f.step?.rawValue ?? "", "code": f.error.code, "message": f.error.message])
            exit(1)
        }
    }
    done.wait()

case "compare" where args.count >= 3 && args.count % 2 == 1:
    var orders: [Int] = []
    for i in stride(from: 1, to: args.count, by: 2) {
        let r = SUStandardVersionComparator.default.compareVersion(args[i], toVersion: args[i + 1])
        orders.append(r == .orderedAscending ? -1 : (r == .orderedDescending ? 1 : 0))
    }
    out(["orders": orders])

case "icon" where args.count == 2:
    let folder = URL(fileURLWithPath: args[1])
    try? FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
    for (name, pixels) in Mark.iconSet {
        guard let image = Mark.icon(pixels: pixels), let png = Mark.png(image) else {
            out(["error": -1, "message": "couldn't draw \(name)"])
            exit(1)
        }
        try png.write(to: folder.appendingPathComponent(name))
    }
    out(["icons": Mark.iconSet.count])

default:
    say("usage: AppHarness apply-update <bundle> <feed> | finish <bundle> [--home H] | compare <a> <b> [<a> <b>…] | icon <folder.iconset>")
    exit(64)
}
