// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "RAKit",
    platforms: [.iOS(.v18), .macOS(.v15)],
    products: [
        .library(name: "RAKit", targets: ["RAKit"]),
    ],
    targets: [
        .target(name: "RAKit"),
        .testTarget(
            name: "RAKitTests",
            dependencies: ["RAKit"]
        ),
    ]
)
