//! Choosing an agent and its options: the agents the server has, each with
//! its version, what's missing and its usage gauge; then its model, effort
//! and permissions. New Session, Fork and Import share it, as the Swift
//! client's HarnessChoiceRows and HarnessOptionRows.

use gpui_kit::component::button::Button;
use gpui_kit::component::input::{Input, InputState};
use gpui_kit::component::menu::{DropdownMenu as _, PopupMenuItem};
use gpui_kit::component::{Icon, IconName, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    AnyElement, App, AppContext as _, ClickEvent, Context, Entity, EventEmitter, FontWeight, InteractiveElement as _,
    IntoElement, ParentElement as _, Render, SharedString, StatefulInteractiveElement as _, Styled as _, Window, div,
};

use crate::conn::ServerConn;
use crate::protocol::{Choice, HarnessInfo};
use crate::store::prefs::AgentChoice;
use crate::ui::common::{caption, limits_gauge_view};
use crate::ui::palette::Palette;

const CUSTOM: &str = "__custom";

pub struct AgentChanged;

impl EventEmitter<AgentChanged> for AgentPicker {}

pub struct AgentPicker {
    pub conn: Entity<ServerConn>,
    pub harness: Option<String>,
    /// "" for the default, CUSTOM for one typed.
    model: String,
    custom: Entity<InputState>,
    effort: String,
    mode: String,
}

impl AgentPicker {
    pub fn new(conn: Entity<ServerConn>, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let custom = cx.new(|cx| InputState::new(window, cx).placeholder("Model id"));
        Self {
            conn,
            harness: None,
            model: String::new(),
            custom,
            effort: String::new(),
            mode: String::new(),
        }
    }

    pub fn info(&self, cx: &App) -> Option<HarnessInfo> {
        self.conn.read(cx).harness(self.harness.as_deref()?).cloned()
    }

    pub fn usable(&self, cx: &App) -> bool {
        self.info(cx).is_some_and(|h| h.usable())
    }

    /// Chooses an agent, with the options it was last used with where it
    /// still offers them; else its defaults.
    pub fn choose(
        &mut self,
        harness: &str,
        restore: Option<&AgentChoice>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.harness = Some(harness.to_string());
        let Some(h) = self.info(cx) else {
            cx.notify();
            return;
        };
        let restore = restore.filter(|r| r.harness == harness);
        self.model = String::new();
        if let Some(m) = restore.and_then(|r| r.model.clone()) {
            if h.models.iter().any(|c| c.id == m) {
                self.model = m;
            } else if h.caps.free_model {
                self.model = CUSTOM.into();
                self.custom.update(cx, |c, cx| c.set_value(m, window, cx));
            }
        }
        self.effort = restore
            .and_then(|r| r.effort.clone())
            .filter(|e| h.efforts_for(self.model_id(cx).as_deref()).iter().any(|c| &c.id == e))
            .unwrap_or_default();
        self.mode = restore
            .and_then(|r| r.mode.clone())
            .filter(|m| h.modes.iter().any(|c| &c.id == m))
            .or_else(|| h.default_mode.clone())
            .or_else(|| h.modes.first().map(|c| c.id.clone()))
            .unwrap_or_default();
        cx.emit(AgentChanged);
        cx.notify();
    }

    /// The model to ask for: none for the default.
    pub fn model_id(&self, cx: &App) -> Option<String> {
        let free = self.info(cx).is_some_and(|h| h.caps.free_model && h.models.is_empty());
        if self.model == CUSTOM || free {
            let typed = self.custom.read(cx).value().trim().to_string();
            return (!typed.is_empty()).then_some(typed);
        }
        (!self.model.is_empty()).then(|| self.model.clone())
    }

    pub fn effort(&self) -> Option<String> {
        (!self.effort.is_empty()).then(|| self.effort.clone())
    }

    pub fn mode(&self) -> Option<String> {
        (!self.mode.is_empty()).then(|| self.mode.clone())
    }

