//! The session's header, its ⋯ menu, and the view as a whole.

use gpui_kit::assets::IconName as L;
use gpui_kit::component::button::{Button, ButtonVariants as _};
use gpui_kit::component::input::Input;
use gpui_kit::component::menu::{ContextMenuExt as _, DropdownMenu as _, PopupMenu, PopupMenuItem};
use gpui_kit::component::{Icon, IconName, Selectable as _, Sizable as _, WindowExt as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    Anchor, AnyElement, App, ClickEvent, ClipboardItem, Context, ExternalPaths, InteractiveElement as _, IntoElement,
    KeyDownEvent, ParentElement as _, Render, SharedString, StatefulInteractiveElement as _, Styled as _, Window, div,
    list, px,
};
use serde_json::json;

use super::SessionView;
use super::dock::Dock;
use crate::protocol::{PullRequest, SessionStatus, TurnStatus, WorkspaceKind};
use crate::ui::badges::{self, project_badge};
use crate::ui::common::{busy_dot, caption, sidebar_toggle, top_bar};
use crate::ui::palette::Palette;
use crate::ui::state::prefs;
use crate::ui::{actions, commit, diff, fork};

impl SessionView {
    pub fn can_commit(&self, cx: &App) -> bool {
        let commit = self.conn.read(cx).info.as_ref().is_some_and(|i| i.commit);
        commit && self.project(cx).is_some_and(|p| p.is_git_repo) && self.session(cx).is_some_and(|s| !s.archived)
    }

    pub fn can_fork(&self, cx: &App) -> bool {
        let forks = self.conn.read(cx).info.as_ref().is_some_and(|i| i.forks);
        forks
            && self.store(cx).is_some_and(|s| {
                s.turns
                    .values()
                    .any(|t| t.status != crate::protocol::TurnStatus::Running)
            })
    }

    /// The latest turn failed, and nothing was tried since.
    pub(crate) fn last_turn_failed(&self, cx: &App) -> bool {
        self.store(cx)
            .and_then(|s| s.turns.values().max_by_key(|t| t.n))
            .is_some_and(|t| t.status == TurnStatus::Failed)
    }

    pub fn can_rename(&self, cx: &App) -> bool {
        self.conn.read(cx).info.as_ref().is_some_and(|i| i.rename)
    }

    pub fn is_git(&self, cx: &App) -> bool {
        self.project(cx).is_some_and(|p| p.is_git_repo)
    }

    pub fn open_changes(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if let Some(s) = self.session(cx).filter(|_| self.is_git(cx)) {
            diff::open(self.conn.clone(), s, window, cx);
        }
    }

    pub fn open_commit(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.can_commit(cx) && !self.is_running(cx) {
            commit::open(self.conn.clone(), self.session_id.clone(), window, cx);
        }
    }

    pub fn open_fork(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if let Some(s) = self.session(cx).filter(|_| self.can_fork(cx)) {
            fork::open(self.conn.clone(), s, window, cx);
        }
    }

    /// The room the session has: the window without the sidebar's columns.
    fn width(&self, window: &Window, cx: &App) -> f32 {
        let prefs = prefs(cx);
        let mut width = window.viewport_size().width.as_f32();
        if !prefs.sidebar_hidden {
            width -= prefs.sidebar_width;
            if prefs.sidebar_layout == "columns" {
                width -= prefs.column_width;
            }
        }
        width
    }

