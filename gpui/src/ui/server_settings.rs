//! A server's settings: rad's version and its update, the agents with their
//! versions, sign-ins and updates, and the agents new sessions start with
//! and that name them. As `ServerSettingsView.swift`.

use std::collections::HashMap;

use gpui_kit::assets::IconName as L;
use gpui_kit::component::WindowExt as _;
use gpui_kit::component::button::{Button, ButtonVariant, ButtonVariants as _};
use gpui_kit::component::input::{Input, InputEvent, InputState};
use gpui_kit::component::menu::{DropdownMenu as _, PopupMenuItem};
use gpui_kit::component::spinner::Spinner;
use gpui_kit::component::{Disableable as _, Icon, IconName, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    Anchor, AnyElement, App, AppContext as _, ClickEvent, ClipboardItem, Context, Entity, FontWeight,
    InteractiveElement as _, IntoElement, ParentElement as _, Render, SharedString, StatefulInteractiveElement as _,
    Styled as _, Subscription, Window, div, px,
};
use serde_json::{Value, json};

use crate::conn::ServerConn;
use crate::protocol::{AgentSpec, HarnessInfo, ServerSettings, ServerTask, TaskList, UpdateInfo};
use crate::ui::agent_picker::{picker, row};
use crate::ui::common::{caption, limits_gauge_view};
use crate::ui::dialogs::{self, Choice};
use crate::ui::palette::Palette;
use crate::ui::session::explain;
use crate::ui::task::{Phase, TaskProgress};

pub struct ServerSettingsView {
    conn: Entity<ServerConn>,
    update: Option<Result<UpdateInfo, String>>,
    update_unsupported: bool,
    checking: bool,
    settings: Option<ServerSettings>,
    settings_unsupported: bool,
    settings_error: HashMap<&'static str, String>,
    /// Tasks followed, by agent id ("" for rad).
    tasks: HashMap<String, Entity<TaskProgress>>,
    errors: Vec<String>,
    loaded_for: Option<String>,
    _subscriptions: Vec<Subscription>,
}

impl ServerSettingsView {
    pub fn new(conn: Entity<ServerConn>, cx: &mut Context<Self>) -> Self {
        let sub = cx.observe(&conn, |this, conn, cx| {
            // Loaded again once connected, and after rad updates.
            let c = conn.read(cx);
            let key = c
                .is_connected()
                .then(|| c.info.as_ref().map(|i| i.version.clone()).unwrap_or_default());
            if key.is_some() && key != this.loaded_for {
                this.loaded_for = key;
                this.load(cx);
            }
            cx.notify();
        });
        let mut this = Self {
            conn,
            update: None,
            update_unsupported: false,
            checking: false,
            settings: None,
            settings_unsupported: false,
            settings_error: HashMap::new(),
            tasks: HashMap::new(),
            errors: Vec::new(),
            loaded_for: None,
            _subscriptions: vec![sub],
        };
        let c = this.conn.read(cx);
        if c.is_connected() {
            this.loaded_for = Some(c.info.as_ref().map(|i| i.version.clone()).unwrap_or_default());
            this.load(cx);
        }
        this
    }

    fn load(&mut self, cx: &mut Context<Self>) {
        self.conn.update(cx, |c, cx| c.refresh_harnesses(cx));
        self.check(false, cx);
        let settings = self.conn.read(cx).call("server.settings", json!({}));
        let tasks = self.conn.read(cx).call("task.list", json!({}));
        cx.spawn(async move |this, cx| {
            let s = settings.await;
            let t = tasks.await;
            let _ = this.update(cx, |this, cx| {
                match s {
                    Ok(v) => this.settings = Some(serde_json::from_value(v).unwrap_or_default()),
                    Err(e) if e.code == "method_not_found" => this.settings_unsupported = true,
                    Err(_) => {}
                }
                if let Ok(v) = t {
                    for task in serde_json::from_value::<TaskList>(v).unwrap_or_default().tasks {
                        if task.running() && task.session_id.is_none() {
                            this.follow(task, cx);
                        }
                    }
                }
                cx.notify();
            });
        })
        .detach();
    }

