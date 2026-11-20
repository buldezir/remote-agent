//! Settings (⌘,), as T3 Code lays them out: a sidebar of categories with a
//! search field, and the chosen one's page beside it. General
//! (notifications, the sidebar, updates), Appearance (the theme, text sizes
//! and fonts), Chat (the transcript and the prompt), Terminal, and each
//! server's settings. As `SettingsView.swift`.

use std::collections::HashMap;

use gpui_kit::assets::IconName as L;
use gpui_kit::component::WindowExt as _;
use gpui_kit::component::button::{Button, ButtonVariant, ButtonVariants as _};
use gpui_kit::component::input::{Input, InputEvent, InputState};
use gpui_kit::component::scroll::{Scrollbar, ScrollbarMode};
use gpui_kit::component::slider::{Slider, SliderEvent, SliderState};
use gpui_kit::component::switch::Switch;
use gpui_kit::component::{Disableable as _, Icon, IconName, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    AnyElement, App, AppContext as _, ClickEvent, Context, Entity, FontWeight, InteractiveElement as _, IntoElement,
    ParentElement as _, Render, ScrollHandle, SharedString, StatefulInteractiveElement as _, Styled as _, Subscription,
    Window, div, px,
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

/// The dialog's widest; it takes 90% of the window's height.
const WIDTH: f32 = 1176.;
/// The sidebar's width. A page's cards fill the rest, as the dialog's
/// own widest keeps their rows short enough to read.
const SIDEBAR: f32 = 230.;

pub fn open(conns: Vec<Entity<ServerConn>>, server: Option<usize>, window: &mut Window, cx: &mut App) {
    if window.has_active_dialog(cx) {
        return;
    }
    let view = cx.new(|cx| SettingsView::new(conns, server, window, cx));
    let size = window.viewport_size();
    let height = size.height * 0.9;
    window.open_dialog(cx, move |dialog, _, _| {
        dialog
            .w((size.width * 0.92).min(px(WIDTH)))
            .p_0()
            .close_button(false)
            .margin_top((size.height - height) / 2.)
            .child(div().h(height).child(view.clone()))
    });
}

/// A page of Settings.
#[derive(PartialEq, Clone, Copy)]
enum Page {
    General,
    Appearance,
    Chat,
    Terminal,
    /// A server's settings, by its place among the connections.
    Server(usize),
}

impl Page {
    fn title(self) -> &'static str {
        match self {
            Page::General => "General",
            Page::Appearance => "Appearance",
            Page::Chat => "Chat",
            Page::Terminal => "Terminal",
            Page::Server(_) => "Server",
        }
    }

    fn icon(self) -> L {
        match self {
            Page::General => L::SlidersHorizontal,
            Page::Appearance => L::Palette,
            Page::Chat => L::MessageSquare,
            Page::Terminal => L::SquareTerminal,
            Page::Server(_) => L::Server,
        }
    }

    /// What the search finds the page by, besides its title: its settings'
    /// names and what they're about.
    fn terms(self) -> &'static str {
        match self {
            Page::General => "notifications notify alerts sidebar layout inbox projects columns updates build host",
            Page::Appearance => {
                "theme dark light system catppuccin text size interface messages font code menlo monospaced"
            }
            Page::Chat => "transcript group tool calls runs prompt box width limit composer",
            Page::Terminal => "shell opens placement below right copy on select clipboard code font size",
            Page::Server(_) => {
                "server rad version update agents sign in login usage limits default agent system agent new sessions"
            }
        }
    }
}

pub struct SettingsView {
    page: Page,
    conns: Vec<Entity<ServerConn>>,
    servers: HashMap<String, Entity<ServerSettingsView>>,
    search: Entity<InputState>,
    text_size: Entity<SliderState>,
    message_size: Entity<SliderState>,
    code_size: Entity<SliderState>,
    /// The page's scroll; it goes back to the top on another page.
    scroll: ScrollHandle,
    _subscriptions: Vec<Subscription>,
}

