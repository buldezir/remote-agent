//! The new session form: the first prompt with its images, then a block of
//! small pickers for the server (when there are several), the project (or a
//! folder added as one), the agent with its options, and where to work (the
//! project folder, a new worktree, or one there is), with what a choice
//! brings under it; and Save as Draft. It starts with the server's default
//! agent (`server.settings`), else the one used last. `NewSessionView.swift`
//! has the same choices, as a form.

use std::path::PathBuf;
use std::rc::Rc;
use std::sync::Arc;
use std::time::Duration;

use gpui_kit::assets::IconName as L;
use gpui_kit::component::WindowExt as _;
use gpui_kit::component::button::{Button, ButtonVariant, ButtonVariants as _};
use gpui_kit::component::input::{InputEvent, Paste, Textarea, TextareaState};
use gpui_kit::component::menu::{DropdownMenu as _, PopupMenu, PopupMenuItem};
use gpui_kit::component::speech::{SpeechEvent, SpeechState};
use gpui_kit::component::spinner::Spinner;
use gpui_kit::component::tooltip::Tooltip;
use gpui_kit::component::{Disableable as _, Icon, IconName, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    Anchor, AnyElement, App, AppContext as _, ClickEvent, ClipboardEntry, Context, Entity, EventEmitter, ExternalPaths,
    FontWeight, Image, InteractiveElement as _, IntoElement, ObjectFit, ParentElement as _, PathPromptOptions, Render,
    SharedString, StatefulInteractiveElement as _, Styled as _, StyledImage as _, Subscription, Window, div, img, px,
};
use serde_json::json;

