//! The window: the sidebar of every server's sessions in the Inbox layout
//! (what needs you first, then the rest), beside the open session, the new
//! session form, or pairing.

use std::rc::Rc;

use chrono::Utc;
use gpui_kit::component::button::{Button, ButtonVariants as _};
use gpui_kit::component::input::{Input, InputEvent, InputState};
use gpui_kit::component::{ActiveTheme as _, Disableable as _, Icon, IconName, Sizable as _, TitleBar, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    AnyElement, App, AppContext as _, ClickEvent, Context, Entity, FocusHandle, FontWeight, InteractiveElement as _,
    IntoElement, ParentElement as _, Render, SharedString, StatefulInteractiveElement as _, Styled as _, Subscription,
    Window, div, px,
};

use crate::NewSession;
use crate::conn::{ConnEvent, ConnState, ServerConn};
use crate::protocol::{Session, SessionStatus};
use crate::store::servers::{ServerStore, custom_home, home_dir, pair, parse_pairing_link};
use crate::store::sidebar::{OpenedSession, inbox_groups, is_busy, short_age_of, stands_out};
use crate::store::tokens::Tokens;
use crate::ui::new_session::{NewSessionEvent, NewSessionView};
use crate::ui::palette::{Palette, follow_appearance};
use crate::ui::session_view::SessionView;

const SIDEBAR_WIDTH: f32 = 290.;

enum Detail {
    Empty,
    Pair,
    Session(Entity<SessionView>),
    NewSession(Entity<NewSessionView>),
}

pub struct AppView {
    /// The window's own focus, so that its actions (⌘N) work with nothing else focused.
    focus: FocusHandle,
    runtime: tokio::runtime::Handle,
    store: ServerStore,
    conns: Vec<Entity<ServerConn>>,
    detail: Detail,
    opened: OpenedSession,
    pair_input: Entity<InputState>,
    pair_error: Option<String>,
    pairing: bool,
    _subscriptions: Vec<Subscription>,
}

/// A row of the sidebar: a session, and the connection it's on.
#[derive(Clone)]
struct SessionRow {
    conn: usize,
    session: Rc<Session>,
}

impl AsRef<Session> for SessionRow {
    fn as_ref(&self) -> &Session {
        &self.session
    }
}

