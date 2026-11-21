//! A session's shell on the server (PROTOCOL.md, "Terminal"): a socket of
//! its own to `/v1/terminal`, which resumes where it left off, drawn by
//! alacritty's terminal emulator. Hiding the panel only detaches; the shell
//! keeps running on the server.

use std::cell::{Cell, RefCell};
use std::rc::Rc;
use std::time::Duration;

use alacritty_terminal::event::{Event as TermEvent, EventListener};
use alacritty_terminal::grid::{Dimensions, Scroll};
use alacritty_terminal::index::{Column, Line, Point, Side};
use alacritty_terminal::selection::{Selection, SelectionType};
use alacritty_terminal::term::cell::Flags;
use alacritty_terminal::term::{Config, Osc52, Term, TermMode};
use alacritty_terminal::vte::ansi::{Color as AnsiColor, NamedColor, Processor, Rgb};
use futures_util::{SinkExt, StreamExt};
use gpui_kit::assets::IconName as L;
use gpui_kit::component::button::{Button, ButtonVariants as _};
use gpui_kit::component::menu::{DropdownMenu as _, PopupMenuItem};
use gpui_kit::component::{Icon, IconName, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    Anchor, App, Bounds, ClickEvent, ClipboardItem, Context, Entity, FocusHandle, Focusable, Font, FontStyle,
    FontWeight, Hsla, InteractiveElement as _, IntoElement, KeyDownEvent, MouseButton, MouseDownEvent, MouseMoveEvent,
    MouseUpEvent, ParentElement as _, Pixels, Render, ScrollEvent, SharedString, StrikethroughStyle, Styled as _,
    StyledText, Subscription, Task, TextRun, UnderlineStyle, Window, canvas, div, font, point, px, rgb,
};
use tokio::sync::mpsc;
use tokio_tungstenite::tungstenite::{Message, client::IntoClientRequest, http::HeaderValue};

use crate::conn::ServerConn;
use crate::protocol::TerminalMessage;
use crate::ui::common::caption;
use crate::ui::palette::Palette;
use crate::ui::state::prefs;

const SCROLLBACK: usize = 5000;
const CHUNK: usize = 16 * 1024;

#[derive(Default, Clone)]
struct Listener(Rc<RefCell<Vec<TermEvent>>>);

impl EventListener for Listener {
    fn send_event(&self, event: TermEvent) {
        self.0.borrow_mut().push(event);
    }
}

#[derive(Clone, Copy)]
struct Size {
    cols: usize,
    rows: usize,
}

impl Dimensions for Size {
    fn total_lines(&self) -> usize {
        self.rows
    }
    fn screen_lines(&self) -> usize {
        self.rows
    }
    fn columns(&self) -> usize {
        self.cols
    }
}

#[derive(Debug, Clone, PartialEq)]
enum State {
    Idle,
    Connecting,
    Open,
    Reconnecting,
    Exited { code: i64, here: bool },
    Gone,
    Failed(String),
}

enum Out {
    Data(Vec<u8>),
    Text(String),
}

enum In {
    Data(Vec<u8>),
    Message(TerminalMessage),
    Closed,
}

pub struct TerminalView {
    conn: Entity<ServerConn>,
    session_id: String,
    term: Term<Listener>,
    parser: Processor,
    events: Listener,
    shell_id: Option<String>,
    offset: u64,
    /// The next connect may start a shell: only the user's own action.
    create: bool,
    closed_here: bool,
    state: State,
    out: Option<mpsc::UnboundedSender<Out>>,
    /// The panel shows: the socket should be open.
    attached: bool,
    backoff: Duration,
    size: Size,
    sent: (u16, u16),
    bounds: Rc<Cell<Option<Bounds<Pixels>>>>,
    /// Settings' code size, which sets the cells' size.
    font_size: f32,
    focus: FocusHandle,
    selecting: bool,
    generation: u64,
    resize_task: Option<Task<()>>,
    _subscriptions: Vec<Subscription>,
}

impl Focusable for TerminalView {
    fn focus_handle(&self, _: &App) -> FocusHandle {
        self.focus.clone()
    }
}

impl TerminalView {
    pub fn new(conn: Entity<ServerConn>, session_id: String, _: &mut Window, cx: &mut Context<Self>) -> Self {
        let events = Listener::default();
        let size = Size { cols: 80, rows: 24 };
        let config = Config {
            scrolling_history: SCROLLBACK,
            osc52: Osc52::OnlyCopy,
            ..Config::default()
        };
        let term = Term::new(config, &size, events.clone());
        // The main connection drops: so does this one, until it's back.
        let sub = cx.observe(&conn, |this, conn, cx| {
            let connected = conn.read(cx).is_connected();
            if !connected && this.out.is_some() {
                this.drop_socket();
                if matches!(this.state, State::Open | State::Connecting) {
                    this.state = State::Reconnecting;
                }
                cx.notify();
            } else if connected && this.attached && this.state == State::Reconnecting && this.out.is_none() {
                this.connect(cx);
            }
        });
        Self {
            conn,
            session_id,
            term,
            parser: Processor::new(),
            events,
            shell_id: None,
            offset: 0,
            create: true,
            closed_here: false,
            state: State::Idle,
            out: None,
            attached: false,
            backoff: Duration::from_secs(1),
            size,
            sent: (0, 0),
            bounds: Rc::new(Cell::new(None)),
            font_size: crate::ui::fonts::code_size(),
            focus: cx.focus_handle(),
            selecting: false,
            generation: 0,
            resize_task: None,
            // A new code size or font changes the cells, and so the rows
            // and columns the shell is told.
            _subscriptions: vec![
                sub,
                cx.observe_global::<crate::ui::state::AppPrefs>(|_, cx| cx.notify()),
            ],
        }
    }