impl SettingsView {
    fn new(conns: Vec<Entity<ServerConn>>, server: Option<usize>, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let p = prefs(cx).clone();
        let slider = |min: f32, max: f32, value: f32| {
            move |_: &mut Context<SliderState>| SliderState::new().min(min).max(max).step(1.).default_value(value)
        };
        let text_size = cx.new(slider(11., 18., p.text_size));
        let message_size = cx.new(slider(10., 24., p.message_size));
        let code_size = cx.new(slider(fonts::CODE_SIZES.0, fonts::CODE_SIZES.1, p.code_size));
        let search = cx.new(|cx| InputState::new(window, cx).placeholder("Search"));
        let on_slide = |set: fn(&mut Prefs, f32)| {
            move |_: &mut Self, _: &Entity<SliderState>, ev: &SliderEvent, _: &mut Window, cx: &mut Context<Self>| {
                let (SliderEvent::Change(v) | SliderEvent::Release(v)) = ev;
                let v = v.start();
                update_prefs(cx, |p| set(p, v));
            }
        };
        let subs = vec![
            cx.subscribe_in(&text_size, window, on_slide(|p, v| p.text_size = v)),
            cx.subscribe_in(&message_size, window, on_slide(|p, v| p.message_size = v)),
            cx.subscribe_in(&code_size, window, on_slide(|p, v| p.code_size = v)),
            cx.subscribe_in(&search, window, |this, _, ev: &InputEvent, _, cx| {
                if let InputEvent::Change = ev {
                    this.search_changed(cx);
                }
            }),
        ];
        Self {
            page: match server {
                Some(i) if i < conns.len() => Page::Server(i),
                _ => Page::General,
            },
            conns,
            servers: HashMap::new(),
            search,
            text_size,
            message_size,
            code_size,
            scroll: ScrollHandle::new(),
            _subscriptions: subs,
        }
    }

    fn show(&mut self, page: Page, cx: &mut Context<Self>) {
        if self.page != page {
            self.page = page;
            self.scroll.set_offset(gpui_kit::Point::default());
        }
        cx.notify();
    }

    /// The pages in the sidebar's order.
    fn pages(&self) -> Vec<Page> {
        let mut pages = vec![Page::General, Page::Appearance, Page::Chat, Page::Terminal];
        pages.extend((0..self.conns.len()).map(Page::Server));
        pages
    }

    fn page_title(&self, page: Page, cx: &App) -> String {
        match page {
            Page::Server(i) => self
                .conns
                .get(i)
                .map(|c| c.read(cx).name.clone())
                .unwrap_or_else(|| "Server".into()),
            _ => page.title().to_string(),
        }
    }

    /// Whether the search finds the page: every word of it, in the page's
    /// title or its terms.
    fn matches(&self, page: Page, cx: &App) -> bool {
        let query = self.search.read(cx).value().to_lowercase();
        let haystack = format!("{} {}", self.page_title(page, cx), page.terms()).to_lowercase();
        query.split_whitespace().all(|w| haystack.contains(w))
    }

    /// A search shows the first page it finds, unless the one shown is found.
    fn search_changed(&mut self, cx: &mut Context<Self>) {
        if !self.matches(self.page, cx)
            && let Some(first) = self.pages().into_iter().find(|p| self.matches(*p, cx))
        {
            self.show(first, cx);
        }
        cx.notify();
    }

    fn at_defaults(prefs: &Prefs) -> bool {
        let d = Prefs::default();
        prefs.appearance == d.appearance
            && prefs.text_size == d.text_size
            && prefs.message_size == d.message_size
            && prefs.message_font == d.message_font
            && prefs.code_font == d.code_font
            && prefs.code_size == d.code_size
            && prefs.group_tool_calls == d.group_tool_calls
            && prefs.limit_prompt_width == d.limit_prompt_width
            && prefs.sidebar_layout == d.sidebar_layout
            && prefs.terminal_placement == d.terminal_placement
            && prefs.terminal_copy_on_select == d.terminal_copy_on_select
            && prefs.notifications == d.notifications
    }

    /// This device's settings back to their defaults; servers' stay.
    fn restore_defaults(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let d = Prefs::default();
        update_prefs(cx, |p| {
            p.appearance = d.appearance.clone();
            p.text_size = d.text_size;
            p.message_size = d.message_size;
            p.message_font = d.message_font.clone();
            p.code_font = d.code_font.clone();
            p.code_size = d.code_size;
            p.group_tool_calls = d.group_tool_calls;
            p.limit_prompt_width = d.limit_prompt_width;
            p.sidebar_layout = d.sidebar_layout.clone();
            p.terminal_placement = d.terminal_placement.clone();
            p.terminal_copy_on_select = d.terminal_copy_on_select;
            p.notifications = d.notifications;
        });
        apply_appearance(window, cx);
        self.text_size.update(cx, |s, cx| s.set_value(d.text_size, window, cx));
        self.message_size
            .update(cx, |s, cx| s.set_value(d.message_size, window, cx));
        self.code_size.update(cx, |s, cx| s.set_value(d.code_size, window, cx));
    }

