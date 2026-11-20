#!/bin/sh
# Downloads Sparkle, the release build's updater, once, checks it and prints
# the folder it unpacked into: Sparkle.framework, which bundle.sh embeds in
# a release build, and bin/sign_update, which apple/scripts/appcast.sh signs
# the update with (SIGN_UPDATE).
#
#   scripts/sparkle.sh            → target/sparkle/<version>
#
# The Server app gets the same version through Swift Package Manager
# (apple/project.yml); bump them together.
set -eu
cd "$(dirname "$0")/.."

version=2.10.0
sha256=c2bf58aa8387266ac179357b1415d6f2635f044da8be41042af32425dae6da0c

dir="$PWD/target/sparkle/$version"
if [ ! -d "$dir/Sparkle.framework" ]; then
    work=$(mktemp -d)
    trap 'rm -rf "$work"' EXIT
    curl -fsSL -o "$work/sparkle.tar.xz" \
        "https://github.com/sparkle-project/Sparkle/releases/download/$version/Sparkle-$version.tar.xz"
    if [ "$(shasum -a 256 "$work/sparkle.tar.xz" | cut -d ' ' -f 1)" != "$sha256" ]; then
        echo "sparkle.sh: Sparkle-$version.tar.xz isn't the one this script expects" >&2
        exit 1
    fi
    mkdir "$work/x"
    tar -xJf "$work/sparkle.tar.xz" -C "$work/x" Sparkle.framework bin
    mkdir -p "$(dirname "$dir")"
    rm -rf "$dir"
    mv "$work/x" "$dir"
fi
echo "$dir"
