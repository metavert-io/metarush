import AppKit
import MetaRushCore
import SwiftUI

/// SetupModel is the first run (D473): `setup questions` gives the panels their
/// suggested answers, the person changes what they like, `setup` runs with
/// every answer as a flag. Nothing secret is asked here: a cloud bucket or a
/// database elsewhere sends the person to setup in Terminal. Another copy's
/// background service is a question of its own, its answer no; opening the
/// app at login is the app's own (D470), suggested yes the first time.
@MainActor
final class SetupModel: ObservableObject {
    enum Page: Equatable {
        case loading, welcome, places, terminal, thisMac, components, working, done, failed, stopped
    }

    @Published var page: Page = .loading
    @Published var questions: SetupQuestions?
    @Published var answers: SetupAnswers?
    @Published var link = true
    @Published var linkState: CommandLink.State = .missing
    @Published var result: SetupResult?
    @Published var error: MetaRushError?
    @Published var startedAt = Date()
    @Published var copied = false
    @Published var notYet = false
    @Published var linkNote: String?
    @Published var linkEffect: CommandLink.Effect = .none
    @Published var loginNote: String?
    @Published var portText = ""

    let metarush: MetaRush
    let commandLink: CommandLink
    let loginItem: LoginItem
    /// firstRun: nothing finished on this Mac yet (opening at login is then suggested).
    let firstRun: Bool
    /// finished: setup is done (the build is marked, the menu looks again).
    let finished: () -> Void
    private var task: Task<Void, Never>?

    init(metarush: MetaRush, commandLink: CommandLink, loginItem: LoginItem, firstRun: Bool, finished: @escaping () -> Void) {
        self.metarush = metarush
        self.commandLink = commandLink
        self.loginItem = loginItem
        self.firstRun = firstRun
        self.finished = finished
    }

    func load() {
        page = .loading
        task = Task {
            do {
                let q = try await metarush.call(["setup", "questions"], as: SetupQuestions.self)
                questions = q
                var a = SetupAnswers(q)
                a.openAtLogin = firstRun || loginItem.isOn
                answers = a
                portText = String(q.port)
                linkState = commandLink.state()
                linkEffect = q.setUp ? commandLink.effect(try? await metarush.call(["service", "status"], as: ServiceStatus.self)) : .none
                // Another program there is replaced only when the person ticks it.
                link = linkState == .missing && linkEffect == .none
                page = .welcome
            } catch {
                self.error = plainError(error)
                page = .failed
            }
        }
    }

    func next() {
        guard let q = questions, let a = answers else { return }
        switch page {
        case .welcome: page = q.setUp ? .thisMac : .places
        case .places: page = a.inTerminal(q) ? .terminal : .thisMac
        case .thisMac: page = .components
        default: break
        }
    }

    func back() {
        guard let q = questions else { return }
        switch page {
        case .places: page = .welcome
        case .terminal: page = .places
        case .thisMac: page = q.setUp ? .welcome : .places
        case .components: page = .thisMac
        default: break
        }
    }

