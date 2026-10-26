#!/usr/bin/env python3
"""Writes the app's icons, Material Symbols (Rounded), as vector drawables.

Each becomes app/src/main/res/drawable/sym_<name>.xml (sym_<name>_fill for the
filled style). Add a name to SYMBOLS and rerun; don't edit the XML by hand.

    python3 android/scripts/symbols.py
"""
import os
import re
import sys
import urllib.request

# name: filled. ui/Icons.kt names them, with the SF Symbol each replaces.
SYMBOLS = {
    "add": False, "add_circle": False, "alt_route": False, "archive": False, "arrow_back": False,
    "arrow_upward": False, "assignment": False, "asterisk": False, "build": False, "cancel": True,
    "check": False, "check_box": True, "check_box_outline_blank": False, "check_circle": True,
    "chevron_right": False, "circle": False, "close": False, "code": False, "compress": False,
    "computer": False, "contact_support": False, "content_copy": False, "content_paste": False,
    "data_object": False, "delete": False, "description": False, "diamond": False, "difference": False,
    "download": False, "edit": False, "edit_square": False, "expand_more": False, "folder": False, "folder_off": False,
    "graphic_eq": False, "group": False, "image": False, "keyboard_arrow_down": False,
    "keyboard_arrow_left": False, "keyboard_arrow_right": False, "keyboard_arrow_up": False,
    "keyboard_tab": False, "language": False, "memory": False, "mic": False, "more_vert": False,
    "pan_tool": True, "pending": False, "photo_library": False, "psychology": False, "qr_code_scanner": False,
    "radio_button_checked": False, "radio_button_unchecked": False, "refresh": False, "schedule": False,
    "search": False, "settings": False, "share": False, "stop": True, "stop_circle": False, "terminal": False,
    "theater_comedy": False, "undo": False, "unfold_more": False, "arrow_selector_tool": False,
    "warning": True, "wifi_off": False, "wrap_text": False, "dangerous": False, "link": False,
    "keyboard_hide": False, "functions": False, "play_circle": False, "inbox": False,
    "dns": False, "login": False, "open_in_new": False, "save": False,
}

URL = "https://fonts.gstatic.com/s/i/short-term/release/materialsymbolsrounded/{name}/{style}/24px.svg"

VECTOR = """<?xml version="1.0" encoding="utf-8"?>
<!-- Material Symbols Rounded "{name}", written by android/scripts/symbols.py. -->
<vector xmlns:android="http://schemas.android.com/apk/res/android"
    android:width="24dp"
    android:height="24dp"
    android:viewportWidth="960"
    android:viewportHeight="960">
    <group android:translateY="960">
        <path
            android:fillColor="#FF000000"
            android:pathData="{path}" />
    </group>
</vector>
"""


def main():
    out = os.path.join(os.path.dirname(__file__), "..", "app", "src", "main", "res", "drawable")
    os.makedirs(out, exist_ok=True)
    for name, filled in sorted(SYMBOLS.items()):
        svg = urllib.request.urlopen(URL.format(name=name, style="fill1" if filled else "default")).read().decode()
        paths = re.findall(r'<path d="([^"]+)"', svg)
        if len(paths) != 1 or 'viewBox="0 -960 960 960"' not in svg:
            sys.exit(f"{name}: unexpected SVG")
        file = f"sym_{name}{'_fill' if filled else ''}.xml"
        with open(os.path.join(out, file), "w") as f:
            f.write(VECTOR.format(name=name, path=paths[0]))
        print(file)


if __name__ == "__main__":
    main()