impl AppView {
    pub fn new(runtime: tokio::runtime::Handle, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let store = ServerStore::load(&home_dir(), Tokens::keychain(custom_home().as_deref()));
        let pair_input =
            cx.new(|cx| InputState::new(window, cx).placeholder("remoteagent://pair?… (from `rad pair --print-url`)"));
        let mut subscriptions = vec![
            cx.subscribe_in(&pair_input, window, |this, _, event, window, cx| {
                if let InputEvent::PressEnter { .. } = event {
                    this.pair(window, cx);
                }
            }),
            cx.observe_window_appearance(window, |_, window, cx| follow_appearance(window, cx)),
            cx.observe_window_activation(window, |this, window, cx| {
                // Back in front: the open session may have been read meanwhile.
                if window.is_window_active()
                    && let Detail::Session(view) = &this.detail
                {
                    view.update(cx, |v, cx| v.mark_read_if_shown(window, cx));
                }
            }),
        ];
        let focus = cx.focus_handle();
        focus.focus(window, cx);
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
            _subscriptions: Vec::new(),
        };
        for saved in this.store.servers.clone() {
            subscriptions.extend(this.connect(&saved, cx));
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

    fn connect(&mut self, saved: &crate::store::servers::SavedServer, cx: &mut Context<Self>) -> Vec<Subscription> {
        let runtime = self.runtime.clone();
        let conn = cx.new(|cx| ServerConn::new(saved, &runtime, cx));
        let id = saved.id.clone();
        let subs = vec![
            cx.observe(&conn, |_, _, cx| cx.notify()),
            cx.subscribe(&conn, move |this, _, event, _| match event {
                ConnEvent::UrlWorked(url) => {
                    let _ = this.store.promote(&id, url);
                }
            }),
        ];
        self.conns.push(conn);
        subs
    }

    fn pair(&mut self, window: &mut Window, cx: &mut Context<Self>) {
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
                    Ok(saved) => {
                        if let Err(e) = this.store.save(saved.clone()) {
                            this.pair_error = Some(format!("Couldn't save the server: {e}"));
                        } else {
                            // Pairing again replaces the connection.
                            if let Some(ix) = this.conns.iter().position(|c| c.read(cx).server_id == saved.id) {
                                this.conns.remove(ix);
                            }
                            let subs = this.connect(&saved, cx);
                            this._subscriptions.extend(subs);
                            this.pair_input.update(cx, |input, cx| input.set_value("", window, cx));
                            this.detail = Detail::Empty;
                        }
                    }
                    Err(e) => this.pair_error = Some(e.to_string()),
                }
                cx.notify();
            });
        })
        .detach();
    }

    fn remove_server(&mut self, ix: usize, cx: &mut Context<Self>) {
        let Some(conn) = self.conns.get(ix).cloned() else {
            return;
        };
        let id = conn.read(cx).server_id.clone();
        // Unpair on rad too, where it's reachable; forget it here either way.
        drop(conn.read(cx).call("device.unpair", serde_json::json!({})));
        if let Detail::Session(view) = &self.detail
            && view.read(cx).conn() == &conn
        {
            self.close_detail(cx);
        }
        self.conns.remove(ix);
        let _ = self.store.remove(&id);
        if self.conns.is_empty() {
            self.detail = Detail::Pair;
        }
        cx.notify();
    }

    fn close_detail(&mut self, cx: &mut Context<Self>) {
        if let Detail::Session(view) = &self.detail {
            view.update(cx, |v, cx| v.close(cx));
        }
        self.detail = Detail::Empty;
        self.opened = OpenedSession::default();
    }

    pub fn open_session(&mut self, conn: usize, session_id: &str, window: &mut Window, cx: &mut Context<Self>) {
        let Some(conn) = self.conns.get(conn).cloned() else {
            return;
        };
        if let Detail::Session(view) = &self.detail {
            let v = view.read(cx);
            if v.conn() == &conn && v.session_id() == session_id {
                return;
            }
        }
        self.close_detail(cx);
        let stood_out = conn.read(cx).session(session_id).is_some_and(|s| stands_out(&s));
        self.opened = OpenedSession {
            id: session_id.to_string(),
            stood_out,
        };
        let view = cx.new(|cx| SessionView::new(conn, session_id.to_string(), window, cx));
        self.detail = Detail::Session(view);
        cx.notify();
    }

    pub fn new_session(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let connected: Vec<Entity<ServerConn>> = self
            .conns
            .iter()
            .filter(|c| c.read(cx).is_connected())
            .cloned()
            .collect();
        if connected.is_empty() {
            return;
        }
        self.close_detail(cx);
        let view = cx.new(|cx| NewSessionView::new(connected, window, cx));
        let sub = cx.subscribe_in(&view, window, |this, _, event, window, cx| match event {
            NewSessionEvent::Created { server_id, session_id } => {
                if let Some(ix) = this.conns.iter().position(|c| &c.read(cx).server_id == server_id) {
                    this.open_session(ix, session_id, window, cx);
                }
            }
            NewSessionEvent::Cancelled => {
                this.detail = Detail::Empty;
                cx.notify();
            }
        });
        self._subscriptions.push(sub);
        self.detail = Detail::NewSession(view);
        cx.notify();
    }

    fn rows(&self, cx: &App) -> Vec<SessionRow> {
        let mut rows: Vec<SessionRow> = Vec::new();
        for (ix, conn) in self.conns.iter().enumerate() {
            for session in conn.read(cx).index.active_sessions() {
                rows.push(SessionRow { conn: ix, session });
            }
        }
        rows.sort_by(|a, b| b.session.updated_at.cmp(&a.session.updated_at));
        rows
    }

    fn render_sidebar(&self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let rows = self.rows(cx);
        let (needs, recent) = inbox_groups(&rows, &self.opened);
        let selected = match &self.detail {
            Detail::Session(v) => Some(v.read(cx).session_id().to_string()),
            _ => None,
        };
        let any_connected = self.conns.iter().any(|c| c.read(cx).is_connected());
        let mut list = v_flex()
            .id("sessions")
            .flex_1()
            .overflow_y_scroll()
            .px_2()
            .pb_2()
            .gap_0p5();
        if !needs.is_empty() {
            list = list.child(section_header("Needs You", &p));
            for row in &needs {
                list = list.child(self.render_row(row, selected.as_deref(), window, cx));
            }
        }
        if !recent.is_empty() {
            list = list.child(section_header("Recent", &p));
            for row in &recent {
                list = list.child(self.render_row(row, selected.as_deref(), window, cx));
            }
        }
        if rows.is_empty() && any_connected {
            list = list.child(div().p_3().text_sm().text_color(p.overlay).child("No sessions yet."));
        }
        v_flex()
            .w(px(SIDEBAR_WIDTH))
            .h_full()
            .flex_shrink_0()
            .bg(p.mantle)
            .border_r_1()
            .border_color(p.crust)
            .child(
                h_flex()
                    .px_3()
                    .py_2()
                    .justify_between()
                    .child(div().text_sm().font_weight(FontWeight::SEMIBOLD).child("Sessions"))
                    .child(
                        Button::new("new-session")
                            .ghost()
                            .small()
                            .icon(IconName::Plus)
                            .tooltip("New Session (⌘N)")
                            .disabled(!any_connected)
                            .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.new_session(window, cx))),
                    ),
            )
            .child(list)
            .child(self.render_servers(cx))
    }

    fn render_row(
        &self,
        row: &SessionRow,
        selected: Option<&str>,
        _: &mut Window,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let p = Palette::of(cx);
        let s = &row.session;
        let conn = self.conns[row.conn].read(cx);
        let project = conn
            .index
            .projects
            .get(&s.project_id)
            .map(|p| p.name.clone())
            .unwrap_or_default();
        let server = if self.conns.len() > 1 {
            format!("{} · ", conn.name)
        } else {
            String::new()
        };
        let is_selected = selected == Some(s.id.as_str());
        let bold = stands_out(s);
        let age = short_age_of(&s.updated_at, Utc::now());
        let conn_ix = row.conn;
        let id = s.id.clone();
        h_flex()
            .id(SharedString::from(format!("row-{}", s.id)))
            .gap_2()
            .px_2()
            .py_1p5()
            .rounded_md()
            .cursor_pointer()
            .when(is_selected, |d| d.bg(p.surface0))
            .when(!is_selected, |d| d.hover(|d| d.bg(p.surface0.opacity(0.5))))
            .child(status_dot(s, &p))
            .child(
                v_flex()
                    .flex_1()
                    .min_w_0()
                    .child(
                        div()
                            .text_sm()
                            .truncate()
                            .when(bold, |d| d.font_weight(FontWeight::SEMIBOLD))
                            .child(if s.title.is_empty() {
                                "New session".to_string()
                            } else {
                                s.title.clone()
                            }),
                    )
                    .child(
                        div()
                            .text_xs()
                            .text_color(p.overlay)
                            .truncate()
                            .child(format!("{server}{project}")),
                    ),
            )
            .child(div().text_xs().text_color(p.overlay).flex_shrink_0().child(age))
            .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| this.open_session(conn_ix, &id, window, cx)))
            .into_any_element()
    }

    fn render_servers(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let mut col = v_flex().border_t_1().border_color(p.crust).px_3().py_2().gap_1();
        for (ix, conn) in self.conns.iter().enumerate() {
            let c = conn.read(cx);
            let (color, text) = match &c.state {
                ConnState::Connected => (
                    p.green,
                    c.info
                        .as_ref()
                        .map(|i| format!("rad {}", i.version))
                        .unwrap_or_default(),
                ),
                ConnState::Connecting(reason) => (p.yellow, reason.clone().unwrap_or_else(|| "Connecting…".into())),
                ConnState::Failed(e) => (p.red, e.to_string()),
            };
            let failed = matches!(c.state, ConnState::Failed(_));
            let conn = conn.clone();
            col = col.child(
                h_flex()
                    .gap_2()
                    .child(div().size(px(7.)).rounded_full().bg(color).flex_shrink_0())
                    .child(
                        v_flex()
                            .flex_1()
                            .min_w_0()
                            .child(
                                div()
                                    .text_xs()
                                    .font_weight(FontWeight::MEDIUM)
                                    .truncate()
                                    .child(c.name.clone()),
                            )
                            .child(div().text_xs().text_color(p.overlay).truncate().child(text)),
                    )
                    .when(failed, |d| {
                        d.child(
                            Button::new(SharedString::from(format!("retry-{ix}")))
                                .ghost()
                                .xsmall()
                                .icon(IconName::RefreshCw)
                                .tooltip("Try again")
                                .on_click(move |_, _, cx| conn.read(cx).retry()),
                        )
                    })
                    .child(
                        Button::new(SharedString::from(format!("remove-{ix}")))
                            .ghost()
                            .xsmall()
                            .icon(IconName::Close)
                            .tooltip("Remove server (unpairs it)")
                            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.remove_server(ix, cx))),
                    ),
            );
        }
        col.child(
            Button::new("pair-server")
                .ghost()
                .xsmall()
                .icon(IconName::Plus)
                .label("Pair a server…")
                .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                    this.close_detail(cx);
                    this.detail = Detail::Pair;
                    cx.notify();
                })),
        )
    }

    fn render_pair(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        v_flex().size_full().items_center().justify_center().child(
            v_flex()
                .w(px(520.))
                .gap_3()
                .p_6()
                .rounded_lg()
                .bg(p.mantle)
                .border_1()
                .border_color(p.surface0)
                .child(div().text_lg().font_weight(FontWeight::SEMIBOLD).child("Pair a server"))
                .child(
                    div()
                        .text_sm()
                        .text_color(p.subtext)
                        .child("On the computer that runs rad, run `rad pair --print-url`, and paste the link here."),
                )
                .child(Input::new(&self.pair_input))
                .when_some(self.pair_error.clone(), |d, e| {
                    d.child(div().text_sm().text_color(p.red).child(e))
                })
                .child(
                    h_flex().justify_end().child(
                        Button::new("pair")
                            .primary()
                            .label(if self.pairing { "Pairing…" } else { "Pair" })
                            .loading(self.pairing)
                            .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.pair(window, cx))),
                    ),
                ),
        )
    }
}

