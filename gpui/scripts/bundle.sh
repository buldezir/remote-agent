#!/bin/sh
# Builds the client as an app that Finder opens, signed (scripts/sign.sh):
#
#   scripts/bundle.sh            → target/release/Remote Agent GPUI.app,
#                                  installed in /Applications
#
# It installs a copy in /Applications, or the folder RA_GPUI_INSTALL_DIR
# names (empty: none), so that every worktree's build lands in one place.
# There, the app on another Mac pulls it with Check for Update, over ssh
# (src/store/build_update.rs): Contents/Resources/build.json says which
# build it is.
#
# A bare executable, such as target/release/remote-agent-gpui, is a
# command-line program to Finder, which opens it in Terminal. The bundle has
# the same identifier as `cargo run`'s builds (dev.remote-agent.gpui) and the
# same signature, so the two share the Keychain's tokens without asking.
set -eu
cd "$(dirname "$0")/.."

${CARGO:-cargo} build --release

app="target/release/Remote Agent GPUI.app"
version=$(sed -n 's/^version = "\(.*\)"$/\1/p' Cargo.toml | head -n 1)
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

# Which build this is, for Check for Update. builtAt tells builds apart.
commit=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
branch=$(git branch --show-current 2>/dev/null || true)
dirty=false
[ -n "$(git status --porcelain -- . 2>/dev/null)" ] && dirty=true
cat > "$app/Contents/Resources/build.json" <<JSON
{"commit": "$commit", "dirty": $dirty, "branch": "$branch", "builtAt": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"}
JSON

cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>remote-agent-gpui</string>
    <key>CFBundleIdentifier</key>
    <string>dev.remote-agent.gpui</string>
    <key>CFBundleName</key>
    <string>Remote Agent GPUI</string>
    <key>CFBundleDisplayName</key>
    <string>Remote Agent GPUI</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>$version</string>
    <key>CFBundleVersion</key>
    <string>$version</string>
    <key>CFBundleIconFile</key>
    <string>AppIcon</string>
    <key>LSMinimumSystemVersion</key>
    <string>13.0</string>
    <key>LSApplicationCategoryType</key>
    <string>public.app-category.developer-tools</string>
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>NSMicrophoneUsageDescription</key>
    <string>Dictation turns what you say into a prompt.</string>
    <key>NSSpeechRecognitionUsageDescription</key>
    <string>Dictation recognizes your speech on this Mac to write a prompt.</string>
    <key>NSSupportsAutomaticGraphicsSwitching</key>
    <true/>
</dict>
</plist>
PLIST

scripts/sign.sh "$app"
echo "$app"

# Installed beside the old one, then swapped in, so that a Check for Update
# never copies half a build.
dest_dir=${RA_GPUI_INSTALL_DIR-/Applications}
if [ -n "$dest_dir" ]; then
    dest="$dest_dir/Remote Agent GPUI.app"
    rm -rf "$dest.new" "$dest.old"
    ditto "$app" "$dest.new"
    if [ -e "$dest" ]; then mv "$dest" "$dest.old"; fi
    mv "$dest.new" "$dest"
    rm -rf "$dest.old"
    echo "$dest"
fi