    /// Reattaches to the session's shell, if it still runs, without starting
    /// one: for a panel shown again rather than by the user's action.
    pub fn resume_only(mut self) -> Self {
        self.create = false;
        self
    }

    /// The panel shows: connect, once the layout gives the terminal a size.
    pub fn attach(&mut self, _: &mut Window, cx: &mut Context<Self>) {
        self.attached = true;
        cx.spawn(async move |this, cx| {
            cx.background_executor().timer(Duration::from_millis(150)).await;
            let _ = this.update(cx, |this, cx| {
                if this.attached && this.out.is_none() && matches!(this.state, State::Idle | State::Reconnecting) {
                    this.connect(cx);
                }
            });
        })
        .detach();
        cx.notify();
    }

    /// The panel hides: the socket closes, the shell keeps running.
    pub fn detach(&mut self, cx: &mut Context<Self>) {
        self.attached = false;
        self.drop_socket();
        if matches!(self.state, State::Open | State::Connecting | State::Reconnecting) {
            self.state = State::Idle;
        }
        cx.notify();
    }

    fn drop_socket(&mut self) {
        self.out = None;
        self.generation += 1;
    }

    fn connect(&mut self, cx: &mut Context<Self>) {
        let conn = self.conn.read(cx);
        let (Some(base), true) = (conn.base_url.clone(), conn.is_connected()) else {
            self.state = State::Reconnecting;
            return;
        };
        let ws = if let Some(rest) = base.strip_prefix("https://") {
            format!("wss://{rest}")
        } else if let Some(rest) = base.strip_prefix("http://") {
            format!("ws://{rest}")
        } else {
            return;
        };
        let mut url = format!(
            "{ws}/v1/terminal?session={}&cols={}&rows={}",
            self.session_id, self.size.cols, self.size.rows
        );
        match &self.shell_id {
            Some(id) => url += &format!("&id={id}&offset={}", self.offset),
            None if self.create => url += "&create=1",
            None => {}
        }
        let token = conn.token.clone();
        let runtime = conn.runtime.clone();
        let (out_tx, out_rx) = mpsc::unbounded_channel();
        let (in_tx, in_rx) = async_channel::unbounded();
        runtime.spawn(socket(url, token, out_rx, in_tx));
        self.out = Some(out_tx);
        self.generation += 1;
        let generation = self.generation;
        if self.state != State::Reconnecting {
            self.state = State::Connecting;
        }
        cx.spawn(async move |this, cx| {
            while let Ok(msg) = in_rx.recv().await {
                let alive = this
                    .update(cx, |this, cx| {
                        if this.generation != generation {
                            return false;
                        }
                        this.receive(msg, cx);
                        true
                    })
                    .unwrap_or(false);
                if !alive {
                    break;
                }
            }
        })
        .detach();
        cx.notify();
    }

    fn receive(&mut self, msg: In, cx: &mut Context<Self>) {
        match msg {
            In::Data(bytes) => {
                self.offset += bytes.len() as u64;
                self.feed(&bytes, cx);
            }
            In::Message(m) => match m.kind.as_str() {
                "attached" => {
                    if m.reset || m.id != self.shell_id {
                        self.clear();
                    }
                    if m.id != self.shell_id {
                        // A shell started, or this view hadn't seen it yet.
                        self.conn.update(cx, |c, cx| c.refresh_terminals(cx));
                    }
                    self.shell_id = m.id.clone();
                    self.offset = m.offset.unwrap_or(0);
                    self.sent = (m.cols.unwrap_or(0), m.rows.unwrap_or(0));
                    self.state = State::Open;
                    self.create = false;
                    self.backoff = Duration::from_secs(1);
                    self.send_size();
                }
                "reset" => {
                    self.clear();
                    self.offset = m.offset.unwrap_or(0);
                }
                "exit" => {
                    self.shell_id = None;
                    self.conn.update(cx, |c, cx| c.refresh_terminals(cx));
                    self.state = State::Exited {
                        code: m.exit_code.unwrap_or(0),
                        here: self.closed_here,
                    };
                    self.drop_socket();
                }
                "error" => {
                    self.shell_id = None;
                    self.conn.update(cx, |c, cx| c.refresh_terminals(cx));
                    self.state = if m.code.as_deref() == Some("gone") {
                        State::Gone
                    } else {
                        State::Failed(m.message.unwrap_or_else(|| "The terminal isn't available".into()))
                    };
                    self.drop_socket();
                }
                _ => {}
            },
            In::Closed => {
                self.out = None;
                if self.attached && matches!(self.state, State::Open | State::Connecting | State::Reconnecting) {
                    self.state = State::Reconnecting;
                    let delay = self.backoff;
                    self.backoff = (self.backoff * 2).min(Duration::from_secs(10));
                    cx.spawn(async move |this, cx| {
                        cx.background_executor().timer(delay).await;
                        let _ = this.update(cx, |this, cx| {
                            if this.attached && this.out.is_none() && this.state == State::Reconnecting {
                                this.connect(cx);
                            }
                        });
                    })
                    .detach();
                }
            }
        }
        cx.notify();
    }

