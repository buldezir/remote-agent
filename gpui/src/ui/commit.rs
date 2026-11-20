//! Commit: what a commit would take, its message, how far it goes (commit,
//! push, pull request), and its steps as rad runs it. As `CommitView.swift`.

use std::time::Duration;

use gpui_kit::assets::IconName as L;
use gpui_kit::component::WindowExt as _;
use gpui_kit::component::button::{Button, ButtonVariants as _};
use gpui_kit::component::input::{Textarea, TextareaState};
use gpui_kit::component::radio::{Radio, RadioGroup};
use gpui_kit::component::spinner::Spinner;
use gpui_kit::component::{Disableable as _, Icon, IconName, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    AnyElement, App, AppContext as _, ClickEvent, ClipboardItem, Context, Entity, FontWeight, InteractiveElement as _,
    IntoElement, ParentElement as _, Render, StatefulInteractiveElement as _, Styled as _, Subscription, Window, div,
    px,
};
use serde_json::json;

use crate::conn::ServerConn;
use crate::protocol::{CommitPreview, ServerTask, StepStatus, TaskList};
use crate::store::commit::{COMMIT_SCOPES, CommitScope};
use crate::ui::common::{MONO, caption, file_row, files_summary, plural};
use crate::ui::palette::Palette;
use crate::ui::session::explain;
use crate::ui::state::{prefs, update_prefs};
use crate::ui::task::{Phase, TaskProgress};

pub fn open(conn: Entity<ServerConn>, session_id: String, window: &mut Window, cx: &mut App) {
    let view = cx.new(|cx| CommitView::new(conn, session_id, window, cx));
    let size = window.viewport_size();
    window.open_dialog(cx, move |dialog, _, _| {
        dialog
            .w(px(560.))
            .title("Commit")
            .child(div().h((size.height * 0.7).min(px(640.))).child(view.clone()))
    });
}

pub struct CommitView {
    conn: Entity<ServerConn>,
    session_id: String,
    preview: Option<Result<CommitPreview, String>>,
    message: Entity<TextareaState>,
    scope: CommitScope,
    progress: Option<Entity<TaskProgress>>,
    starting: bool,
    error: Option<String>,
    show_output: bool,
    _subscriptions: Vec<Subscription>,
}

impl CommitView {
    fn new(conn: Entity<ServerConn>, session_id: String, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let message = cx.new(|cx| {
            TextareaState::new(window, cx)
                .placeholder("Commit message")
                .auto_grow(3, 12)
        });
        let mut this = Self {
            conn,
            session_id,
            preview: None,
            message,
            scope: CommitScope::Commit,
            progress: None,
            starting: false,
            error: None,
            show_output: false,
            _subscriptions: Vec::new(),
        };
        this.load(window, cx);
        this
    }

    /// A commit under way is followed; else the preview.
    fn load(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self.preview = None;
        let list = self
            .conn
            .read(cx)
            .call("task.list", json!({"sessionId": self.session_id}));
        let preview = self.conn.read(cx).call_with_timeout(
            "git.commitPreview",
            json!({"sessionId": self.session_id}),
            Duration::from_secs(90),
        );
        cx.spawn_in(window, async move |this, cx| {
            if let Ok(v) = list.await {
                let tasks = serde_json::from_value::<TaskList>(v).unwrap_or_default().tasks;
                if let Some(t) = tasks.into_iter().rev().find(|t| t.kind == "commit" && t.running()) {
                    let _ = this.update(cx, |this, cx| this.follow(t, cx));
                    return;
                }
            }
            let result = preview.await;
            let _ = this.update_in(cx, |this, window, cx| {
                match result
                    .map_err(|e| explain(&e))
                    .and_then(|v| serde_json::from_value::<CommitPreview>(v).map_err(|e| e.to_string()))
                {
                    Ok(p) => {
                        if this.message.read(cx).value().is_empty() {
                            let m = p.message.clone();
                            this.message.update(cx, |s, cx| s.set_value(m, window, cx));
                        }
                        this.scope = CommitScope::default_for(&prefs(cx).commit_scope, &p);
                        this.preview = Some(Ok(p));
                    }
                    Err(e) => this.preview = Some(Err(e)),
                }
                cx.notify();
            });
        })
        .detach();
        cx.notify();
    }

