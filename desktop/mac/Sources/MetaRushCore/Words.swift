import Foundation

/// Words are everything MetaRush.app says to people, in MetaRush's plain voice
/// (skills/_shared/voice.md): what it means and what to do next; paths,
/// commands and codes only behind Details. internal/macapp's
/// TestAppSpeaksPlainly runs every string here (and any written straight into
/// the app's windows and menu) through internal/plainwords. MetaRush's own
/// answers (`says`, error messages) come from the program and are checked there.
public enum Words {
    // MARK: The menu

    public static let checking = "Looking at MetaRush…"
    public static let notSetUp = "MetaRush isn't set up on this Mac yet"
    public static let cantTell = "MetaRush didn't say how it is"
    public static let running = "MetaRush is running"
    public static let runningWorkOnly = "MetaRush is running its background work, without the dashboard"
    public static let stopped = "MetaRush isn't running"
    public static let stoppedStartsWhenOpened = "MetaRush isn't running; opening it starts it"
    public static func version(_ v: String) -> String { "Version \(v)" }
    public static func versionRunsOther(_ app: String, running: String) -> String {
        "Version \(app) (MetaRush still runs \(running))"
    }
    public static func moreProblems(_ n: Int) -> String {
        n == 1 ? "And 1 more thing to look at" : "And \(n) more things to look at"
    }
    public static let openMetaRush = "Open MetaRush"
    public static let setUpMenu = "Set Up MetaRush…"
    public static let checkForUpdates = "Check for Updates…"
    public static func updateTo(_ v: String) -> String { "Update to MetaRush \(v)…" }
    public static let updatesOff = "Updates are off in this copy of MetaRush"
    public static let updatesOffWhy = "It was built without an update key, so it can't tell a genuine update from another."
    public static let start = "Start MetaRush"
    public static let stop = "Stop MetaRush"
    public static let showLogs = "Show Logs"
    public static let linkMenu = "Let Your Terminal Use MetaRush…"
    public static let finishUpdateMenu = "Finish the Update…"
    public static let about = "About MetaRush"
    public static let quit = "Quit"
    public static let quitTip = "MetaRush keeps running in the background."
    public static let noLogYet = "MetaRush hasn't written a log yet."
    public static let starting = "Starting MetaRush…"
    public static let stopping = "Stopping MetaRush…"
    public static let opening = "Opening MetaRush…"

    // MARK: When the program can't run or answers oddly

    public static let programMissing = "The MetaRush inside this app couldn't start."
    public static let programMissingHint = "Download MetaRush again and replace this copy."
    public static let programStopped = "macOS stopped the MetaRush inside this app."
    public static let programStoppedHint = "Open MetaRush from your Applications folder once more; if it happens again, download it again."
    public static let failedWithoutSaying = "MetaRush stopped without saying why."
    public static let unexpectedAnswer = "MetaRush answered something this app doesn't understand."
    public static let details = "Details"
    public static let tryAgain = "Try Again"
    public static let done = "Done"
    public static let close = "Close"
    public static let notNow = "Not Now"

    // MARK: The steps after an update

    public static let finishingTitle = "Finishing the Update"
    public static func finishingHeading(_ v: String) -> String { "Finishing the update to MetaRush \(v)" }
    public static let finishingIntro = "A few steps bring MetaRush on this Mac up to the new version. Your projects and footage stay as they are."
    public static func stepTitle(_ step: FinishStep, version v: String) -> String {
        switch step {
        case .skills: return "Claude Code's video skills"
        case .renderer: return "The renderer for this version's finished videos"
        case .records: return "Bringing your projects up to date"
        case .restart: return "Restarting MetaRush in the background"
        case .check: return "Checking that MetaRush \(v) is running"
        }
    }
    public static let skillsNotHere = "Not set up on this Mac, so there is nothing to update"
    public static let notRunningStartsLater = "MetaRush isn't running; it starts as this version when you open it"
    public static let notSetUpYet = "MetaRush isn't set up on this Mac yet"
    public static func stillRuns(_ old: String, _ new: String) -> String {
        "MetaRush still runs \(old) in the background, not \(new)."
    }
    public static func notAnswering(_ v: String) -> String { "MetaRush \(v) didn't answer after it restarted." }
    public static let restartHint = "Restart it from the menu; if this happens again, its log says why."
    public static let otherCopyRuns = "MetaRush in the background is another copy, not this app's, so the update doesn't reach it."
    public static let otherCopyHint = "Let this app run MetaRush in the background, and it keeps it up to date from now on."
    public static let useThisCopy = "Use This App's MetaRush"
    public static func finished(_ v: String) -> String { "MetaRush \(v) is ready." }
    public static func updateNotFinished(_ v: String) -> String { "The update to \(v) isn't finished" }

    // MARK: Setting up

    public static let setupTitle = "Set Up MetaRush"
    public static let welcome = "Welcome to MetaRush"
    public static let welcomeText = "MetaRush sets itself up on this Mac in a few minutes. Every question has a suggested answer, so you can just press Continue."
    public static let setUpAlready = "MetaRush is set up on this Mac"
    public static let setUpAlreadyText = "Setup checks that everything is in place, adds components you choose and can have MetaRush start when you log in. Your projects and footage stay where they are."
    public static let continueButton = "Continue"
    public static let back = "Back"
    public static let lookingAround = "Looking at this Mac…"