    pub fn choice(&self, cx: &App) -> Option<AgentChoice> {
        Some(AgentChoice {
            harness: self.harness.clone()?,
            model: self.model_id(cx),
            effort: self.effort(),
            mode: self.mode(),
        })
    }

    /// The agents to choose from, the usable first in the server's order.
    fn harnesses(&self, cx: &App) -> Option<Vec<HarnessInfo>> {
        self.conn.read(cx).harnesses.clone()
    }

    pub fn render_agents(&self, cx: &mut Context<Self>) -> AnyElement {
        let p = Palette::of(cx);
        let Some(list) = self.harnesses(cx) else {
            return caption("Checking installed agents…", &p).into_any_element();
        };
        if list.is_empty() {
            return caption(
                "No agents are installed on the server. Install Claude Code, Codex, Pi or an ACP agent there.",
                &p,
            )
            .into_any_element();
        }
        let limits = self.conn.read(cx).index.limits.clone();
        let mut col = v_flex().gap_0p5();
        for h in list {
            let selected = self.harness.as_deref() == Some(h.id.as_str());
            let usable = h.usable();
            let id = h.id.clone();
            let gauge = limits
                .get(&h.id)
                .and_then(|l| limits_gauge_view(format!("gauge-{}", h.id), l, &p));
            col = col.child(
                h_flex()
                    .id(SharedString::from(format!("agent-{}", h.id)))
                    .gap_2()
                    .px_2()
                    .py_1()
                    .rounded_md()
                    .when(!usable, |d| d.opacity(0.5))
                    .when(usable, |d| d.cursor_pointer().hover(|d| d.bg(p.surface0.opacity(0.5))))
                    .when(selected, |d| d.bg(p.surface0))
                    .child(Icon::new(IconName::Bot).small().text_color(p.overlay))
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
                            .when(!usable, |d| {
                                d.child(
                                    div()
                                        .text_xs()
                                        .text_color(p.yellow)
                                        .child(h.hint.clone().unwrap_or_else(|| "Not signed in".into())),
                                )
                            })
                            .when_some(gauge, |d, g| d.child(g)),
                    )
                    .when(selected, |d| {
                        d.child(Icon::new(IconName::Check).small().text_color(p.accent))
                    })
                    .when(usable, |d| {
                        d.on_click(cx.listener(move |this, _: &ClickEvent, window, cx| {
                            if this.harness.as_deref() != Some(id.as_str()) {
                                this.choose(&id, None, window, cx)
                            }
                        }))
                    }),
            );
        }
        col.into_any_element()
    }

    pub fn render_options(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        let p = Palette::of(cx);
        let h = self.info(cx)?;
        let this = cx.entity().downgrade();
        let mut col = v_flex().gap_2();
        // Model
        if !h.models.is_empty() || h.caps.free_model {
            let mut choices: Vec<(String, String)> = vec![(String::new(), "Default".into())];
            choices.extend(h.models.iter().map(|m| (m.id.clone(), m.name.clone())));
            if h.caps.free_model && !h.models.is_empty() {
                choices.push((CUSTOM.into(), "Custom…".into()));
            }
            let current = choices
                .iter()
                .find(|(id, _)| *id == self.model)
                .map(|(_, n)| n.clone())
                .unwrap_or_else(|| "Default".into());
            if !h.models.is_empty() {
                let w = this.clone();
                col = col.child(row(
                    "Model",
                    picker("model", current, choices, self.model.clone(), move |id, window, cx| {
                        let _ = w.update(cx, |v, cx| v.set_model(id, window, cx));
                    }),
                    None,
                    &p,
                ));
            }
            if self.model == CUSTOM || h.models.is_empty() {
                col = col.child(row(
                    "Model id",
                    Input::new(&self.custom).small().into_any_element(),
                    None,
                    &p,
                ));
            }
        }
        // Effort
        let efforts: Vec<Choice> = h.efforts_for(self.model_id(cx).as_deref()).to_vec();
        if !efforts.is_empty() {
            let mut choices: Vec<(String, String)> = vec![(String::new(), "Default".into())];
            choices.extend(efforts.iter().map(|e| (e.id.clone(), e.name.clone())));
            let current = choices
                .iter()
                .find(|(id, _)| *id == self.effort)
                .map(|(_, n)| n.clone())
                .unwrap_or_else(|| "Default".into());
            let description = efforts
                .iter()
                .find(|e| e.id == self.effort)
                .and_then(|e| e.description.clone());
            let w = this.clone();
            col = col.child(row(
                "Effort",
                picker("effort", current, choices, self.effort.clone(), move |id, _, cx| {
                    let _ = w.update(cx, |v, cx| {
                        v.effort = id;
                        cx.emit(AgentChanged);
                        cx.notify();
                    });
                }),
                description,
                &p,
            ));
        }
        // Permissions
        if !h.modes.is_empty() {
            let choices: Vec<(String, String)> = h.modes.iter().map(|m| (m.id.clone(), m.name.clone())).collect();
            let current = choices
                .iter()
                .find(|(id, _)| *id == self.mode)
                .map(|(_, n)| n.clone())
                .unwrap_or_else(|| self.mode.clone());
            let description = h
                .modes
                .iter()
                .find(|m| m.id == self.mode)
                .and_then(|m| m.description.clone());
            let w = this.clone();
            col = col.child(row(
                "Permissions",
                picker("mode", current, choices, self.mode.clone(), move |id, _, cx| {
                    let _ = w.update(cx, |v, cx| {
                        v.mode = id;
                        cx.emit(AgentChanged);
                        cx.notify();
                    });
                }),
                description,
                &p,
            ));
        }
        Some(col.into_any_element())
    }

    fn set_model(&mut self, id: String, _: &mut Window, cx: &mut Context<Self>) {
        self.model = id;
        // An effort the model doesn't offer goes back to the default.
        if let Some(h) = self.info(cx)
            && !h
                .efforts_for(self.model_id(cx).as_deref())
                .iter()
                .any(|e| e.id == self.effort)
        {
            self.effort.clear();
        }
        cx.emit(AgentChanged);
        cx.notify();
    }
}

