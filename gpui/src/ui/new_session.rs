//! The new session form: a server (when there are several), a project, an
//! agent, a worktree or not, and the first prompt. It starts with the
//! server's default agent (`server.settings`), as the other clients do.

use gpui_kit::component::button::{Button, ButtonVariants as _};
use gpui_kit::component::input::{InputEvent, Textarea, TextareaState};
use gpui_kit::component::select::{Select, SelectEvent, SelectState};
use gpui_kit::component::switch::Switch;
use gpui_kit::component::{IndexPath, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    AppContext as _, ClickEvent, Context, Entity, EventEmitter, FontWeight, IntoElement, ParentElement as _, Render,
    SharedString, Styled as _, Subscription, Window, div, px,
};
use serde_json::json;

use crate::conn::ServerConn;
use crate::protocol::{AgentSpec, ServerSettings, Session};
use crate::ui::palette::Palette;

pub enum NewSessionEvent {
    Created { server_id: String, session_id: String },
    Cancelled,
}

impl EventEmitter<NewSessionEvent> for NewSessionView {}

type Choices = SelectState<Vec<SharedString>>;

pub struct NewSessionView {
    conns: Vec<Entity<ServerConn>>,
    server: Entity<Choices>,
    server_ix: usize,
    project: Entity<Choices>,
    project_ids: Vec<String>,
    harness: Entity<Choices>,
    harness_ids: Vec<String>,
    /// The harnesses the agent select was last filled with, to refill it when they load.
    harness_count: Option<usize>,
    default_agent: Option<AgentSpec>,
    worktree: bool,
    prompt: Entity<TextareaState>,
    creating: bool,
    error: Option<String>,
    _subscriptions: Vec<Subscription>,
}

impl NewSessionView {
    pub fn new(conns: Vec<Entity<ServerConn>>, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let names: Vec<SharedString> = conns
            .iter()
            .map(|c| SharedString::from(c.read(cx).name.clone()))
            .collect();
        let server = cx.new(|cx| SelectState::new(names, Some(IndexPath::new(0)), window, cx));
        let project = cx.new(|cx| SelectState::new(Vec::<SharedString>::new(), None, window, cx));
        let harness = cx.new(|cx| SelectState::new(Vec::<SharedString>::new(), None, window, cx));
        let prompt = cx.new(|cx| {
            TextareaState::new(window, cx)
                .placeholder("What should the agent do? (⌘Return starts)")
                .auto_grow(4, 14)
        });
        let mut subscriptions = vec![
            cx.subscribe_in(
                &server,
                window,
                |this, state, _: &SelectEvent<Vec<SharedString>>, window, cx| {
                    let ix = state.read(cx).selected_index(cx).map(|i| i.row).unwrap_or(0);
                    if ix != this.server_ix {
                        this.server_ix = ix;
                        this.load_server(window, cx);
                    }
                },
            ),
            cx.subscribe_in(&prompt, window, |this, _, event, window, cx| {
                if let InputEvent::PressEnter { secondary: true, .. } = event {
                    this.create(window, cx);
                }
            }),
        ];
        for conn in &conns {
            subscriptions.push(cx.observe_in(conn, window, |this, _, window, cx| {
                this.fill_harnesses(window, cx, false)
            }));
        }
        prompt.update(cx, |p, cx| p.focus(window, cx));
        let mut this = Self {
            conns,
            server,
            server_ix: 0,
            project,
            project_ids: Vec::new(),
            harness,
            harness_ids: Vec::new(),
            harness_count: None,
            default_agent: None,
            worktree: false,
            prompt,
            creating: false,
            error: None,
            _subscriptions: subscriptions,
        };
        this.load_server(window, cx);
        this
    }

    fn conn(&self) -> &Entity<ServerConn> {
        &self.conns[self.server_ix.min(self.conns.len() - 1)]
    }

    fn load_server(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let conn = self.conn().clone();
        let projects = conn.read(cx).index.projects_by_name();
        self.project_ids = projects.iter().map(|p| p.id.clone()).collect();
        // The project of the latest session first.
        let latest = conn
            .read(cx)
            .index
            .active_sessions()
            .first()
            .map(|s| s.project_id.clone());
        let selected = latest
            .and_then(|id| self.project_ids.iter().position(|p| *p == id))
            .or(if projects.is_empty() { None } else { Some(0) });
        let names: Vec<SharedString> = projects.iter().map(|p| SharedString::from(p.name.clone())).collect();
        self.project.update(cx, |s, cx| {
            s.set_items(names, window, cx);
            s.set_selected_index(selected.map(IndexPath::new), window, cx);
        });
        self.default_agent = None;
        self.fill_harnesses(window, cx, true);
        let settings = conn.read(cx).call("server.settings", json!({}));
        cx.spawn_in(window, async move |this, cx| {
            let Ok(value) = settings.await else { return };
            let settings: ServerSettings = serde_json::from_value(value).unwrap_or_default();
            let _ = this.update_in(cx, |this, window, cx| {
                this.default_agent = settings.default_agent;
                this.fill_harnesses(window, cx, true);
            });
        })
        .detach();
        cx.notify();
    }

