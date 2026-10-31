#!/usr/bin/env bash
# Walks the Android app through the fake harness on an emulator, as
# web/e2e/smoke.mjs does the web client: pairing by deep link, a new worktree
# session, the fake keywords, images, changes and revert, the terminal, the
# Inbox and Servers tabs, search and find in a session, a tablet's sidebar, the dark theme, the server's settings (updating and
# signing in the fake agent, naming sessions with it), and a draft saved,
# edited, saved again and sent. Saves screenshots to
# $SCRATCH/shots and removes the server at the end, which unpairs it. Never
# point it at the developer's rad.
#
#   export ANDROID_HOME=… SCRATCH=…    # $SCRATCH/radhome: a scratch rad on 127.0.0.1:7499 with fake: true
#   android/e2e/smoke.sh               # $SCRATCH/root/proj: a git repo under its roots
#
# PORT names another port the scratch rad listens on, RAD another binary.
#
# An emulator must be running (adb devices). Installs the debug build first.
set -euo pipefail
cd "$(dirname "$0")/../.."
. android/e2e/lib.sh
RAD="${RAD:-server/bin/rad}"
PROJECT="${PROJECT:-proj}"
PORT="${PORT:-7499}"
radctl() { RAD_HOME="$SCRATCH/radhome" "$RAD" "$@"; }

fail() {
  echo "FAILED: $*" >&2
  shot failure > /dev/null
  exit 1
}
step() { echo "== $*"; }
# The session's worktree: where the agent and the shell write.
worktree() { dirname "$(find "$SCRATCH/radhome/data/worktrees" -name smoke.txt -newer "$SCRATCH/.smoke-start" | head -1)"; }

touch "$SCRATCH/.smoke-start"
"$A" reverse tcp:$PORT tcp:$PORT > /dev/null
for k in window_animation_scale transition_animation_scale animator_duration_scale; do "$A" shell settings put global $k 0; done
restore() {
  for k in window_animation_scale transition_animation_scale animator_duration_scale; do "$A" shell settings put global $k 1; done
  if [ -n "${WIDE:-}" ]; then "$A" shell wm size reset; "$A" shell wm density reset; fi
  if [ -n "${NIGHT:-}" ]; then "$A" shell cmd uimode night no; fi
}
trap restore EXIT

step "install and pair"
(cd android && ./gradlew -q :app:installDebug)
"$A" shell am force-stop "$PKG"
if [ "$("$A" shell getprop ro.build.version.sdk | tr -d '\r')" -ge 37 ]; then
  step "local network permission: deny, then grant"
  permission=android.permission.ACCESS_LOCAL_NETWORK
  "$A" shell pm revoke "$PKG" "$permission"
  "$A" shell pm clear-permission-flags "$PKG" "$permission" user-set user-fixed
  "$A" shell am start -W -n "$PKG/dev.remoteagent.app.MainActivity" > /dev/null
  wait_for 'id=permission_deny_button' 10 || fail "no local network permission prompt"
  tap 'id=permission_deny_button'
  wait_for 'text=Local network access denied' 10 || fail "no permission denial guidance"
  tap 'text=OK'
  wait_for 'id=tab-inbox' 8 || fail "denial blocked the app"
  "$A" shell am force-stop "$PKG"
  "$A" shell am start -W -n "$PKG/dev.remoteagent.app.MainActivity" > /dev/null
  wait_for 'id=permission_allow_button' 10 || fail "no permission retry"
  tap 'id=permission_allow_button'
  wait_for 'id=tab-inbox' 8 || fail "grant didn't open the app"
  "$A" shell am force-stop "$PKG"
fi
"$A" shell am start -W -n "$PKG/dev.remoteagent.app.MainActivity" > /dev/null
wait_for 'id=tab-inbox' 8 || fail "app didn't start"
devices_before=$(radctl devices | grep -c . || true)
open_link "$(radctl pair --print-url)"
wait_for 'id=pair' 10 && tap 'id=pair' || fail "no pairing sheet"
# Pairing opens the server in the Servers tab.
wait_for 'id=new-session' 15 || fail "pairing didn't open the server"
shot 01-server