    /// The session's bar atop the window: its title, where it works, its
    /// state, and what's done with its work.
    fn render_header(&mut self, window: &mut Window, cx: &mut Context<Self>) -> AnyElement {
        let p = Palette::of(cx);
        let hidden = prefs(cx).sidebar_hidden;
        let Some(s) = self.session(cx) else {
            return top_bar(
                "session-bar",
                hidden,
                p.base,
                Some(p.surface0),
                window,
                h_flex()
                    .gap_2()
                    .when(hidden, |d| d.child(sidebar_toggle(true)))
                    .child(div().text_sm().text_color(p.overlay).child("Loading…")),
            )
            .into_any_element();
        };
        let project = self.project(cx).map(|p| p.name.clone());
        let running = self.is_running(cx);
        let git = self.is_git(cx);
        let (status, color) = match s.status {
            _ if s.archived => ("Archived", p.overlay),
            SessionStatus::Running => ("Working", p.blue),
            SessionStatus::AwaitingApproval => ("Needs you", p.yellow),
            SessionStatus::Error => ("Failed", p.red),
            SessionStatus::Stopped => ("Stopped", p.overlay),
            // rad leaves a session idle after a turn that failed.
            _ if self.last_turn_failed(cx) => ("Failed", p.red),
            _ => ("Idle", p.overlay),
        };
        // A narrow session keeps its title: first the project and the
        // state's name go, then the buttons, which the menu has too.
        let width = self.width(window, cx);
        let (roomy, buttons) = (width >= 620., width >= 470.);
        let dot = div().size(px(7.)).rounded_full().flex_shrink_0();
        let dot = if s.status == SessionStatus::Running && !s.archived {
            busy_dot(dot, color)
        } else {
            dot.bg(color).into_any_element()
        };
        let content = h_flex()
            .w_full()
            .gap_2()
            .when(hidden, |d| d.child(sidebar_toggle(true)))
            // "[L] project / title", as a path to the session.
            .child(
                h_flex()
                    .flex_1()
                    .min_w_0()
                    .gap_2()
                    .text_sm()
                    // In line with the transcript under it.
                    .when(!hidden, |d| d.pl_2())
                    .when_some(project.filter(|_| roomy), |d, name| {
                        d.child(project_badge(&name, 16., &p))
                            .child(div().flex_shrink_0().text_color(p.subtext).child(name))
                            .child(div().flex_shrink_0().text_color(p.overlay).child("/"))
                    })
                    .child(
                        div()
                            .min_w_0()
                            .truncate()
                            .text_color(p.text)
                            .child(if s.title.is_empty() {
                                "Session".to_string()
                            } else {
                                s.title.clone()
                            }),
                    ),
            )
            .child(
                h_flex()
                    .id("status")
                    .gap_1p5()
                    .flex_shrink_0()
                    .child(dot)
                    .when(roomy, |d| d.child(div().text_xs().text_color(p.subtext).child(status)))
                    .when(!roomy, |d| {
                        d.tooltip(move |window, cx| {
                            gpui_kit::component::tooltip::Tooltip::new(status).build(window, cx)
                        })
                    }),
            )
            .when(buttons, |d| {
                d.child(
                    h_flex()
                        .gap_0p5()
                        .flex_shrink_0()
                        .when_some(s.pull_request.as_ref(), |d, pr| d.child(pull_request_button(pr, &p)))
                        .when(git, |d| d.child(self.changes_button(&p, cx)))
                        .when(self.can_commit(cx), |d| {
                            d.child(
                                Button::new("commit")
                                    .small()
                                    .ghost()
                                    .icon(L::GitCommitHorizontal)
                                    .tooltip("Commit… (⌥⌘C)")
                                    .disabled(running)
                                    .on_click(
                                        cx.listener(|this, _: &ClickEvent, window, cx| this.open_commit(window, cx)),
                                    ),
                            )
                        })
                        .when(self.can_terminal(cx), |d| {
                            d.child(
                                Button::new("terminal")
                                    .small()
                                    .ghost()
                                    .icon(L::SquareTerminal)
                                    .selected(self.terminal_shown)
                                    .tooltip(if self.terminal_shown {
                                        "Hide Terminal (⌘J)"
                                    } else {
                                        "Terminal (⌘J)"
                                    })
                                    .on_click(
                                        cx.listener(|this, _: &ClickEvent, window, cx| {
                                            this.toggle_terminal(window, cx)
                                        }),
                                    ),
                            )
                        }),
                )
            })
            .child(self.render_menu(cx));
        top_bar("session-bar", hidden, p.base, Some(p.surface0), window, content).into_any_element()
    }

