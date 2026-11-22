//! The composer: the prompt, its images, slash-command suggestions,
//! queueing and steering, drafts, dictation and Stop, in the prompt box a
//! new session has too, with the session's settings under it.

use std::path::PathBuf;
use std::sync::Arc;

use gpui_kit::assets::IconName as L;
use gpui_kit::component::button::{Button, ButtonVariant, ButtonVariants as _};
use gpui_kit::component::input::{Escape, IndentInline, MoveDown, MoveUp, Paste, Textarea};
use gpui_kit::component::menu::{DropdownMenu as _, PopupMenuItem};
use gpui_kit::component::spinner::Spinner;
use gpui_kit::component::{Disableable as _, Icon, IconName, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    AnyElement, App, AppContext as _, ClickEvent, ClipboardEntry, ClipboardItem, Context, ExternalPaths, FontWeight,
    Image, InteractiveElement as _, IntoElement, ObjectFit, ParentElement as _, PathPromptOptions, SharedString,
    StatefulInteractiveElement as _, Styled as _, StyledImage as _, Window, div, img, px,
};
use serde_json::json;

use super::{AttachState, Attachment, SessionView, explain};
use crate::images::{Loaded, Prepared, prepare};
use crate::protocol::{CommandList, ImageRef, WorkspaceKind};
use crate::store::search::{command_query, command_suggestions};
use crate::ui::badges::{harness_icon, model_name};
use crate::ui::common::caption;
use crate::ui::dialogs::{self, Choice};
use crate::ui::palette::Palette;
use crate::ui::prompt_box::{self, PromptBox};

impl SessionView {
    fn draft_text(&self, cx: &App) -> String {
        self.composer.read(cx).value().to_string()
    }

    fn has_something(&self, cx: &App) -> bool {
        !self.draft_text(cx).trim().is_empty() || !self.attachments.is_empty()
    }

    fn uploads_done(&self) -> bool {
        self.attachments.iter().all(|a| matches!(a.state, AttachState::Done(_)))
    }

    pub(crate) fn can_send(&self, cx: &App) -> bool {
        self.has_something(cx) && self.uploads_done() && self.conn.read(cx).is_connected() && !self.read_only(cx)
    }

    pub(crate) fn update_placeholder(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let status = self.speech.read(cx).status();
        let archived = self.session(cx).is_some_and(|s| s.archived);
        let text = if status.is_active() {
            "Listening…"
        } else if archived && !self.read_only(cx) {
            "Archived. Send to unarchive"
        } else if archived {
            "Archived"
        } else if self.is_running(cx) {
            "Queue a follow-up…"
        } else {
            "Prompt"
        };
        if self.placeholder != text {
            self.placeholder = text;
            self.composer.update(cx, |c, cx| c.set_placeholder(text, window, cx));
        }
    }

    // MARK: Sending

    pub(crate) fn composer_enter(&mut self, shift: bool, secondary: bool, window: &mut Window, cx: &mut Context<Self>) {
        if self.suggestions_shown(cx) && !shift {
            self.accept_suggestion(true, window, cx);
            return;
        }
        if shift && secondary {
            let steer = self.can_steer(&self.draft_text(cx), cx);
            self.send(steer, window, cx);
        } else if !shift {
            self.send(false, window, cx);
        }
    }

    pub fn send(&mut self, steer: bool, window: &mut Window, cx: &mut Context<Self>) {
        if !self.can_send(cx) {
            return;
        }
        if self.session(cx).is_some_and(|s| s.archived) {
            self.unarchive_then(window, cx, move |this, window, cx| this.send(steer, window, cx));
            return;
        }
        let text = self.draft_text(cx).trim().to_string();
        let images: Vec<ImageRef> = self
            .attachments
            .iter()
            .filter_map(|a| match &a.state {
                AttachState::Done(r) => Some(r.clone()),
                _ => None,
            })
            .collect();
        let taken = std::mem::take(&mut self.attachments);
        self.composer.update(cx, |c, cx| c.set_value("", window, cx));
        self.error = None;
        self.list.scroll_to_end();
        let id = self.session_id.clone();
        let task = self
            .conn
            .update(cx, |c, cx| c.send_prompt(&id, text.clone(), images, steer, cx));
        cx.spawn_in(window, async move |this, cx| {
            if let Err(e) = task.await {
                let _ = this.update_in(cx, |this, window, cx| {
                    // Give the prompt back, so it isn't lost.
                    let now = this.draft_text(cx);
                    let back = [text, now]
                        .into_iter()
                        .filter(|t| !t.is_empty())
                        .collect::<Vec<_>>()
                        .join("\n");
                    this.composer.update(cx, |c, cx| c.set_value(back, window, cx));
                    let mut taken = taken;
                    taken.append(&mut this.attachments);
                    this.attachments = taken;
                    dialogs::alert(window, cx, "Error", &explain(&e));
                    cx.notify();
                });
            }
        })
        .detach();
        cx.notify();
    }

