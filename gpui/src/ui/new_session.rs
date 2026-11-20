//! The new session form: a server (when there are several), a project (or
//! a folder added as one), an agent with its options, where to work (the
//! project folder, a new worktree, or one there is), the first prompt with
//! its images, and Save as Draft. It starts with the server's default agent
//! (`server.settings`), else the one used last. As `NewSessionView.swift`.

use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use gpui_kit::assets::IconName as L;
use gpui_kit::component::WindowExt as _;
use gpui_kit::component::button::{Button, ButtonVariants as _};
use gpui_kit::component::input::{Input, InputEvent, InputState, Textarea, TextareaState};
use gpui_kit::component::spinner::Spinner;
use gpui_kit::component::{Disableable as _, Icon, IconName, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    AnyElement, App, AppContext as _, ClickEvent, ClipboardEntry, Context, Entity, EventEmitter, ExternalPaths,
    FontWeight, Image, InteractiveElement as _, IntoElement, ObjectFit, ParentElement as _, PathPromptOptions, Render,
    SharedString, StatefulInteractiveElement as _, Styled as _, StyledImage as _, Subscription, Window, div, img, px,
};
use serde_json::json;

use crate::conn::ServerConn;
use crate::images::{Prepared, prepare};
use crate::protocol::{Branches, FsList, ImageRef, ImportableList, Project, ServerSettings, Session};
use crate::ui::agent_picker::{AgentChanged, AgentPicker, picker, row};
use crate::ui::common::{caption, plural};
use crate::ui::palette::Palette;
use crate::ui::session::explain;
use crate::ui::state::{app_view, prefs, update_prefs};

pub enum NewSessionEvent {
    Created { server_id: String, session_id: String },
    Cancelled,
}

impl EventEmitter<NewSessionEvent> for NewSessionView {}

#[derive(Clone, PartialEq)]
enum Place {
    Project,
    NewWorktree,
    Existing(String),
}

enum Upload {
    Uploading,
    Done(ImageRef),
    Failed(String),
}

struct Attached {
    key: u64,
    image: Arc<Image>,
    prepared: Prepared,
    state: Upload,
}

pub struct NewSessionView {
    conns: Vec<Entity<ServerConn>>,
    server_ix: usize,
    project_id: Option<String>,
    agent: Entity<AgentPicker>,
    settings: Option<ServerSettings>,
    branches: Option<Branches>,
    place: Place,
    /// "" for a new branch.
    branch: String,
    branch_name: Entity<InputState>,
    base_ref: String,
    prompt: Entity<TextareaState>,
    attachments: Vec<Attached>,
    next_key: u64,
    creating: bool,
    error: Option<String>,
    _subscriptions: Vec<Subscription>,
}

