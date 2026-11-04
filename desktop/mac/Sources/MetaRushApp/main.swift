import AppKit

// MetaRush.app (D470): a menu-bar app, without a Dock icon (LSUIElement). What
// it decides is in MetaRushCore; this target is its menu, its windows and Sparkle.
// (The app's views keep their state in ObservableObject models rather than
// SwiftUI's @State: in this SDK @State is a macro whose plugin comes with Xcode,
// and MetaRush.app is built with the Command Line Tools alone.)
@MainActor var appDelegate: AppDelegate?

MainActor.assumeIsolated {
    let delegate = AppDelegate()
    appDelegate = delegate // NSApplication keeps its delegate weakly
    NSApplication.shared.delegate = delegate
    NSApplication.shared.setActivationPolicy(.accessory)
    NSApplication.shared.run()
}