    /// Changes, with what the session added and removed once that's counted.
    fn changes_button(&self, p: &Palette, cx: &mut Context<Self>) -> Button {
        let counted = self.changes.filter(|(files, _, _)| *files > 0);
        Button::new("changes")
            .small()
            .ghost()
            .icon(L::Diff)
            .tooltip(match counted {
                Some((1, _, _)) => "Changes: 1 file".to_string(),
                Some((n, _, _)) => format!("Changes: {n} files"),
                None => "Changes".to_string(),
            })
            // Binary files change no lines.
            .when_some(
                counted.filter(|(_, adds, dels)| adds + dels > 0),
                |b, (_, adds, dels)| {
                    b.child(
                        h_flex()
                            .gap_1()
                            .text_xs()
                            .font_family(crate::ui::fonts::code())
                            .child(div().text_color(p.green).child(format!("+{adds}")))
                            .child(div().text_color(p.red).child(format!("−{dels}"))),
                    )
                },
            )
            .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.open_changes(window, cx)))
    }

    fn render_menu(&mut self, cx: &mut Context<Self>) -> AnyElement {
        let this = cx.entity().downgrade();
        Button::new("session-menu")
            .small()
            .ghost()
            .icon(IconName::Ellipsis)
            .dropdown_menu_with_anchor(Anchor::TopRight, move |mut menu, window, cx| {
                menu = menu.max_w(px(320.));
                let Some(view) = this.upgrade() else { return menu };
                let v = view.read(cx);
                let Some(s) = v.session(cx) else { return menu };
                let conn = v.conn.clone();
                let harness = conn.read(cx).harness(&s.harness).cloned();
                let running = v.is_running(cx);
                let project = v.project(cx);
                let (git, commit, fork, rename, terminal) = (
                    v.is_git(cx),
                    v.can_commit(cx),
                    v.can_fork(cx),
                    v.can_rename(cx),
                    v.can_terminal(cx),
                );
                let terminal_shown = v.terminal_shown;
                let unarchive = conn.read(cx).info.as_ref().is_some_and(|i| i.unarchive);
                let act = |f: fn(&mut SessionView, &mut Window, &mut Context<SessionView>)| {
                    let w = this.clone();
                    move |_: &ClickEvent, window: &mut Window, cx: &mut App| {
                        let _ = w.update(cx, |v, cx| f(v, window, cx));
                    }
                };
                if let Some(h) = harness.filter(|h| !h.modes.is_empty()) {
                    let (w, sid, current) = (this.clone(), s.id.clone(), s.mode.clone());
                    menu = menu.submenu("Permissions", window, cx, move |mut sub, _, _| {
                        for m in &h.modes {
                            let (w, sid, id) = (w.clone(), sid.clone(), m.id.clone());
                            sub = sub.item(
                                PopupMenuItem::new(m.name.clone())
                                    .checked(current.as_deref() == Some(m.id.as_str()))
                                    .on_click(move |_, _, cx| {
                                        let _ = w.update(cx, |v, cx| {
                                            v.run("session.setMode", json!({"sessionId": sid, "mode": id}), cx)
                                        });
                                    }),
                            );
                        }
                        sub
                    });
                }
                menu = menu.item(
                    PopupMenuItem::new("Find in Session")
                        .icon(Icon::new(L::TextSearch))
                        .action(Box::new(crate::FindInSession))
                        .on_click(act(|v, w, cx| v.open_find(w, cx))),
                );
                if git {
                    menu = menu.item(
                        PopupMenuItem::new("Changes")
                            .icon(Icon::new(L::Diff))
                            .on_click(act(|v, w, cx| v.open_changes(w, cx))),
                    );
                }
                if commit {
                    menu = menu.item(
                        PopupMenuItem::new("Commit…")
                            .icon(Icon::new(L::GitCommitHorizontal))
                            .action(Box::new(crate::CommitSession))
                            .disabled(running)
                            .on_click(act(|v, w, cx| v.open_commit(w, cx))),
                    );
                }
                if terminal {
                    menu = menu.item(
                        PopupMenuItem::new(if terminal_shown { "Hide Terminal" } else { "Terminal" })
                            .icon(Icon::new(L::SquareTerminal))
                            .action(Box::new(crate::ToggleTerminal))
                            .on_click(act(|v, w, cx| v.toggle_terminal(w, cx))),
                    );
                }
                if rename {
                    menu = menu.item(
                        PopupMenuItem::new("Rename…")
                            .icon(Icon::new(L::Pencil))
                            .on_click(act(|v, w, cx| v.rename(w, cx))),
                    );
                }
                if fork {
                    menu = menu.item(
                        PopupMenuItem::new("Fork…")
                            .icon(Icon::new(L::GitFork))
                            .on_click(act(|v, w, cx| v.open_fork(w, cx))),
                    );
                }
                if let Some(pr) = s.pull_request.clone() {
                    menu = menu.separator().label(pr.headline());
                    menu = pull_request_items(menu, &pr);
                }
                menu = menu.separator();
                if !s.archived {
                    let (c, s2) = (conn.clone(), s.clone());
                    menu = menu.item(
                        PopupMenuItem::new("Archive…")
                            .icon(Icon::new(L::Archive))
                            .on_click(move |_, window, cx| actions::archive(c.clone(), s2.clone(), window, cx)),
                    );
                } else if unarchive {
                    let (c, s2) = (conn.clone(), s.clone());
                    menu = menu.item(
                        PopupMenuItem::new("Unarchive")
                            .icon(Icon::new(L::ArchiveRestore))
                            .on_click(move |_, window, cx| actions::unarchive(c.clone(), s2.clone(), window, cx)),
                    );
                }
                {
                    let (c, s2) = (conn.clone(), s.clone());
                    menu = menu.item(
                        PopupMenuItem::new("Delete…")
                            .icon(Icon::new(L::Trash))
                            .on_click(move |_, window, cx| actions::delete(c.clone(), s2.clone(), window, cx)),
                    );
                }
                // Where it works, for a window too narrow for the bar to say.
                if let Some(project) = project {
                    menu = menu.separator().label(project.name.clone());
                    if git {
                        menu = menu.label(s.workspace.branch.clone().unwrap_or_else(|| "Detached HEAD".into()));
                    }
                    if s.workspace.kind == WorkspaceKind::Worktree {
                        menu = menu.label("Worktree");
                    }
                }
                if running {
                    menu = menu.separator().item(
                        PopupMenuItem::new("Force stop agent")
                            .icon(Icon::new(L::OctagonX))
                            .on_click(act(|v, _, cx| v.interrupt(true, cx))),
                    );
                }
                menu
            })
            .into_any_element()
    }

    fn render_find(&mut self, cx: &mut Context<Self>) -> Option<AnyElement> {
        let p = Palette::of(cx);
        let f = self.find.as_ref()?;
        let blank = f.input.read(cx).value().trim().is_empty();
        let count = if blank {
            String::new()
        } else if f.matches.is_empty() {
            "No matches".into()
        } else {
            format!("{} of {}", f.current.map(|c| c + 1).unwrap_or(0), f.matches.len())
        };
        let any = !f.matches.is_empty();
        Some(
            h_flex()
                .px_5()
                .py_1p5()
                .gap_2()
                .border_b_1()
                .border_color(p.surface0)
                .bg(p.mantle)
                .key_context("FindBar")
                .on_key_down(cx.listener(|this, ev: &KeyDownEvent, window, cx| {
                    if ev.keystroke.key == "escape" {
                        this.close_find(window, cx);
                        cx.stop_propagation();
                    }
                }))
                // At the right, as a Mac window's find field is.
                .justify_end()
                .child(caption(count, &p))
                .child(div().w(px(260.)).child(Input::new(&f.input).small().cleanable(true)))
                .child(
                    Button::new("find-prev")
                        .xsmall()
                        .ghost()
                        .icon(IconName::ChevronUp)
                        .tooltip("Previous match (⇧Return)")
                        .disabled(!any)
                        .on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.find_step(false, cx))),
                )
                .child(
                    Button::new("find-next")
                        .xsmall()
                        .ghost()
                        .icon(IconName::ChevronDown)
                        .tooltip("Next match (Return)")
                        .disabled(!any)
                        .on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.find_step(true, cx))),
                )
                .child(
                    Button::new("find-done")
                        .xsmall()
                        .ghost()
                        .label("Done")
                        .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.close_find(window, cx))),
                )
                .into_any_element(),
        )
    }
}