impl NewSessionView {
    pub fn new(
        conns: Vec<Entity<ServerConn>>,
        server_ix: usize,
        project: Option<String>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> Self {
        let server_ix = server_ix.min(conns.len().saturating_sub(1));
        let agent = cx.new(|cx| AgentPicker::new(conns[server_ix].clone(), window, cx));
        let prompt = cx.new(|cx| {
            TextareaState::new(window, cx)
                .placeholder("What should the agent do? (⌘Return starts)")
                .auto_grow(4, 12)
        });
        let branch_name = cx.new(|cx| InputState::new(window, cx).placeholder("auto"));
        let mut subscriptions = vec![
            cx.subscribe_in(&prompt, window, |this, _, event, window, cx| {
                if let InputEvent::PressEnter { secondary: true, .. } = event {
                    this.create(false, window, cx);
                }
            }),
            cx.subscribe(&agent, |_, _, _: &AgentChanged, cx| cx.notify()),
        ];
        for conn in &conns {
            subscriptions.push(cx.observe_in(conn, window, |this, _, window, cx| this.choose_agent(window, cx)));
        }
        prompt.update(cx, |p, cx| p.focus(window, cx));
        let mut this = Self {
            conns,
            server_ix,
            project_id: project,
            agent,
            settings: None,
            branches: None,
            place: Place::Project,
            branch: String::new(),
            branch_name,
            base_ref: String::new(),
            prompt,
            attachments: Vec::new(),
            next_key: 0,
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

    fn server_id(&self, cx: &App) -> String {
        self.conn().read(cx).server_id.clone()
    }

    fn load_server(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let conn = self.conn().clone();
        let server = conn.read(cx).server_id.clone();
        self.agent = cx.new(|cx| AgentPicker::new(conn.clone(), window, cx));
        self._subscriptions
            .push(cx.subscribe(&self.agent, |_, _, _: &AgentChanged, cx| cx.notify()));
        let projects = &conn.read(cx).index.projects;
        if self.project_id.as_ref().is_none_or(|p| !projects.contains_key(p)) {
            self.project_id = prefs(cx)
                .last_project
                .get(&server)
                .cloned()
                .filter(|p| projects.contains_key(p));
        }
        // Else the project of the latest session.
        if self.project_id.is_none() {
            self.project_id = conn
                .read(cx)
                .index
                .active_sessions()
                .first()
                .map(|s| s.project_id.clone());
        }
        self.settings = None;
        self.project_changed(cx);
        let settings = conn.read(cx).call("server.settings", json!({}));
        cx.spawn_in(window, async move |this, cx| {
            let s: ServerSettings = settings
                .await
                .ok()
                .and_then(|v| serde_json::from_value(v).ok())
                .unwrap_or_default();
            let _ = this.update_in(cx, |this, window, cx| {
                this.settings = Some(s);
                this.choose_agent(window, cx);
            });
        })
        .detach();
        self.choose_agent(window, cx);
        cx.notify();
    }

    /// The server's default agent where it's usable, else the one used last
    /// here, else the first usable; once the agents are in.
    fn choose_agent(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.agent.read(cx).harness.is_some() {
            cx.notify();
            return;
        }
        let conn = self.conn().read(cx);
        let Some(list) = conn.harnesses.clone() else { return };
        let usable = |id: &str| list.iter().any(|h| h.id == id && h.usable());
        let server = conn.server_id.clone();
        let default = self
            .settings
            .as_ref()
            .and_then(|s| s.default_agent.clone())
            .filter(|d| usable(&d.harness));
        let last = prefs(cx).last_agent.get(&server).cloned();
        let (harness, restore) = if let Some(d) = default {
            let mut restore = last.clone().filter(|l| l.harness == d.harness).unwrap_or_default();
            restore.harness = d.harness.clone();
            restore.model = d.model.clone();
            restore.effort = d.effort.clone();
            (d.harness.clone(), Some(restore))
        } else if let Some(l) = last.filter(|l| usable(&l.harness)) {
            (l.harness.clone(), Some(l))
        } else if let Some(h) = list.iter().find(|h| h.usable()) {
            (h.id.clone(), None)
        } else {
            return;
        };
        self.agent
            .update(cx, |a, cx| a.choose(&harness, restore.as_ref(), window, cx));
    }

    fn project(&self, cx: &App) -> Option<std::rc::Rc<Project>> {
        self.conn()
            .read(cx)
            .index
            .projects
            .get(self.project_id.as_ref()?)
            .cloned()
    }

    fn project_changed(&mut self, cx: &mut Context<Self>) {
        self.place = Place::Project;
        self.branch.clear();
        self.base_ref.clear();
        self.branches = None;
        let Some(p) = self.project(cx).filter(|p| p.is_git_repo) else {
            return;
        };
        let call = self.conn().read(cx).call("git.branches", json!({"projectId": p.id}));
        cx.spawn(async move |this, cx| {
            if let Ok(v) = call.await {
                let _ = this.update(cx, |this, cx| {
                    this.branches = serde_json::from_value(v).ok();
                    cx.notify();
                });
            }
        })
        .detach();
        cx.notify();
    }

    fn set_project(&mut self, id: Option<String>, cx: &mut Context<Self>) {
        if self.project_id != id {
            self.project_id = id;
            self.project_changed(cx);
        }
    }

    fn workspace(&self, cx: &App) -> serde_json::Value {
        if !self.project(cx).is_some_and(|p| p.is_git_repo) {
            return json!({"kind": "root"});
        }
        match &self.place {
            Place::Project => json!({"kind": "root"}),
            Place::Existing(path) => json!({"kind": "worktree", "path": path}),
            Place::NewWorktree if !self.branch.is_empty() => json!({"kind": "worktree", "branch": self.branch}),
            Place::NewWorktree => {
                let mut w = json!({"kind": "worktree"});
                let name = self.branch_name.read(cx).value().trim().to_string();
                if !name.is_empty() {
                    w["branch"] = json!(name);
                }
                if !self.base_ref.is_empty() {
                    w["baseRef"] = json!(self.base_ref);
                }
                w
            }
        }
    }

    fn uploads_done(&self) -> bool {
        self.attachments.iter().all(|a| matches!(a.state, Upload::Done(_)))
    }

    fn can_start(&self, cx: &App) -> bool {
        self.project_id.is_some() && self.agent.read(cx).usable(cx) && self.uploads_done() && !self.creating
    }

    fn remember(&self, cx: &mut App) {
        let server = self.server_id(cx);
        let choice = self.agent.read(cx).choice(cx);
        let project = self.project_id.clone();
        update_prefs(cx, |p| {
            if let Some(c) = choice {
                p.last_agent.insert(server.clone(), c);
            }
            if let Some(pr) = project {
                p.last_project.insert(server, pr);
            }
        });
    }

    fn create(&mut self, draft: bool, window: &mut Window, cx: &mut Context<Self>) {
        if !self.can_start(cx) {
            return;
        }
        let Some(project_id) = self.project_id.clone() else {
            return;
        };
        let a = self.agent.read(cx);
        let Some(harness) = a.harness.clone() else { return };
        let prompt = self.prompt.read(cx).value().trim().to_string();
        let images: Vec<String> = self
            .attachments
            .iter()
            .filter_map(|a| match &a.state {
                Upload::Done(r) => Some(r.id.clone()),
                _ => None,
            })
            .collect();
        let mut params = json!({
            "commandId": uuid::Uuid::new_v4().to_string(),
            "projectId": project_id,
            "harness": harness,
            "workspace": self.workspace(cx),
        });
        if let Some(m) = a.model_id(cx) {
            params["model"] = json!(m);
        }
        if let Some(e) = a.effort() {
            params["effort"] = json!(e);
        }
        if let Some(m) = a.mode() {
            params["mode"] = json!(m);
        }
        if !prompt.is_empty() {
            params["prompt"] = json!(prompt);
        }
        if !images.is_empty() {
            params["images"] = json!(images);
        }
        if draft {
            params["draft"] = json!(true);
        }
        self.creating = true;
        self.error = None;
        cx.notify();
        self.remember(cx);
        let server_id = self.server_id(cx);
        let call = self
            .conn()
            .read(cx)
            .call_with_timeout("session.create", params, Duration::from_secs(90));
        cx.spawn_in(window, async move |this, cx| {
            let result = call.await;
            let _ = this.update(cx, |this, cx| {
                this.creating = false;
                match result.and_then(|v| {
                    serde_json::from_value::<Session>(v)
                        .map_err(|e| crate::protocol::RpcError::new("invalid", e.to_string()))
                }) {
                    Ok(session) => cx.emit(NewSessionEvent::Created {
                        server_id,
                        session_id: session.id,
                    }),
                    Err(e) => this.error = Some(explain(&e)),
                }
                cx.notify();
            });
        })
        .detach();
    }

    // MARK: Images

    fn attach_bytes(&mut self, bytes: Vec<u8>, window: &mut Window, cx: &mut Context<Self>) {
        let task = cx.background_spawn(async move { prepare(&bytes) });
        cx.spawn_in(window, async move |this, cx| {
            let prepared = task.await;
            let _ = this.update(cx, |this, cx| match prepared {
                Ok(prepared) => {
                    this.next_key += 1;
                    let key = this.next_key;
                    let image = Arc::new(Image::from_bytes(prepared.format, prepared.bytes.clone()));
                    this.attachments.push(Attached {
                        key,
                        image,
                        prepared,
                        state: Upload::Uploading,
                    });
                    this.upload(key, cx);
                }
                Err(e) => {
                    this.error = Some(e);
                    cx.notify();
                }
            });
        })
        .detach();
    }

    fn upload(&mut self, key: u64, cx: &mut Context<Self>) {
        let Some(a) = self.attachments.iter_mut().find(|a| a.key == key) else {
            return;
        };
        a.state = Upload::Uploading;
        let prepared = a.prepared.clone();
        let task = self.conn().update(cx, |c, cx| c.upload_image(prepared, cx));
        cx.spawn(async move |this, cx| {
            let r = task.await;
            let _ = this.update(cx, |this, cx| {
                if let Some(a) = this.attachments.iter_mut().find(|a| a.key == key) {
                    a.state = match r {
                        Ok(r) => Upload::Done(r),
                        Err(e) => Upload::Failed(e.to_string()),
                    };
                }
                cx.notify();
            });
        })
        .detach();
        cx.notify();
    }

    fn attach_paths(&mut self, paths: Vec<PathBuf>, window: &mut Window, cx: &mut Context<Self>) {
        for path in paths {
            let read = cx.background_spawn(async move { std::fs::read(&path) });
            cx.spawn_in(window, async move |this, cx| {
                if let Ok(bytes) = read.await {
                    let _ = this.update_in(cx, |this, window, cx| this.attach_bytes(bytes, window, cx));
                }
            })
            .detach();
        }
    }

    fn pick_images(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let rx = cx.prompt_for_paths(PathPromptOptions {
            files: true,
            directories: false,
            multiple: true,
            prompt: Some("Attach".into()),
        });
        cx.spawn_in(window, async move |this, cx| {
            if let Ok(Ok(Some(paths))) = rx.await {
                let _ = this.update_in(cx, |this, window, cx| this.attach_paths(paths, window, cx));
            }
        })
        .detach();
    }

    fn paste(&mut self, window: &mut Window, cx: &mut Context<Self>) -> bool {
        let Some(item) = cx.read_from_clipboard() else {
            return false;
        };
        let mut images = Vec::new();
        let mut text = false;
        for e in item.entries() {
            match e {
                ClipboardEntry::Image(i) => images.push(i.bytes().to_vec()),
                ClipboardEntry::String(s) => text |= !s.text().is_empty(),
                ClipboardEntry::ExternalPaths(_) => {}
            }
        }
        if images.is_empty() || text {
            return false;
        }
        for b in images {
            self.attach_bytes(b, window, cx);
        }
        true
    }

    // MARK: Projects

    fn forget_project(&mut self, cx: &mut Context<Self>) {
        let Some(id) = self.project_id.clone() else { return };
        let call = self.conn().read(cx).call("project.remove", json!({"id": id}));
        cx.spawn(async move |this, cx| {
            let r = call.await;
            let _ = this.update(cx, |this, cx| {
                match r {
                    Ok(_) => this.set_project(None, cx),
                    Err(e) => this.error = Some(explain(&e)),
                }
                cx.notify();
            });
        })
        .detach();
    }

    fn browse(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let conn = self.conn().clone();
        let this = cx.entity().downgrade();
        open_folder_browser(conn, window, cx, move |project, cx| {
            let _ = this.update(cx, |v, cx| v.set_project(Some(project.id), cx));
        });
    }

    fn import(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let (Some(project), Some(choice)) = (self.project(cx), self.agent.read(cx).choice(cx)) else {
            return;
        };
        let conn = self.conn().clone();
        let this = cx.entity().downgrade();
        let view = cx.new(|cx| ImportView::new(conn, project, choice, this, cx));
        let size = window.viewport_size();
        window.open_dialog(cx, move |dialog, _, _| {
            dialog
                .w(px(560.))
                .title("Terminal sessions")
                .child(div().h((size.height * 0.6).min(px(560.))).child(view.clone()))
        });
    }

    fn render_attachments(&self, p: &Palette, cx: &mut Context<Self>) -> Option<AnyElement> {
        if self.attachments.is_empty() {
            return None;
        }
        let mut row = h_flex().gap_1p5().flex_wrap();
        for a in &self.attachments {
            let key = a.key;
            let mut thumb = div()
                .id(SharedString::from(format!("ns-attach-{key}")))
                .relative()
                .size(px(56.))
                .rounded_md()
                .overflow_hidden()
                .bg(p.crust)
                .child(img(a.image.clone()).size_full().object_fit(ObjectFit::Cover));
            match &a.state {
                Upload::Uploading => {
                    thumb = thumb.child(
                        div()
                            .absolute()
                            .inset_0()
                            .flex()
                            .items_center()
                            .justify_center()
                            .bg(p.base.opacity(0.5))
                            .child(Spinner::new().small()),
                    )
                }
                Upload::Failed(e) => {
                    let e = e.clone();
                    thumb = thumb
                        .cursor_pointer()
                        .tooltip(move |window, cx| {
                            gpui_kit::component::tooltip::Tooltip::new(format!("{e} Click to try again."))
                                .build(window, cx)
                        })
                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.upload(key, cx)))
                        .child(
                            div()
                                .absolute()
                                .inset_0()
                                .flex()
                                .items_center()
                                .justify_center()
                                .bg(p.base.opacity(0.6))
                                .child(Icon::new(L::RotateCw).text_color(p.red)),
                        );
                }
                Upload::Done(_) => {}
            }
            thumb = thumb.child(
                div().absolute().top_0p5().right_0p5().child(
                    Button::new(SharedString::from(format!("ns-unattach-{key}")))
                        .xsmall()
                        .icon(IconName::Close)
                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                            this.attachments.retain(|a| a.key != key);
                            cx.notify();
                        })),
                ),
            );
            row = row.child(thumb);
        }
        Some(row.into_any_element())
    }
}

