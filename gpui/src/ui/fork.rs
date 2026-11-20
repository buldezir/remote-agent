//! Fork: a new session that goes on from a turn of this one, with an agent,
//! a workspace and a prompt of its own. As `ForkSessionView.swift`.

use std::rc::Rc;
use std::time::Duration;

use gpui_kit::component::WindowExt as _;
use gpui_kit::component::button::{Button, ButtonVariants as _};
use gpui_kit::component::input::{Input, InputState, Textarea, TextareaState};
use gpui_kit::component::{Disableable as _, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    AppContext as _, ClickEvent, Context, Entity, FontWeight, InteractiveElement as _, IntoElement, ParentElement as _,
    Render, StatefulInteractiveElement as _, Styled as _, Subscription, Window, div, px,
};
use serde_json::json;

use crate::conn::ServerConn;
use crate::protocol::{ItemKind, Session, Turn, TurnStatus, WorkspaceKind};
use crate::store::prefs::AgentChoice;
use crate::ui::agent_picker::{AgentPicker, picker, row};
use crate::ui::common::{caption, first_line};
use crate::ui::palette::Palette;
use crate::ui::session::explain;
use crate::ui::state::app_view;

pub fn open(conn: Entity<ServerConn>, session: Rc<Session>, window: &mut gpui_kit::Window, cx: &mut gpui_kit::App) {
    let view = cx.new(|cx| ForkView::new(conn, session, window, cx));
    let size = window.viewport_size();
    window.open_dialog(cx, move |dialog, _, _| {
        dialog
            .w(px(600.))
            .title("Fork session")
            .child(div().h((size.height * 0.72).min(px(720.))).child(view.clone()))
    });
}

#[derive(Clone, Copy, PartialEq)]
enum Place {
    Project,
    NewWorktree,
    ThisWorktree,
}

pub struct ForkView {
    conn: Entity<ServerConn>,
    source: Rc<Session>,
    turns: Vec<(Rc<Turn>, String)>,
    turn: Option<i64>,
    agent: Entity<AgentPicker>,
    place: Place,
    branch: Entity<InputState>,
    prompt: Entity<TextareaState>,
    forking: bool,
    error: Option<String>,
    _subscriptions: Vec<Subscription>,
}

impl ForkView {
    fn new(conn: Entity<ServerConn>, source: Rc<Session>, window: &mut Window, cx: &mut Context<Self>) -> Self {
        // Turns that ended, newest first, each named by its prompt.
        let mut turns: Vec<(Rc<Turn>, String)> = Vec::new();
        if let Some(store) = conn.read(cx).stores.get(&source.id) {
            for t in store.turns.values().filter(|t| t.status != TurnStatus::Running) {
                let prompt = store
                    .transcript
                    .iter()
                    .find(|i| i.turn() == t.id && i.kind == ItemKind::UserMessage)
                    .and_then(|i| i.text.clone())
                    .map(|t| first_line(&t, 48))
                    .filter(|t| !t.is_empty());
                let label = match prompt {
                    Some(p) => format!("Turn {} · {p}", t.n),
                    None => format!("Turn {}", t.n),
                };
                turns.push((t.clone(), label));
            }
        }
        turns.sort_by_key(|t| std::cmp::Reverse(t.0.n));
        let agent = cx.new(|cx| AgentPicker::new(conn.clone(), window, cx));
        // The source's agent where it's usable, with its options.
        let source_choice = AgentChoice {
            harness: source.harness.clone(),
            model: source.model.clone(),
            effort: source.effort.clone(),
            mode: source.mode.clone(),
        };
        let usable = conn.read(cx).harness(&source.harness).is_some_and(|h| h.usable());
        let first = conn
            .read(cx)
            .harnesses
            .as_ref()
            .and_then(|l| l.iter().find(|h| h.usable()).map(|h| h.id.clone()));
        agent.update(cx, |a, cx| {
            if usable {
                a.choose(&source.harness, Some(&source_choice), window, cx);
            } else if let Some(h) = first {
                a.choose(&h, None, window, cx);
            }
        });
        // Another agent starts with its defaults; the source's, with the source's options.
        let source2 = source_choice.clone();
        let subs = vec![cx.observe(&agent, |_, _, cx| cx.notify()), {
            let mut last = agent.read(cx).harness.clone();
            cx.subscribe_in(
                &agent,
                window,
                move |_, agent, _: &crate::ui::agent_picker::AgentChanged, window, cx| {
                    let now = agent.read(cx).harness.clone();
                    if now != last {
                        last = now.clone();
                        if now.as_deref() == Some(source2.harness.as_str()) {
                            let s = source2.clone();
                            agent.update(cx, |a, cx| a.choose(&s.harness.clone(), Some(&s), window, cx));
                        }
                    }
                },
            )
        }];
        let branch = cx.new(|cx| InputState::new(window, cx).placeholder("auto"));
        let prompt = cx.new(|cx| {
            TextareaState::new(window, cx)
                .placeholder("What should the agent do next?")
                .auto_grow(4, 12)
        });
        let place = if source.workspace.kind == WorkspaceKind::Worktree {
            Place::NewWorktree
        } else {
            Place::Project
        };
        Self {
            conn,
            turn: turns.first().map(|(t, _)| t.n),
            source,
            turns,
            agent,
            place,
            branch,
            prompt,
            forking: false,
            error: None,
            _subscriptions: subs,
        }
    }

