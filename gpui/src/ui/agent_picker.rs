//! Choosing an agent and its options: the agents the server has that can
//! start a session, the others folded into one line that opens to what each
//! lacks, the usage gauge, and the model, effort and permissions. The prompt
//! box shows them as pills (`pill`, `mode_pill`), Fork as rows
//! (`render_agents`, `render_options`); Import takes the choice. As the Swift
//! client's HarnessChoiceRows and HarnessOptionRows.

use gpui_kit::component::button::Button;
use gpui_kit::component::input::{Input, InputState};
use gpui_kit::component::menu::{DropdownMenu as _, PopupMenuItem};
use gpui_kit::component::tooltip::Tooltip;
use gpui_kit::component::{Icon, IconName, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    AnyElement, App, AppContext as _, ClickEvent, Context, Entity, EventEmitter, FontWeight, InteractiveElement as _,
    IntoElement, ParentElement as _, Pixels, Render, SharedString, StatefulInteractiveElement as _, Styled as _,
    Window, div, px,
};

use crate::conn::ServerConn;
use crate::protocol::{Choice, HarnessInfo};
use crate::store::prefs::AgentChoice;
use crate::ui::common::{caption, limits_gauge_view};
use crate::ui::palette::Palette;

const CUSTOM: &str = "__custom";

const NONE_INSTALLED: &str =
    "No agents are installed on the server. Install Claude Code, Codex, Pi or an ACP agent there.";

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
    /// The agents that can't be used, opened to what each lacks.
    unavailable_open: bool,
}

/// One of the chosen agent's options: its control, and the choice's name
/// with what it means.
struct AgentOption {
    label: &'static str,
    control: AnyElement,
    meaning: Option<(String, String)>,
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
            unavailable_open: false,
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

    fn harnesses(&self, cx: &App) -> Option<Vec<HarnessInfo>> {
        self.conn.read(cx).harnesses.clone()
    }

