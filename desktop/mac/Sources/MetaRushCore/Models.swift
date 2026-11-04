import Foundation

// The answers of the commands the app runs, as `metarush … --json` writes them.
// Only the fields the app uses; Tests/MetaRushCoreTests/Fixtures holds answers
// written from the Go types themselves (internal/cli TestMacAppFixtures), so a
// renamed field fails a test instead of a person's setup.

/// `metarush version --json`: the build.
public struct VersionInfo: Codable, Equatable, Sendable {
    public var version: String
    public var release: String
    public var commit: String
    public var dev: Bool?

    public init(version: String, release: String, commit: String, dev: Bool? = nil) {
        self.version = version
        self.release = release
        self.commit = commit
        self.dev = dev
    }
}

/// `metarush setup questions --json` (D473): what setup asks here and its
/// suggested answers.
public struct SetupQuestions: Codable, Equatable, Sendable {
    public var home: String
    public var setUp: Bool
    public var storage: Storage
    public var database: Database
    public var port: Int
    public var service: Service
    public var skills: Skills
    public var components: [Component]

    public struct Storage: Codable, Equatable, Sendable {
        public var suggested: String
        public var folder: String?
        public var says: String
    }

    public struct Database: Codable, Equatable, Sendable {
        public var suggested: String
        public var here: Bool
        public var downloadMB: Int?
        public var note: String?
        public var says: String
    }

    public struct Service: Codable, Equatable, Sendable {
        public var suggested: Bool
        public var installed: Bool
        public var running: Bool
        public var otherCopy: Bool?
        public var says: String
    }

    public struct Skills: Codable, Equatable, Sendable {
        public var suggested: Bool
        public var claudeInstalled: Bool
        public var says: String
    }

    public struct Component: Codable, Equatable, Sendable, Identifiable {
        public var name: String
        public var title: String
        public var unlocks: String
        public var suggested: Bool
        public var ready: Bool
        public var status: String
        public var says: String
        public var size: String
        public var time: String
        public var licence: String?
        public var needs: [String]?
        public var ticked: Bool
        public var downloadMB: Int
        public var minutes: Int
        public var id: String { name }
    }
}

/// `metarush setup --json`: what was set up, what works now and what is left.
public struct SetupResult: Codable, Equatable, Sendable {
    public var home: String
    public var fresh: Bool
    public var dashboard: String
    public var database: Said
    public var storage: Said
    public var service: ServicePart
    public var skills: SkillsPart
    public var components: ComponentsPart
    public var whatWorks: WhatWorks
    public var warnings: [String]?
    public var next: [String]

    public struct Said: Codable, Equatable, Sendable {
        public var says: String
    }

    public struct ServicePart: Codable, Equatable, Sendable {
        public var wanted: Bool
        public var installed: Bool
        public var running: Bool
        public var problem: String?
        public var says: String
    }

    public struct SkillsPart: Codable, Equatable, Sendable {
        public var wanted: Bool
        public var installed: Bool
        public var says: String
    }

    public struct ComponentsPart: Codable, Equatable, Sendable {
        public var chosen: [String]
        public var added: [Added]
        public var failed: [Note]?
        public var needsYou: [Note]?
        public var says: String

        public struct Added: Codable, Equatable, Sendable {
            public var name: String
            public var title: String
            public var says: String
            public var licence: String?
        }

        public struct Note: Codable, Equatable, Sendable {
            public var name: String
            public var title: String
            public var says: String
            public var command: String?
        }
    }

    public struct WhatWorks: Codable, Equatable, Sendable {
        public var summary: String
        public var noKeys: [Item]
        public var withKeys: [Item]?
        public var claude: Claude

        public struct Item: Codable, Equatable, Sendable, Identifiable {
            public var key: String
            public var name: String
            public var ready: Bool
            public var says: String
            public var next: String?
            public var id: String { key }
        }

        public struct Claude: Codable, Equatable, Sendable {
            public var ready: Bool
            public var says: String
            public var next: String?
        }
    }
}

/// `metarush service status --json`: the background service and (D474) the
/// MetaRush running for the home.
public struct ServiceStatus: Codable, Equatable, Sendable {
    public var installed: Bool
    public var loaded: Bool
    public var running: Bool
    public var workerOnly: Bool
    public var binary: String?
    public var sameBinary: Bool
    public var log: String
    public var daemon: Daemon?

    public struct Daemon: Codable, Equatable, Sendable {
        public var pid: Int
        public var port: Int?
        public var mode: String?
        public var version: String?
        public var commit: String?
        public var answering: Bool
    }

    public init(installed: Bool, loaded: Bool, running: Bool, workerOnly: Bool, binary: String?, sameBinary: Bool, log: String, daemon: Daemon?) {
        self.installed = installed
        self.loaded = loaded
        self.running = running
        self.workerOnly = workerOnly
        self.binary = binary
        self.sameBinary = sameBinary
        self.log = log
        self.daemon = daemon
    }
}

/// `metarush doctor --json`: each check, with (D474) its plain words.
public struct DoctorReport: Codable, Equatable, Sendable {
    public var ok: Bool
    public var checks: [Check]

    public struct Check: Codable, Equatable, Sendable {
        public var area: String
        public var name: String
        public var status: String
        public var say: String?
        public var `do`: String?
    }

    /// Problems are the checks that need a look, in plain words: failures first.
    public var problems: [Check] {
        let bad = checks.filter { ($0.status == "fail" || $0.status == "warn") && !($0.say ?? "").isEmpty }
        return bad.filter { $0.status == "fail" } + bad.filter { $0.status == "warn" }
    }
}

/// `metarush dashboard --json`: a one-time sign-in link to the dashboard.
public struct DashboardLink: Codable, Equatable, Sendable {
    public var url: String
    public var started: Bool
}

/// `metarush skills install --dry-run --json`: what installing the skills would do.
public struct SkillsInstall: Codable, Equatable, Sendable {
    public var installed: [String]?
    public var updated: [String]?
    public var unchanged: [String]?
    public var skippedModified: [String]?

    /// There: MetaRush's skills are installed for Claude Code (some file of
    /// them is there already), so an update brings them up to date.
    public var there: Bool {
        !((updated ?? []).isEmpty && (unchanged ?? []).isEmpty && (skippedModified ?? []).isEmpty)
    }
}
