import Foundation

/// Where the app runs from (D476). Opened where it was downloaded, macOS runs a
/// temporary copy of it (App Translocation) that is gone after a restart; from a
/// disk image it can't be updated; in either case the background service and
/// the command-line link would point at a place about to vanish. So the app
/// moves itself to Applications first. Once a person has opened it there, it
/// clears the download flag (quarantine) from its own parts: macOS stops a
/// flagged program that isn't notarized when it is started by itself, as the
/// background service starts the MetaRush the app carries.
public enum Placement: Equatable, Sendable {
    /// In /Applications or ~/Applications.
    case applications
    /// The temporary copy macOS made (not where the person put it).
    case temporaryCopy
    /// A disk image or another read-only volume.
    case diskImage
    /// Somewhere else the person put it (Downloads, the desktop…).
    case elsewhere

    /// mustMove: the app can't set MetaRush up from there.
    public var mustMove: Bool { self == .temporaryCopy || self == .diskImage }

    public static func of(_ bundle: URL, home: URL = FileManager.default.homeDirectoryForCurrentUser) -> Placement {
        let path = bundle.standardizedFileURL.path
        if path.contains("/AppTranslocation/") { return .temporaryCopy }
        if let values = try? bundle.resourceValues(forKeys: [.volumeIsReadOnlyKey]), values.volumeIsReadOnly == true {
            return .diskImage
        }
        let parent = bundle.standardizedFileURL.deletingLastPathComponent().path
        let userApps = home.appendingPathComponent("Applications").standardizedFileURL.path
        if parent == "/Applications" || parent.hasPrefix("/Applications/") || parent == userApps || parent.hasPrefix(userApps + "/") {
            return .applications
        }
        return .elsewhere
    }
}

/// Quarantine is the download flag macOS puts on what a browser downloads.
public enum Quarantine {
    public static let attribute = "com.apple.quarantine"

    public static func has(_ url: URL) -> Bool {
        getxattr(url.path, attribute, nil, 0, 0, XATTR_NOFOLLOW) >= 0
    }

    /// release clears the flag from everything inside a bundle (links are left
    /// as they are) and says how many were cleared.
    @discardableResult
    public static func release(_ bundle: URL) -> Int {
        var cleared = 0
        let clear: (URL) -> Void = { u in
            if removexattr(u.path, attribute, XATTR_NOFOLLOW) == 0 { cleared += 1 }
        }
        clear(bundle)
        let e = FileManager.default.enumerator(at: bundle, includingPropertiesForKeys: nil, options: [], errorHandler: { _, _ in true })
        while let u = e?.nextObject() as? URL {
            clear(u)
        }
        return cleared
    }
}

/// AppVersion is an app bundle's versions, from its Info.plist.
public struct AppVersion: Equatable, Sendable {
    /// CFBundleVersion: what Sparkle compares.
    public var build: String
    /// CFBundleShortVersionString: what people are shown.
    public var shown: String

    public init(build: String, shown: String) {
        self.build = build
        self.shown = shown
    }

    public static func of(_ bundle: URL) -> AppVersion? {
        let url = bundle.appendingPathComponent("Contents/Info.plist")
        guard let data = try? Data(contentsOf: url),
              let plist = try? PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any],
              let build = plist["CFBundleVersion"] as? String else { return nil }
        return AppVersion(build: build, shown: plist["CFBundleShortVersionString"] as? String ?? build)
    }
}

/// MovePlan is what moving the app to Applications does when a MetaRush is
/// there already (D476): an older one, not open, goes to the Trash; a newer
/// one, or one that is open, stays unless the person says to replace it.
public enum MovePlan: Equatable, Sendable {
    /// Nothing there: the copy goes in.
    case copy
    /// An older (or the same) version, not open: moved to the Trash, then the copy goes in.
    case replace(AppVersion)
    /// A newer version is there: asked (opening that one is suggested).
    case askNewer(AppVersion)
    /// The one there is open: asked (using that one is suggested).
    case askOpen(AppVersion?)

    /// plan decides from what is at the destination; `newer(a, b)` is
    /// Sparkle's comparator in the app (a newer than b).
    public static func plan(this: AppVersion?, there: AppVersion?, thereExists: Bool, thereOpen: Bool,
                            newer: (String, String) -> Bool) -> MovePlan {
        guard thereExists else { return .copy }
        if thereOpen { return .askOpen(there) }
        guard let there else { return .replace(AppVersion(build: "", shown: "")) } // not a MetaRush that says its version: replaced, kept in the Trash
        if let this, newer(there.build, this.build) { return .askNewer(there) }
        return .replace(there)
    }
}

/// Moving is the app copying itself into Applications.
public enum Moving {
    /// The folder the app goes to: /Applications when this person may write
    /// there, else their own ~/Applications.
    public static func destinationFolder(system: URL = URL(fileURLWithPath: "/Applications"),
                                         home: URL = FileManager.default.homeDirectoryForCurrentUser) -> URL {
        if FileManager.default.isWritableFile(atPath: system.path) { return system }
        return home.appendingPathComponent("Applications")
    }

    /// copy puts a copy of the bundle into the folder, replacing the MetaRush
    /// there (moved to the Trash, never deleted: MovePlan decided, or the
    /// person said so), and clears the download flag on the copy (the person
    /// opened this one). It returns where the copy is; the app then opens it
    /// and quits.
    public static func copy(_ bundle: URL, into folder: URL,
                            setAside: (URL) throws -> Void = { try FileManager.default.trashItem(at: $0, resultingItemURL: nil) }) throws -> URL {
        let fm = FileManager.default
        try fm.createDirectory(at: folder, withIntermediateDirectories: true)
        let dest = folder.appendingPathComponent(bundle.lastPathComponent)
        if fm.fileExists(atPath: dest.path) {
            try setAside(dest)
        }
        try fm.copyItem(at: bundle, to: dest)
        Quarantine.release(dest)
        return dest
    }
}