    /// The agents that can start a session, as a list; the others in a line.
    pub fn render_agents(&self, cx: &mut Context<Self>) -> AnyElement {
        let p = Palette::of(cx);
        let Some(list) = self.harnesses(cx) else {
            return caption("Checking installed agents…", &p).into_any_element();
        };
        if list.is_empty() {
            return caption(NONE_INSTALLED, &p).into_any_element();
        }
        let limits = self.conn.read(cx).index.limits.clone();
        let mut col = v_flex().gap_0p5();
        for h in list.iter().filter(|h| h.usable()) {
            let selected = self.harness.as_deref() == Some(h.id.as_str());
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
                    .cursor_pointer()
                    .hover(|d| d.bg(p.surface0.opacity(0.5)))
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
                            .when_some(gauge, |d, g| d.child(g)),
                    )
                    .when(selected, |d| {
                        d.child(Icon::new(IconName::Check).small().text_color(p.accent))
                    })
                    .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| {
                        if this.harness.as_deref() != Some(id.as_str()) {
                            this.choose(&id, None, window, cx)
                        }
                    })),
            );
        }
        col.children(self.render_unavailable(&list, &p, cx)).into_any_element()
    }

    /// The agent's options as rows, each with what its choice means.
    pub fn render_options(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        let p = Palette::of(cx);
        let options = self.options(cx);
        if options.is_empty() {
            return None;
        }
        let mut col = v_flex().gap_2();
        for o in options {
            col = col.child(row(o.label, o.control, o.meaning.map(|(_, what)| what), &p));
        }
        Some(col.into_any_element())
    }

    /// The agents that can't start a session, in one line. A click opens it
    /// to what each lacks; with no agent to use, it's open and stands out.
    fn render_unavailable(&self, list: &[HarnessInfo], p: &Palette, cx: &mut Context<Self>) -> Option<AnyElement> {
        let summary = unavailable_summary(list)?;
        let none_usable = !list.iter().any(|h| h.usable());
        let open = self.unavailable_open || none_usable;
        let lacks: Vec<(String, String)> = list
            .iter()
            .filter(|h| !h.usable())
            .map(|h| (h.name.clone(), lack(h)))
            .collect();
        let mut line = h_flex().id("agents-unavailable").gap_1().text_xs();
        if none_usable {
            line = line
                .text_color(p.yellow)
                .child(div().flex_1().min_w_0().child(format!("No agent is ready: {summary}")));
        } else {
            let hover = p.subtext;
            line = line
                .text_color(p.overlay)
                .cursor_pointer()
                .hover(move |d| d.text_color(hover))
                .child(
                    Icon::new(if open {
                        IconName::ChevronDown
                    } else {
                        IconName::ChevronRight
                    })
                    .xsmall()
                    .text_color(p.overlay),
                )
                .child(div().flex_1().min_w_0().child(summary))
                .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                    this.unavailable_open = !this.unavailable_open;
                    cx.notify();
                }));
            if !open {
                let details = lacks
                    .iter()
                    .map(|(name, lack)| format!("{name}: {lack}"))
                    .collect::<Vec<_>>()
                    .join("\n");
                line = line.tooltip(move |window, cx| Tooltip::new(details.clone()).build(window, cx));
            }
        }
        let mut col = v_flex().gap_0p5().child(line);
        if open {
            for (name, lack) in lacks {
                col = col.child(
                    h_flex()
                        .items_start()
                        .gap_2()
                        // Under the line's text, past its chevron.
                        .when(!none_usable, |d| d.pl_4())
                        .text_xs()
                        .child(div().flex_shrink_0().text_color(p.subtext).child(name))
                        .child(div().flex_1().min_w_0().text_color(p.overlay).child(lack)),
                );
            }
        }
        Some(col.into_any_element())
    }

    fn options(&self, cx: &mut Context<Self>) -> Vec<AgentOption> {
        let Some(h) = self.info(cx) else {
            return Vec::new();
        };
        let this = cx.entity().downgrade();
        let mut out = Vec::new();
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
                out.push(AgentOption {
                    label: "Model",
                    control: field_picker("model", current, choices, self.model.clone(), move |id, window, cx| {
                        let _ = w.update(cx, |v, cx| v.set_model(id, window, cx));
                    }),
                    meaning: None,
                });
            }
            if self.model == CUSTOM || h.models.is_empty() {
                out.push(AgentOption {
                    label: "Model id",
                    // An input is as wide as it's let: room for an id.
                    control: div()
                        .w(px(240.))
                        .child(Input::new(&self.custom).small())
                        .into_any_element(),
                    meaning: None,
                });
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
            let w = this.clone();
            out.push(AgentOption {
                label: "Effort",
                control: field_picker("effort", current, choices, self.effort.clone(), move |id, _, cx| {
                    let _ = w.update(cx, |v, cx| {
                        v.effort = id;
                        cx.emit(AgentChanged);
                        cx.notify();
                    });
                }),
                meaning: meaning(&efforts, &self.effort),
            });
        }
        // Permissions
        if !h.modes.is_empty() {
            let choices: Vec<(String, String)> = h.modes.iter().map(|m| (m.id.clone(), m.name.clone())).collect();
            let current = choices
                .iter()
                .find(|(id, _)| *id == self.mode)
                .map(|(_, n)| n.clone())
                .unwrap_or_else(|| self.mode.clone());
            let w = this.clone();
            out.push(AgentOption {
                label: "Permissions",
                control: field_picker("mode", current, choices, self.mode.clone(), move |id, _, cx| {
                    let _ = w.update(cx, |v, cx| {
                        v.mode = id;
                        cx.emit(AgentChanged);
                        cx.notify();
                    });
                }),
                meaning: meaning(&h.modes, &self.mode),
            });
        }
        out
    }

    /// "Opus 5.5 High": the model, else the agent, and the effort.
    fn pill_label(&self, cx: &App) -> String {
        let Some(h) = self.info(cx) else {
            return match self.harnesses(cx) {
                None => "Checking agents…".into(),
                Some(l) if l.is_empty() => "No agents installed".into(),
                Some(l) if !l.iter().any(|h| h.usable()) => "No agent ready".into(),
                Some(_) => "Choose an agent".into(),
            };
        };
        let model = match self.model_id(cx) {
            Some(id) => h.model_name(&id),
            None => h.name.clone(),
        };
        let effort = h
            .efforts_for(self.model_id(cx).as_deref())
            .iter()
            .find(|e| e.id == self.effort)
            .map(|e| e.name.clone());
        match effort {
            Some(e) => format!("{model} {e}"),
            None => model,
        }
    }

    /// The agent, its model and its effort, as one pill in the prompt box,
    /// with a menu of all three; the agents that can't start a session are
    /// in it too, greyed out with what they lack, and the agent's usage.
    pub fn pill(&self, cx: &mut Context<Self>) -> AnyElement {
        let p = Palette::of(cx);
        let ready = self.usable(cx);
        let icon = self
            .harness
            .as_deref()
            .map(|id| crate::ui::badges::harness_icon(id, &p));
        let label = self.pill_label(cx);
        let this = cx.entity().downgrade();
        let mut button = crate::ui::prompt_box::pill("agent-pill", icon, label);
        if !ready {
            button = button.text_color(p.yellow);
        }
        button
            .dropdown_menu_with_anchor(gpui_kit::Anchor::BottomRight, move |mut menu, window, cx| {
                menu = menu.max_w(px(380.)).min_w(px(220.));
                let Some(view) = this.upgrade() else { return menu };
                let v = view.read(cx);
                let p = Palette::of(cx);
                let list = v.harnesses(cx).unwrap_or_default();
                if list.is_empty() {
                    return menu.label(NONE_INSTALLED);
                }
                menu = menu.label("Agent");
                for h in list.iter().filter(|h| h.usable()) {
                    let (w, id) = (this.clone(), h.id.clone());
                    menu = menu.item(
                        PopupMenuItem::new(h.name.clone())
                            .icon(crate::ui::badges::harness_icon(&h.id, &p))
                            .checked(v.harness.as_deref() == Some(h.id.as_str()))
                            .on_click(move |_, window, cx| {
                                let _ = w.update(cx, |v, cx| {
                                    if v.harness.as_deref() != Some(id.as_str()) {
                                        v.choose(&id, None, window, cx)
                                    }
                                });
                            }),
                    );
                }
                for h in list.iter().filter(|h| !h.usable()) {
                    let (name, lack) = (h.name.clone(), lack(h));
                    menu = menu.item(
                        PopupMenuItem::element(move |_, cx| {
                            let p = Palette::of(cx);
                            v_flex()
                                .child(div().text_sm().child(name.clone()))
                                .child(div().text_xs().text_color(p.overlay).child(lack.clone()))
                        })
                        .icon(crate::ui::badges::harness_icon(&h.id, &p))
                        .disabled(true),
                    );
                }
                let Some(h) = v.info(cx).filter(|h| h.usable()) else {
                    return menu;
                };
                if let Some(l) = v.conn.read(cx).index.limits.get(&h.id) {
                    let gauge = crate::store::limits::limits_gauge(l, chrono::Utc::now());
                    if !gauge.is_empty() {
                        menu = menu.label(format!("Usage: {gauge}"));
                    }
                }
                // Model
                let free = h.caps.free_model;
                if !h.models.is_empty() {
                    let mut choices: Vec<(String, String)> = vec![(String::new(), "Default".into())];
                    choices.extend(h.models.iter().map(|m| (m.id.clone(), m.name.clone())));
                    // A custom model checks none of them.
                    let current = if v.model == CUSTOM {
                        CUSTOM.to_string()
                    } else {
                        v.model.clone()
                    };
                    let w = this.clone();
                    menu = crate::ui::prompt_box::choices(
                        menu.separator().label("Model"),
                        &choices,
                        &current,
                        std::rc::Rc::new(move |id, window, cx| {
                            let _ = w.update(cx, |v, cx| v.set_model(id, window, cx));
                        }),
                    );
                }
                if free {
                    let typed = v.custom.read(cx).value().trim().to_string();
                    let custom = v.model == CUSTOM || h.models.is_empty();
                    let label = if custom && !typed.is_empty() {
                        format!("Custom: {typed}…")
                    } else {
                        "Custom…".into()
                    };
                    if h.models.is_empty() {
                        menu = menu.separator().label("Model");
                    }
                    let w = this.clone();
                    menu = menu.item(PopupMenuItem::new(label).checked(custom && !typed.is_empty()).on_click(
                        move |_, window, cx| {
                            let Some(view) = w.upgrade() else { return };
                            let typed = view.read(cx).custom.read(cx).value().to_string();
                            let w = w.clone();
                            crate::ui::dialogs::prompt_text(
                                window,
                                cx,
                                "Model",
                                "The model's id, as the agent takes it.",
                                "Model id",
                                &typed,
                                "Use",
                                gpui_kit::component::button::ButtonVariant::Primary,
                                move |text, window, cx| {
                                    let _ = w.update(cx, |v, cx| {
                                        v.custom.update(cx, |c, cx| c.set_value(text.clone(), window, cx));
                                        let id = if text.is_empty() { String::new() } else { CUSTOM.into() };
                                        v.set_model(id, window, cx);
                                    });
                                },
                            );
                        },
                    ));
                }
                // Effort
                let efforts: Vec<Choice> = h.efforts_for(v.model_id(cx).as_deref()).to_vec();
                if !efforts.is_empty() {
                    let mut choices: Vec<(String, String)> = vec![(String::new(), "Default".into())];
                    choices.extend(efforts.iter().map(|e| (e.id.clone(), e.name.clone())));
                    let w = this.clone();
                    menu = crate::ui::prompt_box::choices(
                        menu.separator().label("Effort"),
                        &choices,
                        &v.effort,
                        std::rc::Rc::new(move |id, _, cx| {
                            let _ = w.update(cx, |v, cx| {
                                v.effort = id;
                                cx.emit(AgentChanged);
                                cx.notify();
                            });
                        }),
                    );
                }
                let _ = window;
                menu
            })
            .into_any_element()
    }

    /// The permissions as a pill, for the row under the prompt box.
    pub fn mode_pill(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        let h = self.info(cx).filter(|h| h.usable() && !h.modes.is_empty())?;
        let choices: Vec<(String, String)> = h.modes.iter().map(|m| (m.id.clone(), m.name.clone())).collect();
        let name = choices
            .iter()
            .find(|(id, _)| *id == self.mode)
            .map(|(_, n)| n.clone())
            .unwrap_or_else(|| self.mode.clone());
        let tooltip = meaning(&h.modes, &self.mode).map(|(_, what)| what);
        let (current, w) = (self.mode.clone(), cx.entity().downgrade());
        Some(crate::ui::prompt_box::pill_menu(
            "mode-pill",
            Some(crate::ui::prompt_box::mode_icon(&self.mode)),
            name,
            tooltip,
            move |menu, _, _| {
                let w = w.clone();
                crate::ui::prompt_box::choices(
                    menu,
                    &choices,
                    &current,
                    std::rc::Rc::new(move |id, _, cx| {
                        let _ = w.update(cx, |v, cx| {
                            v.mode = id;
                            cx.emit(AgentChanged);
                            cx.notify();
                        });
                    }),
                )
            },
        ))
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

/// The chosen one's name and what it means, where it says.
fn meaning(choices: &[Choice], id: &str) -> Option<(String, String)> {
    let c = choices.iter().find(|c| c.id == id)?;
    let what = c.description.clone().filter(|d| !d.is_empty())?;
    Some((c.name.clone(), what))
}

/// What keeps an agent from starting a session, in the server's words.
fn lack(h: &HarnessInfo) -> String {
    h.hint.clone().filter(|t| !t.is_empty()).unwrap_or_else(|| {
        if h.installed {
            "Not signed in".into()
        } else {
            "Not installed".into()
        }
    })
}

/// The agents that can't start a session, in a line: "Claude Code and Codex
/// aren't signed in; Pi isn't installed". None when every agent can.
fn unavailable_summary(list: &[HarnessInfo]) -> Option<String> {
    let part = |installed: bool, what: &str| {
        let names: Vec<&str> = list
            .iter()
            .filter(|h| !h.usable() && h.installed == installed)
            .map(|h| h.name.as_str())
            .collect();
        let verb = if names.len() == 1 { "isn't" } else { "aren't" };
        (!names.is_empty()).then(|| format!("{} {verb} {what}", join_names(&names)))
    };
    let parts: Vec<String> = [part(true, "signed in"), part(false, "installed")]
        .into_iter()
        .flatten()
        .collect();
    (!parts.is_empty()).then(|| parts.join("; "))
}

/// "A", "A and B", "A, B and C".
fn join_names(names: &[&str]) -> String {
    match names {
        [] => String::new(),
        [one] => one.to_string(),
        [rest @ .., last] => format!("{} and {last}", rest.join(", ")),
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
                        .w(px(96.))
                        .text_sm()
                        .text_color(p.subtext)
                        .font_weight(FontWeight::MEDIUM)
                        .child(label.to_string()),
                )
                .child(div().flex_1().min_w_0().child(control)),
        )
        .when_some(description, |d, desc| d.child(caption(desc, p).pl(px(108.))))
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
    menu_button(id, label, choices, current, None, on_pick)
}

