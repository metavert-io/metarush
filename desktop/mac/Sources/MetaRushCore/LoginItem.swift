import Foundation

/// LoginItem is MetaRush.app opening when the person logs in (D470): the app
/// checks for updates only while it runs, so this is offered at the first
/// setup with yes suggested, and the menu turns it on and off. The app's is
/// macOS's own (SMAppService); the tests use a stand-in, so they never
/// register anything on the Mac that runs them.
public protocol LoginItem: AnyObject {
    /// isOn: the app opens at login (or macOS waits for the person to allow it).
    var isOn: Bool { get }
    /// needsApproval: turned on, but macOS waits for the person to allow it in
    /// System Settings → General → Login Items.
    var needsApproval: Bool { get }
    func turn(_ on: Bool) throws
}

extension SetupAnswers {
    /// The person's answer about opening at login, applied when it differs
    /// from how things are. It says what happened, for the last panel.
    public func applyOpenAtLogin(_ item: LoginItem) -> OpenAtLogin {
        guard openAtLogin != item.isOn else { return openAtLogin ? (item.needsApproval ? .needsApproval : .on) : .unchanged }
        do {
            try item.turn(openAtLogin)
        } catch {
            return .failed
        }
        if !openAtLogin { return .off }
        return item.needsApproval ? .needsApproval : .on
    }

    public enum OpenAtLogin: Equatable, Sendable {
        case on, off, unchanged, needsApproval, failed
    }
}
