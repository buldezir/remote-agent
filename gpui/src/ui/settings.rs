//! Settings (⌘,): General (appearance, text sizes, the transcript, the
//! sidebar, the terminal, notifications) and Servers (each server's
//! settings). As `SettingsView.swift`.

use std::collections::HashMap;

use gpui_kit::component::WindowExt as _;
use gpui_kit::component::button::{Button, ButtonVariant, ButtonVariants as _};
use gpui_kit::component::slider::{Slider, SliderEvent, SliderState};
use gpui_kit::component::switch::Switch;
use gpui_kit::component::{Disableable as _, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    AnyElement, App, AppContext as _, ClickEvent, Context, Entity, FontWeight, InteractiveElement as _, IntoElement,
    ParentElement as _, Render, SharedString, StatefulInteractiveElement as _, Styled as _, Subscription, Window, div,
    px,
};

use crate::conn::{ConnState, ServerConn};
use crate::store::prefs::Prefs;
use crate::store::sidebar::Layout;
use crate::ui::agent_picker::picker;
use crate::ui::common::caption;
use crate::ui::dialogs::{self, Choice};
use crate::ui::fonts;
use crate::ui::palette::{Palette, apply_appearance};
use crate::ui::server_settings::ServerSettingsView;
use crate::ui::state::{app_view, prefs, update_prefs};

pub fn open(conns: Vec<Entity<ServerConn>>, server: Option<usize>, window: &mut Window, cx: &mut App) {
    if window.has_active_dialog(cx) {
        return;
    }
    let view = cx.new(|cx| SettingsView::new(conns, server, window, cx));
    let size = window.viewport_size();
    window.open_dialog(cx, move |dialog, _, _| {
        dialog
            .w((size.width * 0.9).min(px(720.)))
            .title("Settings")
            .child(div().h(size.height * 0.75).child(view.clone()))
    });
}

#[derive(PartialEq, Clone, Copy)]
enum Tab {
    General,
    Servers,
}

pub struct SettingsView {
    tab: Tab,
    conns: Vec<Entity<ServerConn>>,
    selected: usize,
    servers: HashMap<String, Entity<ServerSettingsView>>,
    text_size: Entity<SliderState>,
    message_size: Entity<SliderState>,
    code_size: Entity<SliderState>,
    _subscriptions: Vec<Subscription>,
}

impl SettingsView {
    fn new(conns: Vec<Entity<ServerConn>>, server: Option<usize>, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let p = prefs(cx).clone();
        let text_size = cx.new(|_| SliderState::new().min(11.).max(18.).step(1.).default_value(p.text_size));
        let message_size = cx.new(|_| {
            SliderState::new()
                .min(10.)
                .max(24.)
                .step(1.)
                .default_value(p.message_size)
        });
        let code_size = cx.new(|_| {
            SliderState::new()
                .min(fonts::CODE_SIZES.0)
                .max(fonts::CODE_SIZES.1)
                .step(1.)
                .default_value(p.code_size)
        });
        let subs = vec![
            cx.subscribe_in(&code_size, window, |_, _, ev: &SliderEvent, _, cx| {
                let (SliderEvent::Change(v) | SliderEvent::Release(v)) = ev;
                let v = v.start();
                update_prefs(cx, |p| p.code_size = v);
            }),
            cx.subscribe_in(&text_size, window, |_, _, ev: &SliderEvent, _, cx| {
                let (SliderEvent::Change(v) | SliderEvent::Release(v)) = ev;
                let v = v.start();
                update_prefs(cx, |p| p.text_size = v);
            }),
            cx.subscribe_in(&message_size, window, |_, _, ev: &SliderEvent, _, cx| {
                let (SliderEvent::Change(v) | SliderEvent::Release(v)) = ev;
                let v = v.start();
                update_prefs(cx, |p| p.message_size = v);
            }),
        ];
        Self {
            tab: if server.is_some() { Tab::Servers } else { Tab::General },
            selected: server.unwrap_or(0),
            conns,
            servers: HashMap::new(),
            text_size,
            message_size,
            code_size,
            _subscriptions: subs,
        }
    }