    fn feed(&mut self, bytes: &[u8], cx: &mut Context<Self>) {
        self.parser.advance(&mut self.term, bytes);
        let events: Vec<TermEvent> = self.events.0.borrow_mut().drain(..).collect();
        for e in events {
            match e {
                TermEvent::PtyWrite(text) => self.write(text.into_bytes()),
                TermEvent::ClipboardStore(_, text) => cx.write_to_clipboard(ClipboardItem::new_string(text)),
                _ => {}
            }
        }
    }

    /// A full reset: CAN, ESC c.
    fn clear(&mut self) {
        self.parser.advance(&mut self.term, b"\x18\x1bc");
        self.events.0.borrow_mut().clear();
    }

    fn write(&self, bytes: Vec<u8>) {
        if let Some(out) = &self.out {
            for chunk in bytes.chunks(CHUNK) {
                let _ = out.send(Out::Data(chunk.to_vec()));
            }
        }
    }

    fn send_size(&mut self) {
        let size = (self.size.cols as u16, self.size.rows as u16);
        if size == self.sent || self.state != State::Open {
            return;
        }
        self.sent = size;
        let msg = TerminalMessage {
            kind: "resize".into(),
            cols: Some(size.0),
            rows: Some(size.1),
            ..Default::default()
        };
        if let (Some(out), Ok(text)) = (&self.out, serde_json::to_string(&msg)) {
            let _ = out.send(Out::Text(text));
        }
    }

