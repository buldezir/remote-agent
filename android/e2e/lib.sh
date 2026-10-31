# Helpers for driving the app on an emulator with adb. Source it:
#
#   . android/e2e/lib.sh            # needs ANDROID_HOME, SCRATCH; ANDROID_SERIAL if several devices
#
# Views are found in a `uiautomator dump` by selector (see ui.py):
# text=…, desc=…, id=<Compose test tag>, contains=….

A="$ANDROID_HOME/platform-tools/adb"
E2E_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
PKG="${PKG:-dev.remoteagent.app.debug}"
SHOTS="${SHOTS:-$SCRATCH/shots}"
mkdir -p "$SHOTS"

dump() { "$A" exec-out uiautomator dump /dev/tty > "$SCRATCH/ui.xml" 2>/dev/null; }

# The centre of a view, "x y".
find_view() { dump && python3 -I "$E2E_DIR/ui.py" "$SCRATCH/ui.xml" "$1"; }

tap() {
  local xy
  xy=$(find_view "$1") || { echo "not found: $1" >&2; return 1; }
  "$A" shell input tap $xy
}

# Presses a view for a moment, as a long press: a row's menu.
long_press() {
  local xy
  xy=$(find_view "$1") || { echo "not found: $1" >&2; return 1; }
  "$A" shell input swipe $xy $xy 800
}

# Waits up to $2 seconds (default 15) for a view to appear.
wait_for() {
  local i
  for i in $(seq 1 "${2:-15}"); do
    find_view "$1" > /dev/null && return 0
    sleep 1
  done
  echo "timed out waiting for $1" >&2
  return 1
}

# Every text matching a selector, one per line.
texts() { dump && python3 -I "$E2E_DIR/ui.py" "$SCRATCH/ui.xml" "$1" --all; }

# Types into the focused field; spaces become %s, as `input text` wants. Quoted
# for the device's shell, which would take > or ; for its own (no ' in $1).
type_text() { "$A" shell "input text '$(printf '%s' "$1" | sed 's/ /%s/g')'"; }

# Taps a text field and types once the keyboard is up: keys sent before it
# attaches go to the window, where Space presses the focused button.
fill() {
  tap "$1" || return 1
  sleep 1
  type_text "$2"
}

# Hides the keyboard if it is up. A menu opened over it moves once it goes,
# so the place a dump found for an item can be another item's by the tap.
hide_keyboard() {
  if "$A" shell dumpsys input_method | grep -q 'mInputShown=true'; then
    "$A" shell input keyevent KEYCODE_BACK
    sleep 0.5
  fi
}

shot() { "$A" exec-out screencap -p > "$SHOTS/$1.png"; echo "$SHOTS/$1.png"; }

# Opens a pairing link in the app (the link's & would split the remote shell's command).
open_link() { "$A" shell "am start -W -a android.intent.action.VIEW -d '$1' $PKG" > /dev/null; }