impl Render for NewSessionView {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let conn = self.conn().read(cx);
        let info = conn.info.clone().unwrap_or_default();
        let projects = conn.index.projects_by_name();
        let project = self.project(cx);
        let git = project.as_ref().is_some_and(|p| p.is_git_repo);
        let import = self.agent.read(cx).info(cx).is_some_and(|h| h.caps.import) && project.is_some();
        let section = |t: &'static str| {
            div()
                .text_xs()
                .font_weight(FontWeight::SEMIBOLD)
                .text_color(p.overlay)
                .child(t)
        };
        let this = cx.entity().downgrade();

        // Server
        let servers: Vec<(String, String)> = self
            .conns
            .iter()
            .enumerate()
            .map(|(i, c)| (i.to_string(), c.read(cx).name.clone()))
            .collect();
        let server_label = servers.get(self.server_ix).map(|(_, n)| n.clone()).unwrap_or_default();
        let w = this.clone();
        let server_picker = picker(
            "server",
            server_label,
            servers,
            self.server_ix.to_string(),
            move |id, window, cx| {
                let _ = w.update(cx, |v, cx| {
                    let ix: usize = id.parse().unwrap_or(0);
                    if ix != v.server_ix {
                        v.server_ix = ix;
                        v.project_id = None;
                        v.load_server(window, cx);
                    }
                });
            },
        );

        // Project
        let mut choices: Vec<(String, String)> = projects.iter().map(|p| (p.id.clone(), p.name.clone())).collect();
        if choices.is_empty() {
            choices.push((String::new(), "No projects yet".into()));
        }
        let label = project
            .as_ref()
            .map(|p| p.name.clone())
            .unwrap_or_else(|| "Choose…".into());
        let w = this.clone();
        let project_picker = picker(
            "project",
            label,
            choices,
            self.project_id.clone().unwrap_or_default(),
            move |id, _, cx| {
                let _ = w.update(cx, |v, cx| v.set_project((!id.is_empty()).then_some(id), cx));
            },
        );

        // Workspace
        let workspace = git.then(|| {
            let mut places: Vec<(String, String)> = vec![
                ("project".into(), "Project folder".into()),
                ("new".into(), "New worktree".into()),
            ];
            if let Some(b) = &self.branches {
                for wt in &b.worktrees {
                    places.push((format!("path:{}", wt.path), wt.label()));
                }
            }
            let current = match &self.place {
                Place::Project => "project".to_string(),
                Place::NewWorktree => "new".into(),
                Place::Existing(path) => format!("path:{path}"),
            };
            let label = places
                .iter()
                .find(|(id, _)| *id == current)
                .map(|(_, l)| l.clone())
                .unwrap_or_default();
            let w = this.clone();
            let mut col = v_flex().gap_2().child(row(
                "Work in",
                picker("place", label, places, current, move |id, _, cx| {
                    let _ = w.update(cx, |v, cx| {
                        v.place = match id.as_str() {
                            "project" => Place::Project,
                            "new" => Place::NewWorktree,
                            path => Place::Existing(path.trim_start_matches("path:").to_string()),
                        };
                        cx.notify();
                    });
                }),
                None,
                &p,
            ));
            if self.place == Place::NewWorktree {
                let available = self.branches.as_ref().map(|b| b.available()).unwrap_or_default();
                let mut branches = vec![(String::new(), "New branch".to_string())];
                branches.extend(available.iter().map(|b| (b.clone(), b.clone())));
                let label = if self.branch.is_empty() {
                    "New branch".to_string()
                } else {
                    self.branch.clone()
                };
                let w = this.clone();
                col = col.child(row(
                    "Branch",
                    picker("branch", label, branches, self.branch.clone(), move |id, _, cx| {
                        let _ = w.update(cx, |v, cx| {
                            v.branch = id;
                            cx.notify();
                        });
                    }),
                    None,
                    &p,
                ));
                if self.branch.is_empty() {
                    col = col.child(row(
                        "Name",
                        Input::new(&self.branch_name).small().into_any_element(),
                        None,
                        &p,
                    ));
                    let mut bases = vec![(String::new(), "Current HEAD".to_string())];
                    if let Some(b) = &self.branches {
                        bases.extend(b.branches.iter().map(|x| (x.clone(), x.clone())));
                    }
                    let label = if self.base_ref.is_empty() {
                        "Current HEAD".to_string()
                    } else {
                        self.base_ref.clone()
                    };
                    let w = this.clone();
                    col = col.child(row(
                        "Based on",
                        picker("base", label, bases, self.base_ref.clone(), move |id, _, cx| {
                            let _ = w.update(cx, |v, cx| {
                                v.base_ref = id;
                                cx.notify();
                            });
                        }),
                        None,
                        &p,
                    ));
                }
            }
            let footer = match &self.place {
                Place::Project => "The agent works directly in the project folder.".to_string(),
                Place::NewWorktree => {
                    "The agent works on its own branch in a separate checkout. You can revert its turns.".into()
                }
                Place::Existing(path) => {
                    format!("The agent works in {path}, on its branch, beside any session already there.")
                }
            };
            col.child(caption(footer, &p))
        });

        let agents = self.agent.update(cx, |a, cx| a.render_agents(cx));
        let options = self.agent.update(cx, |a, cx| a.render_options(cx));
        let attachments = self.render_attachments(&p, cx);
        let can_start = self.can_start(cx);
        let has_prompt = !self.prompt.read(cx).value().trim().is_empty() || !self.attachments.is_empty();
        let _ = window;
        v_flex()
            .id("new-session")
            .size_full()
            .overflow_y_scroll()
            .items_center()
            .pt_8()
            .pb_8()
            .px_6()
            .on_drop(cx.listener(|this, paths: &ExternalPaths, window, cx| {
                this.attach_paths(paths.paths().to_vec(), window, cx)
            }))
            .child(
                v_flex()
                    .w_full()
                    .max_w(px(680.))
                    .gap_5()
                    .child(div().text_lg().font_weight(FontWeight::SEMIBOLD).child("New Session"))
                    .when(self.conns.len() > 1, |d| d.child(row("Server", server_picker, None, &p)))
                    .child(
                        v_flex()
                            .gap_2()
                            .child(section("Project"))
                            .child(row("Project", project_picker, None, &p))
                            .when_some(project.clone(), |d, pr| {
                                d.child(
                                    h_flex()
                                        .gap_2()
                                        .pl(px(108.))
                                        .child(div().flex_1().min_w_0().truncate().text_xs().font_family(crate::ui::fonts::code()).text_color(p.subtext).child(pr.path.clone()))
                                        .child(
                                            Button::new("forget")
                                                .xsmall()
                                                .ghost()
                                                .label("Forget")
                                                .tooltip(format!("Take {} off the list of projects. The folder stays.", pr.name))
                                                .on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.forget_project(cx))),
                                        ),
                                )
                            })
                            .child(
                                h_flex().pl(px(108.)).child(
                                    Button::new("add-folder")
                                        .xsmall()
                                        .ghost()
                                        .icon(Icon::new(L::FolderPlus))
                                        .label("Add a folder…")
                                        .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.browse(window, cx))),
                                ),
                            ),
                    )
                    .child(v_flex().gap_2().child(section("Agent")).child(agents))
                    .when_some(options, |d, o| {
                        d.child(
                            v_flex()
                                .gap_2()
                                .child(section("Options"))
                                .child(o)
                                .when(import, |d| {
                                    d.child(
                                        h_flex().child(
                                            Button::new("import")
                                                .small()
                                                .ghost()
                                                .icon(Icon::new(L::SquareTerminal))
                                                .label("Continue a terminal session…")
                                                .disabled(self.creating)
                                                .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.import(window, cx))),
                                        ),
                                    )
                                }),
                        )
                    })
                    .when_some(workspace, |d, w| d.child(v_flex().gap_2().child(section("Workspace")).child(w)))
                    .child(
                        v_flex()
                            .gap_2()
                            .child(section("Prompt"))
                            .child(
                                v_flex()
                                    .gap_2()
                                    .p_2()
                                    .rounded_lg()
                                    .border_1()
                                    .border_color(p.surface0)
                                    .bg(p.mantle)
                                    .child(
                                        div()
                                            .capture_key_down(cx.listener(|this, ev: &gpui_kit::KeyDownEvent, window, cx| {
                                                if ev.keystroke.key == "v" && ev.keystroke.modifiers.platform && this.paste(window, cx) {
                                                    cx.stop_propagation();
                                                }
                                            }))
                                            .font_family(crate::ui::fonts::message())
                                            .child(Textarea::new(&self.prompt).appearance(false)),
                                    )
                                    .when_some(attachments, |d, a| d.child(a))
                                    .child(
                                        h_flex().child(
                                            Button::new("add-images")
                                                .xsmall()
                                                .ghost()
                                                .icon(Icon::new(L::ImagePlus))
                                                .label("Add Images")
                                                .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.pick_images(window, cx))),
                                        ),
                                    ),
                            )
                            .child(caption(
                                if info.drafts {
                                    "Optional. Without one, the session starts empty and you write the first prompt in it. Save as Draft keeps the prompt in the session, unsent, to send later from any device."
                                } else {
                                    "Optional. Without one, the session starts empty and you write the first prompt in it."
                                },
                                &p,
                            )),
                    )
                    .when_some(self.error.clone(), |d, e| d.child(div().text_sm().text_color(p.red).child(e)))
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
                            .when(info.drafts, |d| {
                                d.child(
                                    Button::new("save-draft")
                                        .small()
                                        .outline()
                                        .icon(Icon::new(L::Download))
                                        .label("Save as Draft")
                                        .disabled(!can_start || !has_prompt)
                                        .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.create(true, window, cx))),
                                )
                            })
                            .child(
                                Button::new("start")
                                    .small()
                                    .primary()
                                    .label("Start")
                                    .loading(self.creating)
                                    .disabled(!can_start)
                                    .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.create(false, window, cx))),
                            ),
                    ),
            )
    }
}

