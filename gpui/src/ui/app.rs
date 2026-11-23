//! The window: the sidebar of every server's sessions in the chosen layout
//! (Inbox, Projects or Columns), beside the open session, the new session
//! form, or pairing. It also carries the app's commands and notifications.

use std::collections::HashSet;
use std::rc::Rc;
use std::time::Duration;

use gpui_kit::component::input::{InputEvent, InputState};
use gpui_kit::component::notification::Notification;
use gpui_kit::component::{ActiveTheme as _, Icon, IconName, Sizable as _, WindowExt as _, h_flex, v_flex};
use gpui_kit::{
    AnyElement, App, AppContext as _, Context, DragMoveEvent, Entity, FocusHandle, Focusable as _,
    InteractiveElement as _, IntoElement, ParentElement as _, Render, SharedString, Styled as _, Subscription, Task,
    Window, div,
};
use serde_json::json;

use crate::conn::{ConnEvent, ServerConn};
use crate::protocol::{Session, SessionStatus};
use crate::store::alert::SessionAlert;
use crate::store::dock::{Attention, Bounce, Bouncer, badge_count};
use crate::store::prefs::WindowFrame;
use crate::store::search::{Hit, merge_search_results, search_active};
use crate::store::servers::{SavedServer, ServerStore, custom_home, home_dir, pair, parse_pairing_link};
use crate::store::sidebar::{Collection, Layout, OpenedSession, stands_out};
use crate::store::ssh_install::{self, Target};
use crate::store::tokens::Tokens;
use crate::ui::common::{sidebar_toggle, top_bar};
use crate::ui::dock;
use crate::ui::new_session::{NewSessionEvent, NewSessionView};
use crate::ui::palette::{Palette, follow_appearance};
use crate::ui::panes::{self, PaneResize};
use crate::ui::session::SessionView;
use crate::ui::state::{AppPrefs, prefs, update_prefs};
use crate::ui::window_frame;
use crate::*;

pub(crate) enum Detail {
    Empty,
    Pair,
    Session(Entity<SessionView>),
    NewSession(Entity<NewSessionView>),
}

/// The session search across servers.
#[derive(Default)]
pub(crate) struct Search {
    pub query: String,
    pub generation: u64,
    pub hits: Vec<Hit>,
    pub searching: bool,
    pub failures: Vec<(String, String)>,
    pub more: bool,
    pub indexing: Vec<String>,
    pub unsupported: Vec<String>,
    pub answered: bool,
}

/// Install on a server over SSH, as it goes: the steps so far (the last one
/// running, or the one that failed), and why it failed.
#[derive(Default)]
pub(crate) struct SshInstall {
    pub steps: Vec<String>,
    pub running: bool,
    pub error: Option<String>,
}

pub struct AppView {
    /// The window's own focus, so that its actions (⌘N) work with nothing else focused.
    focus: FocusHandle,
    runtime: tokio::runtime::Handle,
    store: ServerStore,
    pub(crate) conns: Vec<Entity<ServerConn>>,
    pub(crate) detail: Detail,
    pub(crate) opened: OpenedSession,
    pub(crate) pair_input: Entity<InputState>,
    pub(crate) pair_error: Option<String>,
    pub(crate) pairing: bool,
    /// Install on a server over SSH: the host, and how it's going.
    pub(crate) ssh_input: Entity<InputState>,
    pub(crate) ssh_install: SshInstall,
    /// The same on this Mac: Pair This Mac.
    pub(crate) local_install: SshInstall,
    pub(crate) search_input: Entity<InputState>,
    pub(crate) search: Search,
    /// The Inbox's Archived section is open.
    pub(crate) archive_open: bool,
    /// Projects' Archived sections that are open, by server id.
    pub(crate) archives_open: HashSet<String>,
    /// The session to open once its server has it: from the last launch.
    pending_open: Option<(String, String)>,
    /// The window's frame since it last moved, until it's saved.
    moved_frame: Option<WindowFrame>,
    frame_save: Option<Task<()>>,
    /// The session list's width (the sidebar's, and in Columns the middle
    /// column's), and the Columns layout's first column's, as dragged;
    /// saved shortly after.
    pub(crate) sidebar_width: f32,
    pub(crate) collections_width: f32,
    pub(crate) widths_save: Option<Task<()>>,
    /// The Dock icon's bounce until the user comes back, and the badge it shows.
    bouncer: Bouncer,
    badge: Option<usize>,
    _subscriptions: Vec<Subscription>,
}

