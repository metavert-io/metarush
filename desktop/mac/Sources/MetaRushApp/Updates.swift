import AppKit
import MetaRushCore
import Sparkle

/// Updates is Sparkle (D471): its standard windows check, download, verify the
/// EdDSA signature against the key the app carries and install. The feed's
/// address comes from UpdateFeed (the build's, or this Mac's setting). A build
/// without a real key never starts Sparkle (it couldn't tell a genuine update).
/// Found in the background, an update waits in the menu (a gentle reminder)
/// instead of a window over whatever the person is doing.
final class Updates: NSObject, SPUUpdaterDelegate, SPUStandardUserDriverDelegate {
    private(set) var controller: SPUStandardUpdaterController?
    /// A version found by a scheduled check, shown in the menu until looked at.
    var waiting: String?
    var changed: () -> Void = {}

    var enabled: Bool { controller != nil }

    func start() {
        guard UpdateFeed.keyIsReal(Bundle.main.object(forInfoDictionaryKey: "SUPublicEDKey") as? String) else { return }
        controller = SPUStandardUpdaterController(startingUpdater: true, updaterDelegate: self, userDriverDelegate: self)
    }

    func check() {
        controller?.checkForUpdates(nil)
    }

    var canCheck: Bool { controller?.updater.canCheckForUpdates ?? false }

    // MARK: SPUUpdaterDelegate

    func feedURLString(for updater: SPUUpdater) -> String? {
        UpdateFeed.choose(built: Bundle.main.object(forInfoDictionaryKey: "SUFeedURL") as? String,
                          override: UserDefaults.standard.string(forKey: UpdateFeed.overrideKey))?.absoluteString
    }

    // MARK: SPUStandardUserDriverDelegate (gentle reminders)

    var supportsGentleScheduledUpdateReminders: Bool { true }

    func standardUserDriverShouldHandleShowingScheduledUpdate(_ update: SUAppcastItem, andInImmediateFocus immediateFocus: Bool) -> Bool {
        immediateFocus
    }

    func standardUserDriverWillHandleShowingUpdate(_ handleShowingUpdate: Bool, forUpdate update: SUAppcastItem, state: SPUUserUpdateState) {
        if !handleShowingUpdate && !state.userInitiated {
            waiting = update.displayVersionString
            changed()
        }
    }

    func standardUserDriverDidReceiveUserAttention(forUpdate update: SUAppcastItem) {
        waiting = nil
        changed()
    }

    func standardUserDriverWillFinishUpdateSession() {
        waiting = nil
        changed()
    }
}

/// sparkleNewer: build a is newer than build b by Sparkle's own comparator,
/// so "newer" means what it means to updates (the marker, moving the app).
func sparkleNewer(_ a: String, _ b: String) -> Bool {
    SUStandardVersionComparator.default.compareVersion(a, toVersion: b) == .orderedDescending
}
