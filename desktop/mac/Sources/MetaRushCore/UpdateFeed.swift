import Foundation

/// Where updates come from (D471). The feed's address is a setting: the one the
/// app was built with (Info.plist SUFeedURL: scripts/mac-app.sh writes the
/// repository's latest-release address unless told another), unless this Mac
/// says another one (`defaults write <the app's bundle ID> UpdateFeedURL
/// <address>`). Only https is taken, or plain http to this computer for tests.
/// Every update is checked against the EdDSA key the app carries (Info.plist
/// SUPublicEDKey); a build without a real key never updates.
public enum UpdateFeed {
    /// The setting a Mac can give another feed address in.
    public static let overrideKey = "UpdateFeedURL"
    /// What the repository's Info.plist holds until a build puts the key in.
    public static let keyPlaceholder = "__ED_PUBLIC_KEY__"

    /// The feed to read: this Mac's setting when it is a good address, else the
    /// one the app was built with.
    public static func choose(built: String?, override: String?) -> URL? {
        if let o = override, let u = acceptable(o) { return u }
        if let b = built, let u = acceptable(b) { return u }
        return nil
    }

    /// An address updates may come from: https, or http to this computer.
    public static func acceptable(_ s: String) -> URL? {
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let u = URL(string: t), let scheme = u.scheme?.lowercased(), let host = u.host?.lowercased(), !host.isEmpty else {
            return nil
        }
        if scheme == "https" { return u }
        if scheme == "http" && (host == "127.0.0.1" || host == "localhost" || host == "::1") { return u }
        return nil
    }

    /// keyIsReal: the build carries an update key (32 bytes, base64), not the
    /// repository's placeholder.
    public static func keyIsReal(_ key: String?) -> Bool {
        guard let key, key != keyPlaceholder, let d = Data(base64Encoded: key.trimmingCharacters(in: .whitespacesAndNewlines)) else {
            return false
        }
        return d.count == 32
    }
}