/// The server's folders, from its roots down, to add one as a project.
pub fn open_folder_browser(
    conn: Entity<ServerConn>,
    window: &mut Window,
    cx: &mut App,
    on_added: impl Fn(Project, &mut App) + 'static,
) {
    let view = cx.new(|cx| FolderBrowser::new(conn, std::rc::Rc::new(on_added), cx));
    let size = window.viewport_size();
    window.open_dialog(cx, move |dialog, _, _| {
        dialog
            .w(px(520.))
            .title("Add a folder")
            .child(div().h((size.height * 0.6).min(px(520.))).child(view.clone()))
    });
}

type OnProject = std::rc::Rc<dyn Fn(Project, &mut App)>;

pub struct FolderBrowser {
    conn: Entity<ServerConn>,
    /// The folder shown; none: the roots.
    stack: Vec<String>,
    listing: Option<Result<FsList, String>>,
    adding: bool,
    on_added: OnProject,
}

impl FolderBrowser {
    fn new(conn: Entity<ServerConn>, on_added: OnProject, cx: &mut Context<Self>) -> Self {
        let mut this = Self {
            conn,
            stack: Vec::new(),
            listing: None,
            adding: false,
            on_added,
        };
        this.load(cx);
        this
    }

    fn load(&mut self, cx: &mut Context<Self>) {
        self.listing = None;
        let path = self.stack.last().cloned().unwrap_or_default();
        let call = self.conn.read(cx).call("fs.list", json!({"path": path}));
        cx.spawn(async move |this, cx| {
            let r = call.await;
            let _ = this.update(cx, |this, cx| {
                this.listing = Some(
                    r.map_err(|e| explain(&e))
                        .and_then(|v| serde_json::from_value(v).map_err(|e| e.to_string())),
                );
                cx.notify();
            });
        })
        .detach();
        cx.notify();
    }