impl AppView {
    pub fn new(runtime: tokio::runtime::Handle, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let store = ServerStore::load(&home_dir(), Tokens::keychain(custom_home().as_deref()));
        let pair_input =
            cx.new(|cx| InputState::new(window, cx).placeholder("remoteagent://pair?… (from `rad pair --print-url`)"));
        let ssh_input = cx.new(|cx| InputState::new(window, cx).placeholder("user@host, or a Host from ~/.ssh/config"));
        let search_input = cx.new(|cx| InputState::new(window, cx).placeholder("Search sessions"));
        let mut subscriptions = vec![
            cx.subscribe_in(&ssh_input, window, |this, _, event, window, cx| {
                if let InputEvent::PressEnter { .. } = event {
                    this.install_over_ssh(window, cx);
                }
            }),
            cx.subscribe_in(&pair_input, window, |this, _, event, window, cx| {
                if let InputEvent::PressEnter { .. } = event {
                    this.pair(window, cx);
                }
            }),
            cx.subscribe_in(&search_input, window, |this, input, event: &InputEvent, _, cx| {
                if let InputEvent::Change = event {
                    let q = input.read(cx).value().to_string();
                    this.set_query(q, cx);
                }
            }),
            cx.observe_window_appearance(window, |_, window, cx| follow_appearance(window, cx)),
            cx.observe_window_activation(window, |this, window, cx| {
                if window.is_window_active() {
                    // macOS stops the Dock's bounce itself.
                    this.bouncer.activated();
                }
                // Back in front: the open session may have been read meanwhile.
                if window.is_window_active()
                    && let Detail::Session(view) = &this.detail
                {
                    view.update(cx, |v, cx| v.mark_read_if_shown(window, cx));
                }
            }),
            cx.observe_global::<AppPrefs>(|this, cx| {
                if Bounce::from_key(&prefs(cx).dock_bounce) != Bounce::UntilActive
                    && let Some(request) = this.bouncer.stop()
                {
                    dock::stop_bouncing(request);
                }
                this.update_dock(cx);
            }),
            // Saved half a second after it stops moving, not at every step of a drag.
            cx.observe_window_bounds(window, |this, window, cx| {
                this.moved_frame = Some(window_frame::current(window, cx));
                this.frame_save = Some(cx.spawn(async move |this, cx| {
                    cx.background_executor().timer(Duration::from_millis(500)).await;
                    this.update(cx, |this, cx| this.save_frame(cx)).ok();
                }));
            }),
            cx.on_app_quit(|this, cx| {
                this.save_frame(cx);
                async {}
            }),
        ];
        let focus = cx.focus_handle();
        focus.focus(window, cx);
        let pending_open = prefs(cx)
            .open_session
            .split_once(' ')
            .map(|(a, b)| (a.to_string(), b.to_string()));
        let (sidebar_width, collections_width) = panes::saved(cx);
        let mut this = Self {
            focus,
            runtime,
            store,
            conns: Vec::new(),
            detail: Detail::Empty,
            opened: OpenedSession::default(),
            pair_input,
            pair_error: None,
            pairing: false,
            ssh_input,
            ssh_install: SshInstall::default(),
            local_install: SshInstall::default(),
            search_input,
            search: Search::default(),
            archive_open: false,
            archives_open: HashSet::new(),
            pending_open,
            moved_frame: None,
            frame_save: None,
            sidebar_width,
            collections_width,
            widths_save: None,
            bouncer: Bouncer::default(),
            badge: None,
            _subscriptions: Vec::new(),
        };
        // The Dock draws the badge only with the user's leave, as for
        // notifications: ask for both before GPUI asks for notifications alone.
        if prefs(cx).notifications || prefs(cx).dock_badge {
            dock::request_authorization();
        }
        for saved in this.store.servers.clone() {
            subscriptions.extend(this.connect(&saved, window, cx));
        }
        if this.conns.is_empty() {
            this.detail = Detail::Pair;
        }
        this._subscriptions = subscriptions;
        // A pairing link on the command line pairs at once.
        if let Some(link) = std::env::args().skip(1).find(|a| a.starts_with("remoteagent://pair")) {
            this.pair_input
                .update(cx, |input, cx| input.set_value(link, window, cx));
            this.detail = Detail::Pair;
            this.pair(window, cx);
        }
        this
    }

    /// Focuses the window's own view, so that its commands reach it.
    fn save_frame(&mut self, cx: &mut Context<Self>) {
        self.frame_save = None;
        if let Some(frame) = self.moved_frame.take() {
            window_frame::save(frame, cx);
        }
    }

    pub fn focus_root(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self.focus.focus(window, cx);
    }

    pub fn layout(&self, cx: &App) -> Layout {
        Layout::from_key(&prefs(cx).sidebar_layout)
    }

    fn connect(&mut self, saved: &SavedServer, window: &mut Window, cx: &mut Context<Self>) -> Vec<Subscription> {
        let runtime = self.runtime.clone();
        let conn = cx.new(|cx| ServerConn::new(saved, &runtime, cx));
        let id = saved.id.clone();
        let subs = vec![
            cx.observe_in(&conn, window, |this, conn, window, cx| {
                this.conn_changed(&conn, window, cx)
            }),
            cx.subscribe_in(&conn, window, move |this, conn, event, window, cx| match event {
                ConnEvent::UrlWorked(url) => {
                    let _ = this.store.promote(&id, url);
                }
                ConnEvent::Alert { session, alert } => {
                    this.alert(conn.clone(), session.clone(), alert.clone(), window, cx)
                }
            }),
        ];
        self.conns.push(conn);
        subs
    }

