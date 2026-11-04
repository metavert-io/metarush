import AppKit
import MetaRushCore
import SwiftUI

/// MoveModel moves the app to Applications (D476): from a temporary copy or a
/// disk image it must, from elsewhere it may stay.
@MainActor
final class MoveModel: ObservableObject {
    let placement: Placement
    let move: () throws -> Void
    @Published var error: MetaRushError?

    init(placement: Placement, move: @escaping () throws -> Void) {
        self.placement = placement
        self.move = move
    }

    func tryMove() {
        do {
            try move()
        } catch {
            self.error = MetaRushError(code: "", message: Words.moveFailed, hint: Words.moveFailedHint, detail: "\(error)")
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
            if let error = model.error { ErrorBox(error: error) }
            HStack {
                if model.placement.mustMove {
                    Button(Words.quit, action: quit)
                } else {
                    Button(Words.keepHere, action: keep)
                }
                Spacer()
                Button(Words.moveButton) { model.tryMove() }.keyboardShortcut(.defaultAction)
            }
        }
        .padding(22)
    }
}

/// LinkModel lets the terminal and Claude Code find the app's MetaRush
/// (D475), asking before it takes another copy's place.
@MainActor
final class LinkModel: ObservableObject {
    let link: CommandLink
    @Published var state: CommandLink.State
    @Published var said: String?
    @Published var failed: MetaRushError?

    init(link: CommandLink) {
        self.link = link
        state = link.state()
    }

    func make(replacing: Bool) {
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
                    if let failed = model.failed { ErrorBox(error: failed) }
                    HStack {
                        Button(Words.notNow, action: close)
                        Spacer()
                        Button(Words.makeLink) { model.make(replacing: false) }.keyboardShortcut(.defaultAction)
                    }
                case .otherLink, .otherProgram:
                    Plain(Words.linkOtherText)
                    if let failed = model.failed { ErrorBox(error: failed) }
                    HStack {
                        Button(Words.keepOther, action: close)
                        Spacer()
                        Button(Words.useAppsCopy) { model.make(replacing: true) }.keyboardShortcut(.defaultAction)
                    }
                }
            }
        }
        .padding(22)
    }
}