    func chooseFolder() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.canCreateDirectories = true
        panel.message = Words.chooseFolderPrompt
        if panel.runModal() == .OK, let url = panel.url {
            answers?.folder = url.path
        }
    }

    func run() {
        guard let q = questions, var a = answers else { return }
        if let p = Int(portText.trimmingCharacters(in: .whitespaces)), p > 1024, p < 65536 { a.port = p }
        answers = a
        error = nil
        startedAt = Date()
        page = .working
        task = Task {
            do {
                let res = try await metarush.call(a.arguments(q), as: SetupResult.self)
                result = res
                if let retry = SetupAnswers.retryService(res) {
                    // One more try (it says why when it can't); the dashboard still starts MetaRush.
                    _ = try? await metarush.call(retry)
                }
                makeLink()
                loginNote = Self.loginWords(a.applyOpenAtLogin(loginItem))
                finished()
                page = .done
                openDashboard(welcome: res.fresh)
            } catch {
                if Task.isCancelled {
                    page = .stopped
                } else {
                    self.error = plainError(error)
                    page = .failed
                }
            }
        }
    }

    func stop() { task?.cancel() }

    private static func loginWords(_ r: SetupAnswers.OpenAtLogin) -> String? {
        switch r {
        case .on: return Words.openAtLoginOn
        case .needsApproval: return Words.openAtLoginApproval
        case .failed: return Words.openAtLoginFailed + " " + Words.openAtLoginFailedHint
        case .off, .unchanged: return nil
        }
    }

    private func makeLink() {
        if case .refused = linkEffect { return }
        guard link, linkState != .ours else { return }
        do {
            let aside = try commandLink.make(replacing: linkState != .missing)
            linkState = .ours
            linkNote = aside == nil ? Words.linkMade : Words.linkMade + " " + Words.linkKeptAside
        } catch {
            linkNote = Words.linkFailed
        }
    }

    func openDashboard(welcome: Bool) {
        Task {
            let words = ["dashboard"] + (welcome ? ["--next", "/%3Fwelcome%3D1"] : [])
            if let l = try? await metarush.call(words, as: DashboardLink.self), let u = URL(string: l.url) {
                NSWorkspace.shared.open(u)
            }
        }
    }

    /// The command setup runs with in Terminal: this app's own program, so it
    /// works before the terminal can find MetaRush by name.
    var terminalCommand: String {
        var c = shellQuoted(metarush.program.path) + " setup"
        if let h = metarush.home { c += " --home " + shellQuoted(h) }
        return c
    }

    func copyCommand() {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(terminalCommand, forType: .string)
        copied = true
    }

    func openTerminal() {
        NSWorkspace.shared.open(URL(fileURLWithPath: "/System/Applications/Utilities/Terminal.app"))
    }

    func checkAgain() {
        task = Task {
            if let q = try? await metarush.call(["setup", "questions"], as: SetupQuestions.self) {
                questions = q
                answers = SetupAnswers(q)
                portText = String(q.port)
                notYet = !q.setUp
                if q.setUp { page = .thisMac }
            }
        }
    }
}

func shellQuoted(_ s: String) -> String {
    "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'"
}