    /// A server's state changed: the open session may be gone, the session
    /// from the last launch may be in, and a search may have servers anew.
    fn conn_changed(&mut self, conn: &Entity<ServerConn>, window: &mut Window, cx: &mut Context<Self>) {
        self.update_dock(cx);
        if let Detail::Session(view) = &self.detail {
            let v = view.read(cx);
            let gone = v.conn() == conn && conn.read(cx).stores.get(v.session_id()).is_some_and(|s| s.removed);
            if gone {
                self.close_detail(cx);
            }
        }
        if let Some((server, session)) = self.pending_open.clone() {
            let c = conn.read(cx);
            if c.server_id == server && c.index.sync.synced {
                self.pending_open = None;
                if c.index.sessions.contains_key(&session) && matches!(self.detail, Detail::Empty) {
                    self.open_by_ids(&server, &session, None, window, cx);
                }
            }
        }
        let searching = conn.read(cx).is_connected() && search_active(&self.search.query) && !self.search.answered;
        if searching {
            self.run_search(cx);
        }
        self.update_title(window, cx);
        cx.notify();
    }

    fn update_title(&self, window: &mut Window, cx: &App) {
        let title = match &self.detail {
            Detail::Session(v) => v
                .read(cx)
                .conn()
                .read(cx)
                .session(v.read(cx).session_id())
                .map(|s| {
                    if s.title.is_empty() {
                        "Session".to_string()
                    } else {
                        s.title.clone()
                    }
                })
                .unwrap_or_else(|| "Remote Agent".into()),
            _ => "Remote Agent".into(),
        };
        window.set_window_title(&title);
    }

    // MARK: Pairing and servers

    pub fn show_pairing(&mut self, cx: &mut Context<Self>) {
        self.close_detail(cx);
        self.detail = Detail::Pair;
        cx.notify();
    }

    pub(crate) fn pair(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.pairing {
            return;
        }
        let text = self.pair_input.read(cx).value().to_string();
        let Some(link) = parse_pairing_link(&text) else {
            self.pair_error =
                Some("That isn't a pairing link. Run `rad pair --print-url` on the server and paste its link.".into());
            cx.notify();
            return;
        };
        self.pairing = true;
        self.pair_error = None;
        cx.notify();
        let task = cx.background_spawn(async move { pair(&link) });
        cx.spawn_in(window, async move |this, cx| {
            let result = task.await;
            let _ = this.update_in(cx, |this, window, cx| {
                this.pairing = false;
                match result {
                    Ok(saved) => match this.add_server(saved, window, cx) {
                        Ok(()) => this.pair_input.update(cx, |input, cx| input.set_value("", window, cx)),
                        Err(e) => this.pair_error = Some(e),
                    },
                    Err(e) => this.pair_error = Some(e.to_string()),
                }
                cx.notify();
            });
        })
        .detach();
    }

    /// Saves a server just paired and connects to it, in place of its old
    /// connection when it was paired before.
    fn add_server(&mut self, saved: SavedServer, window: &mut Window, cx: &mut Context<Self>) -> Result<(), String> {
        self.store
            .save(saved.clone())
            .map_err(|e| format!("Couldn't save the server: {e}"))?;
        if let Some(ix) = self.conns.iter().position(|c| c.read(cx).server_id == saved.id) {
            self.conns.remove(ix);
        }
        let subs = self.connect(&saved, window, cx);
        self._subscriptions.extend(subs);
        self.detail = Detail::Empty;
        Ok(())
    }

    /// Installs rad on the host in `ssh_input` if it has none, starts it,
    /// and pairs with it (`store::ssh_install`), listing the steps as they go.
    pub(crate) fn install_over_ssh(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let host = self.ssh_input.read(cx).value().trim().to_string();
        if !host.is_empty() {
            self.install(Target::Ssh(host), window, cx);
        }
    }

    /// The same on this Mac: no link to paste, no ssh.
    pub(crate) fn pair_this_mac(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if !matches!(self.detail, Detail::Pair) {
            self.show_pairing(cx);
        }
        self.install(Target::Local, window, cx);
    }

    fn install_state(&mut self, target: &Target) -> &mut SshInstall {
        match target {
            Target::Ssh(_) => &mut self.ssh_install,
            Target::Local => &mut self.local_install,
        }
    }

    fn install(&mut self, target: Target, window: &mut Window, cx: &mut Context<Self>) {
        let state = self.install_state(&target);
        if state.running {
            return;
        }
        *state = SshInstall {
            running: true,
            ..Default::default()
        };
        cx.notify();
        let (tx, rx) = async_channel::unbounded::<String>();
        let t = target.clone();
        let task = cx.background_spawn(async move {
            ssh_install::install_and_pair(&t, &|step| {
                let _ = tx.send_blocking(step);
            })
        });
        cx.spawn_in(window, async move |this, cx| {
            while let Ok(step) = rx.recv().await {
                let _ = this.update(cx, |this, cx| {
                    this.install_state(&target).steps.push(step);
                    cx.notify();
                });
            }
            let result = task.await;
            let _ = this.update_in(cx, |this, window, cx| {
                this.install_state(&target).running = false;
                match result.and_then(|saved| this.add_server(saved, window, cx)) {
                    Ok(()) => {
                        *this.install_state(&target) = SshInstall::default();
                        if let Target::Ssh(_) = target {
                            this.ssh_input.update(cx, |input, cx| input.set_value("", window, cx));
                        }
                    }
                    Err(e) => this.install_state(&target).error = Some(e),
                }
                cx.notify();
            });
        })
        .detach();
    }