    fn add(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(path) = self.stack.last().cloned() else { return };
        self.adding = true;
        let call = self.conn.read(cx).call("project.add", json!({"path": path}));
        cx.spawn_in(window, async move |this, cx| {
            let r = call.await;
            let _ = this.update_in(cx, |this, window, cx| {
                this.adding = false;
                match r.map(serde_json::from_value::<Project>) {
                    Ok(Ok(p)) => {
                        window.close_dialog(cx);
                        (this.on_added)(p, cx);
                    }
                    Ok(Err(e)) => this.listing = Some(Err(e.to_string())),
                    Err(e) => this.listing = Some(Err(explain(&e))),
                }
                cx.notify();
            });
        })
        .detach();
    }
}

impl Render for FolderBrowser {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let at_root = self.stack.is_empty();
        let title = self
            .stack
            .last()
            .map(|s| {
                std::path::Path::new(s)
                    .file_name()
                    .map(|n| n.to_string_lossy().into_owned())
                    .unwrap_or_else(|| s.clone())
            })
            .unwrap_or_else(|| "Folders".into());
        let body: AnyElement = match &self.listing {
            None => div().p_4().child(Spinner::new()).into_any_element(),
            Some(Err(e)) => div()
                .p_2()
                .text_sm()
                .text_color(p.red)
                .child(e.clone())
                .into_any_element(),
            Some(Ok(l)) if l.entries.is_empty() => caption("No subfolders", &p).p_2().into_any_element(),
            Some(Ok(l)) => {
                let mut list = v_flex().gap_0p5();
                for (i, e) in l.entries.iter().enumerate() {
                    let path = e.path.clone();
                    list = list.child(
                        h_flex()
                            .id(("folder", i))
                            .gap_2()
                            .px_2()
                            .py_1()
                            .rounded_md()
                            .cursor_pointer()
                            .hover(|d| d.bg(p.surface0))
                            .child(
                                Icon::new(if e.is_git_repo { L::FolderGit2 } else { L::Folder })
                                    .small()
                                    .text_color(p.overlay),
                            )
                            .child(div().flex_1().text_sm().truncate().child(if at_root {
                                e.path.clone()
                            } else {
                                e.name.clone()
                            }))
                            .when(e.is_git_repo, |d| {
                                d.child(
                                    div()
                                        .px_1p5()
                                        .rounded_full()
                                        .bg(p.surface0)
                                        .text_xs()
                                        .text_color(p.subtext)
                                        .child("git"),
                                )
                            })
                            .child(Icon::new(IconName::ChevronRight).xsmall().text_color(p.overlay))
                            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                                this.stack.push(path.clone());
                                this.load(cx);
                            })),
                    );
                }
                list.into_any_element()
            }
        };
        v_flex()
            .size_full()
            .gap_2()
            .child(
                h_flex()
                    .gap_2()
                    .when(!at_root, |d| {
                        d.child(
                            Button::new("up")
                                .xsmall()
                                .ghost()
                                .icon(IconName::ChevronLeft)
                                .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                                    this.stack.pop();
                                    this.load(cx);
                                })),
                        )
                    })
                    .child(
                        div()
                            .flex_1()
                            .text_sm()
                            .font_weight(FontWeight::SEMIBOLD)
                            .truncate()
                            .child(title),
                    ),
            )
            .child(div().id("folders").flex_1().min_h_0().overflow_y_scroll().child(body))
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
                        Button::new("use")
                            .small()
                            .primary()
                            .label("Use folder")
                            .loading(self.adding)
                            .disabled(at_root || self.adding)
                            .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.add(window, cx))),
                    ),
            )
    }
}

