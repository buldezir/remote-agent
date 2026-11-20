#!/bin/sh
# Builds the client as an app that Finder opens, signed (scripts/sign.sh). It
# builds one of two apps, which can sit side by side in /Applications:
#
#   scripts/bundle.sh                    → target/release/Remote Agent Dev.app,
#                                          installed in /Applications
#   scripts/bundle.sh --release [<v>]    → target/release/Remote Agent.app
#
# The development build (dev.remote-agent.gpui) updates from the Mac that
# built it, over ssh: Contents/Resources/build.json says which build it is,
# and the app on another Mac pulls the one at the same path with Check for
# Update (src/store/build_update.rs). It shares its servers and tokens with
# `cargo run`'s builds.
#
# The release build (dev.remote-agent.app, or RA_BUNDLE_ID) is what the
# release workflow publishes. Given a version, such as 0.12.0, it embeds
# Sparkle (scripts/sparkle.sh) and updates itself from the latest GitHub
# release's Remote-Agent-macOS-appcast.xml, which apple/scripts/appcast.sh
# writes; without one it doesn't update. Its servers and tokens are its own.
#
# The development build is installed in /Applications, the release build
# nowhere; RA_GPUI_INSTALL_DIR names another folder (empty: none).
#
# A bare executable, such as target/release/remote-agent-gpui, is a
# command-line program to Finder, which opens it in Terminal.
set -eu
cd "$(dirname "$0")/.."

release=false version=
case "${1:-}" in
--release)
    release=true
    version=${2:-}
    ;;
"") ;;
*)
    echo "usage: bundle.sh [--release [<version>]]" >&2
    exit 2
    ;;
esac

# Sparkle installs only updates signed with the private half of this key, the
# SPARKLE_PRIVATE_KEY secret. The Server app's is the same (apple/project.yml).
# SPARKLE_PUBLIC_KEY replaces it with a scratch key, to try updates.
sparkle_public_key=${SPARKLE_PUBLIC_KEY:-WQontbp7coFEQwc0h6w4Or5DMoyvPz535n1amUTZNqc=}
feed=https://github.com/buldezir/remote-agent/releases/latest/download/Remote-Agent-macOS-appcast.xml

if $release; then
    name="Remote Agent"
    id=${RA_BUNDLE_ID:-dev.remote-agent.app}
    install_dir=${RA_GPUI_INSTALL_DIR-}
else
    name="Remote Agent Dev"
    id=dev.remote-agent.gpui
    install_dir=${RA_GPUI_INSTALL_DIR-/Applications}
fi

# A release with a version updates itself.
sparkle=false
if $release && [ -n "$version" ]; then sparkle=true; fi

# The build number, which Sparkle compares, only grows, as the Android
# app's versionCode: 0.3.0-beta.2 → 30002, 0.3.0 → 30099.
if [ -n "$version" ]; then
    parts=$(echo "$version" | sed -nE 's/^([0-9]+)\.([0-9]+)\.([0-9]+)(-[A-Za-z]*\.?([0-9]+))?.*$/\1 \2 \3 \5/p')
    if [ -z "$parts" ]; then
        echo "bundle.sh: not a version: $version" >&2
        exit 2
    fi
    # shellcheck disable=SC2086
    set -- $parts
    build=$(($1 * 1000000 + $2 * 10000 + $3 * 100 + ${4:-99}))
else
    version=$(sed -n 's/^version = "\(.*\)"$/\1/p' Cargo.toml | head -n 1)
    build=$version
fi

${CARGO:-cargo} build --release

app="target/release/$name.app"
rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp target/release/remote-agent-gpui "$app/Contents/MacOS/remote-agent-gpui"

# The Mac app's icon, from the asset catalog make-app-icon.swift draws.
icons=../apple/Shared/Assets.xcassets/AppIcon.appiconset
work=$(mktemp -d)
mkdir "$work/AppIcon.iconset"
for size in 16 32 128 256 512; do
    cp "$icons/AppIcon-Mac-$size.png" "$work/AppIcon.iconset/icon_${size}x${size}.png"
    cp "$icons/AppIcon-Mac-$size@2x.png" "$work/AppIcon.iconset/icon_${size}x${size}@2x.png"
done
iconutil -c icns -o "$app/Contents/Resources/AppIcon.icns" "$work/AppIcon.iconset"
rm -rf "$work"

updates=
if ! $release; then
    # Which build this is, for Check for Update. builtAt tells builds apart.
    commit=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
    branch=$(git branch --show-current 2>/dev/null || true)
    dirty=false
    [ -n "$(git status --porcelain -- . 2>/dev/null)" ] && dirty=true
    cat > "$app/Contents/Resources/build.json" <<JSON
{"commit": "$commit", "dirty": $dirty, "branch": "$branch", "builtAt": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"}
JSON
elif $sparkle; then
    # Sparkle, and the feed it reads (src/ui/sparkle.rs).
    sparkle_dir=$(scripts/sparkle.sh)
    mkdir -p "$app/Contents/Frameworks"
    ditto "$sparkle_dir/Sparkle.framework" "$app/Contents/Frameworks/Sparkle.framework"
    updates="
    <key>SUFeedURL</key>
    <string>$feed</string>
    <key>SUPublicEDKey</key>
    <string>$sparkle_public_key</string>
    <key>SUEnableAutomaticChecks</key>
    <true/>"
fi

cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>remote-agent-gpui</string>
    <key>CFBundleIdentifier</key>
    <string>$id</string>
    <key>CFBundleName</key>
    <string>$name</string>
    <key>CFBundleDisplayName</key>
    <string>$name</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>$version</string>
    <key>CFBundleVersion</key>
    <string>$build</string>
    <key>CFBundleIconFile</key>
    <string>AppIcon</string>
    <key>LSMinimumSystemVersion</key>
    <string>13.0</string>
    <key>LSApplicationCategoryType</key>
    <string>public.app-category.developer-tools</string>
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>NSLocalNetworkUsageDescription</key>
    <string>Connect to rad running on computers on your network.</string>
    <key>NSMicrophoneUsageDescription</key>
    <string>Dictation turns what you say into a prompt.</string>
    <key>NSSpeechRecognitionUsageDescription</key>
    <string>Dictation recognizes your speech on this Mac to write a prompt.</string>
    <key>NSSupportsAutomaticGraphicsSwitching</key>
    <true/>$updates
</dict>
</plist>
PLIST

scripts/sign.sh "$app"
echo "$app"

# Installed beside the old one, then swapped in, so that a Check for Update
# never copies half a build.
if [ -n "$install_dir" ]; then
    dest="$install_dir/$name.app"
    rm -rf "$dest.new" "$dest.old"
    ditto "$app" "$dest.new"
    if [ -e "$dest" ]; then mv "$dest" "$dest.old"; fi
    mv "$dest.new" "$dest"
    rm -rf "$dest.old"
    echo "$dest"
fi
