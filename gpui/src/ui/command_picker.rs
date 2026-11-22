//! Slash-command suggestions over a prompt: a `/` that starts it looks the
//! commands up, and the list narrows as the user types the name. ↑ and ↓
//! move through it, Tab or Return completes the prompt to the highlighted
//! one, Escape puts it away, and a click picks one.
//!
//! The picker is an entity of its own, made around a prompt. Where the
//! commands come from is its host's business (`CommandSource`), so that any
//! prompt box can have it. The host draws it over the prompt (`visible`),
//! wraps the prompt in `capture_keys`, and asks `enter` before it sends.

use std::future::Future;
use std::pin::Pin;

use gpui_kit::component::input::{Escape, IndentInline, InputEvent, MoveDown, MoveUp, Position, TextareaState};
use gpui_kit::component::{h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    App, ClickEvent, Context, Entity, InteractiveElement, IntoElement, ParentElement as _, Render, ScrollHandle,
    SharedString, StatefulInteractiveElement as _, Styled as _, Subscription, Window, div, px,
};

use crate::protocol::{Command, CommandList, RpcError};
use crate::store::search::{command_query, command_suggestions};
use crate::ui::common::caption;
use crate::ui::palette::Palette;

/// A lookup of the commands: the answer to a `session.commands`-shaped call.
pub type Lookup = Pin<Box<dyn Future<Output = Result<serde_json::Value, RpcError>>>>;

/// Where a picker's commands come from.
pub trait CommandSource: 'static {
    /// Starts a lookup, or `None` while there's no one to ask (offline).
    fn lookup(&self, cx: &App) -> Option<Lookup>;
    /// Commands known without a lookup, such as the agent's own, listed
    /// after the looked-up ones.
    fn builtin(&self, cx: &App) -> Vec<Command>;
    /// Whether the prompt takes commands at all now (not in an archived
    /// session).
    fn available(&self, _cx: &App) -> bool {
        true
    }
}

pub struct CommandPicker {
    prompt: Entity<TextareaState>,
    source: Box<dyn CommandSource>,
    fetched: bool,
    loading: bool,
    failed: bool,
    list: Vec<Command>,
    index: usize,
    /// The list, scrolled to keep the highlighted one in view.
    scroll: ScrollHandle,
    /// The prompt as it was when Escape put the suggestions away.
    dismissed: Option<String>,
    _subscription: Subscription,
}

#[derive(Clone, Copy)]
enum Key {
    Up,
    Down,
    Accept,
    Dismiss,
}

impl CommandPicker {
    pub fn new(prompt: Entity<TextareaState>, source: impl CommandSource, cx: &mut Context<Self>) -> Self {
        let subscription = cx.subscribe(&prompt, |this, _, event: &InputEvent, cx| {
            if let InputEvent::Change = event {
                this.draft_changed(cx);
            }
        });
        Self {
            prompt,
            source: Box::new(source),
            fetched: false,
            loading: false,
            failed: false,
            list: Vec::new(),
            index: 0,
            scroll: ScrollHandle::new(),
            dismissed: None,
            _subscription: subscription,
        }
    }

    fn draft(&self, cx: &App) -> String {
        self.prompt.read(cx).value().to_string()
    }

    fn draft_changed(&mut self, cx: &mut Context<Self>) {
        let draft = self.draft(cx);
        if self.dismissed.as_ref().is_some_and(|d| *d != draft) {
            self.dismissed = None;
        }
        self.index = 0;
        self.scroll.scroll_to_item(0);
        if command_query(&draft).is_none() {
            // Looked up again when the picker opens again.
            self.fetched = false;
            return;
        }
        if self.fetched {
            return;
        }
        let Some(lookup) = self.source.lookup(cx) else { return };
        self.fetched = true;
        self.loading = true;
        self.failed = false;
        cx.spawn(async move |this, cx| {
            let result = lookup.await;
            let _ = this.update(cx, |this, cx| {
                this.loading = false;
                match result {
                    Ok(v) => this.list = serde_json::from_value::<CommandList>(v).unwrap_or_default().commands,
                    Err(e) if e.code == "method_not_found" => this.list.clear(),
                    Err(_) => this.failed = true,
                }
                cx.notify();
            });
        })
        .detach();
    }

    /// The prompt is a command's start, and the user hasn't put the list away.
    fn open(&self, draft: &str, cx: &App) -> bool {
        command_query(draft).is_some() && self.dismissed.as_deref() != Some(draft) && self.source.available(cx)
    }

    fn suggestions(&self, draft: &str, cx: &App) -> Vec<Command> {
        if !self.open(draft, cx) {
            return Vec::new();
        }
        let mut all = self.list.clone();
        all.extend(self.source.builtin(cx));
        command_suggestions(draft, &all)
    }