    fn check(&mut self, refresh: bool, cx: &mut Context<Self>) {
        self.checking = true;
        let call = self
            .conn
            .read(cx)
            .call("server.checkUpdate", json!({"refresh": refresh}));
        cx.spawn(async move |this, cx| {
            let r = call.await;
            let _ = this.update(cx, |this, cx| {
                this.checking = false;
                match r {
                    Ok(v) => this.update = Some(serde_json::from_value(v).map_err(|e| e.to_string())),
                    Err(e) if e.code == "method_not_found" => this.update_unsupported = true,
                    Err(e) => this.update = Some(Err(e.to_string())),
                }
                cx.notify();
            });
        })
        .detach();
        cx.notify();
    }

    fn follow(&mut self, task: ServerTask, cx: &mut Context<Self>) -> Entity<TaskProgress> {
        let key = task.harness.clone().unwrap_or_default();
        if let Some(t) = self.tasks.get(&key).filter(|t| t.read(cx).task.id == task.id) {
            return t.clone();
        }
        let conn = self.conn.clone();
        let progress = cx.new(|cx| TaskProgress::new(conn, task, cx));
        self._subscriptions.push(cx.observe(&progress, |_, _, cx| cx.notify()));
        self.tasks.insert(key, progress.clone());
        progress
    }

    /// Starts a task (an update, a sign-in) and shows it.
    fn start(
        &mut self,
        method: &'static str,
        params: Value,
        failure: String,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let call = self.conn.read(cx).call(method, params);
        cx.spawn_in(window, async move |this, cx| {
            let r = call.await;
            let _ = this.update_in(cx, |this, window, cx| {
                match r.map(serde_json::from_value::<ServerTask>) {
                    Ok(Ok(task)) => {
                        let p = this.follow(task, cx);
                        show_task(p, this.conn.clone(), window, cx);
                    }
                    Ok(Err(e)) => this.errors.push(format!("{failure}: {e}")),
                    Err(e) => this.errors.push(format!("{failure}: {}", explain(&e))),
                }
                cx.notify();
            });
        })
        .detach();
    }

    fn update_rad(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let busy = self
            .update
            .as_ref()
            .and_then(|u| u.as_ref().ok())
            .map(|u| u.busy.clone())
            .unwrap_or_default();
        if busy.is_empty() {
            self.start("server.update", json!({}), "Couldn't update rad".into(), window, cx);
            return;
        }
        let this = cx.entity().downgrade();
        let names = busy.iter().map(|t| format!("“{t}”")).collect::<Vec<_>>().join(", ");
        let title = if busy.len() == 1 {
            "Interrupt 1 session?".to_string()
        } else {
            format!("Interrupt {} sessions?", busy.len())
        };
        dialogs::confirm(
            window,
            cx,
            &title,
            &format!("Updating restarts rad, which interrupts {names}."),
            vec![Choice::new(
                "Update Anyway",
                ButtonVariant::Danger,
                move |window, cx| {
                    let _ = this.update(cx, |this, cx| {
                        this.start("server.update", json!({}), "Couldn't update rad".into(), window, cx)
                    });
                },
            )],
        );
    }

    /// Saves the settings with one changed; on failure that one goes back.
    fn set(&mut self, field: &'static str, spec: Option<AgentSpec>, cx: &mut Context<Self>) {
        let Some(mut s) = self.settings.clone() else { return };
        let before = s.clone();
        match field {
            "default" => s.default_agent = spec,
            _ => s.system_agent = spec,
        }
        self.settings = Some(s.clone());
        self.settings_error.remove(field);
        let call = self
            .conn
            .read(cx)
            .call("server.setSettings", serde_json::to_value(&s).unwrap_or_default());
        cx.spawn(async move |this, cx| {
            let r = call.await;
            let _ = this.update(cx, |this, cx| {
                match r {
                    Ok(v) => {
                        if let Ok(s) = serde_json::from_value(v) {
                            this.settings = Some(s);
                        }
                    }
                    Err(e) => {
                        this.settings = Some(before);
                        this.settings_error.insert(field, explain(&e));
                    }
                }
                cx.notify();
            });
        })
        .detach();
        cx.notify();
    }