/// A picker for a row of fields. It cuts a long choice short, so that the
/// row wraps before a field is wider than the form.
pub fn field_picker(
    id: &'static str,
    label: String,
    choices: Vec<(String, String)>,
    current: String,
    on_pick: impl Fn(String, &mut Window, &mut App) + 'static,
) -> AnyElement {
    menu_button(id, label, choices, current, Some(px(240.)), on_pick)
}

fn menu_button(
    id: &'static str,
    label: String,
    choices: Vec<(String, String)>,
    current: String,
    max_width: Option<Pixels>,
    on_pick: impl Fn(String, &mut Window, &mut App) + 'static,
) -> AnyElement {
    let on_pick = std::rc::Rc::new(on_pick);
    let mut button = Button::new(id)
        .small()
        .outline()
        .label(label)
        .icon(IconName::ChevronsUpDown);
    if let Some(w) = max_width {
        button = button.max_w(w);
    }
    let button = button.dropdown_menu(move |mut menu, _, _| {
        // A long list, such as the fonts, scrolls.
        if choices.len() > 16 {
            menu = menu.scrollable(true).max_h(px(420.));
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

#[cfg(test)]
mod tests {
    use super::*;

    fn agent(name: &str, installed: bool, auth_ok: bool) -> HarnessInfo {
        serde_json::from_value(serde_json::json!({
            "id": name.to_lowercase(),
            "name": name,
            "installed": installed,
            "authOk": auth_ok,
        }))
        .unwrap()
    }

    #[test]
    fn unavailable_agents_fold_into_a_line() {
        let ready = agent("Fake", true, true);
        assert_eq!(unavailable_summary(std::slice::from_ref(&ready)), None);
        assert_eq!(
            unavailable_summary(&[agent("Claude Code", true, false), ready.clone()]).as_deref(),
            Some("Claude Code isn't signed in")
        );
        assert_eq!(
            unavailable_summary(&[
                agent("Claude Code", true, false),
                agent("Codex", true, false),
                ready.clone()
            ])
            .as_deref(),
            Some("Claude Code and Codex aren't signed in")
        );
        assert_eq!(
            unavailable_summary(&[
                agent("Claude Code", true, false),
                agent("Codex", false, false),
                agent("Pi", false, false),
                agent("Gemini", false, true),
                ready,
            ])
            .as_deref(),
            Some("Claude Code isn't signed in; Codex, Pi and Gemini aren't installed")
        );
    }

    #[test]
    fn an_agent_lacks_what_the_server_says() {
        let mut h = agent("Codex", true, false);
        assert_eq!(lack(&h), "Not signed in");
        h.hint = Some("codex app-server failed: connection closed".into());
        assert_eq!(lack(&h), "codex app-server failed: connection closed");
        assert_eq!(lack(&agent("Pi", false, false)), "Not installed");
    }
}