struct SetupView: View {
    @ObservedObject var model: SetupModel
    let close: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            switch model.page {
            case .loading:
                HStack(spacing: 10) {
                    ProgressView().controlSize(.small)
                    Plain(Words.lookingAround)
                }
            case .welcome: welcome
            case .places: places
            case .terminal: terminal
            case .thisMac: thisMac
            case .components: components
            case .working: working
            case .done: done
            case .failed: failed
            case .stopped:
                Plain(Words.stoppedText)
                buttons(primary: Words.tryAgain, { model.run() }, secondary: Words.close, close)
            }
        }
        .padding(22)
    }

    private var welcome: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(model.questions?.setUp == true ? Words.setUpAlready : Words.welcome).font(.title2.bold())
            Plain(model.questions?.setUp == true ? Words.setUpAlreadyText : Words.welcomeText)
            buttons(primary: Words.continueButton, { model.next() }, secondary: Words.close, close)
        }
    }

    @ViewBuilder private var places: some View {
        if let q = model.questions, model.answers != nil {
            VStack(alignment: .leading, spacing: 10) {
                Text(Words.storageQuestion).font(.headline)
                Picker("", selection: binding(\.cloudStorage)) {
                    Text(Words.storageFolder).tag(false)
                    Text(Words.storageBucket).tag(true)
                }
                .pickerStyle(.radioGroup)
                .labelsHidden()
                if model.answers?.cloudStorage == true {
                    Plain(Words.storageBucketText, secondary: true)
                } else {
                    HStack {
                        Text(model.answers?.folder ?? "").font(.callout).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
                        Spacer()
                        Button(Words.chooseFolder) { model.chooseFolder() }
                    }
                }
                Divider()
                Text(Words.databaseQuestion).font(.headline)
                if q.database.here {
                    Picker("", selection: binding(\.databaseElsewhere)) {
                        Text(Words.databaseHere).tag(false)
                        Text(Words.databaseElsewhere).tag(true)
                    }
                    .pickerStyle(.radioGroup)
                    .labelsHidden()
                    Plain(model.answers?.databaseElsewhere == true ? Words.databaseElsewhereText : q.database.says, secondary: true)
                    if let note = q.database.note, !note.isEmpty { Plain(note, secondary: true) }
                } else {
                    Plain(Words.noDatabaseHere)
                    Plain(Words.databaseElsewhereText, secondary: true)
                }
                buttons(primary: Words.continueButton, { model.next() }, secondary: Words.back, { model.back() })
            }
        }
    }

    private var terminal: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(Words.terminalTitle).font(.title3.bold())
            Plain(Words.terminalText)
            HStack {
                Button(model.copied ? Words.copied : Words.copyCommand) { model.copyCommand() }
                Button(Words.openTerminal) { model.openTerminal() }
                Spacer()
                Button(Words.checkAgain) { model.checkAgain() }
            }
            if model.notYet { Plain(Words.notSetUpInTerminalYet, secondary: true) }
            DisclosureGroup(Words.details) {
                Text(model.terminalCommand).font(.system(.caption, design: .monospaced)).textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            buttons(primary: nil, nil, secondary: Words.back, { model.back() })
        }
    }

    @ViewBuilder private var thisMac: some View {
        if let q = model.questions, model.answers != nil {
            VStack(alignment: .leading, spacing: 10) {
                Text(Words.thisMac).font(.title3.bold())
                switch SetupAnswers.serviceQuestion(q) {
                case .startAtLogin:
                    Toggle(Words.serviceToggle, isOn: binding(\.service))
                case .takeOver(let v):
                    // Another copy's service: a question of its own, its answer no (D473).
                    Plain(Words.serviceOtherCopy(v), secondary: true)
                    Toggle(Words.takeOverToggle, isOn: binding(\.takeOver))
                    if model.answers?.takeOver == true { Plain(Words.takeOverNote, secondary: true) }
                case .otherIsNewer(let v):
                    Plain(Words.serviceOtherNewer(v), secondary: true)
                }
                Toggle(Words.openAtLoginToggle, isOn: binding(\.openAtLogin))
                Plain(Words.openAtLoginNote, secondary: true)
                if q.skills.claudeInstalled {
                    Toggle(Words.skillsToggle, isOn: binding(\.skills))
                } else {
                    Plain(q.skills.says, secondary: true)
                }
                if model.linkState != .ours {
                    switch model.linkEffect {
                    case .refused(let running):
                        Plain(Words.linkRefused(running), secondary: true)
                    case .serviceFollows(let running):
                        Toggle(Words.linkToggle, isOn: $model.link)
                        if model.linkState != .missing { Plain(Words.linkToggleOther, secondary: true) }
                        Plain(Words.linkServiceFollows(running), secondary: true)
                    case .none:
                        Toggle(Words.linkToggle, isOn: $model.link)
                        if model.linkState != .missing { Plain(Words.linkToggleOther, secondary: true) }
                    }
                }
                DisclosureGroup(Words.moreSettings) {
                    HStack {
                        Text(Words.portLabel)
                        TextField("", text: $model.portText).frame(width: 80)
                    }
                    .padding(.top, 4)
                }
                buttons(primary: Words.continueButton, { model.next() }, secondary: Words.back, { model.back() })
            }
        }
    }

    @ViewBuilder private var components: some View {
        if let q = model.questions, let a = model.answers {
            VStack(alignment: .leading, spacing: 10) {
                Text(Words.componentsTitle).font(.title3.bold())
                Plain(Words.componentsText, secondary: true)
                ScrollView {
                    VStack(alignment: .leading, spacing: 8) {
                        ForEach(q.components) { c in
                            if c.ready {
                                Label {
                                    VStack(alignment: .leading) {
                                        Text(c.title)
                                        Text(Words.alreadyHere).font(.caption).foregroundStyle(.secondary)
                                    }
                                } icon: {
                                    Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
                                }
                            } else {
                                Toggle(isOn: Binding(get: { model.answers?.components.contains(c.name) ?? false },
                                                     set: { on in
                                                         if on { model.answers?.components.insert(c.name) } else { model.answers?.components.remove(c.name) }
                                                     })) {
                                    VStack(alignment: .leading, spacing: 2) {
                                        Text("\(c.title) (\(c.size))")
                                        Text(c.unlocks).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                                    }
                                }
                            }
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                .frame(height: 300)
                let size = a.size(q)
                Plain(Words.componentsSize(megabytes: size.megabytes, minutes: size.minutes))
                ForEach(a.licences(q), id: \.self) { Plain($0, secondary: true) }
                buttons(primary: Words.setUpButton, { model.run() }, secondary: Words.back, { model.back() })
            }
        }
    }

    private var working: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 10) {
                ProgressView().controlSize(.small)
                Text(Words.working).font(.title3.bold())
            }
            Plain(Words.workingText)
            TimelineView(.periodic(from: model.startedAt, by: 15)) { ctx in
                Plain(Words.elapsed(minutes: Int(ctx.date.timeIntervalSince(model.startedAt) / 60)), secondary: true)
            }
            buttons(primary: nil, nil, secondary: Words.stopSetup, { model.stop() })
        }
    }

    @ViewBuilder private var done: some View {
        if let r = model.result {
            VStack(alignment: .leading, spacing: 10) {
                Text(Words.readyTitle).font(.title2.bold())
                ForEach([r.database.says, r.storage.says, r.service.says, r.skills.says, r.components.says], id: \.self) { line in
                    Label { Plain(line) } icon: { Image(systemName: "checkmark").foregroundStyle(.green) }
                }
                if let note = model.linkNote { Label { Plain(note) } icon: { Image(systemName: "terminal") } }
                if let note = model.loginNote { Label { Plain(note) } icon: { Image(systemName: "power") } }
                Text(Words.whatWorks).font(.headline).padding(.top, 4)
                ForEach(r.whatWorks.noKeys) { w in
                    Label {
                        VStack(alignment: .leading, spacing: 1) {
                            Text(w.name)
                            Text(w.ready ? w.says : (w.next ?? w.says)).font(.caption).foregroundStyle(.secondary)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                    } icon: {
                        Image(systemName: w.ready ? "checkmark.circle.fill" : "circle").foregroundStyle(w.ready ? .green : .secondary)
                    }
                }
                Plain(r.whatWorks.claude.says, secondary: true)
                let left = (r.components.failed ?? []) + (r.components.needsYou ?? [])
                if !left.isEmpty {
                    Text(Words.needsYou).font(.headline).padding(.top, 4)
                    ForEach(left, id: \.name) { n in Plain("\(n.title): \(n.says)", secondary: true) }
                }
                ForEach(r.warnings ?? [], id: \.self) { Plain($0, secondary: true) }
                buttons(primary: Words.openMetaRush, { model.openDashboard(welcome: false); close() }, secondary: Words.close, close)
            }
        }
    }

    @ViewBuilder private var failed: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(Words.failedTitle).font(.title3.bold())
            if let e = model.error { ErrorBox(error: e) }
            buttons(primary: Words.tryAgain, {
                if model.questions == nil { model.load() } else { model.run() }
            }, secondary: Words.close, close)
        }
    }

    private func binding<T>(_ path: WritableKeyPath<SetupAnswers, T>) -> Binding<T> {
        Binding(get: { model.answers![keyPath: path] }, set: { model.answers?[keyPath: path] = $0 })
    }

    private func buttons(primary: String?, _ action: (() -> Void)?, secondary: String, _ other: @escaping () -> Void) -> some View {
        HStack {
            Button(secondary, action: other)
            Spacer()
            if let primary, let action {
                Button(primary, action: action).keyboardShortcut(.defaultAction)
            }
        }
        .padding(.top, 6)
    }
}