step "new worktree session"
tap 'id=new-session'
wait_for 'id=project-picker' 10 || fail "no new session sheet"
tap 'id=project-picker'; sleep 0.5
tap "text=$PROJECT" || fail "no project $PROJECT (add it once with Add a folder…)"
sleep 0.5
tap 'id=harness-fake' || fail "no fake harness (fake: true in the scratch config)"
sleep 0.3
tap 'text=Isolated git worktree'; sleep 0.3
"$A" shell input swipe 540 1800 540 500 300; sleep 0.5   # the prompt, at the foot of the form
fill 'id=new-prompt' 'write smoke.txt'
shot 02-new-session
tap 'id=start'
wait_for 'id=approval' 20 || fail "no approval for write"
shot 03-approval
tap 'text=Allow'
wait_for 'contains=Echo: write smoke.txt' 15 || fail "no echo after allowing"
[ -f "$(worktree)/smoke.txt" ] || fail "the agent's file isn't in a worktree"

step "skill suggestions"
fill 'id=composer' '/prot'
wait_for 'text=/skill:prototype' 15 || fail "no skill suggestion"
shot 03-skill-suggestions
tap 'text=/skill:prototype'
wait_for 'text=/skill:prototype ' 5 || fail "skill selection lost the draft"
"$A" shell input keycombination 113 29
"$A" shell input keyevent KEYCODE_DEL
fill 'id=composer' '/skill:prototype arguments'
if find_view 'id=command-suggestions' >/dev/null; then fail "suggestions replaced arguments"; fi
"$A" shell input keycombination 113 29
"$A" shell input keyevent KEYCODE_DEL

step "question"
fill 'id=composer' 'question'; tap 'id=send'
wait_for 'text=Blue' 15 || fail "no question"
tap 'text=Blue'; sleep 0.3; tap 'text=Submit'
wait_for 'contains=answer' 15 || fail "no answer echoed"

step "deny with a reason"
type_text 'write denied.txt'; tap 'id=send'
wait_for 'text=Deny' 15 && tap 'text=Deny'
wait_for 'text=Reason (optional)' 5 || fail "no reason dialog"
fill 'text=Tell the agent why' 'not this one'; tap 'text=Deny'
wait_for 'contains=not this one' 10 || fail "the reason isn't shown"

step "stop a slow turn"
fill 'id=composer' 'slow a b c d e f g h i j k l m n o p'; xy=$(find_view 'id=send'); "$A" shell input tap $xy
sleep 0.4; tap 'desc=Stop' || fail "no stop button"
wait_for 'text=Interrupted' 15 || fail "the turn wasn't interrupted"

step "fail"
type_text 'fail now'; tap 'id=send'
wait_for 'text=Failed' 15 || fail "no failed turn"

step "pictures"
type_text 'picture'; tap 'id=send'; sleep 3
type_text 'screenshot'; tap 'id=send'; sleep 3
[ "$(texts 'desc=Image' | grep -c .)" -ge 2 ] || fail "images missing"
shot 04-images

step "/compact"
hide_keyboard; tap 'id=session-menu'; sleep 0.5; tap 'text=/compact'
wait_for 'text=Context compacted' 15 || fail "no compact notice"

step "changes and revert"
hide_keyboard; tap 'id=session-menu'; sleep 0.5; tap 'text=Changes'
wait_for 'id=file-row' 10 || fail "no changes"
shot 05-changes
picture=$(ls "$(worktree)"/picture-*.png | head -1)
[ -f "$picture" ] || fail "the picture isn't in the worktree"
tap 'id=revert'; sleep 0.5; tap 'text=Before turn 2'; sleep 0.5; tap 'text=Revert to before turn 2'
wait_for 'id=reverted' 10 || fail "no revert"
[ ! -e "$picture" ] || fail "revert left $(basename "$picture")"
tap 'desc=Done'; sleep 1