    /// Asks to unarchive the session first, then goes on.
    fn unarchive_then(
        &mut self,
        window: &mut Window,
        cx: &mut Context<Self>,
        then: impl Fn(&mut Self, &mut Window, &mut Context<Self>) + 'static,
    ) {
        let this = cx.entity().downgrade();
        let then = std::rc::Rc::new(then);
        dialogs::confirm(
            window,
            cx,
            "Unarchive session?",
            "This session is archived. Unarchive it to send your prompt.",
            vec![Choice::new(
                "Unarchive and Send",
                ButtonVariant::Primary,
                move |window, cx| {
                    let then = then.clone();
                    let _ = this.update(cx, |this, cx| {
                        this.unarchiving = true;
                        let call = this
                            .conn
                            .read(cx)
                            .call("session.unarchive", json!({"sessionId": this.session_id}));
                        cx.spawn_in(window, async move |this, cx| {
                            let result = call.await;
                            let _ = this.update_in(cx, |this, window, cx| {
                                this.unarchiving = false;
                                match result {
                                    Ok(v) => {
                                        // The session as it is now, before its event comes.
                                        if let Ok(s) = serde_json::from_value::<crate::protocol::Session>(v) {
                                            let id = this.session_id.clone();
                                            this.conn.update(cx, |c, _| {
                                                if let Some(store) = c.stores.get_mut(&id) {
                                                    store.set_session(std::rc::Rc::new(s));
                                                }
                                            });
                                        }
                                        then(this, window, cx);
                                    }
                                    Err(e) => {
                                        dialogs::alert(window, cx, "Couldn't unarchive the session", &explain(&e))
                                    }
                                }
                                cx.notify();
                            });
                        })
                        .detach();
                    });
                },
            )],
        );
    }

    pub(crate) fn send_draft(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.session(cx).is_some_and(|s| s.archived) {
            self.unarchive_then(window, cx, |this, window, cx| this.send_draft(window, cx));
            return;
        }
        self.run(
            "session.sendDraft",
            json!({"commandId": uuid::Uuid::new_v4().to_string(), "sessionId": self.session_id}),
            cx,
        );
    }

    /// Takes the session's draft back into the prompt.
    pub(crate) fn edit_draft(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(draft) = self.session(cx).and_then(|s| s.draft.clone()) else {
            return;
        };
        let now = self.draft_text(cx);
        let text = [draft.text.clone(), now]
            .into_iter()
            .filter(|t| !t.is_empty())
            .collect::<Vec<_>>()
            .join("\n");
        self.composer.update(cx, |c, cx| {
            c.set_value(text, window, cx);
            c.focus(window, cx);
        });
        for image in draft.images {
            let key = self.next_key();
            self.attachments.push(Attachment {
                key,
                prepared: None,
                state: AttachState::Done(image),
            });
        }
        self.run(
            "session.saveDraft",
            json!({"sessionId": self.session_id, "text": ""}),
            cx,
        );
        cx.notify();
    }

    /// Save as Draft: offered in any session that has no draft yet.
    pub(crate) fn can_save_draft(&self, cx: &App) -> bool {
        let conn = self.conn.read(cx);
        let drafts = conn.info.as_ref().is_some_and(|i| i.drafts);
        let Some(store) = conn.stores.get(&self.session_id) else {
            return false;
        };
        let Some(s) = &store.session else { return false };
        drafts && !s.archived && s.draft.is_none() && store.sync.synced && self.has_something(cx)
    }

