#!/bin/sh
# Signs the client, a bare build or an app bundle, with the Mac's Apple
# Development identity, so that every build is the same app to the Keychain
# and reads the tokens earlier builds saved without asking. (Ad hoc or
# self-signed, each build is a new app, and macOS asks for the login
# password.)
#
#   scripts/sign.sh target/release/remote-agent-gpui
#   scripts/sign.sh "target/release/Remote Agent Dev.app"
#
# RA_GPUI_SIGN_IDENTITY picks another identity (a name or a SHA-1 hash).
# Without one, it signs ad hoc and says that the Keychain will ask.
#
# A bare build is signed as dev.remote-agent.gpui, the development app's
# identifier; a bundle as its CFBundleIdentifier. With a Developer ID, as the
# release workflow signs, it turns on the hardened runtime, with
# release.entitlements, and timestamps the signature, as notarization wants,
# and signs the Sparkle.framework inside first.
set -eu

target=${1:?usage: sign.sh <binary or app>}
scripts=$(cd "$(dirname "$0")" && pwd)
identity=${RA_GPUI_SIGN_IDENTITY:-}
if [ -z "$identity" ]; then
    identity=$(security find-identity -v -p codesigning 2>/dev/null |
        sed -n 's/^ *[0-9]*) [0-9A-F]* "\(Apple Development: .*\)"$/\1/p' | head -n 1)
fi
if [ -z "$identity" ]; then
    echo "sign.sh: no Apple Development identity; signing ad hoc, so the Keychain will ask for each new build." >&2
    echo "         Create one in Xcode › Settings › Accounts › Manage Certificates, or set RA_GPUI_SIGN_IDENTITY." >&2
    identity=-
fi

if [ -d "$target" ]; then
    id=$(plutil -extract CFBundleIdentifier raw "$target/Contents/Info.plist")
    set -- --force --sign "$identity" --identifier "$id"
    case "$identity" in
    "Developer ID"*)
        if [ -d "$target/Contents/Frameworks/Sparkle.framework" ]; then
            "$scripts/../../apple/scripts/sign-sparkle.sh" "$target" "$identity"
        fi
        set -- "$@" --options runtime --timestamp --entitlements "$scripts/release.entitlements"
        ;;
    esac
    codesign "$@" "$target" 2>&1 |
        grep -v "replacing existing signature" >&2 || true
    codesign --verify --deep --strict "$target"
    exit 0
fi

# Already signed with it: cargo replaces the binary when it builds, so an
# unchanged one keeps its signature.
if [ "$identity" != "-" ] && codesign -dv "$target" 2>&1 | grep -qxF "Authority=$identity" &&
    codesign --verify "$target" 2>/dev/null; then
    exit 0
fi
codesign --force --sign "$identity" --identifier dev.remote-agent.gpui "$target" 2>&1 | grep -v "replacing existing signature" >&2 || true
codesign --verify "$target"