/// The agent's own sessions in the project's folder, to go on with here.
pub struct ImportView {
    conn: Entity<ServerConn>,
    project: std::rc::Rc<Project>,
    choice: crate::store::prefs::AgentChoice,
    form: gpui_kit::WeakEntity<NewSessionView>,
    list: Option<Result<ImportableList, String>>,
    importing: Option<String>,
    error: Option<String>,
}

impl ImportView {
    fn new(
        conn: Entity<ServerConn>,
        project: std::rc::Rc<Project>,
        choice: crate::store::prefs::AgentChoice,
        form: gpui_kit::WeakEntity<NewSessionView>,
        cx: &mut Context<Self>,
    ) -> Self {
        let mut this = Self {
            conn,
            project,
            choice,
            form,
            list: None,
            importing: None,
            error: None,
        };
        this.load(cx);
        this
    }

    fn load(&mut self, cx: &mut Context<Self>) {
        self.list = None;
        let call = self.conn.read(cx).call_with_timeout(
            "session.importable",
            json!({"projectId": self.project.id, "harness": self.choice.harness}),
            Duration::from_secs(60),
        );
        cx.spawn(async move |this, cx| {
            let r = call.await;
            let _ = this.update(cx, |this, cx| {
                this.list = Some(
                    r.map_err(|e| explain(&e))
                        .and_then(|v| serde_json::from_value(v).map_err(|e| e.to_string())),
                );
                cx.notify();
            });
        })
        .detach();
        cx.notify();
    }

