#!/bin/sh
# Builds rad into Remote Agent Server.app/Contents/MacOS, for every arch the
# app is built for, and signs it the way Xcode is about to sign the app.
# Runs as the target's last build phase.
set -eu

go=$(PATH="$PATH:/opt/homebrew/bin:/usr/local/go/bin:/usr/local/bin" command -v go) || {
	echo "error: Go not found. Install it (brew install go) to build rad into the app."
	exit 1
}

out="$TARGET_BUILD_DIR/$EXECUTABLE_FOLDER_PATH/rad"
tmp="$DERIVED_FILE_DIR/rad"
mkdir -p "$tmp"
set --
for arch in $ARCHS; do
	case "$arch" in
	arm64) goarch=arm64 ;;
	x86_64) goarch=amd64 ;;
	*) echo "error: no Go arch for $arch"; exit 1 ;;
	esac
	CGO_ENABLED=0 GOOS=darwin GOARCH=$goarch "$go" -C "$SRCROOT/../server" build -trimpath \
		-ldflags "-X remote-agent/internal/api.Version=$MARKETING_VERSION" -o "$tmp/rad-$arch" ./cmd/rad
	set -- "$@" "$tmp/rad-$arch"
done
# Quoted: derived data may sit under a path with spaces (Application Support).
lipo -create "$@" -output "$out"

# rad sends Apple Events for agents (osascript), so it carries the app's
# entitlements; codesign refuses to sign an app around unsigned code.
# Notarization needs a secure timestamp on every binary, so a Developer ID
# signature asks Apple for one; other builds skip it.
timestamp=--timestamp=none
case "${EXPANDED_CODE_SIGN_IDENTITY_NAME:-${CODE_SIGN_IDENTITY:-}}" in
"Developer ID"*) timestamp=--timestamp ;;
esac
codesign --force --sign "${EXPANDED_CODE_SIGN_IDENTITY:--}" --options runtime "$timestamp" \
	--identifier "$PRODUCT_BUNDLE_IDENTIFIER.rad" --entitlements "$SRCROOT/$CODE_SIGN_ENTITLEMENTS" "$out"