    fn follow(&mut self, task: ServerTask, cx: &mut Context<Self>) {
        let conn = self.conn.clone();
        let progress = cx.new(|cx| TaskProgress::new(conn, task, cx));
        self._subscriptions.push(cx.observe(&progress, |this, p, cx| {
            if matches!(p.read(cx).phase, Phase::Failed(_)) {
                this.show_output = true;
            }
            cx.notify();
        }));
        self.progress = Some(progress);
        cx.notify();
    }

    fn scopes(p: &CommitPreview) -> Vec<CommitScope> {
        COMMIT_SCOPES
            .iter()
            .copied()
            .filter(|s| match s {
                CommitScope::Commit => !p.files.is_empty(),
                _ => s.unavailable(p).is_none(),
            })
            .collect()
    }

    fn start(&mut self, cx: &mut Context<Self>) {
        let Some(Ok(p)) = &self.preview else { return };
        if !Self::scopes(p).contains(&self.scope) || self.starting {
            return;
        }
        let key = self.scope.key().to_string();
        update_prefs(cx, |prefs| prefs.commit_scope = key);
        let mut params = json!({"sessionId": self.session_id});
        let message = self.message.read(cx).value().trim().to_string();
        if !message.is_empty() && !p.files.is_empty() {
            params["message"] = json!(message);
        }
        if self.scope != CommitScope::Commit {
            params["push"] = json!(true);
        }
        if self.scope == CommitScope::PullRequest {
            params["pullRequest"] = json!(true);
        }
        self.starting = true;
        self.error = None;
        let call = self.conn.read(cx).call("git.commit", params);
        cx.spawn(async move |this, cx| {
            let result = call.await;
            let _ = this.update(cx, |this, cx| {
                this.starting = false;
                match result.map(serde_json::from_value::<ServerTask>) {
                    Ok(Ok(t)) => this.follow(t, cx),
                    Ok(Err(e)) => this.error = Some(e.to_string()),
                    Err(e) => this.error = Some(explain(&e)),
                }
                cx.notify();
            });
        })
        .detach();
        cx.notify();
    }