    /// Ends the shell, for every device.
    fn close_shell(&mut self, cx: &mut Context<Self>) {
        self.closed_here = true;
        if let Some(out) = &self.out {
            let _ = out.send(Out::Text(r#"{"type":"close"}"#.into()));
        }
        cx.notify();
    }

    fn restart(&mut self, cx: &mut Context<Self>) {
        self.drop_socket();
        self.closed_here = false;
        self.shell_id = None;
        self.offset = 0;
        self.create = true;
        self.clear();
        self.state = State::Idle;
        self.connect(cx);
    }

    // MARK: Layout

    fn cell_size(&self, window: &mut Window) -> (Pixels, Pixels) {
        let f = mono();
        let id = window.text_system().resolve_font(&f);
        let width = window
            .text_system()
            .advance(id, px(self.font_size), 'm')
            .map(|s| s.width)
            .unwrap_or(px(self.font_size * 0.6));
        (width, px((self.font_size * 1.3).round()))
    }

    fn fit(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(bounds) = self.bounds.get() else { return };
        let (cw, lh) = self.cell_size(window);
        let cols = ((bounds.size.width - px(16.)) / cw).floor().max(2.) as usize;
        let rows = ((bounds.size.height - px(8.)) / lh).floor().max(2.) as usize;
        if cols == self.size.cols && rows == self.size.rows {
            return;
        }
        self.size = Size { cols, rows };
        self.term.resize(self.size);
        self.resize_task = Some(cx.spawn(async move |this, cx| {
            cx.background_executor().timer(Duration::from_millis(100)).await;
            let _ = this.update(cx, |this, _| this.send_size());
        }));
    }

    fn point_at(&self, pos: gpui_kit::Point<Pixels>, window: &mut Window) -> (Point, Side) {
        let Some(bounds) = self.bounds.get() else {
            return (Point::new(Line(0), Column(0)), Side::Left);
        };
        let (cw, lh) = self.cell_size(window);
        let x = (pos.x - bounds.origin.x - px(8.)).max(px(0.));
        let y = (pos.y - bounds.origin.y - px(4.)).max(px(0.));
        let col = ((x / cw).floor() as usize).min(self.size.cols - 1);
        let row = ((y / lh).floor() as usize).min(self.size.rows - 1);
        let side = if (x / cw).fract() < 0.5 {
            Side::Left
        } else {
            Side::Right
        };
        let offset = self.term.grid().display_offset() as i32;
        (Point::new(Line(row as i32 - offset), Column(col)), side)
    }

    // MARK: Input

    /// What a key sends the shell. ⌘C and ⌘V copy and paste, ⌘ with an
    /// arrow is Home, End or a page key; ⌘ and the rest are the app's. Tab, Shift-Tab and ⌘C reach here only because
    /// `bind_keys` takes them back from the kit's root view.
    fn key(&mut self, ev: &KeyDownEvent, _: &mut Window, cx: &mut Context<Self>) {
        let k = &ev.keystroke;
        let m = k.modifiers;
        if m.platform {
            match k.key.as_str() {
                "c" => {
                    if let Some(text) = self.term.selection_to_string().filter(|t| !t.is_empty()) {
                        cx.write_to_clipboard(ClipboardItem::new_string(text));
                    }
                    cx.stop_propagation();
                }
                "v" => {
                    if let Some(text) = cx.read_from_clipboard().and_then(|c| c.text()) {
                        self.paste(&text);
                    }
                    cx.stop_propagation();
                }
                // ⌘← → ↑ ↓ are Home, End, Page Up and Page Down, as in
                // Terminal.app's and iTerm's text editing; ⌘J and the rest
                // are the app's.
                _ => {
                    let app_cursor = self.term.mode().contains(TermMode::APP_CURSOR);
                    if self.state == State::Open
                        && let Some(bytes) = key_bytes(&k.key, k.key_char.as_deref(), m, app_cursor)
                    {
                        self.term.scroll_display(Scroll::Bottom);
                        self.term.selection = None;
                        self.write(bytes);
                        cx.stop_propagation();
                        cx.notify();
                    }
                }
            }
            return;
        }
        if self.state != State::Open {
            return;
        }
        let app_cursor = self.term.mode().contains(TermMode::APP_CURSOR);
        let bytes = key_bytes(&k.key, k.key_char.as_deref(), m, app_cursor);
        if let Some(bytes) = bytes {
            self.term.scroll_display(Scroll::Bottom);
            self.term.selection = None;
            self.write(bytes);
            cx.stop_propagation();
            cx.notify();
        }
    }

    fn paste(&mut self, text: &str) {
        let text = text.replace("\r\n", "\r").replace('\n', "\r");
        if self.term.mode().contains(TermMode::BRACKETED_PASTE) {
            let clean = text.replace('\x1b', "");
            self.write(format!("\x1b[200~{clean}\x1b[201~").into_bytes());
        } else {
            self.write(text.into_bytes());
        }
    }

    fn mouse_down(&mut self, ev: &MouseDownEvent, window: &mut Window, cx: &mut Context<Self>) {
        self.focus.focus(window, cx);
        let (point, side) = self.point_at(ev.position, window);
        self.term.selection = Some(Selection::new(SelectionType::Simple, point, side));
        self.selecting = true;
        cx.notify();
    }

    fn mouse_move(&mut self, ev: &MouseMoveEvent, window: &mut Window, cx: &mut Context<Self>) {
        if !self.selecting || ev.pressed_button != Some(MouseButton::Left) {
            return;
        }
        let (point, side) = self.point_at(ev.position, window);
        if let Some(sel) = &mut self.term.selection {
            sel.update(point, side);
        }
        cx.notify();
    }

    fn mouse_up(&mut self, _: &MouseUpEvent, _: &mut Window, cx: &mut Context<Self>) {
        self.selecting = false;
        let text = self.term.selection_to_string().filter(|t| !t.is_empty());
        match text {
            Some(t) if prefs(cx).terminal_copy_on_select => cx.write_to_clipboard(ClipboardItem::new_string(t)),
            Some(_) => {}
            None => self.term.selection = None,
        }
        cx.notify();
    }

    fn scroll(&mut self, ev: &ScrollEvent, window: &mut Window, cx: &mut Context<Self>) {
        let (_, lh) = self.cell_size(window);
        let dy = ev.delta.pixel_delta(lh).y;
        let lines = (dy / lh).round() as i32;
        if lines == 0 {
            return;
        }
        let mode = *self.term.mode();
        if mode.contains(TermMode::ALT_SCREEN) && mode.contains(TermMode::ALTERNATE_SCROLL) {
            let seq: &[u8] = if lines > 0 { b"\x1bOA" } else { b"\x1bOB" };
            self.write(seq.repeat(lines.unsigned_abs() as usize));
        } else {
            self.term.scroll_display(Scroll::Delta(lines));
        }
        cx.notify();
    }

    // MARK: Drawing

    fn render_lines(&self, p: &Palette, theme: &[Hsla; 16], focused: bool) -> Vec<StyledText> {
        let content = self.term.renderable_content();
        let cursor = content.cursor.point;
        let show_cursor = content.mode.contains(TermMode::SHOW_CURSOR) && content.display_offset == 0;
        let selection = content.selection;
        let colors = content.colors;
        let offset = content.display_offset as i32;
        let rows = self.size.rows;
        let mut lines: Vec<(String, Vec<TextRun>)> = vec![(String::new(), Vec::new()); rows];
        for cell in content.display_iter {
            let row = cell.point.line.0 + offset;
            if row < 0 || row as usize >= rows {
                continue;
            }
            if cell.flags.contains(Flags::WIDE_CHAR_SPACER) || cell.flags.contains(Flags::LEADING_WIDE_CHAR_SPACER) {
                continue;
            }
            let mut fg = resolve(cell.fg, colors, p, theme, true);
            let mut bg = resolve(cell.bg, colors, p, theme, false);
            if cell.flags.contains(Flags::INVERSE) {
                std::mem::swap(&mut fg, &mut bg);
            }
            if cell.flags.contains(Flags::DIM) {
                fg = fg.opacity(0.7);
            }
            let selected = selection.is_some_and(|s| s.contains(cell.point));
            let at_cursor = show_cursor && cell.point == cursor;
            let mut background = if bg == p.base { None } else { Some(bg) };
            if selected {
                background = Some(p.accent.opacity(0.35));
            }
            if at_cursor {
                if focused {
                    background = Some(p.text);
                    fg = p.base;
                } else {
                    background = Some(p.overlay.opacity(0.5));
                }
            }
            let c = if cell.flags.contains(Flags::HIDDEN) || cell.c == '\0' {
                ' '
            } else {
                cell.c
            };
            let mut text = c.to_string();
            if let Some(extra) = cell.zerowidth() {
                text.extend(extra.iter());
            }
            let mut f = mono();
            if cell.flags.contains(Flags::BOLD) {
                f.weight = FontWeight::BOLD;
            }
            if cell.flags.contains(Flags::ITALIC) {
                f.style = FontStyle::Italic;
            }
            let run = TextRun {
                len: text.len(),
                font: f,
                color: fg,
                background_color: background,
                underline: cell.flags.intersects(Flags::ALL_UNDERLINES).then(|| UnderlineStyle {
                    thickness: px(1.),
                    color: Some(fg),
                    wavy: cell.flags.contains(Flags::UNDERCURL),
                }),
                strikethrough: cell.flags.contains(Flags::STRIKEOUT).then(|| StrikethroughStyle {
                    thickness: px(1.),
                    color: Some(fg),
                }),
            };
            let (s, runs) = &mut lines[row as usize];
            s.push_str(&text);
            match runs.last_mut() {
                Some(last)
                    if last.color == run.color
                        && last.background_color == run.background_color
                        && last.font == run.font
                        && last.underline == run.underline
                        && last.strikethrough == run.strikethrough =>
                {
                    last.len += run.len
                }
                _ => runs.push(run),
            }
        }
        lines
            .into_iter()
            .map(|(s, runs)| {
                if s.is_empty() {
                    StyledText::new(" ").with_runs(vec![TextRun {
                        len: 1,
                        font: mono(),
                        color: p.text,
                        background_color: None,
                        underline: None,
                        strikethrough: None,
                    }])
                } else {
                    StyledText::new(SharedString::from(s)).with_runs(runs)
                }
            })
            .collect()
    }
}

fn mono() -> Font {
    font(crate::ui::fonts::code())
}

/// The palette's sixteen ANSI colours, Catppuccin's terminal theme.
fn ansi(dark: bool) -> [Hsla; 16] {
    let hex: [u32; 16] = if dark {
        [
            0x51576d, 0xe78284, 0xa6d189, 0xe5c890, 0x8caaee, 0xf4b8e4, 0x81c8be, 0xb5bfe2, 0x626880, 0xe78284,
            0xa6d189, 0xe5c890, 0x8caaee, 0xf4b8e4, 0x81c8be, 0xa5adce,
        ]
    } else {
        [
            0x5c5f77, 0xd20f39, 0x40a02b, 0xdf8e1d, 0x1e66f5, 0xea76cb, 0x179299, 0xacb0be, 0x6c6f85, 0xd20f39,
            0x40a02b, 0xdf8e1d, 0x1e66f5, 0xea76cb, 0x179299, 0xbcc0cc,
        ]
    };
    hex.map(|h| rgb(h).into())
}

fn from_rgb(c: Rgb) -> Hsla {
    rgb(((c.r as u32) << 16) | ((c.g as u32) << 8) | c.b as u32).into()
}

/// The xterm 256-colour palette beyond the first sixteen.
fn indexed(i: u8, theme: &[Hsla; 16]) -> Hsla {
    match i {
        0..=15 => theme[i as usize],
        16..=231 => {
            let i = i - 16;
            let level = |v: u8| if v == 0 { 0 } else { 55 + v as u32 * 40 };
            let (r, g, b) = (level(i / 36), level((i / 6) % 6), level(i % 6));
            rgb((r << 16) | (g << 8) | b).into()
        }
        _ => {
            let v = 8 + (i as u32 - 232) * 10;
            rgb((v << 16) | (v << 8) | v).into()
        }
    }
}

fn resolve(
    c: AnsiColor,
    colors: &alacritty_terminal::term::color::Colors,
    p: &Palette,
    theme: &[Hsla; 16],
    fg: bool,
) -> Hsla {
    match c {
        AnsiColor::Spec(rgb) => from_rgb(rgb),
        AnsiColor::Indexed(i) => colors[i as usize].map(from_rgb).unwrap_or_else(|| indexed(i, theme)),
        AnsiColor::Named(n) => {
            if let Some(over) = colors[n as usize] {
                return from_rgb(over);
            }
            match n {
                NamedColor::Foreground | NamedColor::BrightForeground => p.text,
                NamedColor::DimForeground => p.subtext,
                NamedColor::Background => p.base,
                NamedColor::Cursor => p.text,
                n if (n as usize) < 16 => theme[n as usize],
                n => {
                    // The dim colours follow the normal ones.
                    let i = (n as usize).wrapping_sub(NamedColor::DimBlack as usize);
                    if i < 8 {
                        theme[i].opacity(0.75)
                    } else if fg {
                        p.text
                    } else {
                        p.base
                    }
                }
            }
        }
    }
}

/// The socket: what rad sends goes to `incoming`; what the view sends
/// comes from `outgoing`, whose end closes it.
async fn socket(
    url: String,
    token: String,
    mut outgoing: mpsc::UnboundedReceiver<Out>,
    incoming: async_channel::Sender<In>,
) {
    let Ok(mut request) = url.into_client_request() else {
        let _ = incoming.send(In::Closed).await;
        return;
    };
    if let Ok(auth) = HeaderValue::from_str(&format!("Bearer {token}")) {
        request.headers_mut().insert("Authorization", auth);
    }
    request
        .headers_mut()
        .insert("Sec-WebSocket-Protocol", HeaderValue::from_static("rad.terminal.v1"));
    let connected = tokio::time::timeout(Duration::from_secs(10), tokio_tungstenite::connect_async(request)).await;
    let Ok(Ok((ws, _))) = connected else {
        let _ = incoming.send(In::Closed).await;
        return;
    };
    let (mut sink, mut stream) = ws.split();
    loop {
        tokio::select! {
            msg = stream.next() => match msg {
                Some(Ok(Message::Binary(b))) => {
                    if incoming.send(In::Data(b.to_vec())).await.is_err() { break; }
                }
                Some(Ok(Message::Text(t))) => {
                    if let Ok(m) = serde_json::from_str::<TerminalMessage>(&t)
                        && incoming.send(In::Message(m)).await.is_err() { break; }
                }
                Some(Ok(Message::Close(_))) | None | Some(Err(_)) => {
                    let _ = incoming.send(In::Closed).await;
                    break;
                }
                Some(Ok(_)) => {}
            },
            out = outgoing.recv() => match out {
                Some(Out::Data(d)) => { if sink.send(Message::binary(d)).await.is_err() { let _ = incoming.send(In::Closed).await; break; } }
                Some(Out::Text(t)) => { if sink.send(Message::text(t)).await.is_err() { let _ = incoming.send(In::Closed).await; break; } }
                // The view let go: detached.
                None => break,
            },
        }
    }
    let _ = sink.close().await;
}

impl Render for TerminalView {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        self.font_size = crate::ui::fonts::code_size();
        self.fit(window, cx);
        let focused = self.focus.is_focused(window);
        let theme = ansi(gpui_kit::component::ActiveTheme::theme(&**cx).is_dark());
        let (_, lh) = self.cell_size(window);
        let lines = self.render_lines(&p, &theme, focused);
        let path = self
            .conn
            .read(cx)
            .session(&self.session_id)
            .map(|s| {
                s.workspace
                    .agent_path
                    .clone()
                    .unwrap_or_else(|| s.workspace.path.clone())
            })
            .unwrap_or_default();
        let bounds = self.bounds.clone();
        let open = self.state == State::Open;
        let this = cx.entity().downgrade();
        let banner: Option<(String, &'static str)> = match &self.state {
            State::Exited { here: true, .. } => Some(("The shell was closed.".into(), "Start New Shell")),
            State::Exited { code: 0, .. } => Some(("The shell exited.".into(), "Start New Shell")),
            State::Exited { code, .. } => Some((format!("The shell exited with code {code}."), "Start New Shell")),
            State::Gone => Some(("The shell has ended.".into(), "Start New Shell")),
            State::Failed(m) => Some((m.clone(), "Try Again")),
            _ => None,
        };
        let reconnecting = self.state == State::Reconnecting;
        let mut grid = v_flex()
            .px_2()
            .py_1()
            .text_size(px(self.font_size))
            .line_height(lh)
            .font_family(crate::ui::fonts::code());
        for l in lines {
            grid = grid.child(div().h(lh).whitespace_nowrap().child(l));
        }
        v_flex()
            .size_full()
            .bg(p.base)
            .child(
                h_flex()
                    .px_3()
                    .py_1()
                    .gap_2()
                    .border_b_1()
                    .border_color(p.surface0)
                    .bg(p.mantle)
                    .child(Icon::new(L::SquareTerminal).small().text_color(p.overlay))
                    .child(
                        div()
                            .flex_1()
                            .min_w_0()
                            .truncate()
                            .text_xs()
                            .text_color(p.subtext)
                            .child(path),
                    )
                    .child(
                        Button::new("terminal-menu")
                            .xsmall()
                            .ghost()
                            .icon(IconName::Ellipsis)
                            .dropdown_menu_with_anchor(Anchor::TopRight, move |menu, _, _| {
                                let w = this.clone();
                                menu.item(
                                    PopupMenuItem::new("Close Shell")
                                        .icon(Icon::new(L::OctagonX))
                                        .disabled(!open)
                                        .on_click(move |_, _, cx| {
                                            let _ = w.update(cx, |t, cx| t.close_shell(cx));
                                        }),
                                )
                            }),
                    )
                    .child(
                        Button::new("terminal-hide")
                            .xsmall()
                            .ghost()
                            .icon(IconName::Close)
                            .tooltip("Hide the terminal (⌘J). The shell keeps running.")
                            .on_click(|_: &ClickEvent, window, cx| {
                                window.dispatch_action(Box::new(crate::ToggleTerminal), cx);
                            }),
                    ),
            )
            .child(
                div()
                    .id("terminal-grid")
                    .relative()
                    .flex_1()
                    .min_h_0()
                    .overflow_hidden()
                    .track_focus(&self.focus)
                    .key_context("Terminal")
                    .cursor_text()
                    .on_key_down(cx.listener(|this, ev: &KeyDownEvent, window, cx| this.key(ev, window, cx)))
                    .on_mouse_down(
                        MouseButton::Left,
                        cx.listener(|this, ev, window, cx| this.mouse_down(ev, window, cx)),
                    )
                    .on_mouse_move(cx.listener(|this, ev, window, cx| this.mouse_move(ev, window, cx)))
                    .on_mouse_up(
                        MouseButton::Left,
                        cx.listener(|this, ev, window, cx| this.mouse_up(ev, window, cx)),
                    )
                    .on_scroll(cx.listener(|this, ev, window, cx| this.scroll(ev, window, cx)))
                    .child(grid)
                    .child(
                        canvas(
                            move |b, window, _| {
                                if bounds.get() != Some(b) {
                                    bounds.set(Some(b));
                                    window.refresh();
                                }
                            },
                            |_, _, _, _| {},
                        )
                        // Pinned to the corner: an absolute element without
                        // an inset sits where the flow would put it, under
                        // the grid, and every click landed rows too high.
                        .absolute()
                        .top_0()
                        .left_0()
                        .size_full(),
                    )
                    .when(reconnecting, |d| {
                        d.child(
                            div().absolute().top_2().w_full().flex().justify_center().child(
                                div()
                                    .px_3()
                                    .py_1()
                                    .rounded_full()
                                    .bg(p.mantle)
                                    .border_1()
                                    .border_color(p.surface0)
                                    .child(caption("Reconnecting…", &p)),
                            ),
                        )
                    })
                    .when_some(banner, |d, (text, action)| {
                        d.child(
                            h_flex()
                                .absolute()
                                .bottom_0()
                                .w_full()
                                .px_3()
                                .py_2()
                                .gap_2()
                                .bg(p.mantle)
                                .border_t_1()
                                .border_color(p.surface0)
                                .child(div().flex_1().text_sm().child(text))
                                .child(
                                    Button::new("restart")
                                        .small()
                                        .primary()
                                        .label(action)
                                        .on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.restart(cx))),
                                ),
                        )
                    }),
            )
    }
}