    // MARK: Pages

    fn render_general(&mut self, p: &Palette, cx: &mut Context<Self>) -> Vec<AnyElement> {
        let prefs = prefs(cx).clone();
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
        let mut groups = vec![
            group(
                "Notifications",
                vec![setting(
                    "Show notifications",
                    Some("When a session needs you, finishes or fails, unless the window shows it."),
                    switch("notifications", prefs.notifications, |p, v| p.notifications = v),
                    p,
                )],
                None,
                p,
            ),
            group(
                "Sidebar",
                vec![setting(
                    "Layout",
                    Some(&format!(
                        "{summary} The View menu switches them too, with ⌘1, ⌘2 and ⌘3."
                    )),
                    layout_picker,
                    p,
                )],
                None,
                p,
            ),
        ];
        if crate::ui::sparkle::available() {
            groups.push(sparkle_updates(p));
        } else if let Some(build) = crate::store::build_update::current() {
            let from = if prefs.update_host.is_empty() {
                "Check for Update asks which Mac to pull builds from.".to_string()
            } else {
                format!("Builds come from {} over ssh.", prefs.update_host)
            };
            groups.push(group(
                "Updates",
                vec![
                    setting(
                        "This build",
                        Some(&build.summary(chrono::Utc::now())),
                        Button::new("check-update")
                            .small()
                            .outline()
                            .label("Check for Update…")
                            .on_click(|_: &ClickEvent, window, cx| crate::ui::build_update::check(window, cx))
                            .into_any_element(),
                        p,
                    ),
                    setting(
                        "Build host",
                        Some(&from),
                        Button::new("update-host")
                            .small()
                            .ghost()
                            .label("Change…")
                            .on_click(|_: &ClickEvent, window, cx| crate::ui::build_update::ask_host(window, cx))
                            .into_any_element(),
                        p,
                    ),
                ],
                None,
                p,
            ));
        }
        groups
    }

    fn render_appearance(&mut self, p: &Palette, cx: &mut Context<Self>) -> Vec<AnyElement> {
        let prefs = prefs(cx).clone();
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
        vec![
            group(
                "Theme",
                vec![setting(
                    "Theme",
                    Some("Catppuccin Latte when light, Frappé when dark."),
                    theme,
                    p,
                )],
                None,
                p,
            ),
            group(
                "Text",
                vec![
                    setting(
                        "Interface size",
                        Some("Lists, labels and the controls under the prompt."),
                        slider(&self.text_size, prefs.text_size, p),
                        p,
                    ),
                    setting(
                        "Message font",
                        Some("Your prompts and the agent's replies."),
                        message_font_picker,
                        p,
                    ),
                    setting(
                        "Message size",
                        None,
                        slider(&self.message_size, prefs.message_size, p),
                        p,
                    ),
                    setting(
                        "Code font",
                        Some("Code in replies, tool calls, diffs and the terminal."),
                        code_font_picker,
                        p,
                    ),
                    setting("Code size", None, slider(&self.code_size, prefs.code_size, p), p),
                ],
                None,
                p,
            ),
        ]
    }

    fn render_chat(&mut self, p: &Palette, cx: &mut Context<Self>) -> Vec<AnyElement> {
        let prefs = prefs(cx).clone();
        vec![
            group(
                "Transcript",
                vec![setting(
                    "Group tool calls",
                    Some(
                        "Folds each run of the agent's tool calls into one line, such as “Ran 4 commands, edited 3 files”, which opens to show them.",
                    ),
                    switch("group-tools", prefs.group_tool_calls, |p, v| p.group_tool_calls = v),
                    p,
                )],
                None,
                p,
            ),
            group(
                "Prompt",
                vec![setting(
                    "Limit its width",
                    Some(&format!(
                        "Keeps the prompt box to {} points, as wide as on a 13-inch laptop, centred in a wider window. Off, it's as wide as the window.",
                        crate::ui::prompt_box::LIMITED_WIDTH as u32
                    )),
                    switch("limit-prompt-width", prefs.limit_prompt_width, |p, v| {
                        p.limit_prompt_width = v
                    }),
                    p,
                )],
                None,
                p,
            ),
        ]
    }