use crate::conn::ServerConn;
use crate::images::{Prepared, prepare};
use crate::protocol::{Branches, FsList, ImageRef, ImportableList, Project, ServerSettings, Session};
use crate::ui::agent_picker::AgentPicker;
use crate::ui::badges::project_badge;
use crate::ui::common::{caption, plural, sidebar_toggle, top_bar};
use crate::ui::palette::Palette;
use crate::ui::prompt_box::{self, PromptBox};
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
    /// A new branch's name; empty for rad to name it.
    branch_name: String,
    base_ref: String,
    prompt: Entity<TextareaState>,
    attachments: Vec<Attached>,
    next_key: u64,
    creating: bool,
    error: Option<String>,
    speech: Entity<SpeechState>,
    /// The prompt as it was when dictation started.
    dictation_base: Option<String>,
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
                .placeholder("What should the agent do?")
                .auto_grow(2, 12)
                .submit_on_enter(true)
        });
        let speech = cx.new(SpeechState::new);
        let mut subscriptions = vec![
            cx.subscribe_in(&prompt, window, |this, _, event, window, cx| {
                if let InputEvent::PressEnter { shift: false, .. } = event {
                    this.create(false, window, cx);
                }
            }),
            cx.subscribe_in(&speech, window, |this, _, event: &SpeechEvent, window, cx| {
                this.dictation(event, window, cx)
            }),
            cx.observe(&agent, |_, _, cx| cx.notify()),
            // Such as the prompt box's width.
            cx.observe_global::<crate::ui::state::AppPrefs>(|_, cx| cx.notify()),
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
            branch_name: String::new(),
            base_ref: String::new(),
            prompt,
            attachments: Vec::new(),
            next_key: 0,
            creating: false,
            error: None,
            speech,
            dictation_base: None,
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
        // Its own state too, such as the line of agents that aren't ready.
        self._subscriptions
            .push(cx.observe(&self.agent, |_, _, cx| cx.notify()));
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
        self.branch_name.clear();
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
                let name = self.branch_name.trim().to_string();
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
        match prompt_box::pasted_images(&item) {
            Some(prompt_box::Pasted::Files(paths)) => self.attach_paths(paths, window, cx),
            Some(prompt_box::Pasted::Images(images)) => {
                for bytes in images {
                    self.attach_bytes(bytes, window, cx);
                }
            }
            None => return false,
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

    // MARK: Dictation

    fn dictation(&mut self, event: &SpeechEvent, window: &mut Window, cx: &mut Context<Self>) {
        match event {
            SpeechEvent::Started => self.dictation_base = Some(self.prompt.read(cx).value().to_string()),
            SpeechEvent::Partial(text) | SpeechEvent::Final(text) => {
                let base = self.dictation_base.clone().unwrap_or_default();
                let joined = if base.is_empty() || base.ends_with(char::is_whitespace) || text.is_empty() {
                    format!("{base}{text}")
                } else {
                    format!("{base} {text}")
                };
                self.prompt.update(cx, |c, cx| c.set_value(joined, window, cx));
                if matches!(event, SpeechEvent::Final(_)) {
                    self.dictation_base = None;
                }
            }
            SpeechEvent::Cancelled => {
                if let Some(base) = self.dictation_base.take() {
                    self.prompt.update(cx, |c, cx| c.set_value(base, window, cx));
                }
            }
            SpeechEvent::Error(e) => {
                self.dictation_base = None;
                self.error = Some(e.to_string());
            }
        }
        cx.notify();
    }

    // MARK: The bar

    /// The projects to choose from, adding a folder as one, and forgetting
    /// the chosen one: the bar's menu and the heading's.
    fn project_menu(
        &self,
        cx: &mut Context<Self>,
    ) -> impl Fn(PopupMenu, &mut Window, &mut Context<PopupMenu>) -> PopupMenu + 'static {
        let projects: Vec<(String, String)> = self
            .conn()
            .read(cx)
            .index
            .projects_by_name()
            .iter()
            .map(|p| (p.id.clone(), p.name.clone()))
            .collect();
        let current = self.project_id.clone().unwrap_or_default();
        let forget = self.project(cx).map(|p| p.name.clone());
        let this = cx.entity().downgrade();
        move |menu, _, _| {
            let w = this.clone();
            let mut menu = prompt_box::choices(
                menu.max_w(px(360.)),
                &projects,
                &current,
                Rc::new(move |id, _, cx| {
                    let _ = w.update(cx, |v, cx| v.set_project(Some(id), cx));
                }),
            );
            if !projects.is_empty() {
                menu = menu.separator();
            }
            let w = this.clone();
            menu = menu.item(
                PopupMenuItem::new("Add a folder…")
                    .icon(Icon::new(L::FolderPlus))
                    .on_click(move |_, window, cx| {
                        let _ = w.update(cx, |v, cx| v.browse(window, cx));
                    }),
            );
            if let Some(name) = &forget {
                let w = this.clone();
                menu = menu.item(
                    PopupMenuItem::new(format!("Forget {name}"))
                        .icon(Icon::new(L::FolderMinus))
                        .on_click(move |_, _, cx| {
                            let _ = w.update(cx, |v, cx| v.forget_project(cx));
                        }),
                );
            }
            menu
        }
    }

    /// "[L] project ▾ / New session": the project, with its menu.
    fn render_bar(&self, window: &mut Window, cx: &mut Context<Self>) -> AnyElement {
        let p = Palette::of(cx);
        let hidden = prefs(cx).sidebar_hidden;
        let project = self.project(cx);
        let trigger = Button::new("project-menu").ghost().small().child(
            h_flex()
                .gap_1p5()
                .when_some(project.as_ref(), |d, pr| d.child(project_badge(&pr.name, 16., &p)))
                .child(
                    div().text_sm().text_color(p.subtext).child(
                        project
                            .as_ref()
                            .map(|p| p.name.clone())
                            .unwrap_or_else(|| "Choose a project".into()),
                    ),
                )
                .child(Icon::new(IconName::ChevronDown).xsmall().text_color(p.overlay)),
        );
        let menu = trigger.dropdown_menu(self.project_menu(cx));
        top_bar(
            "detail-bar",
            hidden,
            p.base,
            None,
            window,
            h_flex()
                .gap_1()
                .when(hidden, |d| d.child(sidebar_toggle(true)))
                .child(menu)
                .child(div().text_sm().text_color(p.overlay).child("/"))
                .child(div().pl_1().text_sm().text_color(p.text).child("New session")),
        )
        .into_any_element()
    }

    // MARK: The pickers

    fn server_pill(&self, cx: &mut Context<Self>) -> AnyElement {
        let servers: Vec<(String, String)> = self
            .conns
            .iter()
            .enumerate()
            .map(|(i, c)| (i.to_string(), c.read(cx).name.clone()))
            .collect();
        let label = servers.get(self.server_ix).map(|(_, n)| n.clone()).unwrap_or_default();
        let current = self.server_ix.to_string();
        let this = cx.entity().downgrade();
        prompt_box::pill_menu(
            "server-pill",
            Some(Icon::new(L::Server)),
            label,
            None,
            move |menu, _, _| {
                let w = this.clone();
                prompt_box::choices(
                    menu,
                    &servers,
                    &current,
                    Rc::new(move |id, window, cx| {
                        let _ = w.update(cx, |v, cx| {
                            let ix: usize = id.parse().unwrap_or(0);
                            if ix != v.server_ix {
                                v.server_ix = ix;
                                v.project_id = None;
                                v.load_server(window, cx);
                            }
                        });
                    }),
                )
            },
        )
    }

    /// Where the agent works: the project's own checkout, a new worktree,
    /// or one there is.
    fn place_pill(&self, git: bool, cx: &mut Context<Self>) -> AnyElement {
        if !git {
            return prompt_box::fixed_pill("place-pill", Some(Icon::new(L::Folder)), "Project folder");
        }
        let mut places: Vec<(String, String)> = vec![
            ("project".into(), "Current checkout".into()),
            ("new".into(), "New worktree".into()),
        ];
        if let Some(b) = self.branches.as_ref().filter(|b| !b.worktrees.is_empty()) {
            places.push(("-".into(), String::new()));
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
        let icon = if self.place == Place::Project {
            L::Folder
        } else {
            L::FolderGit2
        };
        let tooltip = match self.project(cx) {
            Some(pr) => format!("{}\n{}", self.place_note(), pr.path),
            None => self.place_note(),
        };
        let this = cx.entity().downgrade();
        prompt_box::pill_menu(
            "place-pill",
            Some(Icon::new(icon)),
            label,
            Some(tooltip),
            move |menu, _, _| {
                let w = this.clone();
                prompt_box::choices(
                    menu,
                    &places,
                    &current,
                    Rc::new(move |id, _, cx| {
                        let _ = w.update(cx, |v, cx| {
                            v.place = match id.as_str() {
                                "project" => Place::Project,
                                "new" => Place::NewWorktree,
                                path => Place::Existing(path.trim_start_matches("path:").to_string()),
                            };
                            cx.notify();
                        });
                    }),
                )
            },
        )
    }

    /// The branch: the checkout's own, or a new worktree's, which is a new
    /// branch (named, or named by rad) from a base, or one not checked out.
    fn branch_pills(&self, cx: &mut Context<Self>) -> Vec<AnyElement> {
        let branch_icon = || Some(Icon::new(L::GitBranch));
        let Some(b) = self.branches.clone() else {
            return Vec::new();
        };
        match &self.place {
            Place::Project => {
                let name = if b.current.is_empty() {
                    "Detached HEAD".to_string()
                } else {
                    b.current.clone()
                };
                vec![prompt_box::fixed_pill("branch-pill", branch_icon(), name)]
            }
            Place::Existing(path) => {
                let label = b
                    .worktrees
                    .iter()
                    .find(|w| w.path == *path)
                    .map(|w| w.branch.clone().unwrap_or_else(|| w.label()))
                    .unwrap_or_default();
                vec![prompt_box::fixed_pill("branch-pill", branch_icon(), label)]
            }
            Place::NewWorktree => {
                let this = cx.entity().downgrade();
                let label = if !self.branch.is_empty() {
                    self.branch.clone()
                } else if self.branch_name.is_empty() {
                    "New branch".to_string()
                } else {
                    format!("New: {}", self.branch_name)
                };
                let available = b.available();
                let (branch, name) = (self.branch.clone(), self.branch_name.clone());
                let w = this.clone();
                let mut pills = vec![prompt_box::pill_menu(
                    "branch-pill",
                    branch_icon(),
                    label,
                    Some("The worktree's branch".into()),
                    move |mut menu, _, _| {
                        let new = if name.is_empty() {
                            "New branch, named by the agent".to_string()
                        } else {
                            format!("New branch: {name}")
                        };
                        let w2 = w.clone();
                        menu = menu.item(PopupMenuItem::new(new).checked(branch.is_empty()).on_click(
                            move |_, _, cx| {
                                let _ = w2.update(cx, |v, cx| {
                                    v.branch.clear();
                                    cx.notify();
                                });
                            },
                        ));
                        let (w2, name2) = (w.clone(), name.clone());
                        menu = menu.item(
                            PopupMenuItem::new("Name the new branch…").on_click(move |_, window, cx| {
                                let w3 = w2.clone();
                                crate::ui::dialogs::prompt_text(
                                    window,
                                    cx,
                                    "New branch",
                                    "Leave it empty for rad to name it.",
                                    "Branch name",
                                    &name2,
                                    "Use",
                                    ButtonVariant::Primary,
                                    move |text, _, cx| {
                                        let _ = w3.update(cx, |v, cx| {
                                            v.branch.clear();
                                            v.branch_name = text;
                                            cx.notify();
                                        });
                                    },
                                );
                            }),
                        );
                        if !available.is_empty() {
                            let choices: Vec<(String, String)> =
                                available.iter().map(|b| (b.clone(), b.clone())).collect();
                            let w2 = w.clone();
                            menu = prompt_box::choices(
                                menu.separator().label("Branches not checked out"),
                                &choices,
                                &branch,
                                Rc::new(move |id, _, cx| {
                                    let _ = w2.update(cx, |v, cx| {
                                        v.branch = id;
                                        cx.notify();
                                    });
                                }),
                            );
                        }
                        menu
                    },
                )];
                if self.branch.is_empty() {
                    let mut bases = vec![(String::new(), "Current HEAD".to_string())];
                    bases.extend(b.branches.iter().map(|x| (x.clone(), x.clone())));
                    let label = if self.base_ref.is_empty() {
                        "from HEAD".to_string()
                    } else {
                        format!("from {}", self.base_ref)
                    };
                    let current = self.base_ref.clone();
                    pills.push(prompt_box::pill_menu(
                        "base-pill",
                        Some(Icon::new(L::GitBranchPlus)),
                        label,
                        Some("What the new branch starts from".into()),
                        move |menu, _, _| {
                            let w = this.clone();
                            prompt_box::choices(
                                menu.label("Based on"),
                                &bases,
                                &current,
                                Rc::new(move |id, _, cx| {
                                    let _ = w.update(cx, |v, cx| {
                                        v.base_ref = id;
                                        cx.notify();
                                    });
                                }),
                            )
                        },
                    ));
                }
                pills
            }
        }
    }

    fn place_note(&self) -> String {
        match &self.place {
            Place::Project => "The agent works directly in the project folder.".into(),
            Place::NewWorktree => {
                "The agent works on its own branch in a separate checkout. You can revert its turns.".into()
            }
            Place::Existing(path) => {
                format!("The agent works in {path}, on its branch, beside any session already there.")
            }
        }
    }

    /// Save as Draft: a session that keeps the prompt unsent, on servers that
    /// keep drafts.
    fn can_draft(&self, cx: &App) -> bool {
        self.conn().read(cx).info.as_ref().is_some_and(|i| i.drafts)
            && self.can_start(cx)
            && (!self.prompt.read(cx).value().trim().is_empty() || !self.attachments.is_empty())
    }

    /// Save as Draft from the menu bar or ⌘S.
    pub fn save_draft(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.can_draft(cx) {
            self.create(true, window, cx);
        }
    }

    /// The + menu: images, Save as Draft, and picking up a terminal session.
    fn plus_menu(&self, drafts: bool, import: bool, cx: &mut Context<Self>) -> AnyElement {
        let can_draft = self.can_draft(cx);
        let this = cx.entity().downgrade();
        Button::new("ns-plus")
            .small()
            .ghost()
            .icon(IconName::Plus)
            .tooltip("Add images, and more")
            .dropdown_menu_with_anchor(Anchor::BottomLeft, move |mut menu, _, cx| {
                let has_image = cx
                    .read_from_clipboard()
                    .is_some_and(|i| i.entries().iter().any(|e| matches!(e, ClipboardEntry::Image(_))));
                let (a, b) = (this.clone(), this.clone());
                menu = menu
                    .item(
                        PopupMenuItem::new("Files…")
                            .icon(Icon::new(L::Image))
                            .on_click(move |_, window, cx| {
                                let _ = a.update(cx, |v, cx| v.pick_images(window, cx));
                            }),
                    )
                    .item(
                        PopupMenuItem::new("Paste Image")
                            .icon(Icon::new(L::ClipboardPaste))
                            .disabled(!has_image)
                            .on_click(move |_, window, cx| {
                                let _ = b.update(cx, |v, cx| {
                                    v.paste(window, cx);
                                });
                            }),
                    );
                if drafts || import {
                    menu = menu.separator();
                }
                if drafts {
                    let w = this.clone();
                    menu = menu.item(
                        PopupMenuItem::new("Save as Draft")
                            .icon(Icon::new(L::Download))
                            .action(Box::new(crate::SaveDraft))
                            .disabled(!can_draft)
                            .on_click(move |_, window, cx| {
                                let _ = w.update(cx, |v, cx| v.save_draft(window, cx));
                            }),
                    );
                }
                if import {
                    let w = this.clone();
                    menu = menu.item(
                        PopupMenuItem::new("Continue a terminal session…")
                            .icon(Icon::new(L::SquareTerminal))
                            .on_click(move |_, window, cx| {
                                let _ = w.update(cx, |v, cx| v.import(window, cx));
                            }),
                    );
                }
                menu
            })
            .into_any_element()
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
                        .tooltip(move |window, cx| Tooltip::new(format!("{e} Click to try again.")).build(window, cx))
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

    /// "What should we build in lemmary-app?"
    fn render_heading(&self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let big = |d: gpui_kit::Div| d.text_size(px(26.)).line_height(px(34.));
        match self.project(cx) {
            Some(pr) => big(h_flex().flex_wrap().justify_center().gap(px(7.)))
                .text_color(p.subtext)
                .child("What should we build in")
                .child(
                    h_flex()
                        // The project's name opens the projects, as the bar's does.
                        .child(
                            Button::new("heading-project")
                                .ghost()
                                .h(px(40.))
                                // Its hover reaches past the name, which
                                // stays where the text has it.
                                .px_1()
                                .mx(px(-4.))
                                .tooltip("Choose a project")
                                .child(
                                    div()
                                        .text_size(px(26.))
                                        .line_height(px(34.))
                                        .text_color(p.text)
                                        .font_weight(FontWeight::SEMIBOLD)
                                        // A link's underline says it opens something.
                                        .underline()
                                        .text_decoration_color(p.overlay)
                                        .child(pr.name.clone()),
                                )
                                .dropdown_menu_with_anchor(Anchor::TopCenter, self.project_menu(cx)),
                        )
                        .child("?"),
                )
                .into_any_element(),
            None => v_flex()
                .items_center()
                .gap_3()
                .child(big(div()).text_color(p.subtext).child("Choose a project to start"))
                .child(
                    Button::new("add-folder")
                        .small()
                        .outline()
                        .icon(Icon::new(L::FolderPlus))
                        .label("Add a folder…")
                        .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.browse(window, cx))),
                )
                .into_any_element(),
        }
    }
}