#[allow(dead_code)]
fn unused(_: gpui_kit::Point<Pixels>) -> gpui_kit::Point<Pixels> {
    point(px(0.), px(0.))
}

/// Keys the kit binds above the terminal, which a shell needs: the root
/// view's Tab and Shift-Tab move the focus, and its ⌘C copies its own
/// selection. Bound to nothing in the terminal's context, they reach its
/// key handler, as GPUI runs a key's binding before any key-down listener.
pub fn bind_keys(cx: &mut gpui_kit::App) {
    use gpui_kit::{KeyBinding, NoAction};
    cx.bind_keys([
        KeyBinding::new("tab", NoAction, Some("Terminal")),
        KeyBinding::new("shift-tab", NoAction, Some("Terminal")),
        KeyBinding::new("cmd-c", NoAction, Some("Terminal")),
    ]);
}

/// The bytes a key sends, as xterm sends them; none for a key the shell
/// doesn't get. `app_cursor` is the application cursor mode, in which the
/// arrows, Home and End send SS3 rather than CSI.
pub fn key_bytes(key: &str, key_char: Option<&str>, m: gpui_kit::Modifiers, app_cursor: bool) -> Option<Vec<u8>> {
    // xterm's modifier parameter: 1, plus 1 for Shift, 2 for Option, 4 for
    // Control.
    let param = 1 + m.shift as u8 + 2 * m.alt as u8 + 4 * m.control as u8;
    // A cursor key: CSI 1;<mods> <c> with modifiers, else SS3 or CSI <c>.
    let cursor = |c: char| -> Vec<u8> {
        if param > 1 {
            format!("\x1b[1;{param}{c}").into_bytes()
        } else if app_cursor {
            format!("\x1bO{c}").into_bytes()
        } else {
            format!("\x1b[{c}").into_bytes()
        }
    };
    // A key that sends CSI <n> ~, with its modifiers.
    let tilde = |n: u8| -> Vec<u8> {
        if param > 1 {
            format!("\x1b[{n};{param}~").into_bytes()
        } else {
            format!("\x1b[{n}~").into_bytes()
        }
    };
    // F1–F4 are SS3 P–S, or CSI 1;<mods> P–S with modifiers.
    let pf = |c: char| -> Vec<u8> {
        if param > 1 {
            format!("\x1b[1;{param}{c}").into_bytes()
        } else {
            format!("\x1bO{c}").into_bytes()
        }
    };
    // ⌘ with an arrow: the key it stands for, with the other modifiers.
    if m.platform {
        let key = match key {
            "left" => "home",
            "right" => "end",
            "up" => "pageup",
            "down" => "pagedown",
            _ => return None,
        };
        let m = gpui_kit::Modifiers { platform: false, ..m };
        return key_bytes(key, None, m, app_cursor);
    }
    let bytes = match key {
        "enter" if m.alt => b"\x1b\r".to_vec(),
        "enter" => b"\r".to_vec(),
        "backspace" if m.alt => b"\x1b\x7f".to_vec(),
        "backspace" if m.control => b"\x08".to_vec(),
        "backspace" => b"\x7f".to_vec(),
        "tab" if m.shift => b"\x1b[Z".to_vec(),
        "tab" => b"\t".to_vec(),
        "escape" => b"\x1b".to_vec(),
        "space" if m.control => vec![0],
        // Option-← and → move by word, as in Terminal.app and iTerm.
        "left" if m.alt && !m.control && !m.shift => b"\x1bb".to_vec(),
        "right" if m.alt && !m.control && !m.shift => b"\x1bf".to_vec(),
        "up" => cursor('A'),
        "down" => cursor('B'),
        "right" => cursor('C'),
        "left" => cursor('D'),
        "home" => cursor('H'),
        "end" => cursor('F'),
        "insert" => tilde(2),
        "delete" => tilde(3),
        "pageup" => tilde(5),
        "pagedown" => tilde(6),
        "f1" => pf('P'),
        "f2" => pf('Q'),
        "f3" => pf('R'),
        "f4" => pf('S'),
        "f5" => tilde(15),
        "f6" => tilde(17),
        "f7" => tilde(18),
        "f8" => tilde(19),
        "f9" => tilde(20),
        "f10" => tilde(21),
        "f11" => tilde(23),
        "f12" => tilde(24),
        key if m.control && key.chars().count() == 1 => {
            let c = key.chars().next()?.to_ascii_lowercase();
            let byte = match c {
                'a'..='z' => (c as u8) & 0x1f,
                '@' | '2' => 0,
                '[' | '3' => 0x1b,
                '\\' | '4' => 0x1c,
                ']' | '5' => 0x1d,
                '^' | '6' => 0x1e,
                '/' | '_' | '-' | '7' => 0x1f,
                '8' => 0x7f,
                _ => return None,
            };
            if m.alt { vec![0x1b, byte] } else { vec![byte] }
        }
        _ => key_char.filter(|c| !c.is_empty())?.as_bytes().to_vec(),
    };
    Some(bytes)
}

