//! Changes: the session's diff, or a turn's, its files beside the selected
//! one's patch, and reverting a worktree to before a turn. As `DiffView.swift`.

use std::rc::Rc;

use gpui_kit::assets::IconName as L;
use gpui_kit::component::WindowExt as _;
use gpui_kit::component::button::{Button, ButtonVariant, ButtonVariants as _};
use gpui_kit::component::menu::{DropdownMenu as _, PopupMenuItem};
use gpui_kit::component::spinner::Spinner;
use gpui_kit::component::switch::Switch;
use gpui_kit::component::tooltip::Tooltip;
use gpui_kit::component::{Disableable as _, Icon, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    Anchor, AnyElement, App, AppContext as _, ClickEvent, Context, Div, ElementId, Entity, FontWeight,
    InteractiveElement as _, IntoElement, ParentElement as _, Pixels, Render, SharedString,
    StatefulInteractiveElement as _, Styled as _, Window, div, px,
};
use serde_json::json;

use crate::conn::ServerConn;
use crate::protocol::{Diff, FileStat, RevertResult, Session, SessionStatus, Turn, WorkspaceKind};
use crate::ui::common::{caption, file_icon, files_summary, plural};
use crate::ui::dialogs::{self, Choice};
use crate::ui::palette::Palette;
use crate::ui::session::explain;
use crate::ui::state::{prefs, update_prefs};

/// The dialog is this wide at most, and leaves this much of the window clear
/// on each side.
const MAX_WIDTH: Pixels = px(1100.);
const SIDE_MARGIN: Pixels = px(48.);
/// In a dialog narrower than this, the files and the patch take turns.
const SPLIT_MIN_WIDTH: Pixels = px(700.);
const FILES_WIDTH: Pixels = px(260.);

pub fn open(conn: Entity<ServerConn>, session: Rc<Session>, window: &mut Window, cx: &mut App) {
    let view = cx.new(|cx| DiffView::new(conn, session, cx));
    // Sized each time it's drawn, so that it follows the window.
    window.open_dialog(cx, move |dialog, window, _| {
        dialog
            .w(dialog_width(window))
            .title("Changes")
            .child(div().h(window.viewport_size().height * 0.72).child(view.clone()))
    });
}

fn dialog_width(window: &Window) -> Pixels {
    (window.viewport_size().width - SIDE_MARGIN * 2.)
        .min(MAX_WIDTH)
        .max(px(0.))
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
    /// The file whose patch shows, by path: the first one until another is
    /// picked.
    file: Option<String>,
    /// In a narrow dialog, whether the patch has taken the list's place.
    opened: bool,
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
            opened: false,
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
                this.keep_selection();
                cx.notify();
            });
        })
        .detach();
        cx.notify();
    }

    /// A diff that lost the selected file shows its first one, from the list.
    fn keep_selection(&mut self) {
        let Some(Ok(diff)) = &self.diff else { return };
        let file = selection(&diff.files, self.file.as_deref()).map(str::to_string);
        if file != self.file {
            self.file = file;
            self.opened = false;
        }
    }

    fn set_scope(&mut self, scope: Scope, cx: &mut Context<Self>) {
        if self.scope != scope {
            self.scope = scope;
            self.file = None;
            self.opened = false;
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

    /// The files, with `selected` marked where its patch shows beside them.
    fn render_files(&self, diff: &Diff, selected: Option<&str>, p: &Palette, cx: &mut Context<Self>) -> Div {
        let mut list = v_flex().id("files").flex_1().min_h_0().overflow_y_scroll().gap_0p5();
        for (i, f) in diff.files.iter().enumerate() {
            let path = f.path.clone();
            let is_selected = selected == Some(f.path.as_str());
            // The row cuts a long path short.
            let whole = SharedString::from(match &f.old_path {
                Some(old) => format!("{old} → {}", f.path),
                None => f.path.clone(),
            });
            list = list.child(
                div()
                    .id(("file", i))
                    .px_2()
                    .rounded_md()
                    .cursor_pointer()
                    .when(is_selected, |d| d.bg(p.surface0))
                    .when(!is_selected, |d| d.hover(|d| d.bg(p.surface0.opacity(0.5))))
                    .child(file_item(f, p))
                    .tooltip(move |window, cx| Tooltip::new(whole.clone()).build(window, cx))
                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                        this.file = Some(path.clone());
                        this.opened = true;
                        cx.notify();
                    })),
            );
        }
        v_flex()
            .min_h_0()
            .gap_2()
            .child(
                pane_header()
                    .font_weight(FontWeight::SEMIBOLD)
                    .child(files_summary(&diff.files)),
            )
            .child(list)
            .when(diff.truncated, |d| {
                d.child(caption("The patch was truncated; some files may show no lines.", p))
            })
    }

    /// A file's patch under its path, with the way back to the files where it
    /// has taken their place.
    fn render_file(&self, diff: &Diff, path: &str, back: bool, p: &Palette, cx: &mut Context<Self>) -> Div {
        let wrap = prefs(cx).diff_wrap;
        let patch = file_patch(&diff.patch, path);
        let stat = diff.files.iter().find(|f| f.path == path);
        let mut lines = v_flex().w_full().font_family(crate::ui::fonts::code()).text_xs();
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
        let back = back.then(|| {
            Button::new("back")
                .xsmall()
                .ghost()
                .icon(gpui_kit::component::IconName::ChevronLeft)
                .label("Files")
                .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                    this.opened = false;
                    cx.notify();
                }))
        });
        v_flex()
            .flex_1()
            .min_w_0()
            .min_h_0()
            .gap_2()
            .child(
                pane_header()
                    .gap_2()
                    .children(back)
                    .child(
                        div()
                            .flex_1()
                            .min_w_0()
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
                    // An id per file: each patch starts at its top.
                    .id(ElementId::Name(format!("patch-{path}").into()))
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
    }
}

