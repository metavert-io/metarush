import AppKit
import MetaRushCore
import SwiftUI

/// MoveModel moves the app to Applications (D476): from a temporary copy or a
/// disk image it must, from elsewhere it may stay. A MetaRush there that is
/// newer, or open, stays unless the person says to replace it (MovePlan).
@MainActor
final class MoveModel: ObservableObject {
    let placement: Placement
    let plan: MovePlan
    let thisVersion: String
    let move: () throws -> Void
    let useThere: () -> Void
    /// quitThere asks the MetaRush open from Applications to quit, and says whether it did.
    let quitThere: () async -> Bool
    @Published var error: MetaRushError?
    @Published var working = false

    init(placement: Placement, plan: MovePlan, thisVersion: String, move: @escaping () throws -> Void, useThere: @escaping () -> Void,
         quitThere: @escaping () async -> Bool) {
        self.placement = placement
        self.plan = plan
        self.thisVersion = thisVersion
        self.move = move
        self.useThere = useThere
        self.quitThere = quitThere
    }

    func tryMove() {
        do {
            try move()
        } catch {
            self.error = MetaRushError(code: "", message: Words.moveFailed, hint: Words.moveFailedHint, detail: "\(error)")
        }
    }

    func quitThenMove() {
        working = true
        Task {
            if await quitThere() {
                tryMove()
            } else {
                error = MetaRushError(code: "", message: Words.didntQuit, hint: Words.didntQuitHint)
            }
            working = false
        }
    }
}

struct MoveView: View {
    @ObservedObject var model: MoveModel
    let keep: () -> Void
    let quit: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(Words.moveTitle).font(.title3.bold())
            Plain(Words.moveText(model.placement))
            switch model.plan {
            case .askNewer(let there):
                Plain(Words.moveNewerText(there: there.shown, this: model.thisVersion))
            case .askOpen(let there):
                Plain(Words.moveOpenText(there: there?.shown))
            case .replace(let there) where !there.shown.isEmpty:
                Plain(Words.moveReplacesOlder(there.shown), secondary: true)
            default:
                EmptyView()
            }
            if let error = model.error { ErrorBox(error: error) }
            HStack {
                if model.placement.mustMove {
                    Button(Words.quit, action: quit)
                } else {
                    Button(Words.keepHere, action: keep)
                }
                Spacer()
                switch model.plan {
                case .askNewer:
                    Button(Words.replaceWithThis) { model.tryMove() }
                    Button(Words.openTheNewer) { model.useThere() }.keyboardShortcut(.defaultAction)
                case .askOpen:
                    Button(Words.quitAndReplace) { model.quitThenMove() }.disabled(model.working)
                    Button(Words.useThatOne) { model.useThere() }.keyboardShortcut(.defaultAction)
                default:
                    Button(Words.moveButton) { model.tryMove() }.keyboardShortcut(.defaultAction)
                }
            }
        }
        .padding(22)
    }
}

/// LinkModel lets the terminal and Claude Code find the app's MetaRush
/// (D475), asking before it takes another copy's place: it says both versions,
/// what the link means for MetaRush in the background when the service starts
/// MetaRush from there, and refuses when the copy running there is newer.
@MainActor
final class LinkModel: ObservableObject {
    let link: CommandLink
    let metarush: MetaRush
    @Published var state: CommandLink.State
    @Published var effect: CommandLink.Effect
    @Published var otherVersion: String?
    @Published var appVersion: String?
    @Published var said: String?
    @Published var failed: MetaRushError?

    init(link: CommandLink, metarush: MetaRush, service: ServiceStatus?) {
        self.link = link
        self.metarush = metarush
        state = link.state()
        effect = link.effect(service)
    }

    /// load asks the program there its version and this app's program its own,
    /// and the service again (the menu's answer may be a minute old).
    func load() {
        Task {
            if let st = try? await metarush.call(["service", "status"], as: ServiceStatus.self) { effect = link.effect(st) }
            appVersion = (try? await metarush.call(["version"], as: VersionInfo.self))?.version
            if state != .missing && state != .ours { otherVersion = await link.versionThere() }
        }
    }

    func make(replacing: Bool) {
        if case .refused = effect { return }
        do {
            let aside = try link.make(replacing: replacing)
            state = .ours
            said = aside == nil ? Words.linkMade : Words.linkMade + " " + Words.linkKeptAside
        } catch {
            failed = MetaRushError(code: "", message: Words.linkFailed, detail: "\(error)")
        }
    }
}

struct LinkView: View {
    @ObservedObject var model: LinkModel
    let close: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(Words.linkTitle).font(.title3.bold())
            if let said = model.said {
                Plain(said)
                Plain(Words.linkPathHint, secondary: true)
                DisclosureGroup(Words.details) {
                    Text("\(model.link.link.path) → \(model.link.target.path)")
                        .font(.system(.caption, design: .monospaced)).textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                HStack { Spacer(); Button(Words.done, action: close).keyboardShortcut(.defaultAction) }
            } else {
                switch model.state {
                case .ours:
                    Plain(Words.linkAlready)
                    HStack { Spacer(); Button(Words.close, action: close).keyboardShortcut(.defaultAction) }
                case .missing:
                    Plain(Words.linkText)
                    effectNote
                    if let failed = model.failed { ErrorBox(error: failed) }
                    HStack {
                        Button(Words.notNow, action: close)
                        Spacer()
                        if !refused { Button(Words.makeLink) { model.make(replacing: false) }.keyboardShortcut(.defaultAction) }
                    }
                case .otherLink, .otherProgram:
                    Plain(Words.linkOtherText)
                    if let v = Words.linkVersions(other: model.otherVersion, app: model.appVersion) { Plain(v) }
                    effectNote
                    if let failed = model.failed { ErrorBox(error: failed) }
                    HStack {
                        Button(Words.keepOther, action: close).keyboardShortcut(refused ? .defaultAction : .cancelAction)
                        Spacer()
                        if !refused { Button(Words.useAppsCopy) { model.make(replacing: true) } }
                    }
                }
            }
        }
        .padding(22)
    }

    private var refused: Bool {
        if case .refused = model.effect { return true }
        return false
    }

    @ViewBuilder private var effectNote: some View {
        switch model.effect {
        case .none: EmptyView()
        case .serviceFollows(let running): Plain(Words.linkServiceFollows(running))
        case .refused(let running): Plain(Words.linkRefused(running))
        }
    }
}
