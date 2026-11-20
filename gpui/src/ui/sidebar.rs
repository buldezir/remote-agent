//! The sidebar, in the chosen layout: Inbox (every server's sessions by
//! what they need), Projects (each server's projects with their sessions),
//! or Columns (collections, their sessions beside them). Search replaces
//! the lists while a query is in. As `SessionsSidebar.swift` and its kin.

use std::rc::Rc;
use std::time::Duration;

use chrono::Utc;
use gpui_kit::assets::IconName as L;
use gpui_kit::component::button::{Button, ButtonVariants as _};
use gpui_kit::component::hover_card::HoverCard;
use gpui_kit::component::input::Input;
use gpui_kit::component::menu::ContextMenuExt as _;
use gpui_kit::component::menu::{DropdownMenu as _, PopupMenu, PopupMenuItem};
use gpui_kit::component::spinner::Spinner;
use gpui_kit::component::{Disableable as _, Icon, IconName, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    Anchor, AnyElement, App, ClickEvent, Context, Entity, FontWeight, HighlightStyle, Hsla, InteractiveElement as _,
    IntoElement, MouseButton, ParentElement as _, SharedString, StatefulInteractiveElement as _, StyleRefinement,
    Styled as _, StyledText, Window, div, px,
};
use serde_json::json;

use crate::conn::{ConnState, ServerConn};
use crate::protocol::{Session, SessionStatus, WorkspaceKind};
use crate::store::search::search_active;
use crate::store::sidebar::{
    Collection, Layout, inbox_groups, is_busy, most_pressing, needs_you, needs_you_first, projects_in_order,
    short_age_of, stands_out, urgency,
};
use crate::ui::app::{AppView, Detail};
use crate::ui::badges;
use crate::ui::common::{busy_dot, caption, sidebar_toggle, status_dot, top_bar};
use crate::ui::dialogs;
use crate::ui::palette::Palette;
use crate::ui::panes::{self, Pane};
use crate::ui::session::explain;
use crate::ui::session::rename_session;
use crate::ui::state::{prefs, update_prefs};
use crate::ui::{actions, new_session};

/// A row of a list: a session, and the connection it's on.
#[derive(Clone)]
pub(crate) struct SessionRow {
    pub conn: usize,
    pub server_id: String,
    pub session: Rc<Session>,
}

impl AsRef<Session> for SessionRow {
    fn as_ref(&self) -> &Session {
        &self.session
    }
}

fn section_header(title: impl Into<SharedString>, p: &Palette) -> gpui_kit::Div {
    div()
        .px_2()
        .pt_3()
        .pb_1()
        .text_xs()
        .font_weight(FontWeight::SEMIBOLD)
        .text_color(p.overlay)
        .child(title.into())
}

fn conn_color(c: &ServerConn, p: &Palette) -> Hsla {
    match c.state {
        ConnState::Connected => p.green,
        ConnState::Connecting(_) => p.yellow,
        ConnState::Failed(_) => p.red,
    }
}

impl AppView {
    fn selected_id(&self, cx: &App) -> Option<String> {
        match &self.detail {
            Detail::Session(v) => Some(v.read(cx).session_id().to_string()),
            _ => None,
        }
    }

    /// Every server's unarchived sessions, the latest first.
    fn rows(&self, cx: &App) -> Vec<SessionRow> {
        let mut rows: Vec<SessionRow> = Vec::new();
        for (ix, conn) in self.conns.iter().enumerate() {
            let c = conn.read(cx);
            for session in c.index.active_sessions() {
                rows.push(SessionRow {
                    conn: ix,
                    server_id: c.server_id.clone(),
                    session,
                });
            }
        }
        rows.sort_by(|a, b| b.session.updated_at.cmp(&a.session.updated_at));
        rows
    }

    fn archived_rows(&self, cx: &App) -> Vec<SessionRow> {
        let mut rows: Vec<SessionRow> = Vec::new();
        for (ix, conn) in self.conns.iter().enumerate() {
            let c = conn.read(cx);
            for session in c.index.archived_sessions() {
                rows.push(SessionRow {
                    conn: ix,
                    server_id: c.server_id.clone(),
                    session,
                });
            }
        }
        rows.sort_by(|a, b| b.session.updated_at.cmp(&a.session.updated_at));
        rows
    }

    /// The Columns collection's sessions, across servers unless it's a project's.
    pub(crate) fn column_sessions(&self, cx: &App) -> Vec<SessionRow> {
        let (server, column) = self.column(cx);
        let mut rows = Vec::new();
        for (ix, conn) in self.conns.iter().enumerate() {
            let c = conn.read(cx);
            if let Collection::Project(_) = &column
                && server.as_deref() != Some(c.server_id.as_str())
            {
                continue;
            }
            for session in column.sessions(&c.index) {
                rows.push(SessionRow {
                    conn: ix,
                    server_id: c.server_id.clone(),
                    session,
                });
            }
        }
        rows.sort_by(|a, b| b.session.updated_at.cmp(&a.session.updated_at));
        if column == Collection::All {
            let mut needs: Vec<SessionRow> = rows.iter().filter(|r| needs_you(&r.session)).cloned().collect();
            needs.extend(rows.into_iter().filter(|r| !needs_you(&r.session)));
            return needs;
        }
        rows
    }

    // MARK: Menus