    pub fn remove_server_id(&mut self, id: &str, cx: &mut Context<Self>) {
        let Some(ix) = self.conns.iter().position(|c| c.read(cx).server_id == id) else {
            return;
        };
        let conn = self.conns[ix].clone();
        // Unpair on rad too, where it's reachable; forget it here either way.
        drop(
            conn.read(cx)
                .call_with_timeout("device.unpair", json!({}), Duration::from_secs(5)),
        );
        if let Detail::Session(view) = &self.detail
            && view.read(cx).conn() == &conn
        {
            self.close_detail(cx);
        }
        self.conns.remove(ix);
        let _ = self.store.remove(id);
        self.update_dock(cx);
        if self.conns.is_empty() {
            self.detail = Detail::Pair;
        }
        cx.notify();
    }

    // MARK: Opening

    pub(crate) fn close_detail(&mut self, cx: &mut Context<Self>) {
        if let Detail::Session(view) = &self.detail {
            view.update(cx, |v, cx| v.close(cx));
        }
        self.detail = Detail::Empty;
        self.opened = OpenedSession::default();
    }

    pub fn open_session(&mut self, conn: usize, session_id: &str, window: &mut Window, cx: &mut Context<Self>) {
        self.open_session_at(conn, session_id, None, window, cx);
    }

    pub fn open_session_at(
        &mut self,
        conn: usize,
        session_id: &str,
        item: Option<String>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let Some(conn) = self.conns.get(conn).cloned() else {
            return;
        };
        if let Detail::Session(view) = &self.detail {
            let v = view.read(cx);
            if v.conn() == &conn && v.session_id() == session_id && item.is_none() {
                return;
            }
        }
        self.close_detail(cx);
        let session = conn.read(cx).session(session_id);
        let stood_out = session.as_ref().is_some_and(|s| stands_out(s));
        self.opened = OpenedSession {
            id: session_id.to_string(),
            stood_out,
        };
        // An archived session shows its section; in Columns, its collection.
        if session.as_ref().is_some_and(|s| s.archived) {
            self.archive_open = true;
            self.archives_open.insert(conn.read(cx).server_id.clone());
        }
        if self.layout(cx) == Layout::Columns
            && let Some(s) = &session
        {
            let (server, column) = self.column(cx);
            let inside = server.as_ref() == Some(&conn.read(cx).server_id)
                && column.sessions(&conn.read(cx).index).iter().any(|x| x.id == s.id)
                || column == Collection::All && !s.archived
                || column == Collection::NeedsYou && crate::store::sidebar::needs_you(s)
                || column == Collection::Running && crate::store::sidebar::is_busy(s);
            if !inside {
                let reveal = Collection::revealing(s, &conn.read(cx).index);
                let key = format!("{} {}", conn.read(cx).server_id, reveal.key());
                update_prefs(cx, |p| p.column = key);
            }
        }
        let key = format!("{} {}", conn.read(cx).server_id, session_id);
        update_prefs(cx, |p| {
            p.open_session = key.clone();
            p.recent_sessions.retain(|r| *r != key);
            p.recent_sessions.insert(0, key.clone());
            p.recent_sessions.truncate(100);
        });
        let view = cx.new(|cx| SessionView::new(conn, session_id.to_string(), item, window, cx));
        self._subscriptions
            .push(cx.observe_in(&view, window, |this, _, window, cx| this.update_title(window, cx)));
        self.detail = Detail::Session(view);
        // Its notification has been seen.
        window.remove_notification1::<SessionAlertTag>(SharedString::from(session_id.to_string()), cx);
        self.update_title(window, cx);
        cx.notify();
    }

    /// Opens a session by its server's id, at an item if given.
    pub fn open_by_ids(
        &mut self,
        server_id: &str,
        session_id: &str,
        item: Option<String>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if let Some(ix) = self.conns.iter().position(|c| c.read(cx).server_id == server_id) {
            self.open_session_at(ix, session_id, item, window, cx);
        }
    }

    /// The server a new session goes to: the open session's, else the
    /// Columns collection's, else the first connected.
    pub(crate) fn target(&self, cx: &App) -> Option<usize> {
        let connected = |i: &usize| self.conns.get(*i).is_some_and(|c| c.read(cx).is_connected());
        if let Detail::Session(v) = &self.detail
            && let Some(i) = self.conns.iter().position(|c| c == v.read(cx).conn())
            && connected(&i)
        {
            return Some(i);
        }
        if self.layout(cx) == Layout::Columns
            && let (Some(server), _) = self.column(cx)
            && let Some(i) = self.conns.iter().position(|c| c.read(cx).server_id == server)
            && connected(&i)
        {
            return Some(i);
        }
        (0..self.conns.len()).find(connected)
    }