fn section_header(title: &'static str, p: &Palette) -> impl IntoElement {
    div()
        .px_2()
        .pt_3()
        .pb_1()
        .text_xs()
        .font_weight(FontWeight::SEMIBOLD)
        .text_color(p.overlay)
        .child(title)
}

/// The session's state at a glance: waiting for you, failed, working,
/// unread, or nothing.
pub fn status_dot(s: &Session, p: &Palette) -> AnyElement {
    let color = match s.status {
        SessionStatus::AwaitingApproval => Some(p.yellow),
        SessionStatus::Error => Some(p.red),
        _ if is_busy(s) => Some(p.blue),
        _ if s.unread => Some(p.accent),
        _ => None,
    };
    let dot = div().size(px(8.)).rounded_full().flex_shrink_0();
    match color {
        Some(c) => dot.bg(c).into_any_element(),
        None => dot.into_any_element(),
    }
}

impl Render for AppView {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let detail: AnyElement = match &self.detail {
            Detail::Empty => v_flex()
                .size_full()
                .items_center()
                .justify_center()
                .gap_2()
                .text_color(p.overlay)
                .child(Icon::new(IconName::Inbox).large().text_color(p.overlay))
                .child(div().text_sm().child("Choose a session, or start a new one with ⌘N."))
                .into_any_element(),
            Detail::Pair => self.render_pair(cx).into_any_element(),
            Detail::Session(view) => view.clone().into_any_element(),
            Detail::NewSession(view) => view.clone().into_any_element(),
        };
        v_flex()
            .size_full()
            .track_focus(&self.focus)
            .on_action(cx.listener(|this, _: &NewSession, window, cx| this.new_session(window, cx)))
            .bg(p.base)
            .text_color(p.text)
            .font_family(cx.theme().font_family.clone())
            .child(
                TitleBar::new().child(
                    h_flex()
                        .w_full()
                        .justify_center()
                        .text_sm()
                        .text_color(p.subtext)
                        .child("Remote Agent"),
                ),
            )
            .child(
                h_flex()
                    .flex_1()
                    .min_h_0()
                    .items_start()
                    .child(self.render_sidebar(window, cx))
                    .child(div().flex_1().min_w_0().h_full().child(detail)),
            )
    }
}
