//! Mentions of other sessions over a prompt: an `@` at the start or after a
//! space lists the server's sessions, and the list narrows as the user types
//! a name. ↑ and ↓ move through it, Tab or Return writes the highlighted one
//! as `@[title](session:<id>)` (`store::mentions`), Escape puts it away, and
//! a click picks one. rad tells the agent to read the sessions a prompt
//! mentions.
//!
//! The prompt shows each mention as a token, `@title`, which the picker
//! keeps in step with the text (`sync_tokens`). Like `CommandPicker`, it's an
//! entity made around a prompt: the host draws it over the prompt
//! (`visible`), wraps the prompt in `capture_keys`, and asks `enter` before
//! it sends.

use std::rc::Rc;

use chrono::Utc;
use gpui_kit::component::input::{
    Escape, IndentInline, InlineToken, InputContent, InputEvent, MoveDown, MoveUp, TextareaState,
};
use gpui_kit::component::{h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    App, ClickEvent, Context, Entity, InteractiveElement, IntoElement, ParentElement as _, Render, ScrollHandle,
    SharedString, StatefulInteractiveElement as _, Styled as _, Subscription, Window, div, px,
};

use crate::protocol::Session;
use crate::store::mentions::{MentionQuery, complete_mention, mention_query, mention_suggestions, mentions};
use crate::store::sidebar::short_age_of;
use crate::ui::common::caption;
use crate::ui::palette::Palette;

/// Where a picker's sessions come from.
pub trait SessionSource: 'static {
    /// The sessions it may mention: the server's, archived ones too.
    fn sessions(&self, cx: &App) -> Vec<Rc<Session>>;
    /// The session the prompt is in, which it doesn't list.
    fn self_id(&self) -> &str;
    /// A session's project, shown beside its title.
    fn project_name(&self, project_id: &str, cx: &App) -> Option<String>;
}

pub struct MentionPicker {
    prompt: Entity<TextareaState>,
    source: Box<dyn SessionSource>,
    index: usize,
    scroll: ScrollHandle,
    /// The prompt and cursor as they were when Escape put the list away.
    dismissed: Option<(String, usize)>,
    _subscription: Subscription,
}

#[derive(Clone, Copy)]
enum Key {
    Up,
    Down,
    Accept,
    Dismiss,
}

impl MentionPicker {
    pub fn new(prompt: Entity<TextareaState>, source: impl SessionSource, cx: &mut Context<Self>) -> Self {
        let subscription = cx.subscribe(&prompt, |this, _, event: &InputEvent, cx| {
            if let InputEvent::Change = event {
                this.index = 0;
                this.scroll.scroll_to_item(0);
                sync_tokens(&this.prompt, cx);
                cx.notify();
            }
        });
        Self {
            prompt,
            source: Box::new(source),
            index: 0,
            scroll: ScrollHandle::new(),
            dismissed: None,
            _subscription: subscription,
        }
    }

    fn query(&self, cx: &App) -> Option<(MentionQuery, usize)> {
        let p = self.prompt.read(cx);
        let range = p.selected_range();
        if !range.is_empty() {
            return None;
        }
        let text = p.value().to_string();
        let cursor = p.cursor();
        if self.dismissed.as_ref().is_some_and(|(t, c)| *t == text && *c == cursor) {
            return None;
        }
        mention_query(&text, cursor).map(|q| (q, cursor))
    }

    fn suggestions(&self, cx: &App) -> Vec<Rc<Session>> {
        let Some((q, _)) = self.query(cx) else {
            return Vec::new();
        };
        let all = self.source.sessions(cx);
        mention_suggestions(&q.query, &all, self.source.self_id())
            .into_iter()
            .cloned()
            .collect()
    }

    /// Whether the host should draw the picker over its prompt.
    pub fn visible(&self, cx: &App) -> bool {
        !self.suggestions(cx).is_empty()
    }

    /// Return in the prompt: writes the highlighted session's mention and
    /// says so; with no list shown, it's the host's to send.
    pub fn enter(&mut self, window: &mut Window, cx: &mut Context<Self>) -> bool {
        let list = self.suggestions(cx);
        let Some(s) = list.get(self.index.min(list.len().saturating_sub(1))).cloned() else {
            return false;
        };
        self.pick(&s, window, cx);
        true
    }