use gpui_kit::component::Disableable as _;

/// The session's pull request in its bar: its glyph and number in its
/// state's colour, and how its checks went. A click opens it; right-click
/// offers to open it or copy its link, and its tooltip says where it stands.
fn pull_request_button(pr: &PullRequest, p: &Palette) -> AnyElement {
    let url = pr.url.clone();
    let menu_pr = pr.clone();
    div()
        .id("pull-request")
        .flex()
        .child(
            Button::new("pull-request-button")
                .small()
                .ghost()
                .accessibility_label(pr.accessibility_label())
                .tooltip(format!("{}\n{}", pr.headline(), pr.status_line()))
                .child(
                    h_flex()
                        .gap_1()
                        .text_xs()
                        .child(badges::pull_request_icon(pr, p).small())
                        .child(
                            div()
                                .text_color(badges::pull_request_color(pr, p))
                                .child(format!("#{}", pr.number)),
                        )
                        .when_some(badges::checks_mark(pr, p), |d, mark| d.child(mark)),
                )
                .on_click(move |_: &ClickEvent, _, cx| cx.open_url(&url)),
        )
        .context_menu(move |menu, _, _| pull_request_items(menu, &menu_pr))
        .into_any_element()
}

/// Open on GitHub and Copy Link, for the bar's right-click and the ⋯ menu.
fn pull_request_items(menu: PopupMenu, pr: &PullRequest) -> PopupMenu {
    let (open, copy) = (pr.url.clone(), pr.url.clone());
    menu.item(
        PopupMenuItem::new(format!("Open on {}", pr.forge_name()))
            .icon(Icon::new(L::ExternalLink))
            .on_click(move |_, _, cx| cx.open_url(&open)),
    )
    .item(
        PopupMenuItem::new("Copy Link")
            .icon(Icon::new(L::Link))
            .on_click(move |_, _, cx| cx.write_to_clipboard(ClipboardItem::new_string(copy.clone()))),
    )
}