    fn render_rad(&mut self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let c = self.conn.read(cx);
        let version = c
            .info
            .as_ref()
            .map(|i| i.version.clone())
            .or_else(|| {
                self.update
                    .as_ref()
                    .and_then(|u| u.as_ref().ok())
                    .map(|u| u.current.clone())
            })
            .unwrap_or_else(|| "—".into());
        let task = self.tasks.get("").cloned();
        let active = task.as_ref().is_some_and(|t| t.read(cx).active());
        let mut col = v_flex().gap_1().child(row(
            "Version",
            div().text_sm().child(version).into_any_element(),
            None,
            p,
        ));
        if self.update_unsupported {
            col = col.child(caption(
                "This rad is too old to update from here. Update it on the computer with `rad update`, or from the Remote Agent Server app's menu.",
                p,
            ));
            return col.into_any_element();
        }
        if active && let Some(t) = &task {
            let restarting = t.read(cx).phase == Phase::Restarting;
            let (t2, conn) = (t.clone(), self.conn.clone());
            col =
                col.child(
                    h_flex()
                        .gap_2()
                        .child(Spinner::new().small())
                        .child(div().text_sm().child(if restarting {
                            "Restarting rad…"
                        } else {
                            "Updating rad…"
                        }))
                        .child(Button::new("show-rad-task").xsmall().ghost().label("Show").on_click(
                            move |_: &ClickEvent, window, cx| show_task(t2.clone(), conn.clone(), window, cx),
                        )),
                );
        }
        let mut footer: Option<String> = None;
        match &self.update {
            None if self.checking => col = col.child(caption("Checking for updates…", p)),
            None => {}
            Some(Err(e)) => col = col.child(caption(format!("Couldn't check for updates: {e}"), p)),
            Some(Ok(u)) => {
                let installable = u.available && u.via.as_deref().is_some_and(|v| !v.is_empty());
                if u.available
                    && let Some(latest) = &u.latest
                {
                    col = col.child(div().text_sm().child(format!("rad {latest} is available.")));
                    let mut buttons = h_flex().gap_2();
                    if installable {
                        buttons = buttons.child(
                            Button::new("update-rad")
                                .small()
                                .primary()
                                .label(format!("Update to {latest}"))
                                .disabled(active)
                                .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.update_rad(window, cx))),
                        );
                    } else if let Some(r) = &u.reason {
                        col = col.child(caption(r.clone(), p));
                    }
                    if let Some(url) = u.url.clone() {
                        buttons = buttons.child(
                            Button::new("whats-new")
                                .small()
                                .ghost()
                                .label("What's New")
                                .on_click(move |_: &ClickEvent, _, cx| cx.open_url(&url)),
                        );
                    }
                    col = col.child(buttons);
                } else if u.latest.is_some() && u.reason.is_none() {
                    col = col.child(div().text_sm().text_color(p.green).child("Up to date."));
                } else if let Some(r) = &u.reason {
                    col = col.child(caption(r.clone(), p));
                }
                if installable {
                    let mut f = if u.via.as_deref() == Some("app") {
                        "The Remote Agent Server app on the Mac installs the update and restarts, with rad.".to_string()
                    } else {
                        "rad installs the update and restarts. Sessions reconnect by themselves.".to_string()
                    };
                    if !u.busy.is_empty() {
                        f += &format!(
                            " Updating now interrupts {} busy session{}: {}.",
                            u.busy.len(),
                            if u.busy.len() == 1 { "" } else { "s" },
                            u.busy.join(", ")
                        );
                    }
                    footer = Some(f);
                }
            }
        }
        col = col.child(
            h_flex().child(
                Button::new("check-again")
                    .small()
                    .outline()
                    .label("Check Again")
                    .disabled(self.checking || active)
                    .on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.check(true, cx))),
            ),
        );
        if let Some(f) = footer {
            col = col.child(caption(f, p));
        }
        col.into_any_element()
    }

    fn render_agents(&mut self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let c = self.conn.read(cx);
        let Some(list) = c.harnesses.clone() else {
            return caption("Checking installed agents…", p).into_any_element();
        };
        if list.is_empty() {
            return caption("No agents are installed on the server.", p).into_any_element();
        }
        let limits = c.index.limits.clone();
        let mut col = v_flex().gap_2();
        let mut any_actions = false;
        for h in &list {
            any_actions |= h.caps.update || h.caps.login;
            let task = self.tasks.get(&h.id).cloned();
            let active = task.as_ref().is_some_and(|t| t.read(cx).active());
            let gauge = limits
                .get(&h.id)
                .and_then(|l| limits_gauge_view(format!("ss-gauge-{}", h.id), l, p));
            let mut right = h_flex().gap_1();
            if active && let Some(t) = &task {
                let login = t.read(cx).task.kind == "login";
                let (t2, conn) = (t.clone(), self.conn.clone());
                right = right
                    .child(Spinner::new().small())
                    .child(caption(if login { "Signing in…" } else { "Updating…" }, p))
                    .child(
                        Button::new(SharedString::from(format!("show-{}", h.id)))
                            .xsmall()
                            .ghost()
                            .label("Show")
                            .on_click(move |_: &ClickEvent, window, cx| {
                                show_task(t2.clone(), conn.clone(), window, cx)
                            }),
                    );
            } else {
                if h.caps.login && !h.auth_ok {
                    let id = h.id.clone();
                    let name = h.name.clone();
                    right = right.child(
                        Button::new(SharedString::from(format!("signin-{}", h.id)))
                            .xsmall()
                            .outline()
                            .label("Sign In")
                            .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| {
                                this.start(
                                    "harness.login",
                                    json!({"harness": id}),
                                    format!("Couldn't sign in to {name}"),
                                    window,
                                    cx,
                                )
                            })),
                    );
                }
                if h.caps.update || (h.caps.login && h.auth_ok) {
                    right = right.child(self.agent_menu(h, task.clone(), cx));
                }
            }
            col = col.child(
                h_flex()
                    .gap_2()
                    .items_start()
                    .child(crate::ui::badges::harness_icon(&h.id, p).small())
                    .child(
                        v_flex()
                            .flex_1()
                            .min_w_0()
                            .child(h_flex().gap_2().child(div().text_sm().child(h.name.clone())).when_some(
                                h.version.clone(),
                                |d, v| {
                                    d.child(
                                        div()
                                            .text_xs()
                                            .font_family(crate::ui::fonts::code())
                                            .text_color(p.overlay)
                                            .child(v),
                                    )
                                },
                            ))
                            .child(if h.auth_ok {
                                div().text_xs().text_color(p.green).child("Ready")
                            } else {
                                div()
                                    .text_xs()
                                    .text_color(p.peach)
                                    .child(h.hint.clone().unwrap_or_else(|| "Not signed in".into()))
                            })
                            .when_some(gauge, |d, g| d.child(g)),
                    )
                    .child(right),
            );
        }
        if any_actions {
            col = col.child(caption(
                "Update runs the agent's own updater on the server. Sign In shows the agent's sign-in page here, and takes the code it gives you.",
                p,
            ));
        }
        col.into_any_element()
    }

    fn agent_menu(&self, h: &HarnessInfo, task: Option<Entity<TaskProgress>>, cx: &mut Context<Self>) -> AnyElement {
        let this = cx.entity().downgrade();
        let (id, name, update, relogin) = (h.id.clone(), h.name.clone(), h.caps.update, h.caps.login && h.auth_ok);
        let conn = self.conn.clone();
        Button::new(SharedString::from(format!("agent-menu-{}", h.id)))
            .xsmall()
            .ghost()
            .icon(IconName::Ellipsis)
            .dropdown_menu_with_anchor(Anchor::TopRight, move |mut menu, _, _| {
                if update {
                    let (w, id, name) = (this.clone(), id.clone(), name.clone());
                    menu = menu.item(PopupMenuItem::new("Update").on_click(move |_, window, cx| {
                        let _ = w.update(cx, |v, cx| {
                            v.start(
                                "harness.update",
                                json!({"harness": id}),
                                format!("Couldn't update {name}"),
                                window,
                                cx,
                            )
                        });
                    }));
                }
                if relogin {
                    let (w, id, name) = (this.clone(), id.clone(), name.clone());
                    menu = menu.item(PopupMenuItem::new("Sign In Again").on_click(move |_, window, cx| {
                        let _ = w.update(cx, |v, cx| {
                            v.start(
                                "harness.login",
                                json!({"harness": id}),
                                format!("Couldn't sign in to {name}"),
                                window,
                                cx,
                            )
                        });
                    }));
                }
                if let Some(t) = task.clone() {
                    let conn = conn.clone();
                    menu = menu.item(
                        PopupMenuItem::new("Show Last Output")
                            .on_click(move |_, window, cx| show_task(t.clone(), conn.clone(), window, cx)),
                    );
                }
                menu
            })
            .into_any_element()
    }

    fn render_agent_setting(&mut self, field: &'static str, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let Some(settings) = self.settings.clone() else {
            return caption("Loading…", p).into_any_element();
        };
        let harnesses = self.conn.read(cx).harnesses.clone().unwrap_or_default();
        let current = if field == "default" {
            settings.default_agent.clone()
        } else {
            settings.system_agent.clone()
        };
        let none_label = if field == "default" { "Last Used" } else { "None" };
        let mut agents: Vec<(String, String)> = vec![(String::new(), none_label.into())];
        agents.extend(
            harnesses
                .iter()
                .filter(|h| field == "default" || h.caps.one_shot)
                .map(|h| (h.id.clone(), h.name.clone())),
        );
        if let Some(c) = &current
            && !agents.iter().any(|(id, _)| *id == c.harness)
        {
            agents.push((c.harness.clone(), c.harness.clone()));
        }
        let cur_id = current.as_ref().map(|c| c.harness.clone()).unwrap_or_default();
        let label = agents
            .iter()
            .find(|(id, _)| *id == cur_id)
            .map(|(_, n)| n.clone())
            .unwrap_or_default();
        let w = cx.entity().downgrade();
        let mut col = v_flex().gap_2().child(row(
            "Agent",
            picker(
                if field == "default" {
                    "default-agent"
                } else {
                    "system-agent"
                },
                label,
                agents,
                cur_id.clone(),
                move |id, _, cx| {
                    let spec = (!id.is_empty()).then_some(AgentSpec {
                        harness: id,
                        model: None,
                        effort: None,
                    });
                    let _ = w.update(cx, |v, cx| v.set(field, spec, cx));
                },
            ),
            None,
            p,
        ));
        if let Some(c) = current.clone() {
            let h = harnesses.iter().find(|h| h.id == c.harness).cloned();
            let mut models: Vec<(String, String)> = vec![(String::new(), "Default".into())];
            if let Some(h) = &h {
                models.extend(h.models.iter().map(|m| (m.id.clone(), m.name.clone())));
            }
            if let Some(m) = &c.model
                && !models.iter().any(|(id, _)| id == m)
            {
                models.push((m.clone(), m.clone()));
            }
            let mid = c.model.clone().unwrap_or_default();
            let mlabel = models
                .iter()
                .find(|(id, _)| *id == mid)
                .map(|(_, n)| n.clone())
                .unwrap_or_default();
            if models.len() > 1 {
                let (w, c2) = (cx.entity().downgrade(), c.clone());
                col = col.child(row(
                    "Model",
                    picker(
                        if field == "default" {
                            "default-model"
                        } else {
                            "system-model"
                        },
                        mlabel,
                        models,
                        mid,
                        move |id, _, cx| {
                            let spec = AgentSpec {
                                harness: c2.harness.clone(),
                                model: (!id.is_empty()).then_some(id),
                                effort: None,
                            };
                            let _ = w.update(cx, |v, cx| v.set(field, Some(spec), cx));
                        },
                    ),
                    None,
                    p,
                ));
            }
            let efforts = h
                .as_ref()
                .map(|h| h.efforts_for(c.model.as_deref()).to_vec())
                .unwrap_or_default();
            if !efforts.is_empty() || c.effort.is_some() {
                let mut opts: Vec<(String, String)> = vec![(String::new(), "Default".into())];
                opts.extend(efforts.iter().map(|e| (e.id.clone(), e.name.clone())));
                if let Some(e) = &c.effort
                    && !opts.iter().any(|(id, _)| id == e)
                {
                    opts.push((e.clone(), e.clone()));
                }
                let eid = c.effort.clone().unwrap_or_default();
                let elabel = opts
                    .iter()
                    .find(|(id, _)| *id == eid)
                    .map(|(_, n)| n.clone())
                    .unwrap_or_default();
                let (w, c2) = (cx.entity().downgrade(), c.clone());
                col = col.child(row(
                    "Effort",
                    picker(
                        if field == "default" {
                            "default-effort"
                        } else {
                            "system-effort"
                        },
                        elabel,
                        opts,
                        eid,
                        move |id, _, cx| {
                            let spec = AgentSpec {
                                harness: c2.harness.clone(),
                                model: c2.model.clone(),
                                effort: (!id.is_empty()).then_some(id),
                            };
                            let _ = w.update(cx, |v, cx| v.set(field, Some(spec), cx));
                        },
                    ),
                    None,
                    p,
                ));
            }
        }
        if let Some(e) = self.settings_error.get(field) {
            col = col.child(div().text_xs().text_color(p.red).child(e.clone()));
        }
        col = col.child(caption(
            if field == "default" {
                "New sessions start with this agent, model and effort, on every device, and so do the sessions agents start with rad's tools unless they choose their own. With Last Used, each device starts with the agent it used last."
            } else {
                "Names new sessions and their worktree branches from the first prompt, in a call without tools that leaves no history with the agent. A small, fast model is best. With None, a session's title is its prompt's first line."
            },
            p,
        ));
        col.into_any_element()
    }
}