    fn is_git(&self, cx: &gpui_kit::App) -> bool {
        self.conn
            .read(cx)
            .index
            .projects
            .get(&self.source.project_id)
            .is_some_and(|p| p.is_git_repo)
    }

    fn fork(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(turn) = self.turn else { return };
        let a = self.agent.read(cx);
        let Some(harness) = a.harness.clone().filter(|_| a.usable(cx)) else {
            return;
        };
        let mut params = json!({
            "commandId": uuid::Uuid::new_v4().to_string(),
            "sessionId": self.source.id,
            "turn": turn,
        });
        if harness != self.source.harness {
            params["harness"] = json!(harness);
        }
        if let Some(m) = a.model_id(cx) {
            params["model"] = json!(m);
        }
        if let Some(e) = a.effort() {
            params["effort"] = json!(e);
        }
        if let Some(m) = a.mode() {
            params["mode"] = json!(m);
        }
        if self.is_git(cx) {
            params["workspace"] = match self.place {
                Place::Project => json!({"kind": "root"}),
                Place::NewWorktree => {
                    let b = self.branch.read(cx).value().trim().to_string();
                    if b.is_empty() {
                        json!({"kind": "worktree"})
                    } else {
                        json!({"kind": "worktree", "branch": b})
                    }
                }
                Place::ThisWorktree => json!({"kind": "worktree", "path": self.source.workspace.path}),
            };
        }
        let prompt = self.prompt.read(cx).value().trim().to_string();
        if !prompt.is_empty() {
            params["prompt"] = json!(prompt);
        }
        self.forking = true;
        self.error = None;
        let server_id = self.conn.read(cx).server_id.clone();
        let call = self
            .conn
            .read(cx)
            .call_with_timeout("session.fork", params, Duration::from_secs(90));
        cx.spawn_in(window, async move |this, cx| {
            let result = call.await;
            let _ = this.update_in(cx, |this, window, cx| {
                this.forking = false;
                match result.and_then(|v| {
                    serde_json::from_value::<Session>(v)
                        .map_err(|e| crate::protocol::RpcError::new("invalid", e.to_string()))
                }) {
                    Ok(s) => {
                        window.close_dialog(cx);
                        if let Some(app) = app_view(cx) {
                            app.update(cx, |a, cx| a.open_by_ids(&server_id, &s.id, None, window, cx));
                        }
                    }
                    Err(e) => this.error = Some(explain(&e)),
                }
                cx.notify();
            });
        })
        .detach();
        cx.notify();
    }
}