    fn session_menu(
        &self,
        conn: Entity<ServerConn>,
        s: Rc<Session>,
    ) -> impl Fn(PopupMenu, &mut Window, &mut Context<PopupMenu>) -> PopupMenu + 'static {
        move |mut menu, _, cx| {
            let info = conn.read(cx).info.clone().unwrap_or_default();
            if info.rename {
                let (c, s2) = (conn.clone(), s.clone());
                menu = menu.item(
                    PopupMenuItem::new("Rename…")
                        .icon(Icon::new(L::Pencil))
                        .on_click(move |_, window, cx| rename_session(c.clone(), s2.clone(), window, cx)),
                );
            }
            if !s.archived {
                let (c, s2) = (conn.clone(), s.clone());
                menu = menu.item(
                    PopupMenuItem::new("Archive…")
                        .icon(Icon::new(L::Archive))
                        .on_click(move |_, window, cx| actions::archive(c.clone(), s2.clone(), window, cx)),
                );
            } else if info.unarchive {
                let (c, s2) = (conn.clone(), s.clone());
                menu = menu.item(
                    PopupMenuItem::new("Unarchive")
                        .icon(Icon::new(L::ArchiveRestore))
                        .on_click(move |_, window, cx| actions::unarchive(c.clone(), s2.clone(), window, cx)),
                );
            }
            let (c, s2) = (conn.clone(), s.clone());
            menu.item(
                PopupMenuItem::new("Delete…")
                    .icon(Icon::new(L::Trash))
                    .on_click(move |_, window, cx| actions::delete(c.clone(), s2.clone(), window, cx)),
            )
        }
    }

    fn server_menu(
        &self,
        ix: usize,
        cx: &mut Context<Self>,
    ) -> impl Fn(PopupMenu, &mut Window, &mut Context<PopupMenu>) -> PopupMenu + 'static {
        let this = cx.entity().downgrade();
        let conn = self.conns[ix].clone();
        move |menu, _, cx| {
            let c = conn.read(cx);
            let (name, connected) = (c.name.clone(), c.is_connected());
            let (a, b, d) = (this.clone(), this.clone(), conn.clone());
            let r = conn.clone();
            menu.item(
                PopupMenuItem::new(format!("New Session on {name}"))
                    .icon(Icon::new(IconName::Plus))
                    .disabled(!connected)
                    .on_click(move |_, window, cx| {
                        let _ = a.update(cx, |v, cx| v.new_session_on(ix, None, window, cx));
                    }),
            )
            .item(
                PopupMenuItem::new("Reconnect")
                    .icon(Icon::new(IconName::RefreshCw))
                    .on_click(move |_, _, cx| r.read(cx).retry()),
            )
            .item(
                PopupMenuItem::new("Server Settings…")
                    .icon(Icon::new(L::ServerCog))
                    .disabled(!connected)
                    .on_click(move |_, window, cx| {
                        let _ = b.update(cx, |v, cx| v.open_settings(Some(ix), window, cx));
                    }),
            )
            .separator()
            .item(
                PopupMenuItem::new(format!("Remove {name}…"))
                    .icon(Icon::new(L::Trash))
                    .on_click(move |_, window, cx| crate::ui::settings::remove_server(d.clone(), window, cx)),
            )
        }
    }

    fn project_menu(
        &self,
        ix: usize,
        project_id: String,
        cx: &mut Context<Self>,
    ) -> impl Fn(PopupMenu, &mut Window, &mut Context<PopupMenu>) -> PopupMenu + 'static {
        let this = cx.entity().downgrade();
        let conn = self.conns[ix].clone();
        move |menu, _, cx| {
            let c = conn.read(cx);
            let connected = c.is_connected();
            let empty = !c.index.active_sessions().iter().any(|s| s.project_id == project_id);
            let (a, conn2, pid, pid2) = (this.clone(), conn.clone(), project_id.clone(), project_id.clone());
            menu.item(
                PopupMenuItem::new("New Session")
                    .icon(Icon::new(IconName::Plus))
                    .disabled(!connected)
                    .on_click(move |_, window, cx| {
                        let _ = a.update(cx, |v, cx| v.new_session_on(ix, Some(pid.clone()), window, cx));
                    }),
            )
            .separator()
            .item(
                PopupMenuItem::new("Forget Project")
                    .icon(Icon::new(L::FolderMinus))
                    .disabled(!connected || !empty)
                    .on_click(move |_, _, cx| {
                        drop(conn2.read(cx).call("project.remove", serde_json::json!({"id": pid2})))
                    }),
            )
        }
    }

    // MARK: Rows

    fn render_row(
        &self,
        row: &SessionRow,
        selected: Option<&str>,
        details: bool,
        indent: f32,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let p = Palette::of(cx);
        let s = &row.session;
        let conn = self.conns[row.conn].clone();
        let c = conn.read(cx);
        let project_name = c
            .index
            .projects
            .get(&s.project_id)
            .map(|p| p.name.clone())
            .unwrap_or_default();
        // In Projects, a session's project is the one it's under.
        let in_projects = self.layout(cx) == Layout::Projects && !search_active(&self.search.query);
        let project = if in_projects {
            String::new()
        } else {
            project_name.clone()
        };
        let server = if self.conns.len() > 1 && !in_projects {
            format!("{} · ", c.name)
        } else {
            String::new()
        };
        let place = format!("{server}{project}").trim_end_matches(" · ").to_string();
        let is_selected = selected == Some(s.id.as_str());
        let bold = stands_out(s);
        let age = short_age_of(&s.updated_at, Utc::now());
        let conn_ix = row.conn;
        let id = s.id.clone();
        let activity = details.then(|| activity(s, c, &p));
        let worktree = s.workspace.kind == WorkspaceKind::Worktree;
        let branch = worktree.then(|| s.workspace.branch.clone()).flatten();
        let pr = badges::pull_request(s);
        let harness = c.harness(&s.harness).cloned();
        // The title takes the state's colour, and the dot says it too.
        let state = row_state(s, &p);
        let title_color = state.as_ref().map(|(_, color)| *color).unwrap_or(p.text);
        let title = if s.title.is_empty() {
            "New session".to_string()
        } else {
            s.title.clone()
        };
        let card = Card {
            title: title.clone(),
            project: project_name,
            server: c.name.clone(),
            branch: s.workspace.branch.clone(),
            harness: s.harness.clone(),
            model: badges::model_name(s, harness.as_ref()),
            context: badges::context_percent(s),
            pr: pr.as_ref().map(|pr| (pr.number, pr.title.clone())),
        };
        // Its pull request, server and agent, at the end of a line.
        let (pr_number, harness_id) = (pr.as_ref().map(|pr| pr.number), s.harness.clone());
        let marks = move || {
            h_flex()
                .flex_shrink_0()
                .gap_1()
                .text_xs()
                .text_color(p.overlay)
                .when_some(pr_number, |d, n| {
                    d.child(Icon::new(L::GitPullRequest).xsmall().text_color(p.mauve))
                        .child(div().mr_0p5().child(n.to_string()))
                })
                .child(Icon::new(L::Server).xsmall().text_color(p.overlay))
                .child(badges::harness_icon(&harness_id, &p).xsmall())
        };
        // The age, and in its place on hover, Archive.
        let (archive_conn, archive_id) = (conn.clone(), s.id.clone());
        let tail = div()
            .relative()
            .flex_shrink_0()
            .min_w(px(22.))
            .h(px(18.))
            .flex()
            .items_center()
            .justify_end()
            .child(
                div()
                    .text_xs()
                    .text_color(p.overlay)
                    .when(!s.archived, |d| d.group_hover(ROW_GROUP, |d| d.invisible()))
                    .child(age),
            )
            .when(!s.archived, |d| {
                d.child(
                    div()
                        .absolute()
                        .right_0()
                        .top(px(-3.))
                        .invisible()
                        .group_hover(ROW_GROUP, |d| d.visible())
                        // The row mustn't open too.
                        .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                        .child(
                            Button::new(SharedString::from(format!("archive-{}-{}", row.server_id, s.id)))
                                .ghost()
                                .xsmall()
                                .icon(IconName::Check)
                                .tooltip("Archive")
                                .on_click(move |_: &ClickEvent, window, cx| {
                                    cx.stop_propagation();
                                    archive_now(archive_conn.clone(), archive_id.clone(), window, cx)
                                }),
                        ),
                )
            });
        let second_line = !place.is_empty() || worktree;
        // The dot sits in a gutter left of the title, so titles line up
        // with or without one.
        let left = (10. + indent).max(25.);
        let trigger = h_flex()
            .id(SharedString::from(format!("row-{}-{}", row.server_id, s.id)))
            .group(ROW_GROUP)
            .relative()
            .w_full()
            .gap_2()
            .pl(px(left))
            .pr_2()
            .py(px(7.))
            .rounded_md()
            .cursor_pointer()
            .items_start()
            .when(is_selected, |d| d.bg(p.surface0))
            .when(!is_selected, |d| d.hover(|d| d.bg(p.surface0.opacity(0.5))))
            .when_some(state.map(|(dot, _)| dot), |d, dot| {
                d.child(div().absolute().left(px(left - 14.)).top(px(14.)).child(dot))
            })
            .child(
                v_flex()
                    .flex_1()
                    .min_w_0()
                    .gap_0p5()
                    .child(
                        h_flex()
                            .gap_2()
                            .child(
                                div()
                                    .flex_1()
                                    .min_w_0()
                                    .text_sm()
                                    .truncate()
                                    .text_color(title_color)
                                    .when(bold, |d| d.font_weight(FontWeight::SEMIBOLD))
                                    .child(title),
                            )
                            .when(!worktree, |d| d.child(marks()))
                            .child(tail),
                    )
                    .when(second_line, |d| {
                        d.child(
                            h_flex()
                                .gap_1()
                                .text_xs()
                                .text_color(p.overlay)
                                .child(
                                    h_flex()
                                        .flex_1()
                                        .min_w_0()
                                        .gap_1()
                                        .when(!place.is_empty(), |d| {
                                            d.child(div().flex_shrink_0().max_w(px(140.)).truncate().child(place))
                                        })
                                        .when_some(branch, |d, b| {
                                            d.child(Icon::new(L::GitBranch).xsmall().text_color(p.overlay))
                                                .child(div().min_w_0().truncate().child(b))
                                        }),
                                )
                                .when(worktree, |d| d.child(marks())),
                        )
                    })
                    .when_some(activity, |d, a| d.child(a)),
            )
            .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| this.open_session(conn_ix, &id, window, cx)))
            .context_menu(self.session_menu(conn.clone(), s.clone()));
        HoverCard::new(SharedString::from(format!("card-{}-{}", row.server_id, s.id)))
            .anchor(Anchor::LeftCenter)
            .open_delay(Duration::from_millis(500))
            .close_delay(Duration::from_millis(100))
            .trigger_style(StyleRefinement::default().w_full())
            .trigger(trigger)
            .content(move |_, _, cx| card.render(cx))
            .into_any_element()
    }

    fn render_rows(
        &self,
        rows: &[SessionRow],
        selected: Option<&str>,
        details: bool,
        list: gpui_kit::Stateful<gpui_kit::Div>,
        cx: &mut Context<Self>,
    ) -> gpui_kit::Stateful<gpui_kit::Div> {
        self.render_rows_in(rows, selected, details, 0., list, cx)
    }

    /// Rows indented, so that their titles line up with a header's name.
    fn render_rows_in(
        &self,
        rows: &[SessionRow],
        selected: Option<&str>,
        details: bool,
        indent: f32,
        mut list: gpui_kit::Stateful<gpui_kit::Div>,
        cx: &mut Context<Self>,
    ) -> gpui_kit::Stateful<gpui_kit::Div> {
        for row in rows {
            list = list.child(self.render_row(row, selected, details, indent, cx));
        }
        list
    }

    /// A server that isn't connected: why, and Try again.
    fn status_row(&self, ix: usize, p: &Palette, cx: &mut Context<Self>) -> Option<AnyElement> {
        let c = self.conns[ix].read(cx);
        let conn = self.conns[ix].clone();
        match &c.state {
            ConnState::Connected => None,
            ConnState::Connecting(reason) => Some(
                h_flex()
                    .gap_2()
                    .px_2()
                    .py_1()
                    .child(Spinner::new().xsmall())
                    .child(caption(reason.clone().unwrap_or_else(|| "Connecting…".into()), p))
                    .into_any_element(),
            ),
            ConnState::Failed(e) => Some(
                v_flex()
                    .gap_1()
                    .px_2()
                    .py_1()
                    .child(caption(e.to_string(), p))
                    .child(
                        Button::new(SharedString::from(format!("retry-{ix}")))
                            .xsmall()
                            .outline()
                            .label("Try again")
                            .on_click(move |_: &ClickEvent, _, cx| conn.read(cx).retry()),
                    )
                    .into_any_element(),
            ),
        }
    }

    // MARK: Sidebar

    pub(crate) fn render_sidebar(&self, window: &mut Window, cx: &mut Context<Self>) -> AnyElement {
        let p = Palette::of(cx);
        let layout = self.layout(cx);
        let searching = search_active(&self.search.query) && layout != Layout::Columns;
        let list = if searching {
            self.render_search(cx)
        } else {
            match layout {
                Layout::Inbox => self.render_inbox(window, cx),
                Layout::Projects => self.render_projects(cx),
                Layout::Columns => self.render_collections(cx),
            }
        };
        v_flex()
            .relative()
            .w(px(self.sidebar_width))
            .h_full()
            .flex_shrink_0()
            .bg(p.mantle)
            .border_r_1()
            .border_color(p.crust)
            .child(top_bar(
                "sidebar-bar",
                true,
                p.mantle,
                None,
                window,
                h_flex()
                    .w_full()
                    .gap_1()
                    .child(
                        div()
                            .flex_1()
                            .min_w_0()
                            .truncate()
                            .text_sm()
                            .font_weight(FontWeight::SEMIBOLD)
                            .child(layout.title()),
                    )
                    .child(sidebar_toggle(false))
                    // Columns has no search field here: its buttons stay on the bar.
                    .when(layout == Layout::Columns, |d| d.children(self.list_buttons(cx))),
            ))
            .when(layout != Layout::Columns, |d| {
                d.child(self.render_search_field(true, cx))
            })
            .child(list)
            .child(
                div().px_2().py_2().border_t_1().border_color(p.crust).child(
                    Button::new("pair-server")
                        .ghost()
                        .xsmall()
                        .icon(IconName::Plus)
                        .label("Pair a server")
                        .on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.show_pairing(cx))),
                ),
            )
            .child(panes::edge(Pane::Sidebar, &p, cx))
            .into_any_element()
    }

    /// New Session: a menu of servers when several are connected.
    fn new_session_button(&self, id: &'static str, cx: &mut Context<Self>) -> Button {
        let connected: Vec<(usize, String)> = self
            .conns
            .iter()
            .enumerate()
            .filter(|(_, c)| c.read(cx).is_connected())
            .map(|(i, c)| (i, c.read(cx).name.clone()))
            .collect();
        let button = Button::new(id)
            .ghost()
            .small()
            .icon(L::SquarePen)
            .tooltip("New Session (⌘N)");
        if connected.len() <= 1 {
            return button.on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.new_session(window, cx)));
        }
        button
    }

    /// The menu of servers for New Session, with several connected.
    fn new_session_menu(&self, id: &'static str, cx: &mut Context<Self>) -> AnyElement {
        let connected: Vec<(usize, String)> = self
            .conns
            .iter()
            .enumerate()
            .filter(|(_, c)| c.read(cx).is_connected())
            .map(|(i, c)| (i, c.read(cx).name.clone()))
            .collect();
        if connected.len() <= 1 {
            return self
                .new_session_button(id, cx)
                .disabled(connected.is_empty())
                .into_any_element();
        }
        let this = cx.entity().downgrade();
        self.new_session_button(id, cx)
            .dropdown_menu(move |mut menu, _, _| {
                for (ix, name) in &connected {
                    let (w, ix) = (this.clone(), *ix);
                    menu = menu.item(PopupMenuItem::new(format!("On {name}")).on_click(move |_, window, cx| {
                        let _ = w.update(cx, |v, cx| v.new_session_on(ix, None, window, cx));
                    }));
                }
                menu
            })
            .into_any_element()
    }

    /// Search, as a plain row, with the list's buttons at its end.
    fn render_search_field(&self, buttons: bool, cx: &mut Context<Self>) -> AnyElement {
        let p = Palette::of(cx);
        h_flex()
            .pl_3()
            .pr_2()
            .pb_1()
            .gap_0p5()
            .on_key_down(cx.listener(|this, ev: &gpui_kit::KeyDownEvent, window, cx| {
                if ev.keystroke.key == "escape" {
                    this.clear_search(window, cx);
                }
            }))
            .child(Icon::new(IconName::Search).small().text_color(p.overlay))
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .child(Input::new(&self.search_input).small().appearance(false).cleanable(true)),
            )
            .when(buttons, |d| d.children(self.list_buttons(cx)))
            .into_any_element()
    }

    /// The list's buttons: its layout and Archive old sessions, Add project,
    /// and New Session.
    fn list_buttons(&self, cx: &mut Context<Self>) -> Vec<AnyElement> {
        let layout = self.layout(cx);
        let old = self.old_sessions(cx).len();
        let this = cx.entity().downgrade();
        let filter = Button::new("list-filter")
            .ghost()
            .small()
            .icon(L::ListFilter)
            .tooltip("View")
            .dropdown_menu(move |mut menu, _, _| {
                for (title, key, action) in [
                    (
                        "as Inbox",
                        Layout::Inbox,
                        Box::new(crate::LayoutInbox) as Box<dyn gpui_kit::Action>,
                    ),
                    ("as Projects", Layout::Projects, Box::new(crate::LayoutProjects)),
                    ("as Columns", Layout::Columns, Box::new(crate::LayoutColumns)),
                ] {
                    menu = menu.item(
                        PopupMenuItem::new(title)
                            .checked(layout == key)
                            .action(action)
                            .on_click(move |_, _, cx| update_prefs(cx, |p| p.sidebar_layout = key.key().into())),
                    );
                }
                let w = this.clone();
                menu.separator().item(
                    PopupMenuItem::new(if old == 0 {
                        "No sessions older than 7 days".to_string()
                    } else {
                        "Archive sessions with nothing new for 7 days…".to_string()
                    })
                    .icon(Icon::new(L::Archive))
                    .disabled(old == 0)
                    .on_click(move |_, window, cx| {
                        let _ = w.update(cx, |v, cx| v.archive_old(window, cx));
                    }),
                )
            });
        let add = Button::new("add-project")
            .ghost()
            .small()
            .icon(L::FolderPlus)
            .tooltip("Add Project…")
            .disabled(self.target(cx).is_none())
            .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.add_project(window, cx)));
        vec![
            filter.into_any_element(),
            add.into_any_element(),
            self.new_session_menu("new-session", cx),
        ]
    }

    /// Adds a folder as a project on the server New Session would use, and
    /// starts a session there.
    fn add_project(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(ix) = self.target(cx) else { return };
        let this = cx.entity().downgrade();
        let window_handle = window.window_handle();
        new_session::open_folder_browser(self.conns[ix].clone(), window, cx, move |project, cx| {
            let this = this.clone();
            let _ = window_handle.update(cx, |_, window, cx| {
                let _ = this.update(cx, |v, cx| v.new_session_on(ix, Some(project.id.clone()), window, cx));
            });
        });
    }

    fn render_search(&self, cx: &mut Context<Self>) -> AnyElement {
        let p = Palette::of(cx);
        let s = &self.search;
        let selected = self.selected_id(cx);
        let mut list = v_flex()
            .id("search")
            .flex_1()
            .overflow_y_scroll()
            .px_2()
            .pb_2()
            .gap_0p5();
        let archived = prefs(cx).search_archived;
        list = list.child(
            div().px_2().py_1().child(
                gpui_kit::component::checkbox::Checkbox::new("search-archived")
                    .label("Include archived")
                    .checked(archived)
                    .small()
                    .on_click(cx.listener(|this, v: &bool, _, cx| {
                        let v = *v;
                        update_prefs(cx, |p| p.search_archived = v);
                        this.rerun_search(cx);
                    })),
            ),
        );
        for (i, hit) in s.hits.iter().enumerate() {
            let Some(conn_ix) = self.conns.iter().position(|c| c.read(cx).server_id == hit.server_id) else {
                continue;
            };
            let c = self.conns[conn_ix].read(cx);
            let Some(found) = hit.result.session.clone() else {
                continue;
            };
            let session = c
                .index
                .sessions
                .get(&found.id)
                .cloned()
                .unwrap_or_else(|| Rc::new(found));
            let project = c
                .index
                .projects
                .get(&session.project_id)
                .map(|p| p.name.clone())
                .unwrap_or_default();
            let agent = c
                .harness(&session.harness)
                .map(|h| h.name.clone())
                .unwrap_or_else(|| session.harness.clone());
            let mut place = format!(
                "{project} · {agent} · {}",
                short_age_of(&session.updated_at, Utc::now())
            );
            if self.conns.len() > 1 {
                place += &format!(" · {}", c.name);
            }
            // One text, so that it wraps as a sentence does, not at each match.
            let mut text = String::new();
            let mut matches = Vec::new();
            for part in &hit.result.snippet {
                let start = text.len();
                text.push_str(&part.text.replace('\n', " "));
                if part.is_match {
                    matches.push((
                        start..text.len(),
                        HighlightStyle {
                            color: Some(p.text),
                            font_weight: Some(FontWeight::SEMIBOLD),
                            background_color: Some(p.yellow.opacity(0.35)),
                            ..Default::default()
                        },
                    ));
                }
            }
            let snippet = div()
                .text_xs()
                .text_color(p.subtext)
                .line_clamp(2)
                .child(StyledText::new(text).with_highlights(matches));
            let (id, item) = (session.id.clone(), hit.result.item_id.clone());
            let is_selected = selected.as_deref() == Some(session.id.as_str());
            list = list.child(
                h_flex()
                    .id(("hit", i))
                    .gap_2()
                    .px_2()
                    .py_1p5()
                    .rounded_md()
                    .cursor_pointer()
                    .items_start()
                    .when(is_selected, |d| d.bg(p.surface0))
                    .when(!is_selected, |d| d.hover(|d| d.bg(p.surface0.opacity(0.5))))
                    .child(div().pt_1p5().child(status_dot(&session, &p)))
                    .child(
                        v_flex()
                            .flex_1()
                            .min_w_0()
                            .gap_0p5()
                            .child(
                                h_flex()
                                    .gap_2()
                                    .child(
                                        div()
                                            .flex_1()
                                            .min_w_0()
                                            .truncate()
                                            .text_sm()
                                            .when(stands_out(&session), |d| d.font_weight(FontWeight::SEMIBOLD))
                                            .child(session.title.clone()),
                                    )
                                    .when(session.archived, |d| {
                                        d.child(
                                            div()
                                                .px_1p5()
                                                .rounded_full()
                                                .bg(p.surface0)
                                                .text_xs()
                                                .text_color(p.subtext)
                                                .child("Archived"),
                                        )
                                    }),
                            )
                            .child(caption(place, &p).truncate())
                            .child(snippet)
                            .when(hit.result.matches > 1, |d| {
                                d.child(caption(format!("{} matches", hit.result.matches), &p))
                            }),
                    )
                    .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| {
                        this.open_session_at(conn_ix, &id, item.clone(), window, cx)
                    })),
            );
        }
        let q = s.query.trim();
        if s.searching {
            list = list.child(h_flex().p_2().child(Spinner::new().small()));
        } else if s.answered && s.hits.is_empty() {
            let any = self
                .conns
                .iter()
                .any(|c| c.read(cx).is_connected() && c.read(cx).info.as_ref().is_some_and(|i| i.search));
            let text = if any || !s.failures.is_empty() {
                format!("No sessions match “{q}”.")
            } else if s.unsupported.is_empty() {
                "No server is connected.".into()
            } else {
                String::new()
            };
            if !text.is_empty() {
                list = list.child(caption(text, &p).p_2());
            }
        }
        if s.more {
            list = list.child(caption("More sessions match. Add words to narrow it down.", &p).p_2());
        }
        for name in &s.indexing {
            list = list.child(
                caption(
                    format!("{name} is still indexing older sessions, so some may be missing."),
                    &p,
                )
                .p_2(),
            );
        }
        for (name, e) in &s.failures {
            list = list.child(caption(format!("Couldn't search {name}: {e}"), &p).p_2());
        }
        for name in &s.unsupported {
            list = list.child(caption(format!("Update rad on {name} to search its sessions."), &p).p_2());
        }
        list.into_any_element()
    }

    fn render_inbox(&self, _: &mut Window, cx: &mut Context<Self>) -> AnyElement {
        let p = Palette::of(cx);
        let rows = self.rows(cx);
        let (needs, recent) = inbox_groups(&rows, &self.opened);
        let selected = self.selected_id(cx);
        let any_connected = self.conns.iter().any(|c| c.read(cx).is_connected());
        let failed: Vec<usize> = (0..self.conns.len())
            .filter(|i| matches!(self.conns[*i].read(cx).state, ConnState::Failed(_)))
            .collect();
        let mut list = v_flex()
            .id("sessions")
            .flex_1()
            .overflow_y_scroll()
            .px_2()
            .pb_2()
            .gap_0p5();
        if !needs.is_empty() || !failed.is_empty() {
            list = list.child(section_header("Needs You", &p));
            for ix in &failed {
                let c = self.conns[*ix].read(cx);
                let name = c.name.clone();
                list = list.child(
                    v_flex()
                        .px_2()
                        .py_1()
                        .child(
                            div()
                                .text_sm()
                                .font_weight(FontWeight::SEMIBOLD)
                                .child(format!("{name} is offline")),
                        )
                        .when_some(self.status_row(*ix, &p, cx), |d, s| d.child(s)),
                );
            }
            list = self.render_rows(&needs, selected.as_deref(), false, list, cx);
        }
        if !recent.is_empty() {
            list = list.child(section_header("Recent", &p));
            list = self.render_rows(&recent, selected.as_deref(), false, list, cx);
        }
        if rows.is_empty() && any_connected {
            list = list.child(div().p_3().text_sm().text_color(p.overlay).child("No sessions yet."));
        }
        let archived = self.archived_rows(cx);
        if !archived.is_empty() {
            let open = self.archive_open;
            list = list.child(
                crate::ui::common::disclosure("archived", open, &format!("Archived ({})", archived.len()), &p)
                    .px_2()
                    .pt_3()
                    .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                        this.archive_open = !this.archive_open;
                        cx.notify();
                    })),
            );
            if open {
                list = self.render_rows(&archived, selected.as_deref(), false, list, cx);
            }
        }
        // Servers
        list = list.child(section_header("Servers", &p));
        for ix in 0..self.conns.len() {
            let c = self.conns[ix].read(cx);
            let color = conn_color(c, &p);
            let count = c.index.active_sessions().len();
            let connecting = matches!(c.state, ConnState::Connecting(_));
            let failed = matches!(c.state, ConnState::Failed(_));
            let conn = self.conns[ix].clone();
            list = list.child(
                h_flex()
                    .id(("server-row", ix))
                    .gap_2()
                    .px_2()
                    .py_1()
                    .rounded_md()
                    .hover(|d| d.bg(p.surface0.opacity(0.5)))
                    .child(Icon::new(L::Monitor).small().text_color(p.overlay))
                    .child(div().flex_1().min_w_0().truncate().text_sm().child(c.name.clone()))
                    .when(connecting, |d| d.child(Spinner::new().xsmall()))
                    .when(failed, |d| {
                        d.child(
                            Button::new(("server-retry", ix))
                                .xsmall()
                                .ghost()
                                .label("Try again")
                                .on_click(move |_: &ClickEvent, _, cx| conn.read(cx).retry()),
                        )
                    })
                    .child(caption(count.to_string(), &p))
                    .child(div().size(px(7.)).rounded_full().bg(color))
                    .context_menu(self.server_menu(ix, cx)),
            );
        }
        list.into_any_element()
    }

    fn render_projects(&self, cx: &mut Context<Self>) -> AnyElement {
        let p = Palette::of(cx);
        let selected = self.selected_id(cx);
        let prefs = prefs(cx).clone();
        let mut list = v_flex()
            .id("projects")
            .flex_1()
            .overflow_y_scroll()
            .px_2()
            .pb_2()
            .gap_0p5();
        for ix in 0..self.conns.len() {
            let c = self.conns[ix].read(cx);
            let server_id = c.server_id.clone();
            let collapsed = prefs.collapsed_servers.contains(&server_id);
            let active = c.index.active_sessions();
            let by_name = c.index.projects_by_name();
            let archived = c.index.archived_sessions();
            let any_needs = active.iter().any(|s| needs_you(s));
            let any_unread = active.iter().any(|s| s.unread);
            let color = conn_color(c, &p);
            let sid = server_id.clone();
            let failed = matches!(c.state, ConnState::Failed(_));
            let conn = self.conns[ix].clone();
            // One server needs no header: its projects are the list.
            let headed = self.conns.len() > 1;
            let collapsed = collapsed && headed;
            if headed {
                list = list.child(
                    h_flex()
                        .id(("server-head", ix))
                        .gap_2()
                        .px_2()
                        .pt_3()
                        .pb_1()
                        .cursor_pointer()
                        .child(
                            Icon::new(if collapsed {
                                IconName::ChevronRight
                            } else {
                                IconName::ChevronDown
                            })
                            .xsmall()
                            .text_color(p.overlay),
                        )
                        .child(
                            div()
                                .flex_1()
                                .min_w_0()
                                .truncate()
                                .text_xs()
                                .font_weight(FontWeight::SEMIBOLD)
                                .text_color(p.subtext)
                                .child(c.name.clone()),
                        )
                        .when(collapsed, |d| {
                            d.child(caption(active.len().to_string(), &p))
                                .when(any_needs, |d| d.child(Icon::new(L::Hand).xsmall().text_color(p.yellow)))
                                .when(!any_needs && any_unread, |d| {
                                    d.child(div().size(px(6.)).rounded_full().bg(p.accent))
                                })
                        })
                        .child(div().size(px(7.)).rounded_full().bg(color))
                        .on_click(cx.listener(move |_, _: &ClickEvent, _, cx| {
                            if failed {
                                conn.read(cx).retry();
                                return;
                            }
                            let sid = sid.clone();
                            update_prefs(cx, |p| {
                                if !p.collapsed_servers.contains(&sid) {
                                    p.collapsed_servers.push(sid);
                                } else {
                                    p.collapsed_servers.retain(|s| *s != sid);
                                }
                            });
                        }))
                        .context_menu(self.server_menu(ix, cx)),
                );
            }
            if collapsed {
                continue;
            }
            if let Some(status) = self.status_row(ix, &p, cx) {
                list = list.child(status);
                continue;
            }
            if active.is_empty() {
                list = list.child(caption("Start a session", &p).px_2());
            }
            for project in projects_in_order(&by_name, &active) {
                let sessions: Vec<Rc<Session>> =
                    active.iter().filter(|s| s.project_id == project.id).cloned().collect();
                let key = format!("{server_id}/{}", project.id);
                let folded = prefs.folded_projects.contains(&key) || sessions.is_empty();
                let pressing = most_pressing(sessions.iter()).cloned();
                let k = key.clone();
                let has = !sessions.is_empty();
                let (pid, name) = (project.id.clone(), project.name.clone());
                list = list.child(
                    h_flex()
                        .id(SharedString::from(format!("project-{key}")))
                        .group(PROJECT_GROUP)
                        .gap_2()
                        .px_2()
                        .py_1p5()
                        .mt_1()
                        .rounded_md()
                        .when(has, |d| d.cursor_pointer().hover(|d| d.bg(p.surface0.opacity(0.5))))
                        .child(badges::project_badge(&project.name, 18., &p))
                        .child(
                            div()
                                .flex_1()
                                .min_w_0()
                                .truncate()
                                .text_sm()
                                .child(project.name.clone()),
                        )
                        // Folded, what's inside: its most pressing state and how many.
                        .when(folded, |d| {
                            d.when_some(pressing.filter(|s| urgency(s) > 0), |d, s| d.child(status_dot(&s, &p)))
                                .when(has, |d| d.child(caption(sessions.len().to_string(), &p)))
                        })
                        .child(
                            div()
                                .invisible()
                                .group_hover(PROJECT_GROUP, |d| d.visible())
                                .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                                .child(
                                    Button::new(SharedString::from(format!("new-in-{key}")))
                                        .ghost()
                                        .xsmall()
                                        .icon(IconName::Plus)
                                        .tooltip(format!("New Session in {name}"))
                                        .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| {
                                            cx.stop_propagation();
                                            this.new_session_on(ix, Some(pid.clone()), window, cx)
                                        })),
                                ),
                        )
                        .when(has, |d| {
                            d.child(
                                Icon::new(if folded {
                                    IconName::ChevronRight
                                } else {
                                    IconName::ChevronDown
                                })
                                .xsmall()
                                .text_color(p.overlay),
                            )
                        })
                        .when(has, |d| {
                            d.on_click(cx.listener(move |_, _: &ClickEvent, _, cx| {
                                let k = k.clone();
                                update_prefs(cx, |p| {
                                    if p.folded_projects.contains(&k) {
                                        p.folded_projects.retain(|x| *x != k);
                                    } else {
                                        p.folded_projects.push(k);
                                    }
                                });
                            }))
                        })
                        .context_menu(self.project_menu(ix, project.id.clone(), cx)),
                );
                if !folded {
                    let rows: Vec<SessionRow> = needs_you_first(&sessions)
                        .into_iter()
                        .map(|session| SessionRow {
                            conn: ix,
                            server_id: server_id.clone(),
                            session,
                        })
                        .collect();
                    let mut inner = v_flex().id(SharedString::from(format!("sessions-{key}"))).gap_0p5();
                    inner = self.render_rows_in(&rows, selected.as_deref(), false, PROJECT_INDENT, inner, cx);
                    list = list.child(inner);
                }
            }
            if !archived.is_empty() {
                let open = self.archives_open.contains(&server_id);
                let sid = server_id.clone();
                list = list.child(
                    crate::ui::common::disclosure(
                        format!("archived-{server_id}"),
                        open,
                        &format!("Archived ({})", archived.len()),
                        &p,
                    )
                    .px_2()
                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                        if !this.archives_open.remove(&sid) {
                            this.archives_open.insert(sid.clone());
                        }
                        cx.notify();
                    })),
                );
                if open {
                    let rows: Vec<SessionRow> = archived
                        .into_iter()
                        .map(|session| SessionRow {
                            conn: ix,
                            server_id: server_id.clone(),
                            session,
                        })
                        .collect();
                    let mut inner = v_flex()
                        .id(SharedString::from(format!("archived-list-{server_id}")))
                        .gap_0p5();
                    inner = self.render_rows_in(&rows, selected.as_deref(), false, PROJECT_INDENT, inner, cx);
                    list = list.child(inner);
                }
            }
        }
        list.into_any_element()
    }

    #[allow(clippy::too_many_arguments)]
    fn collection_row(
        &self,
        id: SharedString,
        icon: Icon,
        title: String,
        count: Option<usize>,
        selected: bool,
        server: Option<String>,
        c: Collection,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> gpui_kit::Stateful<gpui_kit::Div> {
        h_flex()
            .id(id)
            .gap_2()
            .px_2()
            .py_1()
            .rounded_md()
            .cursor_pointer()
            .when(selected, |d| d.bg(p.surface0))
            .when(!selected, |d| d.hover(|d| d.bg(p.surface0.opacity(0.5))))
            .child(icon.small())
            .child(div().flex_1().min_w_0().truncate().text_sm().child(title))
            .when_some(count, |d, n| d.child(caption(n.to_string(), p)))
            .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| {
                this.choose_column(server.clone(), c.clone(), window, cx)
            }))
    }

    fn render_collections(&self, cx: &mut Context<Self>) -> AnyElement {
        let p = Palette::of(cx);
        let (cur_server, current) = self.column(cx);
        let rows = self.rows(cx);
        let needs = rows.iter().filter(|r| needs_you(&r.session)).count();
        let running = rows.iter().filter(|r| is_busy(&r.session)).count();
        let nonzero = |n: usize| (n > 0).then_some(n);
        let mut list = v_flex()
            .id("collections")
            .flex_1()
            .overflow_y_scroll()
            .px_2()
            .pb_2()
            .gap_0p5();
        list = list
            .child(self.collection_row(
                "c-needs".into(),
                Icon::new(L::Hand).text_color(p.yellow),
                "Needs You".into(),
                nonzero(needs),
                current == Collection::NeedsYou,
                None,
                Collection::NeedsYou,
                &p,
                cx,
            ))
            .child(self.collection_row(
                "c-running".into(),
                Icon::new(L::CirclePlay).text_color(p.blue),
                "Running".into(),
                nonzero(running),
                current == Collection::Running,
                None,
                Collection::Running,
                &p,
                cx,
            ))
            .child(self.collection_row(
                "c-all".into(),
                Icon::new(L::Inbox).text_color(p.overlay),
                "All Sessions".into(),
                nonzero(rows.len()),
                current == Collection::All,
                None,
                Collection::All,
                &p,
                cx,
            ));
        for ix in 0..self.conns.len() {
            let c = self.conns[ix].read(cx);
            let server_id = c.server_id.clone();
            let color = conn_color(c, &p);
            let name = c.name.clone();
            let active = c.index.active_sessions();
            let by_name = c.index.projects_by_name();
            list = list.child(
                h_flex()
                    .id(("col-server", ix))
                    .gap_2()
                    .px_2()
                    .pt_3()
                    .pb_1()
                    .child(
                        div()
                            .flex_1()
                            .truncate()
                            .text_xs()
                            .font_weight(FontWeight::SEMIBOLD)
                            .text_color(p.overlay)
                            .child(name),
                    )
                    .child(div().size(px(7.)).rounded_full().bg(color))
                    .context_menu(self.server_menu(ix, cx)),
            );
            if let Some(status) = self.status_row(ix, &p, cx) {
                list = list.child(status);
                continue;
            }
            for project in projects_in_order(&by_name, &active) {
                let sessions: Vec<Rc<Session>> =
                    active.iter().filter(|s| s.project_id == project.id).cloned().collect();
                let pressing = most_pressing(sessions.iter()).cloned();
                let coll = Collection::Project(project.id.clone());
                let selected = current == coll && cur_server.as_deref() == Some(server_id.as_str());
                let row = self
                    .collection_row(
                        SharedString::from(format!("c-{server_id}-{}", project.id)),
                        Icon::new(L::Folder).text_color(p.overlay),
                        project.name.clone(),
                        Some(sessions.len()),
                        selected,
                        Some(server_id.clone()),
                        coll,
                        &p,
                        cx,
                    )
                    .when_some(pressing.filter(|s| urgency(s) > 0), |d, s| d.child(status_dot(&s, &p)))
                    .context_menu(self.project_menu(ix, project.id.clone(), cx));
                list = list.child(row);
            }
        }
        let archived: usize = self
            .conns
            .iter()
            .map(|c| c.read(cx).index.archived_sessions().len())
            .sum();
        if archived > 0 {
            list = list.child(div().pt_3()).child(self.collection_row(
                "c-archived".into(),
                Icon::new(L::Archive).text_color(p.overlay),
                "Archived".into(),
                Some(archived),
                current == Collection::Archived,
                None,
                Collection::Archived,
                &p,
                cx,
            ));
        }
        list.into_any_element()
    }

    /// The Columns layout's middle column: the collection's sessions.
    pub(crate) fn render_middle(&self, window: &mut Window, cx: &mut Context<Self>) -> AnyElement {
        let p = Palette::of(cx);
        let (server, column) = self.column(cx);
        let title = match (&column, &server) {
            (Collection::Project(_), Some(s)) => self
                .conns
                .iter()
                .find(|c| c.read(cx).server_id == *s)
                .map(|c| column.title(&c.read(cx).index))
                .unwrap_or_else(|| "Project".into()),
            _ => match column {
                Collection::NeedsYou => "Needs You".into(),
                Collection::Running => "Running".into(),
                Collection::Archived => "Archived".into(),
                _ => "All Sessions".into(),
            },
        };
        let rows = self.column_sessions(cx);
        let selected = self.selected_id(cx);
        let searching = search_active(&self.search.query);
        let mut list = v_flex()
            .id("column-list")
            .flex_1()
            .overflow_y_scroll()
            .px_2()
            .pb_2()
            .gap_0p5();
        if searching {
            return v_flex()
                .relative()
                .w(px(self.middle_width))
                .h_full()
                .flex_shrink_0()
                .border_r_1()
                .border_color(p.crust)
                .child(self.middle_header(title, rows.len(), &p, window, cx))
                .child(self.render_search(cx))
                .child(panes::edge(Pane::Middle, &p, cx))
                .into_any_element();
        }
        if rows.is_empty() {
            let (head, sub) = match column {
                Collection::NeedsYou => (
                    "Nothing needs you",
                    "Sessions that wait for your answer, or failed, show here.",
                ),
                Collection::Running => ("Nothing is running", "Sessions show here while their agent works."),
                Collection::Archived => ("Nothing archived", ""),
                _ => ("No sessions", "Start one with the New Session button."),
            };
            list = list.child(
                v_flex()
                    .items_center()
                    .gap_1()
                    .pt_10()
                    .child(div().text_sm().text_color(p.subtext).child(head))
                    .child(caption(sub, &p).text_center()),
            );
        }
        list = self.render_rows(&rows, selected.as_deref(), true, list, cx);
        v_flex()
            .relative()
            .w(px(self.middle_width))
            .h_full()
            .flex_shrink_0()
            .border_r_1()
            .border_color(p.crust)
            .child(self.middle_header(title, rows.len(), &p, window, cx))
            .child(list)
            .child(panes::edge(Pane::Middle, &p, cx))
            .into_any_element()
    }

    fn middle_header(
        &self,
        title: String,
        n: usize,
        p: &Palette,
        window: &Window,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        v_flex()
            .child(top_bar(
                "middle-bar",
                false,
                p.base,
                None,
                window,
                h_flex()
                    .w_full()
                    .gap_2()
                    .child(
                        v_flex()
                            .flex_1()
                            .min_w_0()
                            .child(
                                div()
                                    .text_sm()
                                    .font_weight(FontWeight::SEMIBOLD)
                                    .truncate()
                                    .child(title),
                            )
                            .child(caption(
                                if n == 1 {
                                    "1 session".to_string()
                                } else {
                                    format!("{n} sessions")
                                },
                                p,
                            )),
                    )
                    .child(self.new_session_menu("new-session-col", cx)),
            ))
            .child(self.render_search_field(false, cx))
            .into_any_element()
    }

    pub(crate) fn render_pair(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        v_flex().size_full().items_center().justify_center().px_6().child(
            v_flex()
                .w_full()
                .max_w(px(520.))
                .gap_3()
                .p_6()
                .rounded_lg()
                .bg(p.mantle)
                .border_1()
                .border_color(p.surface0)
                .child(
                    div()
                        .text_lg()
                        .font_weight(FontWeight::SEMIBOLD)
                        .child(if self.conns.is_empty() {
                            "No servers"
                        } else {
                            "Pair a server"
                        }),
                )
                .child(div().text_sm().text_color(p.subtext).child(
                    "Run `rad serve` on your computer, then `rad pair --print-url`, and paste the link it prints.",
                ))
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
                )
                .child(div().h(px(1.)).my_2().bg(p.surface0))
                .child(
                    div()
                        .font_weight(FontWeight::SEMIBOLD)
                        .child("Install on a server over SSH"),
                )
                .child(div().text_sm().text_color(p.subtext).child(
                    "Without rad, the server gets the latest release in ~/.local/bin, running as a background \
                     service; then this Mac pairs with it. `ssh <host>` has to work without a password.",
                ))
                .child(
                    h_flex()
                        .gap_2()
                        .child(
                            div()
                                .flex_1()
                                .child(Input::new(&self.ssh_input).disabled(self.ssh_install.running)),
                        )
                        .child(
                            Button::new("install-ssh")
                                .label(if self.ssh_install.running {
                                    "Installing…"
                                } else {
                                    "Install and Pair"
                                })
                                .loading(self.ssh_install.running)
                                .on_click(
                                    cx.listener(|this, _: &ClickEvent, window, cx| this.install_over_ssh(window, cx)),
                                ),
                        ),
                )
                .child(self.render_ssh_steps(&p)),
        )
    }

    /// Install on a server over SSH's steps: done, running, or failed, and why.
    fn render_ssh_steps(&self, p: &Palette) -> impl IntoElement {
        let state = &self.ssh_install;
        let last = state.steps.len().saturating_sub(1);
        v_flex()
            .gap_1()
            .children(state.steps.iter().enumerate().map(|(i, step)| {
                let icon = if i < last {
                    Icon::new(IconName::CircleCheck)
                        .small()
                        .text_color(p.green)
                        .into_any_element()
                } else if state.running {
                    Spinner::new().small().into_any_element()
                } else {
                    Icon::new(IconName::CircleX)
                        .small()
                        .text_color(p.red)
                        .into_any_element()
                };
                h_flex()
                    .gap_2()
                    .text_sm()
                    .child(icon)
                    .child(div().flex_1().min_w_0().child(step.clone()))
            }))
            .when_some(state.error.clone(), |d, e| {
                d.child(div().text_sm().text_color(p.red).child(e))
            })
    }
}