    public static let storageQuestion = "Where should your footage and finished videos be kept?"
    public static let storageFolder = "In a folder on this Mac (suggested)"
    public static let chooseFolder = "Choose…"
    public static let chooseFolderPrompt = "Choose the folder for your footage and finished videos"
    public static let storageBucket = "In a cloud storage bucket your team shares"
    public static let storageBucketText = "A bucket needs its key, which MetaRush asks for in Terminal, never here. Set MetaRush up in Terminal, or start with a folder and move to a bucket later in Settings → Storage."

    public static let databaseQuestion = "Where should MetaRush keep your projects?"
    public static let databaseHere = "In a database on this Mac (suggested)"
    public static let databaseElsewhere = "In a database you already have (MongoDB Atlas, or your own server)"
    public static let databaseElsewhereText = "Its address includes a password, so MetaRush asks for it in Terminal, never here."
    public static let noDatabaseHere = "MongoDB doesn't make a database for this kind of Mac, so MetaRush needs one you already have."

    public static let terminalTitle = "Set Up MetaRush in Terminal"
    public static let terminalText = "Open Terminal, paste the setup command this app copied for you, and answer its questions there. Come back when it's done."
    public static let copyCommand = "Copy the Setup Command"
    public static let copied = "Copied"
    public static let openTerminal = "Open Terminal"
    public static let checkAgain = "Check Again"
    public static let notSetUpInTerminalYet = "MetaRush isn't set up yet. Finish setup in Terminal, then check again."

    public static let thisMac = "MetaRush on this Mac"
    public static let serviceToggle = "Start MetaRush when you log in, and keep it running"
    public static let serviceOtherCopy = "MetaRush already starts from another copy; this app's copy takes its place."
    public static let skillsToggle = "Teach Claude Code MetaRush's video skills"
    public static let linkToggle = "Let your terminal and Claude Code find MetaRush"
    public static let linkToggleOther = "Another copy of MetaRush is already there. This app's copy takes its place, and the other one is kept beside it."
    public static let moreSettings = "More settings"
    public static let portLabel = "The dashboard's port on this Mac"

    public static let componentsTitle = "Optional components"
    public static let componentsText = "Each one unlocks more of MetaRush. The suggested ones are ticked; add the rest any time in Settings → Components."
    public static let alreadyHere = "Already on this Mac"
    public static func componentsSize(megabytes mb: Int, minutes: Int) -> String {
        if mb <= 0 { return "Nothing is ticked to download." }
        let size = mb >= 1000 ? String(format: "%.1f GB", Double(mb) / 1000).replacingOccurrences(of: ".0 GB", with: " GB") : "\(mb) MB"
        let time = minutes <= 1 ? "a minute" : "\(minutes) minutes"
        return "The ticked ones download about \(size) and take about \(time)."
    }
    public static let setUpButton = "Set Up MetaRush"

    public static let working = "Setting up MetaRush…"
    public static let workingText = "This takes a few minutes: downloading the database and the components you chose is the longest part. You can carry on with something else."
    public static func elapsed(minutes: Int) -> String {
        minutes < 1 ? "Started just now" : (minutes == 1 ? "1 minute so far" : "\(minutes) minutes so far")
    }
    public static let stopSetup = "Stop"
    public static let stoppedText = "Setup stopped. Run it again whenever you like: it carries on where it stopped."
    public static let failedTitle = "Setup didn't finish"
    public static let readyTitle = "MetaRush is ready"
    public static let whatWorks = "What works now"
    public static let needsYou = "Still to do"

    // MARK: Moving to Applications

    public static let moveTitle = "Move MetaRush to your Applications folder"
    public static func moveText(_ p: Placement) -> String {
        switch p {
        case .temporaryCopy:
            return "MetaRush is running from a temporary copy macOS made where it was downloaded. In Applications it can keep itself up to date and start when you log in."
        case .diskImage:
            return "MetaRush is running from the disk image it came in. In Applications it can keep itself up to date and start when you log in."
        default:
            return "MetaRush works best from your Applications folder, where it keeps itself up to date."
        }
    }
    public static let moveButton = "Move to Applications"
    public static let moveFailed = "MetaRush couldn't move itself."
    public static let moveFailedHint = "Drag it into your Applications folder in Finder, then open it from there."
    public static let keepHere = "Keep It Here"

    // MARK: The command-line link

    public static let linkTitle = "Let your terminal use MetaRush"
    public static let linkText = "Your terminal and Claude Code's skills call MetaRush by name. A link in your personal programs folder lets them find this app's copy, which stays up to date with the app."
    public static let linkOtherText = "Another copy of MetaRush is already in your personal programs folder, installed another way. Use this app's copy instead? The other one is kept beside it under another name."
    public static let linkMade = "Your terminal and Claude Code now use this app's MetaRush."
    public static let linkAlready = "Your terminal and Claude Code already use this app's MetaRush."
    public static let linkKeptAside = "The other copy is kept beside it under another name."
    public static let linkPathHint = "If your terminal still can't find it, add your personal programs folder to its search path (see Details)."
    public static let makeLink = "Make the Link"
    public static let useAppsCopy = "Use This App's Copy"
    public static let keepOther = "Keep the Other One"
    public static let linkFailed = "The link couldn't be made."

    // MARK: About

    public static let credits = "MetaRush keeps itself up to date with Sparkle, used under its MIT licence."
}