    fn render_terminal(&mut self, p: &Palette, cx: &mut Context<Self>) -> Vec<AnyElement> {
        let prefs = prefs(cx).clone();
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
        vec![group(
            "Terminal",
            vec![
                setting(
                    "Opens",
                    Some(
                        "Where a session's terminal opens (⌘J). In a narrow window it takes the place of the messages.",
                    ),
                    placement,
                    p,
                ),
                setting(
                    "Copy on select",
                    Some("Puts the text you select on the clipboard."),
                    switch("copy-on-select", prefs.terminal_copy_on_select, |p, v| {
                        p.terminal_copy_on_select = v
                    }),
                    p,
                ),
                setting(
                    "Font and size",
                    Some("The terminal draws with the code font, at the code size: Appearance › Text."),
                    Button::new("to-appearance")
                        .small()
                        .ghost()
                        .label("Appearance…")
                        .on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.show(Page::Appearance, cx)))
                        .into_any_element(),
                    p,
                ),
            ],
            None,
            p,
        )]
    }

    /// A server's page: its state, Reconnect and Remove, and its settings.
    fn render_server(&mut self, i: usize, p: &Palette, cx: &mut Context<Self>) -> Vec<AnyElement> {
        let Some(conn) = self.conns.get(i).cloned() else {
            return vec![caption("No such server.", p).into_any_element()];
        };
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
        let (state, color) = match &c.state {
            ConnState::Connected => ("Connected".to_string(), p.green),
            ConnState::Connecting(r) => (r.clone().unwrap_or_else(|| "Connecting…".into()), p.yellow),
            ConnState::Failed(e) => (e.to_string(), p.red),
        };
        let url = c.base_url.clone().unwrap_or_default();
        let (c1, c2) = (conn.clone(), conn.clone());
        vec![
            group(
                "Connection",
                vec![setting(
                    &url,
                    None,
                    h_flex()
                        .gap_2()
                        .child(
                            h_flex()
                                .gap_1p5()
                                .max_w(px(220.))
                                .child(div().size(px(7.)).rounded_full().flex_shrink_0().bg(color))
                                .child(caption(state, p).truncate()),
                        )
                        .child(
                            Button::new("reconnect")
                                .small()
                                .outline()
                                .label("Reconnect")
                                .on_click(move |_: &ClickEvent, _, cx| c1.read(cx).retry()),
                        )
                        .child(
                            Button::new("remove")
                                .small()
                                .danger()
                                .label("Remove…")
                                .on_click(move |_: &ClickEvent, window, cx| remove_server(c2.clone(), window, cx)),
                        )
                        .into_any_element(),
                    p,
                )],
                None,
                p,
            ),
            view.into_any_element(),
        ]
    }

    // MARK: The sidebar

    fn render_sidebar(&mut self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let item = |this: &Self, page: Page, label: String, trailing: Option<AnyElement>, cx: &mut Context<Self>| {
            let selected = this.page == page;
            let id = match page {
                Page::Server(i) => SharedString::from(format!("page-server-{i}")),
                _ => SharedString::from(format!("page-{}", page.title())),
            };
            h_flex()
                .id(id)
                .w_full()
                .gap_2()
                .px_2()
                .py_1p5()
                .rounded_md()
                .cursor_pointer()
                .text_sm()
                .when(selected, |d| d.bg(p.surface0).text_color(p.text))
                .when(!selected, |d| {
                    d.text_color(p.subtext)
                        .hover(|d| d.bg(p.surface0.opacity(0.5)).text_color(p.text))
                })
                .child(Icon::new(page.icon()).small())
                .child(div().flex_1().min_w_0().truncate().child(label))
                .children(trailing)
                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.show(page, cx)))
                .into_any_element()
        };
        let heading = |text: &'static str| {
            div()
                .px_2()
                .pt_3()
                .pb_1()
                .text_xs()
                .font_weight(FontWeight::MEDIUM)
                .text_color(p.overlay)
                .child(text)
        };
        let mut list = v_flex().gap_0p5();
        for page in [Page::General, Page::Appearance, Page::Chat, Page::Terminal] {
            if self.matches(page, cx) {
                list = list.child(item(self, page, page.title().to_string(), None, cx));
            }
        }
        let servers: Vec<usize> = (0..self.conns.len())
            .filter(|i| self.matches(Page::Server(*i), cx))
            .collect();
        let searching = !self.search.read(cx).value().trim().is_empty();
        if !servers.is_empty() || !searching {
            list = list.child(heading("Servers"));
        }
        for i in servers {
            let c = self.conns[i].read(cx);
            let color = match c.state {
                ConnState::Connected => p.green,
                ConnState::Connecting(_) => p.yellow,
                ConnState::Failed(_) => p.red,
            };
            let name = c.name.clone();
            let dot = div().size(px(7.)).rounded_full().bg(color).into_any_element();
            list = list.child(item(self, Page::Server(i), name, Some(dot), cx));
        }
        if !searching {
            list = list.child(
                h_flex()
                    .id("add-server")
                    .w_full()
                    .gap_2()
                    .px_2()
                    .py_1p5()
                    .rounded_md()
                    .cursor_pointer()
                    .text_sm()
                    .text_color(p.overlay)
                    .hover(|d| d.bg(p.surface0.opacity(0.5)).text_color(p.text))
                    .child(Icon::new(IconName::Plus).small())
                    .child("Add Server")
                    .on_click(|_: &ClickEvent, window, cx| {
                        window.close_dialog(cx);
                        if let Some(app) = app_view(cx) {
                            app.update(cx, |a, cx| a.show_pairing(cx));
                        }
                    }),
            );
        }
        let none = searching && !self.pages().into_iter().any(|page| self.matches(page, cx));
        v_flex()
            .w(px(SIDEBAR))
            .h_full()
            .flex_shrink_0()
            .gap_2()
            .p_2()
            .bg(p.mantle)
            .border_r_1()
            .border_color(p.surface0)
            .child(
                Input::new(&self.search)
                    .small()
                    .prefix(Icon::new(IconName::Search).small().text_color(p.overlay))
                    .cleanable(true),
            )
            .child(
                div()
                    .id("settings-pages")
                    .flex_1()
                    .min_h_0()
                    .overflow_y_scroll()
                    .child(list)
                    .when(none, |d| d.child(caption("No settings match.", p).px_2().pt_2())),
            )
            .into_any_element()
    }
}