#[cfg(test)]
mod tests {
    use super::key_bytes;
    use gpui_kit::Modifiers;

    fn mods(spec: &str) -> Modifiers {
        Modifiers {
            control: spec.contains('c'),
            alt: spec.contains('a'),
            shift: spec.contains('s'),
            ..Default::default()
        }
    }

    fn send(key: &str, spec: &str) -> Vec<u8> {
        key_bytes(key, None, mods(spec), false).unwrap_or_default()
    }

    #[test]
    fn editing_keys() {
        assert_eq!(send("tab", ""), b"\t");
        assert_eq!(send("tab", "s"), b"\x1b[Z");
        assert_eq!(send("enter", ""), b"\r");
        assert_eq!(send("enter", "a"), b"\x1b\r");
        assert_eq!(send("escape", ""), b"\x1b");
        assert_eq!(send("backspace", ""), b"\x7f");
        assert_eq!(send("backspace", "a"), b"\x1b\x7f");
        assert_eq!(send("delete", ""), b"\x1b[3~");
    }

    #[test]
    fn control_keys() {
        for (key, byte) in [("a", 1), ("c", 3), ("d", 4), ("r", 18), ("z", 26), ("l", 12), ("w", 23)] {
            assert_eq!(send(key, "c"), vec![byte], "ctrl-{key}");
        }
        assert_eq!(send("space", "c"), vec![0]);
        assert_eq!(send("[", "c"), vec![0x1b]);
        assert_eq!(send("/", "c"), vec![0x1f]);
        assert_eq!(send("x", "ca"), vec![0x1b, 0x18]);
    }