    pub fn new_session(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let project = match (self.layout(cx), self.column(cx)) {
            (Layout::Columns, (_, Collection::Project(p))) => Some(p),
            _ => None,
        };
        if let Some(ix) = self.target(cx) {
            self.new_session_on(ix, project, window, cx);
        }
    }

    pub fn new_session_on(
        &mut self,
        server: usize,
        project: Option<String>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let connected: Vec<Entity<ServerConn>> = self
            .conns
            .iter()
            .filter(|c| c.read(cx).is_connected())
            .cloned()
            .collect();
        let Some(target) = self.conns.get(server).cloned() else {
            return;
        };
        let Some(ix) = connected.iter().position(|c| *c == target) else {
            return;
        };
        // Cancel goes back to the session that was open.
        let before = match &self.detail {
            Detail::Session(v) => {
                let v = v.read(cx);
                Some((v.conn().read(cx).server_id.clone(), v.session_id().to_string()))
            }
            _ => None,
        };
        self.close_detail(cx);
        let view = cx.new(|cx| NewSessionView::new(connected, ix, project, window, cx));
        let sub = cx.subscribe_in(&view, window, move |this, _, event, window, cx| match event {
            NewSessionEvent::Created {
                server_id, session_id, ..
            } => {
                this.open_by_ids(server_id, session_id, None, window, cx);
            }
            NewSessionEvent::Cancelled => {
                this.detail = Detail::Empty;
                if let Some((server, session)) = &before {
                    this.open_by_ids(server, session, None, window, cx);
                }
                cx.notify();
            }
        });
        self._subscriptions.push(sub);
        self.detail = Detail::NewSession(view);
        cx.notify();
    }

    // MARK: Columns

    /// The Columns layout's collection, and its server for a project.
    pub(crate) fn column(&self, cx: &App) -> (Option<String>, Collection) {
        let stored = prefs(cx).column.clone();
        if let Some((server, rest)) = stored.split_once(' ')
            && let Some(c) = Collection::parse(rest)
        {
            // A forgotten project falls back to all.
            if let Collection::Project(p) = &c {
                let known = self
                    .conns
                    .iter()
                    .any(|conn| conn.read(cx).server_id == server && conn.read(cx).index.projects.contains_key(p));
                if !known {
                    return (None, Collection::All);
                }
            }
            return (Some(server.to_string()), c);
        }
        (None, Collection::All)
    }

    /// Chooses a collection, and opens the session opened there last.
    pub(crate) fn choose_column(
        &mut self,
        server: Option<String>,
        c: Collection,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let key = format!("{} {}", server.clone().unwrap_or_else(|| "-".into()), c.key());
        update_prefs(cx, |p| p.column = key);
        let sessions = self.column_sessions(cx);
        let recent = prefs(cx).recent_sessions.clone();
        let pick = recent
            .iter()
            .find_map(|r| {
                sessions
                    .iter()
                    .find(|row| format!("{} {}", row.server_id, row.session.id) == *r)
            })
            .or(sessions.first())
            .cloned();
        if let Some(row) = pick {
            self.open_session(row.conn, &row.session.id, window, cx);
        }
        cx.notify();
    }

    // MARK: Search

    fn set_query(&mut self, query: String, cx: &mut Context<Self>) {
        let same = query.trim() == self.search.query.trim();
        self.search.query = query;
        if !same {
            self.search.answered = false;
            self.run_search(cx);
        }
    }

    pub(crate) fn rerun_search(&mut self, cx: &mut Context<Self>) {
        self.search.answered = false;
        self.run_search(cx);
    }