    pub fn save_draft(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if !self.can_save_draft(cx) || !self.uploads_done() || self.saving_draft {
            return;
        }
        let text = self.draft_text(cx).trim().to_string();
        let ids: Vec<String> = self
            .attachments
            .iter()
            .filter_map(|a| match &a.state {
                AttachState::Done(r) => Some(r.id.clone()),
                _ => None,
            })
            .collect();
        let keys: Vec<u64> = self.attachments.iter().map(|a| a.key).collect();
        self.saving_draft = true;
        let call = self.conn.read(cx).call(
            "session.saveDraft",
            json!({"sessionId": self.session_id, "text": text, "images": ids}),
        );
        cx.spawn_in(window, async move |this, cx| {
            let result = call.await;
            let _ = this.update_in(cx, |this, window, cx| {
                this.saving_draft = false;
                match result {
                    Ok(_) => {
                        if this.draft_text(cx).trim() == text {
                            this.composer.update(cx, |c, cx| c.set_value("", window, cx));
                        }
                        this.attachments.retain(|a| !keys.contains(&a.key));
                    }
                    Err(e) => this.error = Some(explain(&e)),
                }
                cx.notify();
            });
        })
        .detach();
        cx.notify();
    }

    // MARK: Slash commands

    pub(crate) fn update_commands(&mut self, cx: &mut Context<Self>) {
        let draft = self.draft_text(cx);
        if self.commands_dismissed.as_ref().is_some_and(|d| *d != draft) {
            self.commands_dismissed = None;
        }
        self.command_index = 0;
        self.command_scroll.scroll_to_item(0);
        let active = command_query(&draft).is_some();
        if !active {
            // Looked up again when the picker opens again.
            self.commands.fetched = false;
            return;
        }
        if self.commands.fetched || !self.conn.read(cx).is_connected() {
            return;
        }
        self.commands.fetched = true;
        self.commands.loading = true;
        self.commands.failed = false;
        let call = self.conn.read(cx).call_with_timeout(
            "session.commands",
            json!({"sessionId": self.session_id}),
            std::time::Duration::from_secs(80),
        );
        cx.spawn(async move |this, cx| {
            let result = call.await;
            let _ = this.update(cx, |this, cx| {
                this.commands.loading = false;
                match result {
                    Ok(v) => this.commands.list = serde_json::from_value::<CommandList>(v).unwrap_or_default().commands,
                    Err(e) if e.code == "method_not_found" => this.commands.list.clear(),
                    Err(_) => this.commands.failed = true,
                }
                cx.notify();
            });
        })
        .detach();
    }

    fn suggestions(&self, cx: &App) -> Vec<crate::protocol::Command> {
        let draft = self.draft_text(cx);
        if self.commands_dismissed.as_deref() == Some(draft.as_str()) || self.session(cx).is_some_and(|s| s.archived) {
            return Vec::new();
        }
        let conn = self.conn.read(cx);
        let mut all = self.commands.list.clone();
        if let Some(h) = conn.session(&self.session_id).and_then(|s| conn.harness(&s.harness)) {
            all.extend(h.commands.iter().cloned());
        }
        command_suggestions(&draft, &all)
    }

    fn suggestions_shown(&self, cx: &App) -> bool {
        command_query(&self.draft_text(cx)).is_some()
            && self.commands_dismissed.as_deref() != Some(self.draft_text(cx).as_str())
            && !self.suggestions(cx).is_empty()
    }

    /// Tab or Return on a suggestion: completes the prompt to it, or sends
    /// it when the prompt is that command already.
    fn accept_suggestion(&mut self, enter: bool, window: &mut Window, cx: &mut Context<Self>) {
        let list = self.suggestions(cx);
        let Some(c) = list.get(self.command_index.min(list.len().saturating_sub(1))) else {
            return;
        };
        let prompt = format!("/{}", c.name);
        if enter && self.draft_text(cx).trim() == prompt {
            self.send(false, window, cx);
            return;
        }
        let text = format!("{prompt} ");
        self.composer.update(cx, |c, cx| {
            c.set_value(text.clone(), window, cx);
            c.set_cursor_position(
                gpui_kit::component::input::Position::new(0, text.len() as u32),
                window,
                cx,
            );
        });
    }