    fn render_general(&mut self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let prefs = prefs(cx).clone();
        let defaults = Prefs::default();
        let at_defaults = prefs.appearance == defaults.appearance
            && prefs.text_size == defaults.text_size
            && prefs.message_size == defaults.message_size
            && prefs.message_font == defaults.message_font
            && prefs.code_font == defaults.code_font
            && prefs.code_size == defaults.code_size
            && prefs.group_tool_calls == defaults.group_tool_calls
            && prefs.limit_prompt_width == defaults.limit_prompt_width
            && prefs.sidebar_layout == defaults.sidebar_layout
            && prefs.terminal_placement == defaults.terminal_placement
            && prefs.terminal_copy_on_select == defaults.terminal_copy_on_select
            && prefs.notifications == defaults.notifications;
        let theme = picker(
            "theme",
            match prefs.appearance.as_str() {
                "dark" => "Dark",
                "light" => "Light",
                _ => "System",
            }
            .into(),
            vec![
                ("system".into(), "System".into()),
                ("dark".into(), "Dark".into()),
                ("light".into(), "Light".into()),
            ],
            prefs.appearance.clone(),
            |id, window, cx| {
                update_prefs(cx, |p| p.appearance = id);
                apply_appearance(window, cx);
            },
        );
        let layout = Layout::from_key(&prefs.sidebar_layout);
        let layout_picker = picker(
            "layout",
            layout.title().into(),
            [Layout::Inbox, Layout::Projects, Layout::Columns]
                .iter()
                .map(|l| (l.key().to_string(), l.title().to_string()))
                .collect(),
            layout.key().into(),
            |id, _, cx| update_prefs(cx, |p| p.sidebar_layout = id),
        );
        let summary = match layout {
            Layout::Inbox => "Sessions from every server, those that need you first.",
            Layout::Projects => "Each server's projects, with their sessions under them.",
            Layout::Columns => "Needs You, Running and each project in the sidebar, their sessions beside it.",
        };
        let placement = picker(
            "placement",
            match prefs.terminal_placement.as_str() {
                "below" => "Below the Messages",
                "right" => "Right Sidebar",
                _ => "Instead of the Messages",
            }
            .into(),
            vec![
                ("over".into(), "Instead of the Messages".into()),
                ("below".into(), "Below the Messages".into()),
                ("right".into(), "Right Sidebar".into()),
            ],
            prefs.terminal_placement.clone(),
            |id, _, cx| update_prefs(cx, |p| p.terminal_placement = id),
        );
        let families = fonts::installed_families(cx);
        let message_font = fonts::shown(&prefs.message_font, families);
        let mut message_choices = vec![("".to_string(), "System".to_string()), ("-".into(), String::new())];
        message_choices.extend(families.iter().map(|f| (f.clone(), f.clone())));
        let message_font_picker = picker(
            "message-font",
            if message_font.is_empty() {
                "System".into()
            } else {
                message_font.clone()
            },
            message_choices,
            message_font,
            |id, _, cx| update_prefs(cx, |p| p.message_font = id),
        );
        let code_font = fonts::shown(&prefs.code_font, families);
        let code_font = if code_font == fonts::MONO {
            String::new()
        } else {
            code_font
        };
        let mut code_choices = vec![(String::new(), fonts::MONO.to_string()), ("-".into(), String::new())];
        code_choices.extend(
            fonts::monospaced_families(cx)
                .iter()
                .filter(|f| *f != fonts::MONO)
                .map(|f| (f.clone(), f.clone())),
        );
        let code_font_picker = picker(
            "code-font",
            if code_font.is_empty() {
                fonts::MONO.into()
            } else {
                code_font.clone()
            },
            code_choices,
            code_font,
            |id, _, cx| update_prefs(cx, |p| p.code_font = id),
        );
        let switch = |id: &'static str, on: bool, set: fn(&mut Prefs, bool)| {
            Switch::new(id)
                .checked(on)
                .on_change(move |v: &bool, _, cx| {
                    let v = *v;
                    update_prefs(cx, |p| set(p, v))
                })
                .into_any_element()
        };
        let slider = |state: &Entity<SliderState>, value: f32| {
            h_flex()
                .gap_3()
                .child(div().w(px(180.)).child(Slider::new(state)))
                .child(caption(format!("{value} pt"), p).w(px(36.)))
                .into_any_element()
        };
        let mut col = v_flex()
            .w_full()
            .min_w_0()
            .gap_5()
            .child(group(
                "Appearance",
                vec![setting("Theme", theme, p)],
                Some("Catppuccin Latte when light, Frappé when dark.".into()),
                p,
            ))
            .child(group(
                "Interface",
                vec![setting("Text size", slider(&self.text_size, prefs.text_size), p)],
                Some("Lists, labels and the controls under the prompt.".into()),
                p,
            ))
            .child(group(
                "Messages",
                vec![
                    setting("Font", message_font_picker, p),
                    setting("Text size", slider(&self.message_size, prefs.message_size), p),
                ],
                Some("Your prompts and the agent's replies.".into()),
                p,
            ))
            .child(group(
                "Code",
                vec![
                    setting("Font", code_font_picker, p),
                    setting("Text size", slider(&self.code_size, prefs.code_size), p),
                ],
                Some("Code in replies, tool calls, diffs and the terminal.".into()),
                p,
            ))
            .child(group(
                "Transcript",
                vec![setting(
                    "Group tool calls",
                    switch("group-tools", prefs.group_tool_calls, |p, v| p.group_tool_calls = v),
                    p,
                )],
                Some("Folds each run of the agent's tool calls into one line, such as “Ran 4 commands, edited 3 files”, which opens to show them.".into()),
                p,
            ))
            .child(group(
                "Prompt",
                vec![setting(
                    "Limit its width",
                    switch("limit-prompt-width", prefs.limit_prompt_width, |p, v| p.limit_prompt_width = v),
                    p,
                )],
                Some(format!(
                    "Keeps the prompt box, in a session and a new one, to {} points: as wide as on a 13-inch laptop, centred in a wider window. Off, it's as wide as the window.",
                    crate::ui::prompt_box::LIMITED_WIDTH as u32
                )),
                p,
            ))
            .child(group(
                "Sidebar",
                vec![setting("Layout", layout_picker, p)],
                Some(format!("{summary} The View menu switches them too, with ⌘1, ⌘2 and ⌘3.")),
                p,
            ))
            .child(group(
                "Terminal",
                vec![
                    setting("Opens", placement, p),
                    setting(
                        "Copy on select",
                        switch("copy-on-select", prefs.terminal_copy_on_select, |p, v| {
                            p.terminal_copy_on_select = v
                        }),
                        p,
                    ),
                ],
                Some("Where a session's terminal opens; in a narrow window it takes the place of the messages. Copy on select puts the text you select on the clipboard.".into()),
                p,
            ))
            .child(group(
                "Notifications",
                vec![setting(
                    "Show notifications",
                    switch("notifications", prefs.notifications, |p, v| p.notifications = v),
                    p,
                )],
                Some("When a session needs you, finishes or fails, unless the window shows it.".into()),
                p,
            ));
        if let Some(build) = crate::store::build_update::current() {
            let from = if prefs.update_host.is_empty() {
                "Check for Update asks which Mac to pull builds from.".to_string()
            } else {
                format!("Builds come from {} over ssh.", prefs.update_host)
            };
            col = col.child(group(
                "Updates",
                vec![
                    setting(
                        "This build",
                        caption(build.summary(chrono::Utc::now()), p).into_any_element(),
                        p,
                    ),
                    setting(
                        "Builds",
                        h_flex()
                            .gap_2()
                            .child(
                                Button::new("update-host")
                                    .small()
                                    .ghost()
                                    .label("Change Host…")
                                    .on_click(|_: &ClickEvent, window, cx| {
                                        crate::ui::build_update::ask_host(window, cx)
                                    }),
                            )
                            .child(
                                Button::new("check-update")
                                    .small()
                                    .outline()
                                    .label("Check for Update…")
                                    .on_click(|_: &ClickEvent, window, cx| crate::ui::build_update::check(window, cx)),
                            )
                            .into_any_element(),
                        p,
                    ),
                ],
                Some(from),
                p,
            ));
        }
        col.child(
            h_flex().justify_end().child(
                Button::new("restore")
                    .small()
                    .ghost()
                    .label("Restore Defaults")
                    .disabled(at_defaults)
                    .on_click(cx.listener(|this, _: &ClickEvent, window, cx| {
                        let d = Prefs::default();
                        update_prefs(cx, |p| {
                            p.appearance = d.appearance;
                            p.text_size = d.text_size;
                            p.message_size = d.message_size;
                            p.message_font = d.message_font;
                            p.code_font = d.code_font;
                            p.code_size = d.code_size;
                            p.group_tool_calls = d.group_tool_calls;
                            p.limit_prompt_width = d.limit_prompt_width;
                            p.sidebar_layout = d.sidebar_layout;
                            p.terminal_placement = d.terminal_placement;
                            p.terminal_copy_on_select = d.terminal_copy_on_select;
                            p.notifications = d.notifications;
                        });
                        apply_appearance(window, cx);
                        this.text_size.update(cx, |s, cx| s.set_value(d.text_size, window, cx));
                        this.message_size
                            .update(cx, |s, cx| s.set_value(d.message_size, window, cx));
                        this.code_size.update(cx, |s, cx| s.set_value(d.code_size, window, cx));
                    })),
            ),
        )
        .into_any_element()
    }

    fn render_servers(&mut self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let mut list = v_flex().w(px(190.)).flex_shrink_0().gap_0p5();
        for (i, conn) in self.conns.iter().enumerate() {
            let c = conn.read(cx);
            let color = match c.state {
                ConnState::Connected => p.green,
                ConnState::Connecting(_) => p.yellow,
                ConnState::Failed(_) => p.red,
            };
            let selected = i == self.selected;
            list = list.child(
                h_flex()
                    .id(("server", i))
                    .gap_2()
                    .px_2()
                    .py_1()
                    .rounded_md()
                    .cursor_pointer()
                    .when(selected, |d| d.bg(p.surface0))
                    .child(div().size(px(7.)).rounded_full().bg(color))
                    .child(
                        v_flex()
                            .flex_1()
                            .min_w_0()
                            .child(div().text_sm().truncate().child(c.name.clone()))
                            .child(caption(c.base_url.clone().unwrap_or_default(), p).truncate()),
                    )
                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                        this.selected = i;
                        cx.notify();
                    })),
            );
        }
        list = list.child(Button::new("add-server").small().ghost().label("Add Server").on_click(
            |_: &ClickEvent, window, cx| {
                window.close_dialog(cx);
                if let Some(app) = app_view(cx) {
                    app.update(cx, |a, cx| a.show_pairing(cx));
                }
            },
        ));
        let detail: AnyElement =
            match self.conns.get(self.selected).cloned() {
                None => v_flex()
                    .flex_1()
                    .items_center()
                    .justify_center()
                    .child(div().text_sm().child("No Servers"))
                    .child(caption("Add a server to manage it here.", p))
                    .into_any_element(),
                Some(conn) => {
                    let id = conn.read(cx).server_id.clone();
                    let view = self
                        .servers
                        .entry(id)
                        .or_insert_with(|| {
                            let conn = conn.clone();
                            cx.new(|cx| ServerSettingsView::new(conn, cx))
                        })
                        .clone();
                    let c = conn.read(cx);
                    let name = c.name.clone();
                    let state = match &c.state {
                        ConnState::Connected => None,
                        ConnState::Connecting(r) => Some(r.clone().unwrap_or_else(|| "Connecting…".into())),
                        ConnState::Failed(e) => Some(e.to_string()),
                    };
                    let (c1, c2) = (conn.clone(), conn.clone());
                    v_flex()
                        .flex_1()
                        .min_w_0()
                        .gap_4()
                        .child(
                            h_flex()
                                .gap_2()
                                .child(
                                    div()
                                        .flex_1()
                                        .min_w_0()
                                        .truncate()
                                        .text_lg()
                                        .font_weight(FontWeight::SEMIBOLD)
                                        .child(name.clone()),
                                )
                                .child(
                                    Button::new("reconnect")
                                        .small()
                                        .outline()
                                        .label("Reconnect")
                                        .on_click(move |_: &ClickEvent, _, cx| c1.read(cx).retry()),
                                )
                                .child(
                                    Button::new("remove").small().danger().label("Remove Server…").on_click(
                                        move |_: &ClickEvent, window, cx| remove_server(c2.clone(), window, cx),
                                    ),
                                ),
                        )
                        .when_some(state, |d, s| d.child(caption(s, p)))
                        .child(view)
                        .into_any_element()
                }
            };
        h_flex()
            .size_full()
            .items_start()
            .gap_4()
            .child(list)
            .child(
                div()
                    .id("server-detail")
                    .flex_1()
                    .min_w_0()
                    .h_full()
                    .overflow_y_scroll()
                    .child(detail),
            )
            .into_any_element()
    }
}