    fn run_search(&mut self, cx: &mut Context<Self>) {
        self.search.generation += 1;
        let generation = self.search.generation;
        if !search_active(&self.search.query) {
            let q = std::mem::take(&mut self.search.query);
            self.search = Search {
                query: q,
                generation,
                ..Default::default()
            };
            cx.notify();
            return;
        }
        self.search.searching = true;
        cx.notify();
        cx.spawn(async move |this, cx| {
            cx.background_executor().timer(Duration::from_millis(250)).await;
            let Ok(calls) = this.update(cx, |this, cx| {
                if this.search.generation != generation {
                    return Vec::new();
                }
                let query = this.search.query.trim().to_string();
                let archived = prefs(cx).search_archived;
                let mut unsupported = Vec::new();
                let mut calls = Vec::new();
                for conn in &this.conns {
                    let c = conn.read(cx);
                    if !c.is_connected() {
                        continue;
                    }
                    if !c.info.as_ref().is_some_and(|i| i.search) {
                        unsupported.push(c.name.clone());
                        continue;
                    }
                    calls.push((
                        c.server_id.clone(),
                        c.name.clone(),
                        c.call_with_timeout(
                            "session.search",
                            json!({"query": query, "archived": archived, "limit": 30}),
                            Duration::from_secs(15),
                        ),
                    ));
                }
                this.search.unsupported = unsupported;
                if calls.is_empty() {
                    this.search.hits.clear();
                    this.search.searching = false;
                    this.search.answered = true;
                    cx.notify();
                }
                calls
            }) else {
                return;
            };
            let n = calls.len();
            let mut lists: Vec<Vec<Hit>> = vec![Vec::new(); n];
            let mut failures: Vec<Option<(String, String)>> = vec![None; n];
            let mut indexing: Vec<Option<String>> = vec![None; n];
            let mut more = false;
            for (i, (server, name, call)) in calls.into_iter().enumerate() {
                match call.await {
                    Ok(v) => {
                        let r: crate::protocol::SearchResults = serde_json::from_value(v).unwrap_or_default();
                        more |= r.more;
                        if r.indexing {
                            indexing[i] = Some(name.clone());
                        }
                        lists[i] = r
                            .results
                            .into_iter()
                            .filter(|x| x.session.is_some())
                            .map(|x| Hit {
                                server_id: server.clone(),
                                result: x,
                            })
                            .collect();
                    }
                    Err(e) => failures[i] = Some((name.clone(), e.to_string())),
                }
                let pending = i + 1 < n;
                let snapshot = (lists.clone(), failures.clone(), indexing.clone(), more);
                let alive = this.update(cx, |this, cx| {
                    if this.search.generation != generation {
                        return false;
                    }
                    this.search.hits = merge_search_results(&snapshot.0);
                    this.search.failures = snapshot.1.iter().flatten().cloned().collect();
                    this.search.indexing = snapshot.2.iter().flatten().cloned().collect();
                    this.search.more = snapshot.3;
                    this.search.searching = pending;
                    this.search.answered = !pending;
                    cx.notify();
                    true
                });
                if !matches!(alive, Ok(true)) {
                    return;
                }
            }
        })
        .detach();
    }

    pub(crate) fn focus_search(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self.search_input.focus_handle(cx).focus(window, cx);
    }

    pub(crate) fn clear_search(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self.search_input.update(cx, |i, cx| i.set_value("", window, cx));
        self.set_query(String::new(), cx);
    }

    // MARK: Notifications

    fn alert(
        &mut self,
        conn: Entity<ServerConn>,
        session: Rc<Session>,
        alert: SessionAlert,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if !prefs(cx).notifications && Bounce::from_key(&prefs(cx).dock_bounce) == Bounce::Off {
            return;
        }
        // A turn that ended may start the next queued prompt at once.
        if alert == SessionAlert::Finished {
            let id = session.id.clone();
            cx.spawn_in(window, async move |this, cx| {
                cx.background_executor().timer(Duration::from_secs(1)).await;
                let _ = this.update_in(cx, |this, window, cx| {
                    if let Some(s) = conn.read(cx).index.sessions.get(&id).cloned()
                        && s.status == SessionStatus::Idle
                    {
                        this.alert_user(conn.clone(), s, SessionAlert::Finished, window, cx);
                    }
                });
            })
            .detach();
        } else {
            self.alert_user(conn, session, alert, window, cx);
        }
    }

    /// Bounces the Dock icon, which macOS does only while another app is in
    /// front, and posts the notification.
    fn alert_user(
        &mut self,
        conn: Entity<ServerConn>,
        session: Rc<Session>,
        alert: SessionAlert,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let route = format!("{} {}", conn.read(cx).server_id, session.id);
        let mode = Bounce::from_key(&prefs(cx).dock_bounce);
        match self.bouncer.alert(mode, &alert, &route, dock::app_active()) {
            Attention::None => {}
            Attention::Once => dock::bounce_once(),
            Attention::UntilActive => self.bouncer.started(dock::bounce_until_active()),
        }
        if prefs(cx).notifications {
            self.post(conn, session, alert, window, cx);
        }
    }

    /// The Dock icon's badge, and its bounce, which stops once none of its
    /// sessions needs the user, such as when they answered on another device.
    fn update_dock(&mut self, cx: &mut Context<Self>) {
        let count = if prefs(cx).dock_badge {
            self.conns
                .iter()
                .map(|c| badge_count(c.read(cx).index.sessions.values().map(|s| &**s)))
                .sum()
        } else {
            0
        };
        if self.badge != Some(count) {
            self.badge = Some(count);
            dock::set_badge(count);
        }
        let resolved: Vec<String> = self
            .bouncer
            .routes()
            .filter(|route| !self.needs_you(route, cx))
            .cloned()
            .collect();
        for route in resolved {
            if let Some(request) = self.bouncer.resolved(&route) {
                dock::stop_bouncing(request);
            }
        }
    }

    /// Whether the session "<serverId> <sessionId>" waits for an answer or failed.
    fn needs_you(&self, route: &str, cx: &App) -> bool {
        let Some((server, id)) = route.split_once(' ') else {
            return false;
        };
        self.conns
            .iter()
            .map(|c| c.read(cx))
            .find(|c| c.server_id == server)
            .and_then(|c| c.index.sessions.get(id))
            .is_some_and(|s| !s.archived && crate::store::sidebar::needs_you(s))
    }