    #[test]
    fn cursor_keys() {
        assert_eq!(send("up", ""), b"\x1b[A");
        assert_eq!(key_bytes("up", None, mods(""), true).unwrap(), b"\x1bOA");
        assert_eq!(send("left", "a"), b"\x1bb");
        assert_eq!(send("right", "a"), b"\x1bf");
        assert_eq!(send("left", "c"), b"\x1b[1;5D");
        assert_eq!(send("right", "s"), b"\x1b[1;2C");
        assert_eq!(send("home", ""), b"\x1b[H");
        assert_eq!(send("end", ""), b"\x1b[F");
        assert_eq!(send("pageup", ""), b"\x1b[5~");
        assert_eq!(send("pagedown", "s"), b"\x1b[6;2~");
    }

    #[test]
    fn command_arrows_are_home_end_and_the_page_keys() {
        let cmd = |spec: &str| Modifiers {
            platform: true,
            ..mods(spec)
        };
        assert_eq!(key_bytes("left", None, cmd(""), false).unwrap(), b"\x1b[H");
        assert_eq!(key_bytes("right", None, cmd(""), false).unwrap(), b"\x1b[F");
        assert_eq!(key_bytes("left", None, cmd(""), true).unwrap(), b"\x1bOH");
        assert_eq!(key_bytes("up", None, cmd(""), false).unwrap(), b"\x1b[5~");
        assert_eq!(key_bytes("down", None, cmd(""), false).unwrap(), b"\x1b[6~");
        assert_eq!(key_bytes("right", None, cmd("s"), false).unwrap(), b"\x1b[1;2F");
        // ⌘ with anything else is the app's.
        assert_eq!(key_bytes("j", Some("j"), cmd(""), false), None);
        assert_eq!(key_bytes("tab", None, cmd(""), false), None);
    }

    #[test]
    fn function_keys() {
        assert_eq!(send("f1", ""), b"\x1bOP");
        assert_eq!(send("f4", ""), b"\x1bOS");
        assert_eq!(send("f5", ""), b"\x1b[15~");
        assert_eq!(send("f12", ""), b"\x1b[24~");
        assert_eq!(send("f1", "s"), b"\x1b[1;2P");
    }

    #[test]
    fn text_is_what_the_key_types() {
        assert_eq!(key_bytes("a", Some("a"), mods(""), false).unwrap(), b"a");
        assert_eq!(key_bytes("a", Some("A"), mods("s"), false).unwrap(), b"A");
        assert_eq!(key_bytes("space", Some(" "), mods(""), false).unwrap(), b" ");
        // Option with a letter types its character, as Terminal.app does.
        assert_eq!(key_bytes("e", Some("é"), mods("a"), false).unwrap(), "é".as_bytes());
        assert_eq!(key_bytes("shift", None, mods("s"), false), None);
    }
}
