#!/usr/bin/env bash
# Run with a scratch Fake session open; checks filtering after scrolling a long catalog.
set -euo pipefail
cd "$(dirname "$0")/../.."
. android/e2e/lib.sh
wait_for 'id=composer' 10
tap 'id=composer'
sleep 1
"$A" shell input keycombination 113 29
"$A" shell input keyevent KEYCODE_DEL
type_text '/'
wait_for 'text=/skill:ask-guide' 15
# Swipe only inside the suggestion list, leaving the transcript and keyboard alone.
for i in 1 2 3 4; do
  dump
  coords=$(python3 -I - "$SCRATCH/ui.xml" <<'PY'
import re, sys, xml.etree.ElementTree as ET
raw = open(sys.argv[1]).read()
root = ET.fromstring(raw[raw.find('<?xml'):raw.rfind('>')+1])
node = next(n for n in root.iter('node') if n.get('resource-id') == 'command-suggestions')
x1,y1,x2,y2 = map(int,re.findall(r'\d+',node.get('bounds')))
print((x1+x2)//2,y2-15,(x1+x2)//2,y1+15)
PY
)
  "$A" shell input swipe $coords 250
  sleep 0.3
done
# Replace the token directly: removing it first would hide the list and mask retained state.
"$A" shell input keycombination 113 29
type_text '/ask'
sleep 0.5
dump
python3 -I - "$SCRATCH/ui.xml" <<'PY'
import re, sys, xml.etree.ElementTree as ET
raw = open(sys.argv[1]).read()
root = ET.fromstring(raw[raw.find('<?xml'):raw.rfind('>')+1])
nodes = list(root.iter('node'))
listing = next(n for n in nodes if n.get('resource-id') == 'command-suggestions')
first = next((n for n in nodes if n.get('text') == '/skill:ask-guide'), None)
assert first is not None, 'first matching skill is offscreen after filtering'
bounds = lambda n: list(map(int,re.findall(r'\d+', n.get('bounds'))))
l, f = bounds(listing), bounds(first)
assert l[1] <= f[1] < f[3] <= l[3], f'first skill is clipped: {f}, list {l}'
print('PASS: first matching skill is visible after filtering a scrolled catalog')
PY
shot suggestions-filtered

# A command typed in full must send with one Enter, rather than merely completing it.
tap 'id=composer'
"$A" shell input keycombination 113 29
type_text '/skill:prototype'
sleep 0.3
"$A" shell input keyevent KEYCODE_ENTER
sleep 0.3
dump
python3 -I - "$SCRATCH/ui.xml" <<'PYTEST'
import sys, xml.etree.ElementTree as ET
raw = open(sys.argv[1]).read()
root = ET.fromstring(raw[raw.find('<?xml'):raw.rfind('>')+1])
field = next(n for n in root.iter('node') if n.get('resource-id') == 'composer')
assert field.get('text', '') == '', 'Enter completed an exact command instead of sending it'
print('PASS: an exact command sends on one Enter')
PYTEST