    fn status(&self) -> Option<&'static str> {
        if self.loading {
            Some("Loading skills…")
        } else if self.failed {
            Some("Skills unavailable. Try typing / again.")
        } else {
            None
        }
    }

    /// Whether the host should draw the picker over its prompt.
    pub fn visible(&self, cx: &App) -> bool {
        let draft = self.draft(cx);
        self.open(&draft, cx) && (self.status().is_some() || !self.suggestions(&draft, cx).is_empty())
    }

    /// Return in the prompt: completes it to the highlighted suggestion and
    /// says so. It's the host's to send when the prompt is a command
    /// already, or no suggestion is shown.
    pub fn enter(&mut self, window: &mut Window, cx: &mut Context<Self>) -> bool {
        let draft = self.draft(cx);
        let list = self.suggestions(&draft, cx);
        let Some(c) = list.get(self.index.min(list.len().saturating_sub(1))) else {
            return false;
        };
        if draft.trim() == format!("/{}", c.name) {
            return false;
        }
        self.complete(&c.name.clone(), window, cx);
        true
    }

    fn complete(&mut self, name: &str, window: &mut Window, cx: &mut Context<Self>) {
        let text = format!("/{name} ");
        self.prompt.update(cx, |p, cx| {
            p.set_value(text.clone(), window, cx);
            p.set_cursor_position(Position::new(0, text.len() as u32), window, cx);
            p.focus(window, cx);
        });
    }

    /// The prompt binds ↑, ↓, Tab and Escape (`MoveUp`, `MoveDown`,
    /// `IndentInline`, `Escape`), and GPUI runs a key's binding before any
    /// key-down listener, so the picker catches the actions in the capture
    /// phase, on an element around the prompt. With no suggestions shown
    /// they go on to the prompt.
    pub fn capture_keys<E: InteractiveElement>(this: &Entity<Self>, element: E, window: &Window) -> E {
        element
            .capture_action(window.listener_for(this, |p: &mut Self, _: &MoveUp, w, cx| p.key(Key::Up, w, cx)))
            .capture_action(window.listener_for(this, |p: &mut Self, _: &MoveDown, w, cx| p.key(Key::Down, w, cx)))
            .capture_action(
                window.listener_for(this, |p: &mut Self, _: &IndentInline, w, cx| p.key(Key::Accept, w, cx)),
            )
            .capture_action(window.listener_for(this, |p: &mut Self, _: &Escape, w, cx| p.key(Key::Dismiss, w, cx)))
    }

    fn key(&mut self, key: Key, window: &mut Window, cx: &mut Context<Self>) {
        let draft = self.draft(cx);
        let list = self.suggestions(&draft, cx);
        if list.is_empty() {
            return;
        }
        match key {
            Key::Down => {
                self.index = (self.index + 1).min(list.len() - 1);
                self.scroll.scroll_to_item(self.index);
            }
            Key::Up => {
                self.index = self.index.saturating_sub(1);
                self.scroll.scroll_to_item(self.index);
            }
            Key::Accept => {
                let c = &list[self.index.min(list.len() - 1)];
                self.complete(&c.name, window, cx);
            }
            Key::Dismiss => self.dismissed = Some(draft),
        }
        cx.stop_propagation();
        cx.notify();
    }
}

impl Render for CommandPicker {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let draft = self.draft(cx);
        let list = self.suggestions(&draft, cx);
        let mut col = v_flex()
            .id("suggestions")
            .max_h(px(220.))
            .overflow_y_scroll()
            .track_scroll(&self.scroll)
            .gap_0p5();
        for (i, c) in list.iter().enumerate() {
            let selected = i == self.index.min(list.len() - 1);
            let name = c.name.clone();
            col = col.child(
                h_flex()
                    .id(SharedString::from(format!("cmd-{i}")))
                    .gap_2()
                    .px_2()
                    .py_1()
                    .rounded_md()
                    .cursor_pointer()
                    .when(selected, |d| d.bg(p.surface0))
                    .child(
                        v_flex()
                            .flex_1()
                            .min_w_0()
                            .child(
                                div()
                                    .text_xs()
                                    .font_family(crate::ui::fonts::code())
                                    .child(format!("/{}", c.name)),
                            )
                            .when_some(c.description.clone(), |d, desc| {
                                d.child(div().text_xs().text_color(p.subtext).line_clamp(2).child(desc))
                            }),
                    )
                    .when_some(c.scope.clone(), |d, s| d.child(caption(capitalize(&s), &p)))
                    .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| this.complete(&name, window, cx))),
            );
        }
        if let Some(s) = self.status() {
            col = col.child(caption(s, &p).px_2().py_1());
        }
        div()
            .p_1()
            .rounded_lg()
            .border_1()
            .border_color(p.surface0)
            .bg(p.mantle)
            .child(col)
    }
}

fn capitalize(s: &str) -> String {
    let mut c = s.chars();
    match c.next() {
        Some(f) => f.to_uppercase().collect::<String>() + c.as_str(),
        None => String::new(),
    }
}