    /// ↑, ↓, Tab and Escape on the suggestions. The prompt binds those keys
    /// (`MoveUp`, `MoveDown`, `IndentInline`, `Escape`), and GPUI runs a key's
    /// binding before any key-down listener, so the composer catches the
    /// actions in the capture phase; with no suggestions shown they go on to
    /// the prompt.
    fn suggestion_key(&mut self, key: SuggestionKey, window: &mut Window, cx: &mut Context<Self>) {
        if !self.suggestions_shown(cx) {
            return;
        }
        let n = self.suggestions(cx).len();
        match key {
            SuggestionKey::Down => {
                self.command_index = (self.command_index + 1).min(n - 1);
                self.command_scroll.scroll_to_item(self.command_index);
            }
            SuggestionKey::Up => {
                self.command_index = self.command_index.saturating_sub(1);
                self.command_scroll.scroll_to_item(self.command_index);
            }
            SuggestionKey::Accept => self.accept_suggestion(false, window, cx),
            SuggestionKey::Dismiss => self.commands_dismissed = Some(self.draft_text(cx)),
        }
        cx.stop_propagation();
        cx.notify();
    }

    // MARK: Images

    /// ⌘V or Edit › Paste with images on the pasteboard and no text:
    /// attaches them. It catches the prompt's Paste action before the prompt
    /// does, as GPUI runs a key's binding before any key-down listener.
    pub(crate) fn paste_images(&mut self, window: &mut Window, cx: &mut Context<Self>) -> bool {
        let Some(item) = cx.read_from_clipboard() else {
            return false;
        };
        self.attach_clipboard(&item, window, cx)
    }