    fn pick(&mut self, s: &Session, window: &mut Window, cx: &mut Context<Self>) {
        let Some((q, cursor)) = self.query(cx) else { return };
        let text = self.prompt.read(cx).value().to_string();
        // The text and cursor it ends with; the mention ends a character
        // before the cursor, at the space it adds or finds after it.
        let (result, end) = complete_mention(&text, cursor, &q, &s.title, &s.id);
        let mention = result[q.start..end - 1].to_string();
        let label = mentions(&mention)
            .first()
            .map(|m| format!("@{}", m.title))
            .unwrap_or_default();
        let token = InlineToken::new(s.id.clone(), mention.clone()).with_label(label);
        let spaced = text[cursor..].starts_with(char::is_whitespace);
        self.prompt.update(cx, |p, cx| {
            if p.replace_range_with_token(q.start..cursor, token, window, cx).is_err() {
                p.set_selected_range(q.start..cursor, cx);
                p.replace(mention.clone(), window, cx); // `sync_tokens` makes it a token
            }
            if spaced {
                p.set_selected_range(end..end, cx);
            } else {
                let after = end - 1;
                p.set_selected_range(after..after, cx);
                p.insert(" ", window, cx);
            }
            p.focus(window, cx);
        });
        sync_tokens(&self.prompt, cx);
        cx.notify();
    }

    /// As `CommandPicker::capture_keys`: the prompt's ↑, ↓, Tab and Escape,
    /// caught while the list shows.
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
        let list = self.suggestions(cx);
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
                let s = list[self.index.min(list.len() - 1)].clone();
                self.pick(&s, window, cx);
            }
            Key::Dismiss => {
                let p = self.prompt.read(cx);
                self.dismissed = Some((p.value().to_string(), p.cursor()));
            }
        }
        cx.stop_propagation();
        cx.notify();
    }
}

/// Shows a prompt's mentions as tokens, as after its text was set.
pub fn sync_tokens(prompt: &Entity<TextareaState>, cx: &mut App) {
    prompt.update(cx, |p, cx| {
        let text = p.value().to_string();
        let found = mentions(&text);
        let same = p.tokens().len() == found.len()
            && p.tokens()
                .iter()
                .zip(&found)
                .all(|(t, m)| t.range() == m.range && t.token().id() == &m.id);
        if same {
            return;
        }
        let mut content = InputContent::new(text.clone());
        for m in found {
            let token =
                InlineToken::new(m.id.clone(), text[m.range.clone()].to_string()).with_label(format!("@{}", m.title));
            content = match content.clone().with_token(m.range, token) {
                Ok(c) => c,
                Err(_) => content,
            };
        }
        let _ = p.set_tokens(content, cx);
    });
}

impl Render for MentionPicker {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let list = self.suggestions(cx);
        let now = Utc::now();
        let mut col = v_flex()
            .id("mentions")
            .max_h(px(220.))
            .overflow_y_scroll()
            .track_scroll(&self.scroll)
            .gap_0p5();
        for (i, s) in list.iter().enumerate() {
            let selected = i == self.index.min(list.len() - 1);
            let title = if s.title.trim().is_empty() {
                "Untitled".to_string()
            } else {
                s.title.clone()
            };
            let project = self.source.project_name(&s.project_id, cx);
            let age = short_age_of(&s.updated_at, now);
            let detail = [project.unwrap_or_default(), age]
                .into_iter()
                .filter(|s| !s.is_empty())
                .collect::<Vec<_>>()
                .join(" · ");
            let pick = s.clone();
            col = col.child(
                h_flex()
                    .id(SharedString::from(format!("mention-{i}")))
                    .gap_2()
                    .px_2()
                    .py_1()
                    .rounded_md()
                    .cursor_pointer()
                    .when(selected, |d| d.bg(p.surface0))
                    .child(div().flex_1().min_w_0().text_xs().truncate().child(format!("@{title}")))
                    .when(!detail.is_empty(), |d| d.child(caption(detail, &p)))
                    .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| this.pick(&pick, window, cx))),
            );
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