impl Render for ForkView {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let git = self.is_git(cx);
        let info = self.conn.read(cx).info.clone().unwrap_or_default();
        let this = cx.entity().downgrade();
        let turn_label = self
            .turns
            .iter()
            .find(|(t, _)| Some(t.n) == self.turn)
            .map(|(_, l)| l.clone())
            .unwrap_or_else(|| "Choose a turn".into());
        let turn_choices: Vec<(String, String)> =
            self.turns.iter().map(|(t, l)| (t.n.to_string(), l.clone())).collect();
        let w = this.clone();
        let turn_picker = picker(
            "turn",
            turn_label,
            turn_choices,
            self.turn.map(|n| n.to_string()).unwrap_or_default(),
            move |id, _, cx| {
                let _ = w.update(cx, |v, cx| {
                    v.turn = id.parse().ok();
                    cx.notify();
                });
            },
        );
        let this_worktree = self.source.workspace.kind == WorkspaceKind::Worktree && info.fork_in_worktree;
        let mut places = vec![
            ("project".to_string(), "Project folder".to_string()),
            ("new".into(), "New worktree".into()),
        ];
        if this_worktree {
            places.push(("this".into(), "This session's worktree".into()));
        }
        let current = match self.place {
            Place::Project => "project",
            Place::NewWorktree => "new",
            Place::ThisWorktree => "this",
        };
        let label = places
            .iter()
            .find(|(id, _)| id == current)
            .map(|(_, l)| l.clone())
            .unwrap_or_default();
        let w = this.clone();
        let place_picker = picker("place", label, places, current.to_string(), move |id, _, cx| {
            let _ = w.update(cx, |v, cx| {
                v.place = match id.as_str() {
                    "new" => Place::NewWorktree,
                    "this" => Place::ThisWorktree,
                    _ => Place::Project,
                };
                cx.notify();
            });
        });
        let place_footer = match self.place {
            Place::Project => "The fork works in the project folder, as it is now.".to_string(),
            Place::NewWorktree => "The fork gets its own branch, with the files as they were after that turn.".into(),
            Place::ThisWorktree => format!(
                "The fork works beside this session in its worktree, on {}, with the files as they are now.",
                self.source
                    .workspace
                    .branch
                    .clone()
                    .unwrap_or_else(|| "its branch".into())
            ),
        };
        let agent = self.agent.read(cx);
        let can = agent.usable(cx) && self.turn.is_some_and(|t| t > 0);
        let agents = self.agent.update(cx, |a, cx| a.render_agents(cx));
        let options = self.agent.update(cx, |a, cx| a.render_options(cx));
        let section = |t: &'static str| {
            div()
                .text_xs()
                .font_weight(FontWeight::SEMIBOLD)
                .text_color(p.overlay)
                .child(t)
        };
        v_flex()
            .size_full()
            .gap_3()
            .child(
                v_flex()
                    .id("fork-body")
                    .flex_1()
                    .min_h_0()
                    .overflow_y_scroll()
                    .gap_4()
                    .child(v_flex().gap_1().child(section("From")).child(row("After turn", turn_picker, None, &p)))
                    .child(v_flex().gap_1().child(section("Agent")).child(agents))
                    .when_some(options, |d, o| d.child(v_flex().gap_1().child(section("Options")).child(o)))
                    .when(git, |d| {
                        d.child(
                            v_flex()
                                .gap_1()
                                .child(section("Workspace"))
                                .child(row("Work in", place_picker, None, &p))
                                .when(self.place == Place::NewWorktree, |d| {
                                    d.child(row("Branch", Input::new(&self.branch).small().into_any_element(), None, &p))
                                })
                                .child(caption(place_footer, &p)),
                        )
                    })
                    .child(
                        v_flex()
                            .gap_1()
                            .child(section("Prompt"))
                            .child(
                                div()
                                    .p_2()
                                    .rounded_md()
                                    .border_1()
                                    .border_color(p.surface0)
                                    .child(Textarea::new(&self.prompt).appearance(false)),
                            )
                            .child(caption(
                                "Optional. Without one, the fork opens with the conversation so far, and you write the next prompt in it. The same agent picks up its own conversation; another agent gets it as a transcript with the first prompt.",
                                &p,
                            )),
                    ),
            )
            .when_some(self.error.clone(), |d, e| d.child(div().text_sm().text_color(p.red).child(e)))
            .child(
                h_flex()
                    .gap_2()
                    .justify_end()
                    .child(
                        Button::new("cancel")
                            .small()
                            .outline()
                            .label("Cancel")
                            .on_click(|_: &ClickEvent, window, cx| window.close_dialog(cx)),
                    )
                    .child(
                        Button::new("fork")
                            .small()
                            .primary()
                            .label("Fork")
                            .loading(self.forking)
                            .disabled(!can || self.forking)
                            .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.fork(window, cx))),
                    ),
            )
    }
}
