import MetaRushCore
import ServiceManagement

/// AppLoginItem is the app opening when the person logs in, macOS's own way
/// (SMAppService, D470). Only the app uses it; the tests have a stand-in.
final class AppLoginItem: LoginItem {
    private var service: SMAppService { .mainApp }
    var isOn: Bool { service.status == .enabled || service.status == .requiresApproval }
    var needsApproval: Bool { service.status == .requiresApproval }

    func turn(_ on: Bool) throws {
        if on {
            try service.register()
        } else {
            try service.unregister()
        }
    }
}