/// A group of settings, as System Settings has them: its title, a card of
/// rows divided by rules, and what they do under it.
pub fn group(title: &str, rows: Vec<AnyElement>, footer: Option<String>, p: &Palette) -> AnyElement {
    let mut card = v_flex()
        .w_full()
        .rounded_lg()
        .border_1()
        .border_color(p.surface0)
        .bg(p.mantle);
    for (i, row) in rows.into_iter().enumerate() {
        card = card.child(
            div()
                .when(i > 0, |d| d.border_t_1().border_color(p.surface0))
                .child(row),
        );
    }
    v_flex()
        .w_full()
        .min_w_0()
        .gap_1p5()
        .child(
            div()
                .px_1()
                .text_sm()
                .font_weight(FontWeight::SEMIBOLD)
                .text_color(p.subtext)
                .child(title.to_string()),
        )
        .child(card)
        .when_some(footer, |d, f| d.child(caption(f, p).px_1()))
        .into_any_element()
}

/// A row of a group: its label at the start, its control at the end.
pub fn setting(label: &str, control: AnyElement, p: &Palette) -> AnyElement {
    h_flex()
        .w_full()
        .min_h(px(40.))
        .gap_4()
        .px_3()
        .py_1p5()
        .child(
            div()
                .flex_1()
                .min_w_0()
                .text_sm()
                .text_color(p.text)
                .child(label.to_string()),
        )
        .child(div().flex_shrink_0().child(control))
        .into_any_element()
}

