// swift-tools-version:5.9
// MetaRush.app (D470): a small menu-bar app that sets MetaRush up, keeps it up to
// date with Sparkle and opens the dashboard. Everything it does goes through the
// `metarush` program it carries (Contents/Helpers/metarush) and that program's
// --json commands, so nothing MetaRush knows is written twice. Built with the
// Command Line Tools alone (no Xcode project) by scripts/mac-app.sh; docs/mac-app.md.
import PackageDescription

let package = Package(
    name: "MetaRushApp",
    platforms: [.macOS(.v13)],
    products: [
        .executable(name: "MetaRush", targets: ["MetaRushApp"]),
        // Tests and the build only, never shipped: applies an update with the real
        // Sparkle headlessly, runs the steps after an update, compares versions the
        // way Sparkle does, draws the app's icon.
        .executable(name: "AppHarness", targets: ["AppHarness"]),
    ],
    targets: [
        // Sparkle 2 (MIT; docs/mac-app.md, THIRD_PARTY.md): the project's own archive for
        // Swift Package Manager, pinned by its version (in the address) and SHA-256.
        .binaryTarget(
            name: "Sparkle",
            url: "https://github.com/sparkle-project/Sparkle/releases/download/2.10.1/Sparkle-for-Swift-Package-Manager.zip",
            checksum: "4c2e48a3e50ebcc2c88a58a4c7264a1a99c3c31857ce754357afb9cdb323022a"),
        // Everything the app decides, without AppKit or Sparkle, so it is tested headlessly.
        .target(name: "MetaRushCore"),
        .executableTarget(
            name: "MetaRushApp", dependencies: ["MetaRushCore", "Sparkle"],
            linkerSettings: [.unsafeFlags(["-Xlinker", "-rpath", "-Xlinker", "@executable_path/../Frameworks"])]),
        .executableTarget(
            name: "AppHarness", dependencies: ["MetaRushCore", "Sparkle"],
            linkerSettings: [.unsafeFlags(["-Xlinker", "-rpath", "-Xlinker", "@executable_path"])]),
        // Fixtures: the program's answers written from the Go types (internal/cli
        // TestMacAppFixtures) and the stand-in program; read by path, not bundled.
        .testTarget(name: "MetaRushCoreTests", dependencies: ["MetaRushCore"], exclude: ["Fixtures"]),
    ]
)