/// A section, in the card General's groups have.
fn section(title: &'static str, body: AnyElement, p: &Palette) -> AnyElement {
    crate::ui::settings::group(
        title,
        vec![div().px_3().py_2p5().child(body).into_any_element()],
        None,
        p,
    )
}

impl Render for ServerSettingsView {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let connected = self.conn.read(cx).is_connected();
        let rad = self.render_rad(&p, cx);
        let agents = self.render_agents(&p, cx);
        let mut col = v_flex()
            .w_full()
            .min_w_0()
            .gap_5()
            .when(!connected, |d| d.opacity(0.5))
            .child(section("rad", rad, &p))
            .child(section("Agents", agents, &p));
        if !self.settings_unsupported {
            let default = self.render_agent_setting("default", &p, cx);
            let system = self.render_agent_setting("system", &p, cx);
            col = col
                .child(section("New Sessions", default, &p))
                .child(section("System Agent", system, &p));
        }
        for e in &self.errors {
            col = col.child(div().text_sm().text_color(p.red).child(e.clone()));
        }
        col
    }
}

/// A task's sheet: what it's doing, its sign-in page and code, its output.
pub fn show_task(progress: Entity<TaskProgress>, conn: Entity<ServerConn>, window: &mut Window, cx: &mut App) {
    let view = cx.new(|cx| TaskView::new(progress, conn, window, cx));
    window.open_dialog(cx, move |dialog, _, _| dialog.w(px(560.)).child(view.clone()));
}