/// "Remove {name}?": unpairs it and forgets it.
pub fn remove_server(conn: Entity<ServerConn>, window: &mut Window, cx: &mut App) {
    let name = conn.read(cx).name.clone();
    let id = conn.read(cx).server_id.clone();
    dialogs::confirm(
        window,
        cx,
        &format!("Remove {name}?"),
        "This device is unpaired from the server. Run `rad pair` on the computer to add it again.",
        vec![Choice::new("Remove", ButtonVariant::Danger, move |window, cx| {
            window.close_all_dialogs(cx);
            if let Some(app) = app_view(cx) {
                app.update(cx, |a, cx| a.remove_server_id(&id, cx));
            }
        })],
    );
}

impl Render for SettingsView {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let body = match self.tab {
            Tab::General => self.render_general(&p, cx),
            Tab::Servers => self.render_servers(&p, cx),
        };
        // A segmented control: the chosen tab raised on a sunken track.
        let tab = |id: &'static str, label: &'static str, t: Tab, current: Tab| {
            let b = Button::new(id).small().ghost().label(label).px_4();
            if t == current { b.bg(p.base).shadow_sm() } else { b }
        };
        v_flex()
            .size_full()
            .gap_3()
            .child(
                h_flex().justify_center().child(
                    h_flex()
                        .gap_0p5()
                        .p_0p5()
                        .rounded_lg()
                        .bg(p.crust)
                        .child(
                            tab("tab-general", "General", Tab::General, self.tab).on_click(cx.listener(
                                |this, _: &ClickEvent, _, cx| {
                                    this.tab = Tab::General;
                                    cx.notify();
                                },
                            )),
                        )
                        .child(
                            tab("tab-servers", "Servers", Tab::Servers, self.tab).on_click(cx.listener(
                                |this, _: &ClickEvent, _, cx| {
                                    this.tab = Tab::Servers;
                                    cx.notify();
                                },
                            )),
                        ),
                ),
            )
            .child(
                div()
                    .id("settings-body")
                    .flex_1()
                    .min_h_0()
                    .overflow_y_scroll()
                    .px_1()
                    .child(body),
            )
            .child(
                h_flex().justify_end().child(
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
fn unused(_: SharedString) {}