step "terminal"
hide_keyboard; tap 'id=session-menu'; sleep 0.5; tap 'text=Terminal'; sleep 3
fill 'id=terminal' 'expr 40 + 2 > t42.txt'; "$A" shell input keyevent KEYCODE_ENTER; sleep 1.5
[ "$(cat "$(worktree)/t42.txt" 2>/dev/null)" = 42 ] || fail "the shell didn't write t42.txt"
shot 06-terminal
tap 'desc=Hide Terminal'; sleep 0.5

step "inbox tab"
# Every server's sessions: what needs you or is unread first, the rest,
# working or not, under Recent. A session that ended with no one looking is
# unread.
hide_keyboard; tap 'desc=Back'; sleep 1
tap 'id=tab-inbox'
wait_for 'text=Inbox' 5 || fail "no Inbox tab"
radctl debug run --cwd "$SCRATCH/root/$PROJECT" "hello from the inbox" > /dev/null
wait_for 'text=hello from the inbox' 10 || fail "the new session isn't in the Inbox"
wait_for 'text=Needs You' 5 || fail "no Needs You for an unread session"
find_view 'text=Recent' > /dev/null || fail "the read session isn't under Recent"
shot 06b-inbox
tap 'text=hello from the inbox'
wait_for 'contains=Echo: hello from the inbox' 10 || fail "the unread session didn't open"
! find_view 'id=tab-inbox' > /dev/null || fail "the tabs show over a session"
shot 06c-inbox-session
# Wide and narrow again, the session stays open, then back goes to the Inbox.
WIDE=1; "$A" shell wm size 2560x1600; "$A" shell wm density 320
wait_for 'id=sidebar' 10 || fail "no sidebar beside the Inbox's session"
[ "$(texts 'text=hello from the inbox' | grep -c .)" -ge 2 ] || fail "the Inbox's session isn't open beside the sidebar"
shot 06d-inbox-tablet
"$A" shell wm size reset; "$A" shell wm density reset; WIDE=; sleep 2
wait_for 'contains=Echo: hello from the inbox' 10 || fail "narrowing closed the Inbox's session"
tap 'desc=Back'; sleep 1
wait_for 'text=Recent' 5 || fail "no Recent after reading"
! find_view 'text=Needs You' > /dev/null || fail "the session read is still under Needs You"
shot 06e-inbox-read

step "search and find"
# The Inbox's search finds the session by what was said in it, in place of
# its rows, and opens it at the item that matched.
fill 'id=search-field' 'hello from the'
wait_for 'id=search-result' 10 || fail "no search results"
! find_view 'text=Recent' > /dev/null || fail "the Inbox's rows show with the results"
hide_keyboard; shot 06g-search
tap 'id=search-result'
wait_for 'contains=Echo: hello from the inbox' 10 || fail "the result didn't open its session"
# Find in it: the prompt and the reply, starting at the newest; Previous goes
# up, Next down, wrapping around.
tap 'id=session-menu'; sleep 0.5; tap 'text=Find in Session'
wait_for 'id=find-field' 5 || fail "no find bar"
fill 'id=find-field' 'hello'
wait_for 'text=2 of 2' 5 || fail "find didn't count the prompt and the reply"
tap 'id=find-previous'
wait_for 'text=1 of 2' 5 || fail "Previous didn't go to the prompt"
tap 'id=find-previous'
wait_for 'text=2 of 2' 5 || fail "Previous didn't wrap around"
hide_keyboard; shot 06h-find
tap 'id=find-done'; sleep 0.5
! find_view 'id=find-bar' > /dev/null || fail "Done didn't close the find bar"
# Back goes to the results, and clearing the field to the Inbox's rows.
hide_keyboard; tap 'desc=Back'; sleep 1
wait_for 'id=search-result' 5 || fail "back didn't return to the results"
tap 'id=search-clear'
wait_for 'text=Recent' 5 || fail "clearing the search didn't bring back the Inbox"
hide_keyboard

step "servers tab keeps its place"
tap 'id=tab-servers'
# The server's sessions, which have a back button, not the list of servers.
wait_for 'desc=Back' 5 || fail "the Servers tab lost its place"
find_view 'text=hello from the inbox' > /dev/null || fail "the server's sessions don't list the Inbox's"
shot 06f-servers-tab
tap 'text=write smoke.txt' || fail "no smoke session"
wait_for 'id=composer' 10 || fail "the session didn't open"

