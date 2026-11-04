import AppKit
import MetaRushCore
import SwiftUI

/// AppDelegate is the menu: what MetaRush on this Mac is doing (D474), Open
/// MetaRush, Check for Updates…, Start/Stop, Show Logs, Quit. At start it moves
/// itself to Applications when it must (D476), finishes an update when one was
/// installed (D472), or opens setup the first time (D473).
@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate {
    private var statusItem: NSStatusItem!
    private let menu = NSMenu()
    private let updates = Updates()
    private var metarush: MetaRush!
    private var link: CommandLink!
    private var status = MenuStatus()
    private var appVersion: VersionInfo?
    private var doctor: DoctorReport?
    private var doctorAt: Date?
    private var statusAt: Date?
    private var refreshing = false
    /// The version whose update isn't finished (the steps failed or were closed).
    private var unfinished: String?
    private var busy: String?
    private var timer: Timer?

    private let setupWindow = Window(), finishWindow = Window(), moveWindow = Window(), linkWindow = Window()
    private var setupModel: SetupModel?
    private var finishModel: FinishingModel?

    /// This build (what the steps after an update finish); its version for people.
    private var build: String { Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "0" }
    private var shortVersion: String { Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? build }

    func applicationDidFinishLaunching(_ notification: Notification) {
        let bundle = Bundle.main.bundleURL
        // MetaRushHome: a Mac may point the app at another home (defaults write <bundle ID> MetaRushHome <folder>).
        metarush = MetaRush(program: MetaRush.inBundle(bundle), home: UserDefaults.standard.string(forKey: "MetaRushHome"))
        link = CommandLink(folder: CommandLink.personalFolder(), target: metarush.program)
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        statusItem.button?.toolTip = "MetaRush"
        menu.autoenablesItems = false
        menu.delegate = self
        statusItem.menu = menu
        rebuild()

        let placement = Placement.of(bundle)
        if placement.mustMove {
            status.headline = Words.moveTitle
            rebuild()
            showMove(placement)
            return
        }
        Quarantine.release(bundle)
        updates.changed = { [weak self] in self?.rebuild() }
        updates.start()
        Task { await firstLook() }
        timer = Timer.scheduledTimer(withTimeInterval: 60, repeats: true) { [weak self] _ in
            Task { @MainActor in await self?.refresh(doctorToo: false) }
        }
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }

    private func firstLook() async {
        let marker = FinishMarker()
        if marker.needed(build: build) {
            showFinishing()
        } else if marker.last == nil {
            showSetup()
        }
        await refresh(doctorToo: true)
    }

    // MARK: Status

    func menuWillOpen(_ menu: NSMenu) {
        if statusAt == nil || Date().timeIntervalSince(statusAt!) > 10 {
            Task { await refresh(doctorToo: false) }
        }
    }

    private func refresh(doctorToo: Bool) async {
        guard !refreshing, metarush != nil else { return }
        refreshing = true
        defer { refreshing = false }
        if appVersion == nil {
            appVersion = try? await metarush.call(["version"], as: VersionInfo.self)
        }
        let service: Result<ServiceStatus, MetaRushError>
        do {
            service = .success(try await metarush.call(["service", "status"], as: ServiceStatus.self))
        } catch {
            service = .failure(plainError(error))
        }
        // doctor looks at much (the database, the tools, Claude Code): every half hour, or when asked,
        // and only while MetaRush runs — doctor starts the database on this computer, which a person
        // who stopped MetaRush wants left stopped.
        if case .success(let st) = service, st.daemon != nil {
            if doctorToo || doctorAt == nil || Date().timeIntervalSince(doctorAt!) > 1800 {
                doctor = try? await metarush.call(["doctor"], as: DoctorReport.self)
                doctorAt = Date()
            }
        } else {
            (doctor, doctorAt) = (nil, nil)
        }
        status = MenuStatus.build(app: appVersion, service: service, doctor: doctor)
        statusAt = Date()
        rebuild()
    }

    private func rebuild() {
        menu.removeAllItems()
        if let v = updates.waiting {
            add(Words.updateTo(v), #selector(checkForUpdates))
            menu.addItem(.separator())
        }
        if let v = unfinished, !finishWindow.visible {
            info(Words.updateNotFinished(v))
            add(Words.finishUpdateMenu, #selector(finishUpdate))
            menu.addItem(.separator())
        }
        info(busy ?? status.headline)
        if let v = status.version { info(v) }
        for p in status.problems {
            let item = info("⚠︎ " + p.says)
            item.toolTip = p.todo
        }
        menu.addItem(.separator())
        add(Words.openMetaRush, #selector(openMetaRush), key: "o")
        add(Words.setUpMenu, #selector(setUp))
        menu.addItem(.separator())
        let check = add(updates.enabled ? Words.checkForUpdates : Words.updatesOff, #selector(checkForUpdates))
        check.isEnabled = updates.enabled
        if !updates.enabled { check.toolTip = Words.updatesOffWhy }
        switch status.running {
        case .running: add(Words.stop, #selector(stopMetaRush)).isEnabled = busy == nil
        case .stopped: add(Words.start, #selector(startMetaRush)).isEnabled = busy == nil
        default: break
        }
        add(Words.showLogs, #selector(showLogs))
        if link?.state() != .ours { add(Words.linkMenu, #selector(showLink)) }
        menu.addItem(.separator())
        add(Words.about, #selector(about))
        add(Words.quit, #selector(quit), key: "q").toolTip = Words.quitTip

        let attention = updates.waiting != nil || unfinished != nil || !status.problems.isEmpty
        if let cg = Mark.menuBar(pixels: 36, dot: attention) {
            let image = NSImage(cgImage: cg, size: NSSize(width: 18, height: 18))
            image.isTemplate = true
            statusItem?.button?.image = image
        }
    }

    @discardableResult
    private func add(_ title: String, _ action: Selector, key: String = "") -> NSMenuItem {
        let item = NSMenuItem(title: title, action: action, keyEquivalent: key)
        item.target = self
        menu.addItem(item)
        return item
    }

    @discardableResult
    private func info(_ title: String) -> NSMenuItem {
        let item = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        item.isEnabled = false
        menu.addItem(item)
        return item
    }

    // MARK: Actions

    @objc private func openMetaRush() {
        if status.running == .notSetUp {
            showSetup()
            return
        }
        Task {
            busy = Words.opening
            rebuild()
            defer { busy = nil; rebuild() }
            do {
                let l = try await metarush.call(["dashboard"], as: DashboardLink.self)
                if let u = URL(string: l.url) { NSWorkspace.shared.open(u) }
            } catch {
                show(plainError(error))
            }
            await refresh(doctorToo: false)
        }
    }

    @objc private func setUp() { showSetup() }

    @objc private func checkForUpdates() { updates.check() }

    @objc private func startMetaRush() {
        Task {
            busy = Words.starting
            rebuild()
            defer { busy = nil; rebuild() }
            do {
                if status.serviceInstalled {
                    try await metarush.call(["service", "restart"])
                } else {
                    _ = try await metarush.call(["dashboard"], as: DashboardLink.self) // starts it; the link isn't used
                }
            } catch {
                show(plainError(error))
            }
            await refresh(doctorToo: false)
        }
    }

    @objc private func stopMetaRush() {
        Task {
            busy = Words.stopping
            rebuild()
            defer { busy = nil; rebuild() }
            do {
                try await metarush.call(["stop"])
            } catch {
                show(plainError(error))
            }
            await refresh(doctorToo: false)
        }
    }

    @objc private func showLogs() {
        guard let path = status.log, FileManager.default.fileExists(atPath: path) else {
            show(MetaRushError(code: "", message: Words.noLogYet))
            return
        }
        let console = URL(fileURLWithPath: "/System/Applications/Utilities/Console.app")
        NSWorkspace.shared.open([URL(fileURLWithPath: path)], withApplicationAt: console, configuration: NSWorkspace.OpenConfiguration())
    }

    @objc private func showLink() {
        linkWindow.show(Words.linkTitle, LinkView(model: LinkModel(link: link), close: { [weak self] in
            self?.linkWindow.close()
            self?.rebuild()
        }))
    }

    @objc private func finishUpdate() { showFinishing() }

    @objc private func about() {
        NSApp.activate(ignoringOtherApps: true)
        NSApp.orderFrontStandardAboutPanel(options: [
            .applicationName: "MetaRush",
            .applicationVersion: shortVersion,
            .version: build,
            .credits: NSAttributedString(string: Words.credits),
        ])
    }

    @objc private func quit() { NSApp.terminate(nil) }

    // MARK: Windows

    private func showSetup() {
        if setupWindow.visible { setupWindow.show(Words.setupTitle, SetupView(model: setupModel!, close: closeSetup)); return }
        let model = SetupModel(metarush: metarush, commandLink: link) { [weak self] in
            guard let self else { return }
            FinishMarker().finished(build: self.build)
            self.unfinished = nil
            Task { await self.refresh(doctorToo: true) }
        }
        setupModel = model
        setupWindow.show(Words.setupTitle, SetupView(model: model, close: closeSetup))
        model.load()
    }

    private func closeSetup() {
        setupWindow.close()
        rebuild()
    }

    private func showFinishing() {
        unfinished = shortVersion
        // Steps still running (their window closed) are shown again, never started twice.
        if let model = finishModel, finishWindow.visible || model.running {
            finishWindow.show(Words.finishingTitle, FinishingView(model: model, close: closeFinishing))
            return
        }
        let model = FinishingModel(metarush: metarush, version: shortVersion, onFinished: { [weak self] in
            guard let self else { return }
            FinishMarker().finished(build: self.build)
            self.unfinished = nil
            Task { await self.refresh(doctorToo: true) }
        }, showLogs: { [weak self] in self?.showLogs() })
        finishModel = model
        finishWindow.show(Words.finishingTitle, FinishingView(model: model, close: closeFinishing))
        model.run()
    }

    private func closeFinishing() {
        finishWindow.close()
        rebuild()
    }

    private func showMove(_ placement: Placement) {
        let model = MoveModel(placement: placement) { [weak self] in
            let copy = try Moving.copy(Bundle.main.bundleURL, into: Moving.destinationFolder())
            let config = NSWorkspace.OpenConfiguration()
            config.createsNewApplicationInstance = true
            NSWorkspace.shared.openApplication(at: copy, configuration: config) { _, _ in
                DispatchQueue.main.async { NSApp.terminate(nil) }
            }
            self?.moveWindow.close()
        }
        moveWindow.show(Words.moveTitle, MoveView(model: model, keep: { [weak self] in
            self?.moveWindow.close()
        }, quit: {
            NSApp.terminate(nil)
        }))
    }

    private func show(_ error: MetaRushError) {
        let alert = NSAlert()
        alert.messageText = error.message
        alert.informativeText = error.hint ?? ""
        alert.alertStyle = .warning
        alert.addButton(withTitle: Words.close)
        if !error.details.isEmpty { alert.addButton(withTitle: Words.details) }
        NSApp.activate(ignoringOtherApps: true)
        if alert.runModal() == .alertSecondButtonReturn {
            let more = NSAlert()
            more.messageText = Words.details
            more.informativeText = error.details
            more.addButton(withTitle: Words.close)
            more.runModal()
        }
    }
}
