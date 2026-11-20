//! The session's header, its ⋯ menu, and the view as a whole.

use gpui_kit::assets::IconName as L;
use gpui_kit::component::button::{Button, ButtonVariants as _};
use gpui_kit::component::input::Input;
use gpui_kit::component::menu::{DropdownMenu as _, PopupMenuItem};
use gpui_kit::component::{Icon, IconName, Selectable as _, Sizable as _, WindowExt as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    Anchor, AnyElement, App, ClickEvent, Context, ExternalPaths, FontWeight, InteractiveElement as _, IntoElement,
    KeyDownEvent, ParentElement as _, Render, SharedString, StatefulInteractiveElement as _, Styled as _, Window, div,
    list, px,
};
use serde_json::json;

use super::SessionView;
use crate::protocol::{SessionStatus, TurnStatus, WorkspaceKind};
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

    /// "{model} · {effort} · {mode} · NN% context", with the project before it.
    fn subtitle(&self, cx: &App) -> String {
        let conn = self.conn.read(cx);
        let Some(s) = conn.session(&self.session_id) else {
            return String::new();
        };
        let harness = conn.harness(&s.harness);
        let info = s.model_info.clone().unwrap_or_default();
        let model = info.name.clone().or_else(|| {
            let id = info.id.clone().or_else(|| s.model.clone())?;
            Some(harness.map(|h| h.model_name(&id)).unwrap_or(id))
        });
        let model = model
            .or_else(|| harness.map(|h| h.name.clone()))
            .unwrap_or_else(|| s.harness.clone());
        let mut parts = vec![model];
        if let Some(e) = info.effort.clone().or_else(|| s.effort.clone()) {
            parts.push(e);
        }
        if let Some(m) = &s.mode {
            parts.push(
                harness
                    .and_then(|h| h.modes.iter().find(|c| &c.id == m).map(|c| c.name.clone()))
                    .unwrap_or_else(|| m.clone()),
            );
        }
        if let Some(c) = &s.context
            && let Some(w) = c.window.filter(|w| *w > 0)
        {
            let pct = ((c.used as f64 / w as f64).min(1.) * 100.).round();
            parts.push(format!("{pct}% context"));
        }
        let tail = parts.join(" · ");
        match conn.index.projects.get(&s.project_id) {
            Some(p) => format!("{} — {tail}", p.name),
            None => tail,
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
        let subtitle = self.subtitle(cx);
        let git = self.is_git(cx);
        let worktree = s.workspace.kind == WorkspaceKind::Worktree;
        let running = self.is_running(cx);
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
        let path = s
            .workspace
            .agent_path
            .clone()
            .unwrap_or_else(|| s.workspace.path.clone());
        // A narrow session keeps its title: first the folder and the state's
        // name go, then the buttons, which the menu has too.
        let width = self.width(window, cx);
        let (roomy, buttons) = (width >= 620., width >= 470.);
        let dot = div().size(px(7.)).rounded_full().flex_shrink_0();
        let dot = if s.status == SessionStatus::Running && !s.archived {
            busy_dot(dot, color)
        } else {
            dot.bg(color).into_any_element()
        };
        let content =
            h_flex()
                .w_full()
                .gap_2()
                .when(hidden, |d| d.child(sidebar_toggle(true)))
                .child(
                    v_flex()
                        .flex_1()
                        .min_w_0()
                        // In line with the transcript under it.
                        .when(!hidden, |d| d.pl_2())
                        .child(div().text_sm().font_weight(FontWeight::SEMIBOLD).truncate().child(
                            if s.title.is_empty() {
                                "Session".to_string()
                            } else {
                                s.title.clone()
                            },
                        ))
                        .child(div().text_xs().text_color(p.overlay).truncate().child(subtitle)),
                )
                .when(roomy && (git || worktree), |d| {
                    d.child(
                        h_flex()
                            .id("workspace")
                            .gap_1p5()
                            .max_w(px(280.))
                            .text_xs()
                            .text_color(p.subtext)
                            .when(worktree, |d| {
                                d.child(Icon::new(L::SquareDashed).xsmall().text_color(p.overlay))
                                    .child("Worktree")
                            })
                            .when(
                                s.workspace.branch.is_some() || s.workspace.kind == WorkspaceKind::Root,
                                |d| {
                                    d.child(Icon::new(L::GitBranch).xsmall().text_color(p.overlay)).child(
                                        div().truncate().child(
                                            s.workspace.branch.clone().unwrap_or_else(|| "Detached HEAD".into()),
                                        ),
                                    )
                                },
                            )
                            .tooltip(move |window, cx| {
                                gpui_kit::component::tooltip::Tooltip::new(path.clone()).build(window, cx)
                            }),
                    )
                })
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
                                            cx.listener(|this, _: &ClickEvent, window, cx| {
                                                this.open_commit(window, cx)
                                            }),
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
                                        .on_click(cx.listener(|this, _: &ClickEvent, window, cx| {
                                            this.toggle_terminal(window, cx)
                                        })),
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
                let height = prefs(cx).terminal_height.clamp(120., 900.);
                v_flex()
                    .flex_1()
                    .min_h_0()
                    .child(messages)
                    .child(
                        div()
                            .h(px(height))
                            .flex_shrink_0()
                            .border_t_1()
                            .border_color(p.surface0)
                            .child(t),
                    )
                    .into_any_element()
            }
            (Some(t), "right") => h_flex()
                .flex_1()
                .min_h_0()
                .items_start()
                .child(messages.h_full())
                .child(
                    div()
                        .w(px(440.))
                        .h_full()
                        .flex_shrink_0()
                        .border_l_1()
                        .border_color(p.surface0)
                        .child(t),
                )
                .into_any_element(),
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