    fn post(
        &mut self,
        conn: Entity<ServerConn>,
        s: Rc<Session>,
        alert: SessionAlert,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let active = window.is_window_active();
        let shown = matches!(&self.detail, Detail::Session(v) if v.read(cx).session_id() == s.id);
        if active && shown {
            return;
        }
        let c = conn.read(cx);
        let agent = c
            .harness(&s.harness)
            .map(|h| h.name.clone())
            .unwrap_or_else(|| s.harness.clone());
        let body = match &alert {
            SessionAlert::NeedsYou => format!("{agent} needs you."),
            SessionAlert::Finished => format!("{agent} finished."),
            SessionAlert::Failed(Some(e)) => format!("{agent} failed: {e}"),
            SessionAlert::Failed(None) => format!("{agent} failed."),
        };
        let project = c
            .index
            .projects
            .get(&s.project_id)
            .map(|p| p.name.clone())
            .unwrap_or_default();
        let place = if self.conns.len() > 1 {
            if project.is_empty() {
                c.name.clone()
            } else {
                format!("{project} on {}", c.name)
            }
        } else {
            project
        };
        let title = if s.title.is_empty() {
            "Session".to_string()
        } else {
            s.title.clone()
        };
        let (server, id) = (c.server_id.clone(), s.id.clone());
        let this = cx.entity().downgrade();
        let mut note = Notification::new()
            .id1::<SessionAlertTag>(SharedString::from(s.id.clone()))
            .title(title)
            .message(if place.is_empty() {
                body
            } else {
                format!("{place} — {body}")
            })
            .on_click(move |_, window, cx| {
                cx.activate(true);
                let _ = this.update(cx, |a, cx| a.open_by_ids(&server, &id, None, window, cx));
            });
        if !active {
            note = note.system();
        }
        window.push_notification(note, cx);
    }

    // MARK: Commands

    fn with_session(&mut self, cx: &mut Context<Self>, f: impl FnOnce(&mut SessionView, &mut Context<SessionView>)) {
        if let Detail::Session(v) = &self.detail {
            v.update(cx, f);
        }
    }

    fn session_view(&self) -> Option<Entity<SessionView>> {
        match &self.detail {
            Detail::Session(v) => Some(v.clone()),
            _ => None,
        }
    }

    pub fn open_settings(&mut self, server: Option<usize>, window: &mut Window, cx: &mut Context<Self>) {
        crate::ui::settings::open(self.conns.clone(), server, window, cx);
    }

    /// Archives the sessions with nothing new for a week, after asking.
    pub(crate) fn archive_old(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let old = self.old_sessions(cx);
        if old.is_empty() {
            return;
        }
        let n = old.len();
        crate::ui::dialogs::confirm(
            window,
            cx,
            &if n == 1 {
                "Archive 1 session?".to_string()
            } else {
                format!("Archive {n} sessions?")
            },
            "Sessions on every connected server with nothing new for 7 days. Their worktrees are kept.",
            vec![crate::ui::dialogs::Choice::new(
                "Archive",
                gpui_kit::component::button::ButtonVariant::Primary,
                move |_, cx| {
                    for (conn, id) in &old {
                        drop(
                            conn.read(cx)
                                .call("session.archive", json!({"sessionId": id, "removeWorktree": false})),
                        );
                    }
                },
            )],
        );
    }

    pub(crate) fn old_sessions(&self, cx: &App) -> Vec<(Entity<ServerConn>, String)> {
        let week = chrono::Utc::now() - chrono::Duration::days(7);
        let mut out = Vec::new();
        for conn in &self.conns {
            let c = conn.read(cx);
            if !c.is_connected() {
                continue;
            }
            for s in c.index.active_sessions() {
                let old = chrono::DateTime::parse_from_rfc3339(&s.updated_at).is_ok_and(|t| t < week);
                let busy = crate::store::sidebar::is_busy(&s) || s.status == SessionStatus::AwaitingApproval;
                if old && !busy && s.draft.is_none() {
                    out.push((conn.clone(), s.id.clone()));
                }
            }
        }
        out
    }
}

/// Tells the notifications of sessions apart.
pub struct SessionAlertTag;