    fn render_preview(&mut self, prev: &CommitPreview, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let scopes = Self::scopes(prev);
        let files = !prev.files.is_empty();
        let branch = prev.branch.clone().unwrap_or_else(|| "Detached HEAD".into());
        let mut col = v_flex().gap_3();
        // Branch
        col = col.child(
            v_flex()
                .gap_0p5()
                .child(section("Branch", p))
                .child(
                    h_flex()
                        .gap_2()
                        .text_sm()
                        .child(Icon::new(L::GitBranch).small().text_color(p.overlay))
                        .child(branch)
                        .child(caption(
                            match &prev.remote {
                                Some(r) => format!("→ {r}"),
                                None => "No remote to push to".into(),
                            },
                            p,
                        )),
                )
                .when(self.scope == CommitScope::PullRequest, |d| {
                    d.child(caption(
                        format!(
                            "Pull request into {}",
                            prev.base.clone().unwrap_or_else(|| "the default branch".into())
                        ),
                        p,
                    ))
                }),
        );
        // Changes
        let mut changes = v_flex().gap_0p5().child(section(
            if files {
                files_summary(&prev.files)
            } else {
                "Changes".into()
            },
            p,
        ));
        if !files {
            changes = changes.child(div().text_sm().child("Nothing to commit."));
            if prev.ahead > 0 {
                changes = changes.child(caption(
                    format!("{} to push", plural(prev.ahead as usize, "commit", "commits")),
                    p,
                ));
            }
        }
        for f in &prev.files {
            changes = changes.child(file_row(f, p));
        }
        col = col.child(changes);
        if files {
            col = col.child(
                v_flex()
                    .gap_1()
                    .child(section("Message", p))
                    .child(
                        div()
                            .p_2()
                            .rounded_md()
                            .border_1()
                            .border_color(p.surface0)
                            .child(Textarea::new(&self.message).appearance(false)),
                    )
                    .child(caption("Leave it empty and rad writes one.", p)),
            );
        }
        // Do
        let footer = if prev.branch.is_some() && prev.remote.is_none() {
            Some("The repository has no remote to push to.".to_string())
        } else if prev.branch.is_none() {
            Some("HEAD is detached: check out a branch to push it.".to_string())
        } else if let Some(n) = prev.no_pull_request.as_ref().filter(|n| !n.is_empty()) {
            Some(if n.ends_with('.') { n.clone() } else { format!("{n}.") })
        } else if self.scope == CommitScope::PullRequest {
            Some(
                "GitHub's gh opens it on the computer, with a title and description written from the branch's commits."
                    .into(),
            )
        } else {
            None
        };
        let label = |s: CommitScope| -> String {
            if files {
                s.choice().to_string()
            } else {
                match s {
                    CommitScope::PullRequest => "Push & open PR".into(),
                    _ => "Push".into(),
                }
            }
        };
        let mut doing = v_flex().gap_1().child(section("Do", p));
        if scopes.is_empty() {
            doing = doing.child(div().text_sm().child("Nothing to commit or push."));
        } else if scopes.len() == 1 {
            doing = doing.child(div().text_sm().child(label(scopes[0])));
        } else {
            let selected = scopes.iter().position(|s| *s == self.scope);
            let mut group = RadioGroup::vertical("scope").selected_index(selected);
            for s in &scopes {
                group = group.child(Radio::new(s.key()).label(label(*s)));
            }
            let options = scopes.clone();
            doing = doing.child(group.on_click(cx.listener(move |this, ix: &usize, _, cx| {
                if let Some(s) = options.get(*ix) {
                    this.scope = *s;
                    cx.notify();
                }
            })));
        }
        if let Some(f) = footer {
            doing = doing.child(caption(f, p));
        }
        col.child(doing).into_any_element()
    }

    fn render_progress(&mut self, progress: &Entity<TaskProgress>, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let pr = progress.read(cx);
        let task = pr.task.clone();
        let phase = pr.phase.clone();
        let mut steps = v_flex().gap_1().child(section("Steps", p));
        if task.steps.is_empty() {
            steps = steps.child(caption("Starting…", p));
        }
        for s in &task.steps {
            let (icon, color): (AnyElement, _) = match s.status {
                StepStatus::Running => (Spinner::new().small().into_any_element(), p.text),
                StepStatus::Succeeded => (
                    Icon::new(IconName::CircleCheck)
                        .small()
                        .text_color(p.green)
                        .into_any_element(),
                    p.text,
                ),
                StepStatus::Skipped => (
                    Icon::new(IconName::Minus)
                        .small()
                        .text_color(p.overlay)
                        .into_any_element(),
                    p.subtext,
                ),
                StepStatus::Failed => (
                    Icon::new(IconName::CircleX)
                        .small()
                        .text_color(p.red)
                        .into_any_element(),
                    p.text,
                ),
                _ => (
                    Icon::new(L::Circle).small().text_color(p.overlay).into_any_element(),
                    p.subtext,
                ),
            };
            let failed = s.status == StepStatus::Failed;
            steps = steps.child(
                h_flex().gap_2().items_start().child(icon).child(
                    v_flex()
                        .flex_1()
                        .child(div().text_sm().text_color(color).child(s.title.clone()))
                        .when_some(s.detail.clone(), |d, detail| {
                            d.child(
                                div()
                                    .text_xs()
                                    .text_color(if failed { p.red } else { p.subtext })
                                    .child(detail),
                            )
                        }),
                ),
            );
        }
        let status = match &phase {
            Phase::Running | Phase::Restarting => div().text_sm().text_color(p.subtext).child("Working…"),
            Phase::Done => div().text_sm().text_color(p.green).child("Done."),
            Phase::Failed(e) => div().text_sm().text_color(p.red).child(format!("Failed: {e}")),
            Phase::Cancelled => div().text_sm().text_color(p.subtext).child("Cancelled."),
        };
        let url = task
            .url
            .clone()
            .filter(|u| u.starts_with("http://") || u.starts_with("https://"));
        let output = task.output.clone();
        let show = self.show_output;
        v_flex()
            .gap_3()
            .child(steps)
            .child(status)
            .when_some(url, |d, url| {
                let (open, copy) = (url.clone(), url.clone());
                d.child(
                    v_flex()
                        .gap_1()
                        .child(
                            h_flex()
                                .gap_2()
                                .child(
                                    Button::new("open-pr")
                                        .small()
                                        .primary()
                                        .label("Open Pull Request")
                                        .on_click(move |_: &ClickEvent, _, cx| cx.open_url(&open)),
                                )
                                .child(Button::new("copy-pr").small().outline().label("Copy Link").on_click(
                                    move |_: &ClickEvent, _, cx| {
                                        cx.write_to_clipboard(ClipboardItem::new_string(copy.clone()))
                                    },
                                )),
                        )
                        .child(div().text_xs().font_family(MONO).text_color(p.subtext).child(url)),
                )
            })
            .when(!output.is_empty(), |d| {
                d.child(
                    crate::ui::common::disclosure("output", show, "Output", p).on_click(cx.listener(
                        |this, _: &ClickEvent, _, cx| {
                            this.show_output = !this.show_output;
                            cx.notify();
                        },
                    )),
                )
                .when(show, |d| d.child(crate::ui::common::code_block(None, &output, p)))
            })
            .into_any_element()
    }
}