impl Render for NewSessionView {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let drafts = self.conn().read(cx).info.as_ref().is_some_and(|i| i.drafts);
        let project = self.project(cx);
        let git = project.as_ref().is_some_and(|p| p.is_git_repo);
        let import = self.agent.read(cx).info(cx).is_some_and(|h| h.caps.import) && project.is_some();
        let can_start = self.can_start(cx);
        let listening = self.speech.read(cx).status().is_active();
        let dictation = crate::ui::state::in_app_bundle() && self.speech.read(cx).is_available(cx);
        let empty = self.prompt.read(cx).value().trim().is_empty() && self.attachments.is_empty();

        let mut trailing = vec![self.agent.update(cx, |a, cx| a.pill(cx))];
        if dictation && (empty || listening) {
            trailing.push(prompt_box::mic_button(
                listening,
                true,
                cx.listener(|this, _: &ClickEvent, _, cx| this.speech.update(cx, |s, cx| s.toggle(cx))),
            ));
        }
        trailing.push(if self.creating {
            Spinner::new().small().into_any_element()
        } else {
            prompt_box::send_button(
                can_start,
                "Start (Return)",
                cx.listener(|this, _: &ClickEvent, window, cx| this.create(false, window, cx)),
            )
        });
        let mut footer_start = Vec::new();
        footer_start.push(self.server_pill(cx));
        if project.is_some() {
            footer_start.push(self.place_pill(git, cx));
        }
        let mut footer_end = if git { self.branch_pills(cx) } else { Vec::new() };
        footer_end.extend(self.agent.update(cx, |a, cx| a.mode_pill(cx)));
        let input = div()
            // Before the prompt's own Paste: GPUI runs ⌘V's binding before
            // any key-down listener.
            .capture_action(cx.listener(|this, _: &Paste, window, cx| {
                if this.paste(window, cx) {
                    cx.stop_propagation();
                }
            }))
            .child(Textarea::new(&self.prompt).appearance(false))
            .into_any_element();
        let prompt = PromptBox {
            above: None,
            attachments: self.render_attachments(&p, cx),
            input: Some(input),
            leading: vec![self.plus_menu(drafts, import, cx)],
            trailing,
            footer_start,
            footer_end,
            max_width: prompt_box::max_width(cx),
        }
        .render(&p);
        let bar = self.render_bar(window, cx);
        let heading = self.render_heading(&p, cx);
        v_flex()
            .size_full()
            .key_context("NewSession")
            .on_key_down(cx.listener(|_, ev: &gpui_kit::KeyDownEvent, _, cx| {
                if ev.keystroke.key == "escape" {
                    cx.emit(NewSessionEvent::Cancelled);
                    cx.stop_propagation();
                }
            }))
            .on_drop(cx.listener(|this, paths: &ExternalPaths, window, cx| {
                this.attach_paths(paths.paths().to_vec(), window, cx)
            }))
            .child(bar)
            .child(
                v_flex()
                    .id("new-session")
                    .flex_1()
                    .min_h_0()
                    .overflow_y_scroll()
                    .items_center()
                    .justify_end()
                    .px_6()
                    .pb_8()
                    .child(heading),
            )
            .child(
                v_flex()
                    .w_full()
                    .flex_shrink_0()
                    .items_center()
                    .gap_2()
                    // As a session's composer sits, so the box stays put
                    // when the session starts.
                    .px_5()
                    .pb_3()
                    .pt_1()
                    .when_some(self.error.clone(), |d, e| {
                        d.child(
                            div()
                                .w_full()
                                .when_some(prompt_box::max_width(cx), |d, w| d.max_w(px(w)))
                                .text_sm()
                                .text_color(p.red)
                                .child(e),
                        )
                    })
                    .child(prompt),
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