/// A label, its control, and what the choice means.
pub fn row(label: &str, control: AnyElement, description: Option<String>, p: &Palette) -> AnyElement {
    v_flex()
        .gap_0p5()
        .child(
            h_flex()
                .gap_3()
                .child(
                    div()
                        .w(gpui_kit::px(96.))
                        .text_sm()
                        .text_color(p.subtext)
                        .font_weight(FontWeight::MEDIUM)
                        .child(label.to_string()),
                )
                .child(div().flex_1().min_w_0().child(control)),
        )
        .when_some(description, |d, desc| d.child(caption(desc, p).pl(gpui_kit::px(108.))))
        .into_any_element()
}

/// A button that shows the choice, and a menu of the others. A choice with
/// the id "-" is a separator.
pub fn picker(
    id: &'static str,
    label: String,
    choices: Vec<(String, String)>,
    current: String,
    on_pick: impl Fn(String, &mut Window, &mut App) + 'static,
) -> AnyElement {
    let on_pick = std::rc::Rc::new(on_pick);
    let button = Button::new(id)
        .small()
        .outline()
        .label(label)
        .icon(IconName::ChevronsUpDown)
        .dropdown_menu(move |mut menu, _, _| {
            // A long list, such as the fonts, scrolls.
            if choices.len() > 16 {
                menu = menu.scrollable(true).max_h(gpui_kit::px(420.));
            }
            for (cid, name) in &choices {
                if cid == "-" {
                    menu = menu.separator();
                    continue;
                }
                let (f, cid2) = (on_pick.clone(), cid.clone());
                menu = menu.item(
                    PopupMenuItem::new(name.clone())
                        .checked(*cid == current)
                        .on_click(move |_, window, cx| f(cid2.clone(), window, cx)),
                );
            }
            menu
        });
    // As wide as its label, not the row.
    h_flex().child(button).into_any_element()
}

impl Render for AgentPicker {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let agents = self.render_agents(cx);
        let options = self.render_options(cx);
        v_flex().gap_3().child(agents).when_some(options, |d, o| d.child(o))
    }
}