step "tablet"
# Wide, every server's sessions are in a sidebar beside the open session.
WIDE=1; "$A" shell wm size 2560x1600; "$A" shell wm density 320
wait_for 'id=sidebar' 10 || fail "no sidebar on a tablet"
[ "$(texts 'text=write smoke.txt' | grep -c .)" -ge 2 ] || fail "the session isn't open beside the sidebar"
shot 07-tablet
# Narrow again, back from the session goes to its server's sessions (below).
"$A" shell wm size reset; "$A" shell wm density reset; WIDE=; sleep 2

step "dark theme"
tap 'desc=Back'; sleep 1
tap 'desc=More'; sleep 0.5; tap 'text=Settings'; sleep 1
tap 'text=Dark'; sleep 2
shot 08-settings-dark
tap 'text=System'; sleep 2
"$A" shell input keyevent KEYCODE_BACK; sleep 1

step "server settings"
# A turn that fails as a signed-out agent's would signs the fake agent out.
tap 'text=write smoke.txt' || fail "no smoke session"
wait_for 'id=composer' 10 || fail "the session didn't open"
fill 'id=composer' 'logout'; tap 'id=send'
wait_for 'contains=Invalid API key' 15 || fail "logout didn't fail the turn"
hide_keyboard; tap 'desc=Back'; sleep 1
tap 'desc=More'; sleep 0.5; tap 'text=Server Settings…'
wait_for 'id=server-settings' 10 || fail "no server settings"
wait_for 'id=sign-in-fake' 15 || fail "Fake isn't shown signed out"
shot 09-server-settings

step "update an agent"
# Only the fake agent: the others' updaters and sign-ins are real.
tap 'id=agent-menu-fake'; sleep 0.5; tap 'text=Update'
wait_for 'contains=Fake is up to date.' 15 || fail "no update output"
wait_for 'contains=Fake (scripted) 1' 10 || fail "the update didn't finish"
tap 'id=task-done'; sleep 0.5

step "sign in"
tap 'id=sign-in-fake'
wait_for 'id=task-code' 10 || fail "no code field"
find_view 'id=open-sign-in' > /dev/null || fail "no sign-in page button"
shot 10-sign-in
fill 'id=task-code' 'abc123'; tap 'id=task-send'
wait_for 'text=Signed in.' 15 || fail "not signed in"
hide_keyboard; shot 11-signed-in
tap 'id=task-done'; sleep 1
! find_view 'id=sign-in-fake' > /dev/null || fail "Fake still shows Sign In"

step "system agent"
tap 'id=system-agent'; sleep 0.5; tap 'text=Fake (scripted)'
wait_for 'id=system-model' 10 || fail "the system agent wasn't saved"
tap 'desc=Done'; sleep 0.5
tap 'id=new-session'
wait_for 'id=project-picker' 10 || fail "no new session sheet"
tap 'id=project-picker'; sleep 0.5; tap "text=$PROJECT"; sleep 0.5
tap 'id=harness-fake'; sleep 0.3
"$A" shell input swipe 540 1800 540 500 300; sleep 0.5
fill 'id=new-prompt' 'tidy up the readme please'; hide_keyboard
tap 'id=start'
wait_for 'contains=Named: tidy up the readme' 20 || fail "the system agent didn't name the session"
shot 12-named-session
tap 'desc=Back'; sleep 1
# None again, so that a session's title is its prompt, as the steps above expect.
tap 'desc=More'; sleep 0.5; tap 'text=Server Settings…'
wait_for 'id=system-agent' 10 || fail "no server settings"
tap 'id=system-agent'; sleep 0.5; tap 'text=None'; sleep 1
find_view 'id=system-model' > /dev/null && fail "the system agent wasn't unset"
tap 'desc=Done'; sleep 0.5