    fn import(&mut self, native_id: String, window: &mut Window, cx: &mut Context<Self>) {
        self.importing = Some(native_id.clone());
        self.error = None;
        let mut params = json!({
            "commandId": uuid::Uuid::new_v4().to_string(),
            "projectId": self.project.id,
            "harness": self.choice.harness,
            "nativeId": native_id,
        });
        if let Some(m) = &self.choice.model {
            params["model"] = json!(m);
        }
        if let Some(e) = &self.choice.effort {
            params["effort"] = json!(e);
        }
        if let Some(m) = &self.choice.mode {
            params["mode"] = json!(m);
        }
        let server_id = self.conn.read(cx).server_id.clone();
        let call = self
            .conn
            .read(cx)
            .call_with_timeout("session.import", params, Duration::from_secs(90));
        cx.spawn_in(window, async move |this, cx| {
            let r = call.await;
            let _ = this.update_in(cx, |this, window, cx| {
                this.importing = None;
                match r.and_then(|v| {
                    serde_json::from_value::<Session>(v)
                        .map_err(|e| crate::protocol::RpcError::new("invalid", e.to_string()))
                }) {
                    Ok(s) => {
                        if let Some(form) = this.form.upgrade() {
                            form.update(cx, |f, cx| f.remember(cx));
                        }
                        window.close_all_dialogs(cx);
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

impl Render for ImportView {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let harness = self
            .conn
            .read(cx)
            .harness(&self.choice.harness)
            .map(|h| h.name.clone())
            .unwrap_or_else(|| self.choice.harness.clone());
        let body: AnyElement = match &self.list {
            None => v_flex()
                .gap_2()
                .items_center()
                .pt_6()
                .child(Spinner::new())
                .child(caption("Looking for sessions…", &p))
                .into_any_element(),
            Some(Err(e)) => v_flex()
                .gap_2()
                .child(div().text_sm().text_color(p.red).child(e.clone()))
                .child(
                    Button::new("retry")
                        .small()
                        .outline()
                        .label("Retry")
                        .on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.load(cx))),
                )
                .into_any_element(),
            Some(Ok(l)) if l.sessions.is_empty() => caption(
                format!("No {harness} sessions to continue in {}.", self.project.name),
                &p,
            )
            .into_any_element(),
            Some(Ok(l)) => {
                let mut list = v_flex().gap_1();
                let busy = self.importing.is_some();
                for (i, s) in l.sessions.iter().enumerate() {
                    let id = s.id.clone();
                    let importing = self.importing.as_deref() == Some(s.id.as_str());
                    let age = crate::store::sidebar::short_age_of(&s.updated_at, chrono::Utc::now());
                    list = list.child(
                        h_flex()
                            .id(("importable", i))
                            .gap_2()
                            .px_2()
                            .py_1p5()
                            .rounded_md()
                            .when(!busy, |d| d.cursor_pointer().hover(|d| d.bg(p.surface0)))
                            .when(busy && !importing, |d| d.opacity(0.5))
                            .child(
                                v_flex()
                                    .flex_1()
                                    .min_w_0()
                                    .child(div().text_sm().line_clamp(2).child(s.title.clone()))
                                    .child(caption(format!("{} · {age}", plural(s.messages as usize, "message", "messages")), &p))
                                    .when(s.active, |d| {
                                        d.child(div().text_xs().text_color(p.yellow).child(
                                            "Active in the last minute: if it still runs in a terminal, quit it there first.",
                                        ))
                                    }),
                            )
                            .when(importing, |d| d.child(Spinner::new().small()))
                            .when(!busy, |d| {
                                d.on_click(cx.listener(move |this, _: &ClickEvent, window, cx| this.import(id.clone(), window, cx)))
                            }),
                    );
                }
                list.into_any_element()
            }
        };
        v_flex()
            .size_full()
            .gap_2()
            .when_some(self.error.clone(), |d, e| d.child(div().text_sm().text_color(p.red).child(e)))
            .child(div().id("importables").flex_1().min_h_0().overflow_y_scroll().child(body))
            .child(caption(
                format!("Picks up a session {harness} ran in this folder outside Remote Agent. Its next prompt goes on with it here, in the project folder."),
                &p,
            ))
            .child(
                h_flex().justify_end().child(
                    Button::new("cancel")
                        .small()
                        .outline()
                        .label("Cancel")
                        .on_click(|_: &ClickEvent, window, cx| window.close_dialog(cx)),
                ),
            )
    }
}