/// A switch for a setting of the device's.
fn switch(id: &'static str, on: bool, set: fn(&mut Prefs, bool)) -> AnyElement {
    Switch::new(id)
        .checked(on)
        .on_change(move |v: &bool, _, cx| {
            let v = *v;
            update_prefs(cx, |p| set(p, v))
        })
        .into_any_element()
}

/// The release build's Updates, which Sparkle keeps (`ui::sparkle`).
fn sparkle_updates(p: &Palette) -> AnyElement {
    use crate::ui::sparkle;
    let checks = sparkle::checks_automatically();
    let downloads = sparkle::downloads_automatically();
    let version = crate::store::identity::current()
        .version
        .clone()
        .unwrap_or_else(|| env!("CARGO_PKG_VERSION").to_string());
    let footer = if checks && downloads {
        "Once a day. A new version installs when you quit Remote Agent."
    } else {
        "Once a day. Remote Agent asks before installing a new version."
    };
    group(
        "Updates",
        vec![
            setting(
                "Remote Agent",
                Some(&format!("Version {version}")),
                Button::new("check-updates")
                    .small()
                    .outline()
                    .label("Check for Updates…")
                    .on_click(|_: &ClickEvent, _, _| sparkle::check())
                    .into_any_element(),
                p,
            ),
            setting(
                "Check for updates automatically",
                None,
                Switch::new("sparkle-checks")
                    .checked(checks)
                    .on_change(|v: &bool, window, _| {
                        sparkle::set_checks_automatically(*v);
                        window.refresh();
                    })
                    .into_any_element(),
                p,
            ),
            setting(
                "Download and install automatically",
                None,
                Switch::new("sparkle-downloads")
                    .checked(checks && downloads)
                    .disabled(!checks)
                    .on_change(|v: &bool, window, _| {
                        sparkle::set_downloads_automatically(*v);
                        window.refresh();
                    })
                    .into_any_element(),
                p,
            ),
        ],
        Some(footer.to_string()),
        p,
    )
}