fn section(title: impl Into<gpui_kit::SharedString>, p: &Palette) -> gpui_kit::Div {
    div()
        .text_xs()
        .font_weight(FontWeight::SEMIBOLD)
        .text_color(p.overlay)
        .child(title.into())
}

impl Render for CommitView {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let mut footer = h_flex().gap_2().justify_end();
        let body: AnyElement = if let Some(progress) = self.progress.clone() {
            let active = progress.read(cx).active();
            if active {
                let pr = progress.clone();
                footer = footer.child(
                    Button::new("cancel-task")
                        .small()
                        .outline()
                        .label("Cancel")
                        .on_click(move |_: &ClickEvent, _, cx| pr.update(cx, |p, cx| p.cancel(cx))),
                );
            }
            footer = footer.child(
                Button::new("done")
                    .small()
                    .primary()
                    .label("Done")
                    .on_click(|_: &ClickEvent, window, cx| window.close_dialog(cx)),
            );
            self.render_progress(&progress, &p, cx)
        } else {
            match self.preview.clone() {
                None => v_flex()
                    .gap_2()
                    .items_center()
                    .pt_10()
                    .child(Spinner::new())
                    .child(caption("Looking at the changes…", &p))
                    .into_any_element(),
                Some(Err(e)) => v_flex()
                    .gap_2()
                    .child(div().text_sm().text_color(p.red).child(e))
                    .child(
                        Button::new("retry")
                            .small()
                            .outline()
                            .label("Retry")
                            .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.load(window, cx))),
                    )
                    .into_any_element(),
                Some(Ok(prev)) => {
                    let allowed = Self::scopes(&prev).contains(&self.scope);
                    footer = footer
                        .child(
                            Button::new("cancel")
                                .small()
                                .outline()
                                .label("Cancel")
                                .on_click(|_: &ClickEvent, window, cx| window.close_dialog(cx)),
                        )
                        .child(
                            Button::new("start")
                                .small()
                                .primary()
                                .label(self.scope.action(!prev.files.is_empty()))
                                .loading(self.starting)
                                .disabled(!allowed || self.starting)
                                .on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.start(cx))),
                        );
                    self.render_preview(&prev, &p, cx)
                }
            }
        };
        let _ = window;
        v_flex()
            .size_full()
            .gap_3()
            .child(
                div()
                    .id("commit-body")
                    .flex_1()
                    .min_h_0()
                    .overflow_y_scroll()
                    .child(body),
            )
            .when_some(self.error.clone(), |d, e| {
                d.child(div().text_sm().text_color(p.red).child(e))
            })
            .child(footer)
    }
}