    pub(crate) fn attach_clipboard(
        &mut self,
        item: &ClipboardItem,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> bool {
        match prompt_box::pasted_images(item) {
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

    pub(crate) fn pick_images(&mut self, window: &mut Window, cx: &mut Context<Self>) {
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

    pub(crate) fn drop_paths(&mut self, paths: &ExternalPaths, window: &mut Window, cx: &mut Context<Self>) {
        let images: Vec<PathBuf> = paths
            .paths()
            .iter()
            .filter(|p| prompt_box::is_image_path(p))
            .cloned()
            .collect();
        self.attach_paths(images, window, cx);
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

    fn attach_bytes(&mut self, bytes: Vec<u8>, window: &mut Window, cx: &mut Context<Self>) {
        if self.read_only(cx) {
            return;
        }
        let task = cx.background_spawn(async move { prepare(&bytes) });
        cx.spawn_in(window, async move |this, cx| {
            let prepared = task.await;
            let _ = this.update_in(cx, |this, _, cx| match prepared {
                Ok(prepared) => {
                    let image = Arc::new(Image::from_bytes(prepared.format, prepared.bytes.clone()));
                    let key = this.next_key();
                    this.attachments.push(Attachment {
                        key,
                        prepared: Some((prepared, image)),
                        state: AttachState::Uploading,
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
        let Some((prepared, _)) = &a.prepared else { return };
        let prepared: Prepared = prepared.clone();
        a.state = AttachState::Uploading;
        let task = self.conn.update(cx, |c, cx| c.upload_image(prepared, cx));
        cx.spawn(async move |this, cx| {
            let result = task.await;
            let _ = this.update(cx, |this, cx| {
                if let Some(a) = this.attachments.iter_mut().find(|a| a.key == key) {
                    a.state = match result {
                        Ok(r) => AttachState::Done(r),
                        Err(e) => AttachState::Failed(e.to_string()),
                    };
                }
                cx.notify();
            });
        })
        .detach();
        cx.notify();
    }

    // MARK: Drawing

    fn render_attachments(&mut self, p: &Palette, cx: &mut Context<Self>) -> Option<AnyElement> {
        if self.attachments.is_empty() {
            return None;
        }
        let mut row = h_flex().gap_1p5().flex_wrap();
        let shown: Vec<Shown> = self
            .attachments
            .iter()
            .map(|a| {
                let id = match &a.state {
                    AttachState::Done(r) => Some(r.id.clone()),
                    _ => None,
                };
                let failed = match &a.state {
                    AttachState::Failed(e) => Some(e.clone()),
                    _ => None,
                };
                (
                    a.key,
                    a.prepared.as_ref().map(|(_, i)| i.clone()),
                    id,
                    matches!(a.state, AttachState::Uploading),
                    failed,
                )
            })
            .collect();
        for (key, image, id, uploading, failed) in shown {
            let image = image.or_else(|| {
                let id = id?;
                match self.conn.update(cx, |c, cx| c.image(&id, cx)) {
                    Loaded::Ready(i) => Some(i),
                    _ => None,
                }
            });
            let mut thumb = div()
                .id(SharedString::from(format!("attach-{key}")))
                .relative()
                .size(px(56.))
                .rounded_md()
                .overflow_hidden()
                .bg(p.crust);
            if let Some(image) = image {
                thumb = thumb.child(img(image).size_full().object_fit(ObjectFit::Cover));
            }
            if uploading {
                thumb = thumb.child(
                    div()
                        .absolute()
                        .inset_0()
                        .flex()
                        .items_center()
                        .justify_center()
                        .bg(p.base.opacity(0.5))
                        .child(Spinner::new().small()),
                );
            }
            if let Some(e) = failed {
                thumb = thumb
                    .cursor_pointer()
                    .tooltip(move |window, cx| {
                        gpui_kit::component::tooltip::Tooltip::new(format!("{e} Click to try again.")).build(window, cx)
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
            thumb = thumb.child(
                div().absolute().top_0p5().right_0p5().child(
                    Button::new(SharedString::from(format!("unattach-{key}")))
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

    fn render_suggestions(&self, p: &Palette, cx: &mut Context<Self>) -> Option<AnyElement> {
        if command_query(&self.draft_text(cx)).is_none()
            || self.commands_dismissed.as_deref() == Some(self.draft_text(cx).as_str())
        {
            return None;
        }
        let list = self.suggestions(cx);
        let status = if self.commands.loading {
            Some("Loading skills…")
        } else if self.commands.failed {
            Some("Skills unavailable. Try typing / again.")
        } else {
            None
        };
        if list.is_empty() && status.is_none() {
            return None;
        }
        let mut col = v_flex()
            .id("suggestions")
            .max_h(px(220.))
            .overflow_y_scroll()
            .track_scroll(&self.command_scroll)
            .gap_0p5();
        for (i, c) in list.iter().enumerate() {
            let selected = i == self.command_index.min(list.len() - 1);
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
                    .when_some(c.scope.clone(), |d, s| d.child(caption(capitalize(&s), p)))
                    .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| {
                        let text = format!("/{name} ");
                        this.composer.update(cx, |c, cx| {
                            c.set_value(text.clone(), window, cx);
                            c.focus(window, cx);
                        });
                    })),
            );
        }
        if let Some(s) = status {
            col = col.child(caption(s, p).px_2().py_1());
        }
        Some(
            div()
                .p_1()
                .rounded_lg()
                .border_1()
                .border_color(p.surface0)
                .bg(p.mantle)
                .child(col)
                .into_any_element(),
        )
    }

    /// The agent, model and effort the session runs with, greyed out: they
    /// stay as the session started.
    fn agent_pill(&self, cx: &App) -> Option<AnyElement> {
        let p = Palette::of(cx);
        let conn = self.conn.read(cx);
        let s = conn.session(&self.session_id)?;
        let harness = conn.harness(&s.harness);
        let model = model_name(&s, harness);
        let effort_id = s
            .model_info
            .as_ref()
            .and_then(|m| m.effort.clone())
            .or_else(|| s.effort.clone());
        let effort = effort_id.map(|e| {
            harness
                .and_then(|h| {
                    h.efforts_for(s.model.as_deref())
                        .iter()
                        .find(|c| c.id == e)
                        .map(|c| c.name.clone())
                })
                .unwrap_or(e)
        });
        let label = match effort {
            Some(e) => format!("{model} {e}"),
            None => model,
        };
        Some(prompt_box::fixed_pill(
            "agent-pill",
            Some(harness_icon(&s.harness, &p)),
            label,
        ))
    }

    /// The row under the box: where the session works, which it keeps, and
    /// its permissions, which it can change.
    fn footer(&self, cx: &mut Context<Self>) -> (Vec<AnyElement>, Vec<AnyElement>) {
        let Some(s) = self.session(cx) else {
            return (Vec::new(), Vec::new());
        };
        let conn = self.conn.read(cx);
        let server = conn.name.clone();
        let harness = conn.harness(&s.harness).cloned();
        let git = self.is_git(cx);
        let worktree = s.workspace.kind == WorkspaceKind::Worktree;
        let mut start = vec![prompt_box::fixed_pill(
            "server-pill",
            Some(Icon::new(L::Server)),
            server,
        )];
        start.push(prompt_box::fixed_pill(
            "place-pill",
            Some(Icon::new(if worktree { L::FolderGit2 } else { L::Folder })),
            if worktree {
                "Worktree"
            } else if git {
                "Current checkout"
            } else {
                "Project folder"
            },
        ));
        let mut end = Vec::new();
        if git {
            end.push(prompt_box::fixed_pill(
                "branch-pill",
                Some(Icon::new(L::GitBranch)),
                s.workspace.branch.clone().unwrap_or_else(|| "Detached HEAD".into()),
            ));
        }
        // How full the context is; a click shows the rest of its usage.
        if let Some(pill) = s
            .context
            .as_ref()
            .and_then(|usage| super::usage::context_pill(self.conn.clone(), s.id.clone(), usage, cx))
        {
            end.push(pill);
        }
        if let Some(h) = harness.filter(|h| !h.modes.is_empty()) {
            let current = s.mode.clone().unwrap_or_default();
            let name = h
                .modes
                .iter()
                .find(|m| m.id == current)
                .map(|m| m.name.clone())
                .unwrap_or_else(|| current.clone());
            if self.read_only(cx) {
                end.push(prompt_box::fixed_pill(
                    "mode-pill",
                    Some(prompt_box::mode_icon(&current)),
                    name,
                ));
            } else {
                let choices: Vec<(String, String)> = h.modes.iter().map(|m| (m.id.clone(), m.name.clone())).collect();
                let tooltip = h
                    .modes
                    .iter()
                    .find(|m| m.id == current)
                    .and_then(|m| m.description.clone())
                    .filter(|d| !d.is_empty());
                let (w, sid) = (cx.entity().downgrade(), s.id.clone());
                end.push(prompt_box::pill_menu(
                    "mode-pill",
                    Some(prompt_box::mode_icon(&current)),
                    name,
                    tooltip,
                    move |menu, _, _| {
                        let (w, sid) = (w.clone(), sid.clone());
                        prompt_box::choices(
                            menu,
                            &choices,
                            &current,
                            std::rc::Rc::new(move |id, _, cx| {
                                let _ = w.update(cx, |v, cx| {
                                    v.run("session.setMode", json!({"sessionId": sid, "mode": id}), cx)
                                });
                            }),
                        )
                    },
                ));
            }
        }
        (start, end)
    }

    pub(crate) fn render_composer(&mut self, window: &mut Window, cx: &mut Context<Self>) -> AnyElement {
        let p = Palette::of(cx);
        let running = self.is_running(cx);
        let connected = self.conn.read(cx).is_connected();
        let read_only = self.read_only(cx);
        let removed = self.store(cx).is_some_and(|s| s.removed);
        let dictating = self.speech.read(cx).status().is_active();
        let dictation = crate::ui::state::in_app_bundle() && self.speech.read(cx).is_available(cx);
        let empty = !self.has_something(cx);
        let can_send = self.can_send(cx) && !self.unarchiving;
        let steers = can_send && self.can_steer(&self.draft_text(cx), cx);
        let save_draft = self.can_save_draft(cx);
        let drafting = save_draft && self.uploads_done() && !self.saving_draft;
        let attachments = self.render_attachments(&p, cx);
        let suggestions = self.render_suggestions(&p, cx);
        let this = cx.entity().downgrade();
        let plus =
            Button::new("attach")
                .small()
                .ghost()
                .icon(IconName::Plus)
                .tooltip("Add images, and more")
                .disabled(read_only || removed)
                .dropdown_menu_with_anchor(gpui_kit::Anchor::BottomLeft, move |mut menu, _, cx| {
                    let has_image = cx
                        .read_from_clipboard()
                        .is_some_and(|i| i.entries().iter().any(|e| matches!(e, ClipboardEntry::Image(_))));
                    let (a, b, c) = (this.clone(), this.clone(), this.clone());
                    menu =
                        menu.item(PopupMenuItem::new("Files…").icon(Icon::new(L::Image)).on_click(
                            move |_, window, cx| {
                                let _ = a.update(cx, |v, cx| v.pick_images(window, cx));
                            },
                        ))
                        .item(
                            PopupMenuItem::new("Paste Image")
                                .icon(Icon::new(L::ClipboardPaste))
                                .disabled(!has_image)
                                .on_click(move |_, window, cx| {
                                    let _ = b.update(cx, |v, cx| {
                                        if let Some(item) = cx.read_from_clipboard() {
                                            v.attach_clipboard(&item, window, cx);
                                        }
                                    });
                                }),
                        );
                    if save_draft {
                        menu = menu.separator().item(
                            PopupMenuItem::new("Save as Draft")
                                .icon(Icon::new(L::Download))
                                .action(Box::new(crate::SaveDraft))
                                .disabled(!drafting)
                                .on_click(move |_, window, cx| {
                                    let _ = c.update(cx, |v, cx| v.save_draft(window, cx));
                                }),
                        );
                    }
                    menu
                });
        let mut leading = vec![plus.into_any_element()];
        if !connected {
            leading.push(caption("Not connected", &p).into_any_element());
        }
        if read_only {
            leading.push(caption("Archived", &p).into_any_element());
        }
        let mut trailing: Vec<AnyElement> = self.agent_pill(cx).into_iter().collect();
        if running {
            trailing.push(
                Button::new("stop")
                    .small()
                    .outline()
                    .icon(IconName::Square)
                    .label("Stop")
                    .tooltip("Stop (⌘.)")
                    .on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.interrupt(false, cx)))
                    .into_any_element(),
            );
        }
        if steers {
            trailing.push(
                Button::new("steer")
                    .small()
                    .outline()
                    .label("Steer")
                    .tooltip("Steer into the turn (⇧⌘Return)")
                    .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.send(true, window, cx)))
                    .into_any_element(),
            );
        }
        if dictation && !read_only && (empty || dictating) {
            trailing.push(prompt_box::mic_button(
                dictating,
                true,
                cx.listener(|this, _: &ClickEvent, _, cx| this.toggle_dictation(cx)),
            ));
        }
        if !dictating {
            trailing.push(prompt_box::send_button(
                can_send && connected && !removed,
                if steers {
                    "Queue (Return) · Steer into the turn (⇧⌘Return)"
                } else if running {
                    "Queue (Return)"
                } else {
                    "Send (Return)"
                },
                cx.listener(|this, _: &ClickEvent, window, cx| this.send(false, window, cx)),
            ));
        }
        let (footer_start, footer_end) = self.footer(cx);
        let input =
            div()
                .capture_action(
                    cx.listener(|this, _: &MoveUp, window, cx| this.suggestion_key(SuggestionKey::Up, window, cx)),
                )
                .capture_action(
                    cx.listener(|this, _: &MoveDown, window, cx| this.suggestion_key(SuggestionKey::Down, window, cx)),
                )
                .capture_action(cx.listener(|this, _: &IndentInline, window, cx| {
                    this.suggestion_key(SuggestionKey::Accept, window, cx)
                }))
                .capture_action(
                    cx.listener(|this, _: &Escape, window, cx| this.suggestion_key(SuggestionKey::Dismiss, window, cx)),
                )
                .capture_action(cx.listener(|this, _: &Paste, window, cx| {
                    if this.paste_images(window, cx) {
                        cx.stop_propagation();
                    }
                }))
                .child(
                    Textarea::new(&self.composer)
                        .appearance(false)
                        .disabled(read_only || removed),
                )
                .into_any_element();
        let _ = window;
        let prompt = PromptBox {
            above: suggestions,
            attachments,
            input: Some(input),
            leading,
            trailing,
            footer_start,
            footer_end,
            max_width: prompt_box::max_width(cx),
        }
        .render(&p);
        v_flex()
            .items_center()
            .px_5()
            .pb_3()
            .pt_1()
            .child(prompt)
            .into_any_element()
    }
}

#[derive(Clone, Copy)]
enum SuggestionKey {
    Up,
    Down,
    Accept,
    Dismiss,
}

/// An attachment as the strip draws it: its key, picture, id once
/// uploaded, whether it uploads, and why it failed.
type Shown = (u64, Option<Arc<Image>>, Option<String>, bool, Option<String>);

fn capitalize(s: &str) -> String {
    let mut c = s.chars();
    match c.next() {
        Some(f) => f.to_uppercase().collect::<String>() + c.as_str(),
        None => String::new(),
    }
}

#[allow(dead_code)]
fn bold() -> FontWeight {
    FontWeight::SEMIBOLD
}