    /// Fills the agent select with the usable agents, the default agent selected.
    fn fill_harnesses(&mut self, window: &mut Window, cx: &mut Context<Self>, force: bool) {
        let conn = self.conn().read(cx);
        let Some(harnesses) = &conn.harnesses else { return };
        let usable: Vec<_> = harnesses.iter().filter(|h| h.usable()).cloned().collect();
        if !force && self.harness_count == Some(usable.len()) {
            return;
        }
        self.harness_count = Some(usable.len());
        let current = self.selected_harness(cx);
        self.harness_ids = usable.iter().map(|h| h.id.clone()).collect();
        let preferred = current
            .or_else(|| self.default_agent.as_ref().map(|a| a.harness.clone()))
            .and_then(|id| self.harness_ids.iter().position(|h| *h == id))
            .or(if usable.is_empty() { None } else { Some(0) });
        let names: Vec<SharedString> = usable.iter().map(|h| SharedString::from(h.name.clone())).collect();
        self.harness.update(cx, |s, cx| {
            s.set_items(names, window, cx);
            s.set_selected_index(preferred.map(IndexPath::new), window, cx);
        });
        cx.notify();
    }

    fn selected_harness(&self, cx: &Context<Self>) -> Option<String> {
        let ix = self.harness.read(cx).selected_index(cx)?.row;
        self.harness_ids.get(ix).cloned()
    }

    fn selected_project(&self, cx: &Context<Self>) -> Option<String> {
        let ix = self.project.read(cx).selected_index(cx)?.row;
        self.project_ids.get(ix).cloned()
    }

    fn create(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.creating {
            return;
        }
        let (Some(project_id), Some(harness)) = (self.selected_project(cx), self.selected_harness(cx)) else {
            self.error = Some("Choose a project and an agent.".into());
            cx.notify();
            return;
        };
        let prompt = self.prompt.read(cx).value().trim().to_string();
        if prompt.is_empty() {
            self.error = Some("Write the first prompt.".into());
            cx.notify();
            return;
        }
        let is_git = self
            .conn()
            .read(cx)
            .index
            .projects
            .get(&project_id)
            .is_some_and(|p| p.is_git_repo);
        let mut params = json!({
            "commandId": uuid::Uuid::new_v4().to_string(),
            "projectId": project_id,
            "harness": harness,
            "workspace": {"kind": if self.worktree && is_git { "worktree" } else { "root" }},
            "prompt": prompt,
        });
        // The default agent's model and effort, when it's the one chosen.
        if let Some(default) = self.default_agent.as_ref().filter(|d| d.harness == harness) {
            if let Some(m) = &default.model {
                params["model"] = json!(m);
            }
            if let Some(e) = &default.effort {
                params["effort"] = json!(e);
            }
        }
        self.creating = true;
        self.error = None;
        cx.notify();
        let conn = self.conn().clone();
        let server_id = conn.read(cx).server_id.clone();
        let call = conn.read(cx).call("session.create", params);
        cx.spawn_in(window, async move |this, cx| {
            let result = call.await;
            let _ = this.update_in(cx, |this, _, cx| {
                this.creating = false;
                match result.and_then(|v| {
                    serde_json::from_value::<Session>(v)
                        .map_err(|e| crate::protocol::RpcError::new("invalid", e.to_string()))
                }) {
                    Ok(session) => cx.emit(NewSessionEvent::Created {
                        server_id,
                        session_id: session.id,
                    }),
                    Err(e) => this.error = Some(e.to_string()),
                }
                cx.notify();
            });
        })
        .detach();
    }
}

impl Render for NewSessionView {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let project_id = self.selected_project(cx);
        let is_git = project_id
            .as_ref()
            .and_then(|id| self.conn().read(cx).index.projects.get(id).cloned())
            .is_some_and(|p| p.is_git_repo);
        let loading_agents = self.conn().read(cx).harnesses.is_none();
        let label = |text: &'static str| {
            div()
                .text_xs()
                .font_weight(FontWeight::SEMIBOLD)
                .text_color(p.subtext)
                .child(text)
        };
        v_flex().size_full().items_center().pt_10().px_6().child(
            v_flex()
                .w_full()
                .max_w(px(640.))
                .gap_4()
                .child(div().text_lg().font_weight(FontWeight::SEMIBOLD).child("New Session"))
                .when(self.conns.len() > 1, |d| {
                    d.child(v_flex().gap_1().child(label("Server")).child(Select::new(&self.server)))
                })
                .child(
                    h_flex()
                        .gap_3()
                        .items_start()
                        .child(
                            v_flex()
                                .flex_1()
                                .gap_1()
                                .child(label("Project"))
                                .child(Select::new(&self.project).placeholder("No projects on this server")),
                        )
                        .child(v_flex().flex_1().gap_1().child(label("Agent")).child(
                            Select::new(&self.harness).placeholder(if loading_agents {
                                "Loading agents…"
                            } else {
                                "No agent is ready"
                            }),
                        )),
                )
                .when(is_git, |d| {
                    d.child(
                        Switch::new("worktree")
                            .checked(self.worktree)
                            .label("Work in a new worktree, on a branch of its own")
                            .on_change(cx.listener(|this, checked: &bool, _, cx| {
                                this.worktree = *checked;
                                cx.notify();
                            })),
                    )
                })
                .child(
                    v_flex().gap_1().child(label("Prompt")).child(
                        div()
                            .p_2()
                            .rounded_lg()
                            .border_1()
                            .border_color(p.surface0)
                            .bg(p.mantle)
                            .child(Textarea::new(&self.prompt).appearance(false)),
                    ),
                )
                .when_some(self.error.clone(), |d, e| {
                    d.child(div().text_sm().text_color(p.red).child(e))
                })
                .child(
                    h_flex()
                        .gap_2()
                        .justify_end()
                        .child(
                            Button::new("cancel")
                                .small()
                                .ghost()
                                .label("Cancel")
                                .on_click(cx.listener(|_, _: &ClickEvent, _, cx| cx.emit(NewSessionEvent::Cancelled))),
                        )
                        .child(
                            Button::new("start")
                                .small()
                                .primary()
                                .label("Start")
                                .loading(self.creating)
                                .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.create(window, cx))),
                        ),
                ),
        )
    }
}