pub struct TaskView {
    progress: Entity<TaskProgress>,
    conn: Entity<ServerConn>,
    code: Entity<InputState>,
    _subscriptions: Vec<Subscription>,
}

impl TaskView {
    fn new(
        progress: Entity<TaskProgress>,
        conn: Entity<ServerConn>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> Self {
        let code = cx.new(|cx| InputState::new(window, cx).placeholder("Paste the code here"));
        let subs = vec![
            cx.observe(&progress, |_, _, cx| cx.notify()),
            cx.subscribe_in(&code, window, |this, _, ev: &InputEvent, window, cx| {
                if let InputEvent::PressEnter { .. } = ev {
                    this.send_code(window, cx);
                }
            }),
        ];
        Self {
            progress,
            conn,
            code,
            _subscriptions: subs,
        }
    }

    fn send_code(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let text = self.code.read(cx).value().trim().to_string();
        if text.is_empty() {
            return;
        }
        let call = self.progress.update(cx, |p, cx| p.input(text, cx));
        cx.spawn_in(window, async move |this, cx| {
            if call.await.is_ok() {
                let _ = this.update_in(cx, |this, window, cx| {
                    this.code.update(cx, |c, cx| c.set_value("", window, cx))
                });
            }
        })
        .detach();
    }
}

impl Render for TaskView {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let pr = self.progress.read(cx);
        let task = pr.task.clone();
        let phase = pr.phase.clone();
        let agent = task
            .harness
            .as_ref()
            .and_then(|h| {
                self.conn
                    .read(cx)
                    .harness(h)
                    .map(|i| i.name.clone())
                    .or(Some(h.clone()))
            })
            .unwrap_or_default();
        let title = match task.kind.as_str() {
            "update_server" => "Updating rad".to_string(),
            "login" => format!("Signing In to {agent}"),
            "commit" => "Committing".to_string(),
            _ => format!("Updating {agent}"),
        };
        let url = task
            .url
            .clone()
            .filter(|u| u.starts_with("http://") || u.starts_with("https://"));
        let status = match &phase {
            Phase::Running if url.is_some() && task.kind == "login" => "Waiting for you to sign in…".to_string(),
            Phase::Running => "Running…".into(),
            Phase::Restarting => "Restarting rad…".into(),
            Phase::Done => match task.kind.as_str() {
                "update_server" => format!("rad {} is running.", task.version.clone().unwrap_or_default()),
                "login" => "Signed in.".into(),
                "commit" => "Done.".into(),
                _ => match &task.version {
                    Some(v) => format!("{agent} {v}"),
                    None => "Updated.".into(),
                },
            },
            Phase::Failed(e) => format!("Failed: {e}"),
            Phase::Cancelled => "Cancelled.".into(),
        };
        let running = matches!(phase, Phase::Running | Phase::Restarting);
        let pr2 = self.progress.clone();
        v_flex()
            .gap_3()
            .child(div().text_lg().font_weight(FontWeight::SEMIBOLD).child(title))
            .child(
                h_flex()
                    .gap_2()
                    .when(running, |d| d.child(Spinner::new().small()))
                    .child(
                        div()
                            .text_sm()
                            .text_color(match phase {
                                Phase::Failed(_) => p.red,
                                Phase::Done => p.green,
                                _ => p.subtext,
                            })
                            .child(status),
                    ),
            )
            .when_some(url.filter(|_| running), |d, url| {
                let (open, copy) = (url.clone(), url.clone());
                d.child(
                    h_flex()
                        .gap_2()
                        .child(
                            Button::new("open-page")
                                .small()
                                .primary()
                                .label("Open Sign-In Page")
                                .on_click(move |_: &ClickEvent, _, cx| cx.open_url(&open)),
                        )
                        .child(Button::new("copy-link").small().outline().label("Copy Link").on_click(
                            move |_: &ClickEvent, _, cx| cx.write_to_clipboard(ClipboardItem::new_string(copy.clone())),
                        )),
                )
            })
            .when(running && task.input, |d| {
                d.child(
                    h_flex()
                        .gap_2()
                        .child(div().flex_1().child(Input::new(&self.code).small()))
                        .child(
                            Button::new("send-code")
                                .small()
                                .primary()
                                .label("Send")
                                .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.send_code(window, cx))),
                        ),
                )
            })
            .when(!task.output.is_empty(), |d| {
                d.child(
                    div()
                        .id("task-output")
                        .max_h(px(280.))
                        .overflow_y_scroll()
                        .p_2()
                        .rounded_md()
                        .bg(p.crust)
                        .text_xs()
                        .font_family(crate::ui::fonts::code())
                        .text_color(p.subtext)
                        .child(task.output.clone()),
                )
            })
            .child(
                h_flex()
                    .gap_2()
                    .justify_end()
                    .when(running, |d| {
                        d.child(
                            Button::new("cancel-task")
                                .small()
                                .outline()
                                .label("Cancel")
                                .on_click(move |_: &ClickEvent, _, cx| pr2.update(cx, |p, cx| p.cancel(cx))),
                        )
                    })
                    .child(
                        Button::new("done")
                            .small()
                            .primary()
                            .label("Done")
                            .on_click(|_: &ClickEvent, window, cx| window.close_dialog(cx)),
                    ),
            )
    }
}

#[allow(dead_code)]
fn icon() -> Icon {
    Icon::new(L::Server)
}