step "empty session: its prompt takes the keyboard"
tap 'id=new-session'
wait_for 'id=project-picker' 10 || fail "no new session sheet"
tap 'id=project-picker'; sleep 0.5; tap "text=$PROJECT"; sleep 0.5
tap 'id=harness-fake'; sleep 0.3
tap 'id=start'
wait_for 'contains=No prompts yet' 10 || fail "the empty session didn't open"
sleep 1   # for the keyboard, as fill waits
type_text 'hi'
wait_for 'text=hi' 5 || fail "typing in a session started empty doesn't reach its prompt"
shot 13-empty-session
hide_keyboard; tap 'desc=Back'; sleep 1

step "draft"
# Save as Draft starts a session that keeps the prompt, unsent: nothing runs.
tap 'id=new-session'
wait_for 'id=project-picker' 10 || fail "no new session sheet"
tap 'id=project-picker'; sleep 0.5; tap "text=$PROJECT"; sleep 0.5
tap 'id=harness-fake'; sleep 0.3
"$A" shell input swipe 540 1800 540 500 300; sleep 0.5
fill 'id=new-prompt' 'remember the milk'; hide_keyboard
shot 14-new-draft
tap 'id=save-as-draft' || fail "no Save as Draft in the new session sheet"
wait_for 'id=draft-bubble' 10 || fail "the session didn't open with its draft"
sleep 2
! find_view 'contains=Echo' > /dev/null || fail "the draft ran"
! find_view 'contains=No prompts yet' > /dev/null || fail "an empty session's hint shows with the draft"
shot 15-draft
# The server's list shows a pencil where the idle ring would be.
tap 'desc=Back'; sleep 1
wait_for 'desc=Draft' 5 || fail "no pencil for the draft in the list"
shot 16-draft-in-list
# Edit takes the draft back into the composer, which saves it again.
tap 'text=remember the milk' || fail "no draft session in the list"
wait_for 'id=edit-draft' 10 || fail "no Edit under the draft"
tap 'id=edit-draft'
wait_for 'id=save-draft' 10 || fail "Edit didn't put the draft in the composer"
! find_view 'id=draft-bubble' > /dev/null || fail "the draft stayed in the transcript after Edit"
type_text ' and eggs'
shot 17-draft-in-composer
tap 'id=save-draft'
wait_for 'id=draft-bubble' 10 || fail "Save as Draft in the composer didn't keep the draft"
! find_view 'id=save-draft' > /dev/null || fail "the composer kept the saved draft"
hide_keyboard
shot 18-draft-saved
# The same in the system's dark theme.
NIGHT=1; "$A" shell cmd uimode night yes; sleep 2
shot 19-draft-dark
tap 'desc=Back'; sleep 1
wait_for 'desc=Draft' 5 || fail "no pencil for the draft in the dark list"
shot 20-draft-in-list-dark
"$A" shell cmd uimode night no; NIGHT=; sleep 2
# Send runs it, and the draft becomes the session's first prompt.
tap 'text=remember the milk and eggs' || fail "the draft didn't name the session"
wait_for 'id=send-draft' 10 || fail "no Send under the draft"
tap 'id=send-draft'
wait_for 'contains=Echo: remember the milk and eggs' 15 || fail "the draft didn't run once sent"
! find_view 'id=draft-bubble' > /dev/null || fail "the draft stayed after it was sent"
find_view 'id=user-bubble' > /dev/null || fail "the sent draft isn't a prompt in the transcript"
shot 21-draft-sent
hide_keyboard; tap 'desc=Back'; sleep 1
! find_view 'desc=Draft' > /dev/null || fail "the list still shows the sent draft's pencil"

step "remove the server"
tap 'desc=More'; sleep 0.5; tap 'text=Remove server'; sleep 0.5; tap 'text=Remove'; sleep 2
[ "$(radctl devices | grep -c . || true)" -lt "$((devices_before + 1))" ] || fail "the device is still paired"
# Both tabs offer to pair one.
wait_for 'text=No servers' 5 || fail "the Servers tab doesn't offer to pair a server"
shot 22-no-servers
tap 'id=tab-inbox'
wait_for 'text=No servers' 5 || fail "the Inbox tab doesn't offer to pair a server"
shot 23-no-servers-inbox
echo "ok: screenshots in $SHOTS"
