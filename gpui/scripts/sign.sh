#!/bin/sh
# Signs the GPUI client with the Mac's Apple Development identity, so that
# every build is the same app to the Keychain and reads the tokens earlier
# builds saved without asking. (Ad hoc or self-signed, each build is a new
# app, and macOS asks for the login password.)
#
#   scripts/sign.sh target/release/remote-agent-gpui
#
# RA_GPUI_SIGN_IDENTITY picks another identity (a name or a SHA-1 hash).
# Without one, it signs ad hoc and says that the Keychain will ask.
set -eu

bin=${1:?usage: sign.sh <binary>}
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

# Already signed with it: cargo replaces the binary when it builds, so an
# unchanged one keeps its signature.
if [ "$identity" != "-" ] && codesign -dv "$bin" 2>&1 | grep -qxF "Authority=$identity" &&
    codesign --verify "$bin" 2>/dev/null; then
    exit 0
fi
codesign --force --sign "$identity" --identifier dev.remote-agent.gpui "$bin" 2>&1 | grep -v "replacing existing signature" >&2 || true
codesign --verify "$bin"