/// What a session is doing, for the Columns list.
fn activity(s: &Session, c: &ServerConn, p: &Palette) -> AnyElement {
    let (text, color) = match s.status {
        SessionStatus::AwaitingApproval => ("Waiting for your answer".to_string(), p.yellow),
        SessionStatus::Error => (s.error.clone().unwrap_or_else(|| "Failed".into()), p.red),
        SessionStatus::Running => ("Working…".into(), p.blue),
        SessionStatus::Stopped => ("Stopped".into(), p.overlay),
        _ if !s.background_tasks.is_empty() => (
            format!("In the background: {}", s.background_tasks[0].description),
            p.blue,
        ),
        _ => {
            let model = s
                .model_info
                .as_ref()
                .and_then(|m| m.name.clone().or(m.id.clone()))
                .or(s.model.clone())
                .or_else(|| c.harness(&s.harness).map(|h| h.name.clone()))
                .unwrap_or_else(|| s.harness.clone());
            let context = s.context.as_ref().and_then(|c| {
                c.window
                    .filter(|w| *w > 0)
                    .map(|w| ((c.used as f64 / w as f64).min(1.) * 100.).round())
            });
            (
                match context {
                    Some(pct) => format!("{model} · {pct}% context"),
                    None => model,
                },
                p.overlay,
            )
        }
    };
    div()
        .text_xs()
        .text_color(color)
        .line_clamp(2)
        .child(text)
        .into_any_element()
}