/// The file to select: the one that was, while the diff has it; else its
/// first.
fn selection<'a>(files: &'a [FileStat], was: Option<&str>) -> Option<&'a str> {
    files
        .iter()
        .find(|f| Some(f.path.as_str()) == was)
        .or(files.first())
        .map(|f| f.path.as_str())
}

/// The line over the files and the one over the patch: as tall as each
/// other, so that the list and the patch start level.
fn pane_header() -> Div {
    h_flex().h_6().flex_shrink_0().text_sm()
}

/// A changed file in the list. As `common::file_row`, but a long name gives
/// way before the counts do, since the list may be a narrow column.
fn file_item(f: &FileStat, p: &Palette) -> Div {
    let path = std::path::Path::new(&f.path);
    let name = path
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_else(|| f.path.clone());
    let place = match &f.old_path {
        Some(old) => format!("{old} → {}", f.path),
        None => path
            .parent()
            .map(|d| d.to_string_lossy().into_owned())
            .unwrap_or_default(),
    };
    h_flex()
        .gap_2()
        .py_1()
        .text_sm()
        .child(file_icon(f.status, p).small())
        .child(div().min_w_0().truncate().child(name))
        .child(
            div()
                .flex_1()
                .min_w_0()
                .truncate()
                .text_xs()
                .text_color(p.overlay)
                .child(place),
        )
        .child(if f.binary {
            caption("binary", p).flex_shrink_0().into_any_element()
        } else {
            h_flex()
                .flex_shrink_0()
                .gap_1()
                .text_xs()
                .font_family(crate::ui::fonts::code())
                .child(div().text_color(p.green).child(format!("+{}", f.additions)))
                .child(div().text_color(p.red).child(format!("−{}", f.deletions)))
                .into_any_element()
        })
}

impl Render for DiffView {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let split = dialog_width(window) >= SPLIT_MIN_WIDTH;
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
            Some(Ok(diff)) => match self.file.as_deref() {
                None => pane_header()
                    .font_weight(FontWeight::SEMIBOLD)
                    .child("No changes")
                    .into_any_element(),
                Some(path) if split => div()
                    .flex()
                    .flex_1()
                    .min_h_0()
                    .gap_3()
                    .child(
                        self.render_files(diff, Some(path), &p, cx)
                            .w(FILES_WIDTH)
                            .flex_shrink_0(),
                    )
                    .child(self.render_file(diff, path, false, &p, cx))
                    .into_any_element(),
                Some(path) if self.opened => self.render_file(diff, path, true, &p, cx).into_any_element(),
                Some(_) => self.render_files(diff, None, &p, cx).flex_1().into_any_element(),
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

    #[test]
    fn selects_the_first_file_unless_the_selected_one_is_still_there() {
        let file = |path: &str| FileStat {
            path: path.into(),
            old_path: None,
            status: Default::default(),
            additions: 0,
            deletions: 0,
            binary: false,
        };
        let files = [file("a.go"), file("dir/b.go")];
        assert_eq!(selection(&files, None), Some("a.go"));
        assert_eq!(selection(&files, Some("dir/b.go")), Some("dir/b.go"));
        assert_eq!(selection(&files, Some("gone.go")), Some("a.go"));
        assert_eq!(selection(&[], Some("a.go")), None);
    }
}
