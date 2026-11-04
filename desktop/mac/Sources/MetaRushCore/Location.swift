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

/// Moving is the app copying itself into Applications.
public enum Moving {
    /// The folder the app goes to: /Applications when this person may write
    /// there, else their own ~/Applications.
    public static func destinationFolder(system: URL = URL(fileURLWithPath: "/Applications"),
                                         home: URL = FileManager.default.homeDirectoryForCurrentUser) -> URL {
        if FileManager.default.isWritableFile(atPath: system.path) { return system }
        return home.appendingPathComponent("Applications")
    }

    /// copy puts a copy of the bundle into the folder, replacing an older
    /// MetaRush there (moved to the Trash, never deleted), and clears the
    /// download flag on the copy (the person opened this one). It returns
    /// where the copy is; the app then opens it and quits.
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