/// The name the session rows' hover styles look up.
const ROW_GROUP: &str = "session-row";

/// The same for project headers.
const PROJECT_GROUP: &str = "project-head";

/// How far Projects indents its sessions: their titles line up with the
/// project's name, past its badge.
const PROJECT_INDENT: f32 = 24.;

/// The session's state as a dot and the title's colour; none when there's
/// nothing to say.
fn row_state(s: &Session, p: &Palette) -> Option<(AnyElement, Hsla)> {
    let dot = div().size(px(7.)).rounded_full().flex_shrink_0();
    match s.status {
        SessionStatus::AwaitingApproval => Some((dot.bg(p.yellow).into_any_element(), p.yellow)),
        SessionStatus::Error => Some((dot.bg(p.red).into_any_element(), p.red)),
        _ if is_busy(s) => Some((busy_dot(dot, p.blue), p.blue)),
        _ if s.unread => Some((dot.bg(p.accent).into_any_element(), p.text)),
        _ => None,
    }
}

/// Archives a session at once: the row's ✓, which doesn't ask.
fn archive_now(conn: Entity<ServerConn>, id: String, window: &mut Window, cx: &mut App) {
    let call = conn
        .read(cx)
        .call("session.archive", json!({"sessionId": id, "removeWorktree": false}));
    window
        .spawn(cx, async move |cx| {
            if let Err(e) = call.await {
                let _ =
                    cx.update(|window, cx| dialogs::alert(window, cx, "Couldn't archive the session", &explain(&e)));
            }
        })
        .detach();
}