impl Render for AppView {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let layout = self.layout(cx);
        let detail: AnyElement = match &self.detail {
            Detail::Empty => v_flex()
                .size_full()
                .items_center()
                .justify_center()
                .gap_2()
                .text_color(p.overlay)
                .child(Icon::new(IconName::Inbox).large().text_color(p.overlay))
                .child(div().text_sm().child("No session selected"))
                .child(
                    div()
                        .text_xs()
                        .child("Choose a session in the sidebar, or start a new one with ⌘N."),
                )
                .into_any_element(),
            Detail::Pair => self.render_pair(cx).into_any_element(),
            Detail::Session(view) => view.clone().into_any_element(),
            Detail::NewSession(view) => view.clone().into_any_element(),
        };
        // A session and a new one have bars of their own; the others get a
        // bare one, to move the window by.
        let hidden = prefs(cx).sidebar_hidden;
        let detail = match &self.detail {
            Detail::Session(_) | Detail::NewSession(_) => detail,
            _ => v_flex()
                .size_full()
                .child(top_bar(
                    "detail-bar",
                    hidden,
                    p.base,
                    None,
                    window,
                    h_flex().when(hidden, |d| d.child(sidebar_toggle(true))),
                ))
                .child(div().flex_1().min_h_0().child(detail))
                .into_any_element(),
        };
        let sidebar = (!hidden).then(|| self.render_sidebar(window, cx));
        let middle = (!hidden && layout == Layout::Columns).then(|| self.render_middle(window, cx));
        v_flex()
            .size_full()
            .track_focus(&self.focus)
            .key_context("Workspace")
            .on_action(cx.listener(|this, _: &NewSession, window, cx| this.new_session(window, cx)))
            .on_action(cx.listener(|this, _: &PairServer, _, cx| this.show_pairing(cx)))
            .on_action(cx.listener(|this, _: &PairThisMac, window, cx| this.pair_this_mac(window, cx)))
            .on_action(cx.listener(|this, _: &OpenSettings, window, cx| this.open_settings(None, window, cx)))
            .on_action(cx.listener(|_, _: &CheckForUpdate, window, cx| {
                if crate::ui::sparkle::available() {
                    crate::ui::sparkle::check();
                } else {
                    crate::ui::build_update::check(window, cx);
                }
            }))
            .on_action(cx.listener(|this, _: &SearchSessions, window, cx| this.focus_search(window, cx)))
            .on_action(cx.listener(|this, _: &FindInSession, window, cx| {
                if let Some(v) = this.session_view() {
                    v.update(cx, |v, cx| v.open_find(window, cx));
                }
            }))
            .on_action(cx.listener(|this, _: &FindNext, _, cx| this.with_session(cx, |v, cx| v.find_step(true, cx))))
            .on_action(
                cx.listener(|this, _: &FindPrevious, _, cx| this.with_session(cx, |v, cx| v.find_step(false, cx))),
            )
            .on_action(cx.listener(|this, _: &RenameSession, window, cx| {
                if let Some(v) = this.session_view() {
                    v.update(cx, |v, cx| {
                        if v.can_rename(cx) {
                            v.rename(window, cx)
                        }
                    });
                }
            }))
            .on_action(cx.listener(|this, _: &ShowChanges, window, cx| {
                if let Some(v) = this.session_view() {
                    v.update(cx, |v, cx| v.open_changes(window, cx));
                }
            }))
            .on_action(cx.listener(|this, _: &CommitSession, window, cx| {
                if let Some(v) = this.session_view() {
                    v.update(cx, |v, cx| v.open_commit(window, cx));
                }
            }))
            .on_action(cx.listener(|this, _: &ForkSession, window, cx| {
                if let Some(v) = this.session_view() {
                    v.update(cx, |v, cx| v.open_fork(window, cx));
                }
            }))
            .on_action(cx.listener(|this, _: &ToggleTerminal, window, cx| {
                if let Some(v) = this.session_view() {
                    v.update(cx, |v, cx| v.toggle_terminal(window, cx));
                }
            }))
            .on_action(cx.listener(|this, _: &StopAgent, _, cx| this.with_session(cx, |v, cx| v.interrupt(false, cx))))
            .on_action(cx.listener(|this, _: &SaveDraft, window, cx| match &this.detail {
                Detail::Session(v) => v.update(cx, |v, cx| v.save_draft(window, cx)),
                Detail::NewSession(v) => v.update(cx, |v, cx| v.save_draft(window, cx)),
                _ => {}
            }))
            .on_action(cx.listener(|_, _: &LayoutInbox, _, cx| update_prefs(cx, |p| p.sidebar_layout = "inbox".into())))
            .on_action(
                cx.listener(|_, _: &LayoutProjects, _, cx| update_prefs(cx, |p| p.sidebar_layout = "projects".into())),
            )
            .on_action(
                cx.listener(|_, _: &LayoutColumns, _, cx| update_prefs(cx, |p| p.sidebar_layout = "columns".into())),
            )
            .on_action(cx.listener(|_, _: &LayoutAllProjects, _, cx| {
                update_prefs(cx, |p| p.sidebar_layout = Layout::AllProjects.key().into())
            }))
            .on_action(
                cx.listener(|_, _: &ToggleSidebar, _, cx| update_prefs(cx, |p| p.sidebar_hidden = !p.sidebar_hidden)),
            )
            .bg(p.base)
            .text_color(p.text)
            .font_family(cx.theme().font_family.clone())
            .child(
                h_flex()
                    .flex_1()
                    .min_h_0()
                    .items_start()
                    .on_drag_move(
                        cx.listener(|this, e: &DragMoveEvent<PaneResize>, window, cx| this.drag_pane(e, window, cx)),
                    )
                    .when_some(sidebar, |d, s| d.child(s))
                    .when_some(middle, |d, m| d.child(m))
                    .child(div().flex_1().min_w_0().h_full().child(detail)),
            )
    }
}

use gpui_kit::prelude::FluentBuilder as _;

#[allow(dead_code)]
fn unused(_: Icon) -> Option<Icon> {
    Some(Icon::new(IconName::Inbox).small())
}