impl Render for SessionView {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        // Changes and Commit are dialogs: one that closed may have changed the count.
        let dialog = window.has_active_dialog(cx);
        if self.had_dialog && !dialog {
            self.count_changes(true, cx);
        }
        self.had_dialog = dialog;
        let terminal = self
            .terminal
            .clone()
            .filter(|_| self.terminal_shown && self.can_terminal(cx));
        let placement = prefs(cx).terminal_placement.clone();
        let narrow = window.viewport_size().width < px(900.);
        let placement = if narrow { "over".to_string() } else { placement };
        let transcript = list(
            self.list.clone(),
            cx.processor(|this, ix, window, cx| this.render_row(ix, window, cx)),
        )
        .size_full()
        .py_3();
        let error = self.error.clone();
        let find = self.render_find(cx);
        let composer = self.render_composer(window, cx);
        let header = self.render_header(window, cx);
        // Over the messages, the terminal takes the transcript's place.
        let over = terminal
            .clone()
            .filter(|_| placement != "below" && placement != "right");
        let center = match over {
            Some(t) => div().flex_1().min_h_0().child(t),
            None => div().flex_1().min_h_0().child(transcript),
        };
        let messages = v_flex()
            .flex_1()
            .min_h_0()
            .min_w_0()
            .when_some(find, |d, f| d.child(f))
            .child(center)
            .when_some(error, |d, e| {
                d.child(
                    h_flex()
                        .mx_5()
                        .mb_1()
                        .gap_2()
                        .text_sm()
                        .text_color(p.red)
                        .child(div().flex_1().child(e))
                        .child(
                            Button::new("dismiss-error")
                                .xsmall()
                                .ghost()
                                .icon(IconName::Close)
                                .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                                    this.error = None;
                                    cx.notify();
                                })),
                        ),
                )
            })
            .child(composer);
        let body = match (terminal, placement.as_str()) {
            (Some(t), "below") => {
                self.render_docked(Dock::Below, messages.into_any_element(), t.into_any_element(), cx)
            }
            (Some(t), "right") => {
                self.render_docked(Dock::Right, messages.into_any_element(), t.into_any_element(), cx)
            }
            _ => messages.into_any_element(),
        };
        v_flex()
            .size_full()
            .key_context("SessionView")
            .on_drop(cx.listener(|this, paths: &ExternalPaths, window, cx| this.drop_paths(paths, window, cx)))
            .drag_over::<ExternalPaths>(move |style, _, _, _| style.bg(p.accent.opacity(0.06)))
            .child(header)
            .child(body)
    }
}

#[allow(dead_code)]
fn unused(_: SharedString) {}