/// What a session row's hover card shows.
struct Card {
    title: String,
    project: String,
    server: String,
    branch: Option<String>,
    harness: String,
    model: String,
    context: Option<f64>,
    pr: Option<(u32, String)>,
}

impl Card {
    fn render(&self, cx: &App) -> AnyElement {
        let p = Palette::of(cx);
        let line = |icon: AnyElement, text: String| {
            h_flex()
                .gap_2()
                .text_sm()
                .text_color(p.subtext)
                .child(div().w(px(16.)).flex().justify_center().child(icon))
                .child(div().min_w_0().truncate().child(text))
        };
        let model = match self.context {
            Some(pct) => format!("{} · {pct}% context", self.model),
            None => self.model.clone(),
        };
        v_flex()
            .w(px(320.))
            .gap_1p5()
            .p_1()
            .child(
                div()
                    .pb_0p5()
                    .text_sm()
                    .font_weight(FontWeight::SEMIBOLD)
                    .text_color(p.text)
                    .line_clamp(2)
                    .child(self.title.clone()),
            )
            .when(!self.project.is_empty(), |d| {
                d.child(line(
                    badges::project_badge(&self.project, 14., &p).into_any_element(),
                    self.project.clone(),
                ))
            })
            .child(line(
                Icon::new(L::Server).small().text_color(p.overlay).into_any_element(),
                self.server.clone(),
            ))
            .when_some(self.branch.clone(), |d, b| {
                d.child(line(
                    Icon::new(L::GitBranch).small().text_color(p.overlay).into_any_element(),
                    b,
                ))
            })
            .child(line(
                badges::harness_icon(&self.harness, &p).small().into_any_element(),
                model,
            ))
            .when_some(self.pr.clone(), |d, (n, title)| {
                d.child(div().my_1().h(px(1.)).bg(p.surface0)).child(
                    h_flex()
                        .gap_2()
                        .text_sm()
                        .text_color(p.subtext)
                        .child(
                            div()
                                .w(px(16.))
                                .flex()
                                .justify_center()
                                .child(Icon::new(L::GitPullRequest).small().text_color(p.mauve)),
                        )
                        .child(div().flex_shrink_0().text_color(p.overlay).child(format!("#{n}")))
                        .child(div().min_w_0().truncate().child(title)),
                )
            })
            .into_any_element()
    }
}
