import AppKit
import Sparkle

// MetaRush.app (D470): a menu-bar app, without a Dock icon (LSUIElement). What
// it decides is in MetaRushCore; this target is its menu, its windows and Sparkle.
// (The app's views keep their state in ObservableObject models rather than
// SwiftUI's @State: in this SDK @State is a macro whose plugin comes with Xcode,
// and MetaRush.app is built with the Command Line Tools alone.)

// `--version`: says the build and the Sparkle it loaded, then ends, before any
// window: the build's and the tests' check that the app starts.
if CommandLine.arguments.dropFirst().first == "--version" {
    let info = Bundle.main.infoDictionary ?? [:]
    let sparkle = Bundle(for: SPUUpdater.self).object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "?"
    print("MetaRush \(info["CFBundleShortVersionString"] as? String ?? "?") (\(info["CFBundleVersion"] as? String ?? "?")), Sparkle \(sparkle)")
    exit(0)
}

@MainActor var appDelegate: AppDelegate?

MainActor.assumeIsolated {
    let delegate = AppDelegate()
    appDelegate = delegate // NSApplication keeps its delegate weakly
    NSApplication.shared.delegate = delegate
    NSApplication.shared.setActivationPolicy(.accessory)
    NSApplication.shared.run()
}
