//! Changes: the session's diff, or a turn's, with its files and patch, and
//! reverting a worktree to before a turn. As `DiffView.swift`.

use std::rc::Rc;

use gpui_kit::assets::IconName as L;
use gpui_kit::component::WindowExt as _;
use gpui_kit::component::button::{Button, ButtonVariant, ButtonVariants as _};
use gpui_kit::component::menu::{DropdownMenu as _, PopupMenuItem};
use gpui_kit::component::spinner::Spinner;
use gpui_kit::component::switch::Switch;
use gpui_kit::component::{Disableable as _, Icon, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    Anchor, AnyElement, App, AppContext as _, ClickEvent, Context, Entity, FontWeight, InteractiveElement as _,
    IntoElement, ParentElement as _, Render, StatefulInteractiveElement as _, Styled as _, Window, div,
};
use serde_json::json;

use crate::conn::ServerConn;
use crate::protocol::{Diff, RevertResult, Session, SessionStatus, Turn, WorkspaceKind};
use crate::ui::common::{MONO, caption, file_row, files_summary, plural};
use crate::ui::dialogs::{self, Choice};
use crate::ui::palette::Palette;
use crate::ui::session::explain;
use crate::ui::state::{prefs, update_prefs};

pub fn open(conn: Entity<ServerConn>, session: Rc<Session>, window: &mut Window, cx: &mut App) {
    let view = cx.new(|cx| DiffView::new(conn, session, cx));
    let size = window.viewport_size();
    window.open_dialog(cx, move |dialog, _, _| {
        dialog
            .w(size.width * 0.86)
            .title("Changes")
            .child(div().h(size.height * 0.72).child(view.clone()))
    });
}

#[derive(Clone, PartialEq)]
enum Scope {
    Session,
    Turn(Rc<Turn>),
}

pub struct DiffView {
    conn: Entity<ServerConn>,
    session: Rc<Session>,
    scope: Scope,
    diff: Option<Result<Diff, String>>,
    /// The file shown, by path; none: the list.
    file: Option<String>,
    reverted: Option<String>,
    reverting: bool,
}

impl DiffView {
    fn new(conn: Entity<ServerConn>, session: Rc<Session>, cx: &mut Context<Self>) -> Self {
        let mut this = Self {
            conn,
            session,
            scope: Scope::Session,
            diff: None,
            file: None,
            reverted: None,
            reverting: false,
        };
        this.load(cx);
        this
    }

    fn turns(&self, cx: &App) -> Vec<Rc<Turn>> {
        let mut turns: Vec<Rc<Turn>> = self
            .conn
            .read(cx)
            .stores
            .get(&self.session.id)
            .map(|s| {
                s.turns
                    .values()
                    .filter(|t| t.checkpoint_before.is_some())
                    .cloned()
                    .collect()
            })
            .unwrap_or_default();
        turns.sort_by_key(|t| t.n);
        turns
    }

    fn load(&mut self, cx: &mut Context<Self>) {
        self.diff = None;
        let call = match &self.scope {
            Scope::Session => self
                .conn
                .read(cx)
                .call("git.sessionDiff", json!({"sessionId": self.session.id})),
            Scope::Turn(t) => self.conn.read(cx).call("git.turnDiff", json!({"turnId": t.id})),
        };
        cx.spawn(async move |this, cx| {
            let result = call.await;
            let _ = this.update(cx, |this, cx| {
                this.diff = Some(
                    result
                        .map_err(|e| explain(&e))
                        .and_then(|v| serde_json::from_value::<Diff>(v).map_err(|e| e.to_string())),
                );
                cx.notify();
            });
        })
        .detach();
        cx.notify();
    }

    fn set_scope(&mut self, scope: Scope, cx: &mut Context<Self>) {
        if self.scope != scope {
            self.scope = scope;
            self.file = None;
            self.load(cx);
        }
    }

    fn running(&self, cx: &App) -> bool {
        self.conn
            .read(cx)
            .session(&self.session.id)
            .is_some_and(|s| matches!(s.status, SessionStatus::Running | SessionStatus::AwaitingApproval))
    }

