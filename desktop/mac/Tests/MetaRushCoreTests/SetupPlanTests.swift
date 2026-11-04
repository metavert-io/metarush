import Foundation
import Testing
@testable import MetaRushCore

/// The panels start from setup's own suggestions and give every answer back
/// to setup as a flag (D473).
@Suite struct SetupPlanTests {
    func questions(_ name: String) throws -> SetupQuestions {
        try JSONDecoder().decode(Envelope.self, from: try fixture(name)).data
    }

    struct Envelope: Decodable { var data: SetupQuestions }

    @Test func theSuggestedAnswersAsFlags() throws {
        let q = try questions("setup-questions.json")
        let a = SetupAnswers(q)
        #expect(a.folder == "/Users/you/.metarush/media" && !a.cloudStorage && !a.databaseElsewhere && a.service && a.skills)
        // Ticked and not here yet: Node.js and the renderer (FFmpeg is here; studio voices aren't suggested).
        #expect(a.components == ["node", "renderer"])
        #expect(!a.inTerminal(q))
        #expect(a.arguments(q) == ["setup", "--yes", "--storage", "folder", "--media-root", "/Users/you/.metarush/media", "--database", "here",
                                   "--port", "4845", "--service=true", "--skills=true", "--components", "node,renderer"])
        let size = a.size(q)
        #expect(size.megabytes == 450 && size.minutes == 6)
        #expect(a.licences(q).count == 1 && a.licences(q)[0].contains("Remotion Company License"))
    }

    @Test func aPersonsChanges() throws {
        let q = try questions("setup-questions.json")
        var a = SetupAnswers(q)
        a.folder = "/Volumes/Footage/MetaRush"
        a.service = false
        a.skills = false
        a.port = 4900
        a.components.remove("renderer")
        a.components.insert("studio-voice")
        a.components.insert("ffmpeg") // here already: never asked to add again
        #expect(a.arguments(q) == ["setup", "--yes", "--storage", "folder", "--media-root", "/Volumes/Footage/MetaRush", "--database", "here",
                                   "--port", "4900", "--service=false", "--skills=false", "--components", "node,studio-voice"])
        a.components = []
        #expect(a.arguments(q).last == "--no-components")
        #expect(a.licences(q).isEmpty && a.size(q).megabytes == 0)
    }

    @Test func secretsAreAskedInTerminal() throws {
        let q = try questions("setup-questions.json")
        var a = SetupAnswers(q)
        a.cloudStorage = true
        #expect(a.inTerminal(q))
        a.cloudStorage = false
        a.databaseElsewhere = true
        #expect(a.inTerminal(q))
    }

    @Test func aHomeSetUpKeepsWhereThingsAreKept() throws {
        let q = try questions("setup-questions-set-up.json")
        var a = SetupAnswers(q)
        // Setup refuses to move storage or the database on a home set up: none of those flags.
        #expect(a.arguments(q) == ["setup", "--yes", "--service=true", "--skills=true", "--components", "node,renderer"])
        a.databaseElsewhere = true
        #expect(!a.inTerminal(q))
        a.port = 4852
        #expect(a.arguments(q).contains("--port") && a.arguments(q).contains("4852"))
    }

    @Test func withoutClaudeCodeSetupDecidesTheSkills() throws {
        var q = try questions("setup-questions.json")
        q.skills = SetupQuestions.Skills(suggested: false, claudeInstalled: false, says: "Claude Code isn't installed")
        #expect(!SetupAnswers(q).arguments(q).contains { $0.hasPrefix("--skills") })
    }

    @Test func theSizeInWords() {
        #expect(Words.componentsSize(megabytes: 450, minutes: 6) == "The ticked ones download about 450 MB and take about 6 minutes.")
        #expect(Words.componentsSize(megabytes: 2000, minutes: 1) == "The ticked ones download about 2 GB and take about a minute.")
        #expect(Words.componentsSize(megabytes: 1850, minutes: 20) == "The ticked ones download about 1.9 GB and take about 20 minutes.")
        #expect(Words.componentsSize(megabytes: 0, minutes: 0) == "Nothing is ticked to download.")
    }
}
