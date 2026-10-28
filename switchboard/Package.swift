// swift-tools-version:6.0
// Build:  swift build -c release --arch arm64 --arch x86_64
// The binary needs nothing but macOS 13 or later, on Intel or Apple silicon. It's in the
// folder that the same command prints with --show-bin-path. Check it: ./smoke-test.sh
import PackageDescription

let package = Package(
    name: "ringback-switchboard",
    platforms: [.macOS(.v13)],
    targets: [.executableTarget(name: "ringback-switchboard", path: "Sources")],
    swiftLanguageModes: [.v5]
)