    fn revert(&mut self, n: i64, window: &mut Window, cx: &mut Context<Self>) {
        let this = cx.entity().downgrade();
        dialogs::confirm(
            window,
            cx,
            "Revert files?",
            &format!(
                "Files changed in turn {n} and later are restored in the worktree. The conversation is kept, and the agent is told about the revert."
            ),
            vec![Choice::new(
                format!("Revert to before turn {n}"),
                ButtonVariant::Danger,
                move |_, cx| {
                    let _ = this.update(cx, |this, cx| {
                        this.reverting = true;
                        let call = this
                            .conn
                            .read(cx)
                            .call("git.revert", json!({"sessionId": this.session.id, "turn": n}));
                        cx.spawn(async move |this, cx| {
                            let result = call.await;
                            let _ = this.update(cx, |this, cx| {
                                this.reverting = false;
                                match result.map(|v| serde_json::from_value::<RevertResult>(v).unwrap_or_default()) {
                                    Ok(r) => {
                                        this.reverted =
                                            Some(format!("Reverted {}", plural(r.files.len(), "file", "files")));
                                        this.load(cx);
                                    }
                                    Err(e) => this.reverted = Some(explain(&e)),
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

    fn render_file(&self, diff: &Diff, path: &str, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let wrap = prefs(cx).diff_wrap;
        let patch = file_patch(&diff.patch, path);
        let stat = diff.files.iter().find(|f| f.path == path);
        let mut lines = v_flex().w_full().font_family(MONO).text_xs();
        let mut count = 0;
        for line in patch_lines(&patch) {
            count += 1;
            let (bg, color) = match line.0 {
                LineKind::Hunk => (None, p.peach),
                LineKind::Added => (Some(p.green.opacity(0.15)), p.text),
                LineKind::Deleted => (Some(p.red.opacity(0.15)), p.text),
                LineKind::Meta => (None, p.subtext),
                LineKind::Context => (None, p.text),
            };
            lines = lines.child(
                div()
                    .w_full()
                    .px_2()
                    .text_color(color)
                    .when_some(bg, |d, b| d.bg(b))
                    .when(!wrap, |d| d.whitespace_nowrap())
                    .child(if line.1.is_empty() { " ".to_string() } else { line.1 }),
            );
        }
        v_flex()
            .size_full()
            .gap_2()
            .child(
                h_flex()
                    .gap_2()
                    .child(
                        Button::new("back")
                            .xsmall()
                            .ghost()
                            .icon(gpui_kit::component::IconName::ChevronLeft)
                            .label("Files")
                            .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                                this.file = None;
                                cx.notify();
                            })),
                    )
                    .child(
                        div()
                            .flex_1()
                            .text_sm()
                            .font_weight(FontWeight::SEMIBOLD)
                            .truncate()
                            .child(path.to_string()),
                    )
                    .child(Switch::new("wrap").checked(wrap).label("Wrap lines").small().on_change(
                        |v: &bool, _, cx| {
                            let v = *v;
                            update_prefs(cx, |p| p.diff_wrap = v)
                        },
                    )),
            )
            .child(
                div()
                    .id("patch")
                    .flex_1()
                    .min_h_0()
                    .overflow_y_scroll()
                    .when(!wrap, |d| d.overflow_x_scroll())
                    .rounded_md()
                    .bg(p.crust)
                    .py_1()
                    .child(if count == 0 {
                        caption(
                            if stat.is_some_and(|s| s.binary) {
                                "Binary file"
                            } else {
                                "No textual changes"
                            },
                            p,
                        )
                        .p_3()
                        .into_any_element()
                    } else {
                        lines.into_any_element()
                    }),
            )
            .into_any_element()
    }
}

impl Render for DiffView {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let turns = self.turns(cx);
        let running = self.running(cx);
        let worktree = self.session.workspace.kind == WorkspaceKind::Worktree;
        let can_commit = self.conn.read(cx).info.as_ref().is_some_and(|i| i.commit) && !self.session.archived;
        let scope_label = match &self.scope {
            Scope::Session => "Whole session".to_string(),
            Scope::Turn(t) => format!("Turn {}", t.n),
        };
        let this = cx.entity().downgrade();
        let scope_turns = turns.clone();
        let scope_menu =
            Button::new("scope")
                .small()
                .outline()
                .label(scope_label)
                .dropdown_menu(move |mut menu, _, _| {
                    let w = this.clone();
                    menu = menu.item(PopupMenuItem::new("Whole session").on_click(move |_, _, cx| {
                        let _ = w.update(cx, |v, cx| v.set_scope(Scope::Session, cx));
                    }));
                    for t in &scope_turns {
                        let (w, t2) = (this.clone(), t.clone());
                        menu = menu.item(PopupMenuItem::new(format!("Turn {}", t.n)).on_click(move |_, _, cx| {
                            let _ = w.update(cx, |v, cx| v.set_scope(Scope::Turn(t2.clone()), cx));
                        }));
                    }
                    menu
                });
        let this = cx.entity().downgrade();
        let revert_turns: Vec<i64> = turns.iter().rev().map(|t| t.n).collect();
        let revert = (worktree && !revert_turns.is_empty()).then(|| {
            Button::new("revert")
                .small()
                .outline()
                .icon(Icon::new(L::Undo2))
                .label("Revert files…")
                .disabled(running || self.reverting)
                .dropdown_menu_with_anchor(Anchor::TopRight, move |mut menu, _, _| {
                    for n in &revert_turns {
                        let (w, n) = (this.clone(), *n);
                        menu = menu.item(PopupMenuItem::new(format!("Before turn {n}")).on_click(
                            move |_, window, cx| {
                                let _ = w.update(cx, |v, cx| v.revert(n, window, cx));
                            },
                        ));
                    }
                    menu
                })
        });
        let (conn, sid) = (self.conn.clone(), self.session.id.clone());
        let toolbar = h_flex()
            .gap_2()
            .child(scope_menu)
            .when_some(self.reverted.clone(), |d, r| {
                d.child(div().text_xs().text_color(p.green).child(r))
            })
            .child(div().flex_1())
            .when_some(revert, |d, r| d.child(r))
            .when(can_commit, |d| {
                d.child(
                    Button::new("commit")
                        .small()
                        .outline()
                        .icon(Icon::new(L::GitCommitHorizontal))
                        .label("Commit…")
                        .disabled(running)
                        .on_click(move |_: &ClickEvent, window, cx| {
                            crate::ui::commit::open(conn.clone(), sid.clone(), window, cx)
                        }),
                )
            });
        let body: AnyElement = match &self.diff {
            None => div()
                .flex_1()
                .flex()
                .items_center()
                .justify_center()
                .child(Spinner::new())
                .into_any_element(),
            Some(Err(e)) => div()
                .p_3()
                .text_sm()
                .text_color(p.red)
                .child(e.clone())
                .into_any_element(),
            Some(Ok(diff)) => match self.file.clone() {
                Some(path) => self.render_file(diff, &path, &p, cx),
                None => {
                    let mut list = v_flex().id("files").flex_1().min_h_0().overflow_y_scroll().gap_0p5();
                    for (i, f) in diff.files.iter().enumerate() {
                        let path = f.path.clone();
                        list = list.child(
                            div()
                                .id(("file", i))
                                .px_2()
                                .rounded_md()
                                .cursor_pointer()
                                .hover(|d| d.bg(p.surface0))
                                .child(file_row(f, &p))
                                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                                    this.file = Some(path.clone());
                                    cx.notify();
                                })),
                        );
                    }
                    v_flex()
                        .flex_1()
                        .min_h_0()
                        .gap_2()
                        .child(
                            div()
                                .text_sm()
                                .font_weight(FontWeight::SEMIBOLD)
                                .child(if diff.files.is_empty() {
                                    "No changes".to_string()
                                } else {
                                    files_summary(&diff.files)
                                }),
                        )
                        .child(list)
                        .when(diff.truncated, |d| {
                            d.child(caption("The patch was truncated; some files may show no lines.", &p))
                        })
                        .into_any_element()
                }
            },
        };
        v_flex().size_full().gap_3().child(toolbar).child(body)
    }
}

#[derive(Clone, Copy, PartialEq, Debug)]
enum LineKind {
    Hunk,
    Added,
    Deleted,
    Meta,
    Context,
}

/// A file's part of a unified diff, found by its path.
fn file_patch(patch: &str, path: &str) -> String {
    let mut chunks: Vec<Vec<&str>> = Vec::new();
    for line in patch.lines() {
        if line.starts_with("diff --git ") || chunks.is_empty() {
            chunks.push(Vec::new());
        }
        chunks.last_mut().unwrap().push(line);
    }
    for chunk in chunks {
        if chunk_path(&chunk).as_deref() == Some(path) {
            return chunk.join("\n");
        }
    }
    String::new()
}

fn chunk_path(chunk: &[&str]) -> Option<String> {
    let mut minus = None;
    for line in chunk.iter().take(8) {
        if let Some(p) = line.strip_prefix("+++ b/") {
            return Some(p.to_string());
        }
        if let Some(p) = line.strip_prefix("rename to ") {
            return Some(p.to_string());
        }
        if minus.is_none()
            && let Some(p) = line.strip_prefix("--- a/")
        {
            minus = Some(p.to_string());
        }
    }
    minus.or_else(|| {
        let header = chunk.first()?;
        header.split_once(" b/").map(|(_, p)| p.to_string())
    })
}

/// The lines after the first hunk header, each with its kind.
fn patch_lines(patch: &str) -> Vec<(LineKind, String)> {
    let mut out = Vec::new();
    let mut started = false;
    for line in patch.lines() {
        if line.starts_with("@@") {
            started = true;
            out.push((LineKind::Hunk, line.to_string()));
            continue;
        }
        if !started {
            continue;
        }
        let kind = match line.chars().next() {
            Some('+') => LineKind::Added,
            Some('-') => LineKind::Deleted,
            Some('\\') => LineKind::Meta,
            _ => LineKind::Context,
        };
        out.push((kind, line.to_string()));
    }
    if out.last().is_some_and(|(_, l)| l.is_empty()) {
        out.pop();
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn splits_the_fixture_patch() {
        let text = std::fs::read_to_string(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../protocol/fixtures/response_diff.json"
        ))
        .unwrap();
        let v: serde_json::Value = serde_json::from_str(&text).unwrap();
        let diff: Diff = serde_json::from_value(v["result"].clone()).unwrap();
        let patch = file_patch(&diff.patch, "main.go");
        let lines = patch_lines(&patch);
        assert_eq!(lines[0].0, LineKind::Hunk);
        assert_eq!(lines.iter().filter(|l| l.0 == LineKind::Added).count(), 3);
        assert_eq!(lines.iter().filter(|l| l.0 == LineKind::Deleted).count(), 1);
        assert!(file_patch(&diff.patch, "new.go").is_empty());
    }
}
