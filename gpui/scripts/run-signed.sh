#!/bin/sh
# Cargo's runner (.cargo/config.toml): signs the app before `cargo run`
# runs it (scripts/sign.sh). Test binaries run as they are.
set -eu
case "$(basename "$1")" in
remote-agent-gpui) "$(dirname "$0")/sign.sh" "$1" ;;
esac
exec "$@"