/// A slider and its value in points.
fn slider(state: &Entity<SliderState>, value: f32, p: &Palette) -> AnyElement {
    h_flex()
        .gap_3()
        .child(div().w(px(160.)).child(Slider::new(state)))
        .child(caption(format!("{value} pt"), p).w(px(36.)))
        .into_any_element()
}

/// A column that scrolls, with its scrollbar always shown, so that what's
/// below the fold is plain to see: the kit's own shows only while scrolling.
fn scroll_area(id: &'static str, handle: &ScrollHandle, content: AnyElement) -> AnyElement {
    div()
        .relative()
        .size_full()
        .child(
            div()
                .id(id)
                .size_full()
                .overflow_y_scroll()
                .track_scroll(handle)
                .child(content),
        )
        .child(Scrollbar::vertical(handle).mode(ScrollbarMode::Always))
        .into_any_element()
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
        .gap_2()
        .child(
            div()
                .px_1()
                .font_weight(FontWeight::SEMIBOLD)
                .text_color(p.text)
                .child(title.to_string()),
        )
        .child(card)
        .when_some(footer, |d, f| d.child(caption(f, p).px_1()))
        .into_any_element()
}

/// A row of a group: its name and what it does at the start, its control at
/// the end.
pub fn setting(label: &str, description: Option<&str>, control: AnyElement, p: &Palette) -> AnyElement {
    h_flex()
        .w_full()
        .min_h(px(48.))
        .gap_6()
        .px_4()
        .py_2p5()
        .child(
            v_flex()
                .flex_1()
                .min_w_0()
                .gap_0p5()
                .child(
                    div()
                        .text_sm()
                        .font_weight(FontWeight::MEDIUM)
                        .text_color(p.text)
                        .child(label.to_string()),
                )
                .when_some(description, |d, text| d.child(caption(text.to_string(), p))),
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
        // A server removed while Settings shows it.
        if let Page::Server(i) = self.page
            && i >= self.conns.len()
        {
            self.page = Page::General;
        }
        let page = self.page;
        let groups = match page {
            Page::General => self.render_general(&p, cx),
            Page::Appearance => self.render_appearance(&p, cx),
            Page::Chat => self.render_chat(&p, cx),
            Page::Terminal => self.render_terminal(&p, cx),
            Page::Server(i) => self.render_server(i, &p, cx),
        };
        let device = !matches!(page, Page::Server(_));
        let at_defaults = Self::at_defaults(prefs(cx));
        let title = self.page_title(page, cx);
        let header = h_flex()
            .h(px(52.))
            .flex_shrink_0()
            .gap_2()
            .px_5()
            .border_b_1()
            .border_color(p.surface0)
            .child(div().text_sm().text_color(p.subtext).child("Settings"))
            .child(div().text_sm().text_color(p.overlay).child("/"))
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .truncate()
                    .text_sm()
                    .text_color(p.text)
                    .child(title),
            )
            .when(device, |d| {
                d.child(
                    Button::new("restore")
                        .small()
                        .ghost()
                        .icon(Icon::new(L::RotateCcw))
                        .label("Restore Defaults")
                        .tooltip("This device's settings, not the servers'")
                        .disabled(at_defaults)
                        .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.restore_defaults(window, cx))),
                )
            })
            .child(
                Button::new("close-settings")
                    .small()
                    .ghost()
                    .icon(IconName::Close)
                    .tooltip("Close (Esc)")
                    .on_click(|_: &ClickEvent, window, cx| window.close_dialog(cx)),
            );
        let body = h_flex()
            .w_full()
            .px_8()
            .py_6()
            .child(v_flex().w_full().gap_6().children(groups));
        let sidebar = self.render_sidebar(&p, cx);
        h_flex()
            .size_full()
            .rounded_lg()
            .overflow_hidden()
            .child(sidebar)
            .child(
                v_flex()
                    .flex_1()
                    .min_w_0()
                    .h_full()
                    .child(header)
                    .child(div().flex_1().min_h_0().child(scroll_area(
                        "settings-page",
                        &self.scroll,
                        body.into_any_element(),
                    ))),
            )
    }
}
