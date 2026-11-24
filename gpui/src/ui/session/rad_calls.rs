//! Drawing calls of rad's own tools, with which an agent starts and drives
//! other sessions, as sentences with those sessions' pills
//! (`store::rad_tools`), and the reports those sessions send back.

use std::rc::Rc;

use gpui_kit::assets::IconName as L;
use gpui_kit::component::button::{Button, ButtonVariants as _};
use gpui_kit::component::spinner::Spinner;
use gpui_kit::component::{Icon, IconName, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    AnyElement, App, ClickEvent, Context, FontWeight, Hsla, InteractiveElement as _, IntoElement, ParentElement as _,
    SharedString, StatefulInteractiveElement as _, Styled as _, div, px,
};

use super::SessionView;
use crate::protocol::{Item, ItemStatus, TurnStatus};
use crate::store::rad_tools::{RadCall, RadTool, Section, SessionRef, describe, report_words};
use crate::ui::common::{caption, code_block, markdown};
use crate::ui::palette::Palette;
use crate::ui::state::prefs;

/// What an open call's text sections show before "Show all".
const SECTION_LINES: usize = 30;

fn icon(tool: RadTool) -> L {
    use RadTool::*;
    match tool {
        StartSession => L::Rocket,
        ForkSession => L::GitFork,
        SendPrompt => L::Send,
        WaitForSession => L::Hourglass,
        ReadSession => L::BookOpenText,
        StopSession => L::CircleStop,
        RenameSession => L::PenLine,
        SaveDraft => L::FilePen,
        ArchiveSession => L::Archive,
        UnarchiveSession => L::ArchiveRestore,
        ReadDiff => L::FileDiff,
        SearchSessions => L::TextSearch,
        ListSessions => L::ListTree,
        ListProjects => L::FolderGit2,
        ListAgents => L::Bot,
        ListBranches => L::GitBranch,
    }
}

impl SessionView {
    /// The session a pill names: its id in full, where this server knows
    /// it, and its title, else the start of its id.
    fn resolve(&self, r: &SessionRef, cx: &App) -> (Option<String>, String) {
        let conn = self.conn.read(cx);
        let found = if r.id.is_empty() {
            None
        } else if let Some(s) = conn.session(&r.id) {
            Some(s)
        } else {
            // Agents may name a session by a unique start of its id.
            let mut matches = conn.index.sessions.values().filter(|s| s.id.starts_with(&r.id));
            match (matches.next(), matches.next()) {
                (Some(s), None) => Some(s.clone()),
                _ => None,
            }
        };
        let title = found
            .as_ref()
            .map(|s| s.title.clone())
            .filter(|t| !t.is_empty())
            .or_else(|| r.title.clone())
            .unwrap_or_else(|| r.id.chars().take(8).collect());
        (found.map(|s| s.id.clone()), title)
    }

    /// The title of the session a rad call or a report names, as the row
    /// shows it: a rename redraws the row.
    pub(crate) fn named_session_title(&self, it: &Item, cx: &App) -> Option<String> {
        if let Some(r) = &it.report {
            let r = SessionRef {
                id: r.session_id.clone(),
                title: Some(r.title.clone()),
            };
            return Some(self.resolve(&r, cx).1);
        }
        let call = describe(it)?;
        Some(self.resolve(call.session.as_ref()?, cx).1)
    }

    /// A session's pill, @title, which opens it.
    fn pill(&self, key: &str, r: &SessionRef, p: &Palette, cx: &App) -> AnyElement {
        let (id, title) = self.resolve(r, cx);
        let server = self.server_id(cx);
        div()
            .id(SharedString::from(format!("pill-{key}")))
            .flex_shrink_1()
            .min_w_0()
            .max_w(px(360.))
            .truncate()
            .px_1p5()
            .rounded_full()
            .bg(p.accent.opacity(0.16))
            .text_color(p.accent)
            .font_weight(FontWeight::SEMIBOLD)
            .child(format!("@{title}"))
            .when_some(id, |d, id| {
                d.cursor_pointer().hover(|s| s.bg(p.accent.opacity(0.26))).on_click(
                    move |_: &ClickEvent, window, cx| {
                        cx.stop_propagation();
                        if let Some(app) = crate::ui::state::app_view(cx) {
                            app.update(cx, |a, cx| a.open_by_ids(&server, &id, None, window, cx));
                        }
                    },
                )
            })
            .into_any_element()
    }

    /// A call of one of rad's tools; None for other tools.
    pub(crate) fn render_rad_tool(&mut self, it: &Rc<Item>, p: &Palette, cx: &mut Context<Self>) -> Option<AnyElement> {
        let call = describe(it)?;
        Some(self.render_rad_call(it, call, p, cx))
    }

    fn render_rad_call(&mut self, it: &Rc<Item>, call: RadCall, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let open = self.open_items.contains(&it.id);
        let running = matches!(it.status, ItemStatus::InProgress | ItemStatus::Pending);
        let failed = it.status == ItemStatus::Failed;
        let starts = matches!(call.tool, RadTool::StartSession | RadTool::ForkSession);
        let icon_color = if failed {
            p.red
        } else if starts || call.tool == RadTool::SendPrompt {
            p.accent
        } else {
            p.overlay
        };
        let id = it.id.clone();
        let has_body = !call.sections.is_empty();
        let mut line = h_flex()
            .id(SharedString::from(format!("rad-{}", it.id)))
            .gap_1p5()
            .py_0p5()
            .min_w_0()
            .text_sm()
            .text_color(p.subtext)
            .child(Icon::new(icon(call.tool)).small().text_color(icon_color))
            .child(
                div()
                    .flex_shrink_0()
                    .text_color(if failed { p.red } else { p.text })
                    .child(call.before.clone()),
            );
        if let Some(r) = call.session.as_ref().filter(|r| !r.id.is_empty() || r.title.is_some()) {
            line = line.child(self.pill(&it.id, r, p, cx));
        }
        if !call.after.is_empty() {
            line = line.child(
                div()
                    .min_w_0()
                    .truncate()
                    .text_color(p.overlay)
                    .child(call.after.clone()),
            );
        }
        line = line
            .when(running, |d| d.child(Spinner::new().xsmall().color(p.blue)))
            .when(it.status == ItemStatus::Pending, |d| {
                d.child(
                    div()
                        .flex_shrink_0()
                        .text_xs()
                        .text_color(p.yellow)
                        .child("waiting for you"),
                )
            })
            .when(has_body, |d| {
                d.cursor_pointer()
                    .child(
                        Icon::new(if open {
                            IconName::ChevronDown
                        } else {
                            IconName::ChevronRight
                        })
                        .xsmall()
                        .text_color(p.overlay),
                    )
                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.toggle_item(id.clone(), cx)))
            })
            .child(div().flex_1());
        let mut body = v_flex().ml_6().mt_1().gap_2();
        if open {
            for (i, sec) in call.sections.iter().enumerate() {
                body = body.child(self.render_section(it, i, sec, p, cx));
            }
        }
        let size = prefs(cx).message_size;
        let col = v_flex()
            .text_size(px(size))
            .child(line)
            .when(open && has_body, |d| d.child(body));
        if !starts {
            return col.into_any_element();
        }
        // Starting a session is an event of its own: a card.
        let border = if failed {
            p.red.opacity(0.4)
        } else {
            p.accent.opacity(0.3)
        };
        div()
            .w_full()
            .px_3()
            .py_1p5()
            .rounded_lg()
            .border_1()
            .border_color(border)
            .bg(p.mantle)
            .child(col)
            .into_any_element()
    }

    fn render_section(
        &mut self,
        it: &Item,
        i: usize,
        sec: &Section,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        match sec {
            Section::Markdown { label, text } => v_flex()
                .gap_0p5()
                .child(caption(*label, p).font_weight(FontWeight::SEMIBOLD))
                .child(
                    div()
                        .pl_3()
                        .border_l_2()
                        .border_color(p.surface0)
                        .child(markdown(format!("{}-sec{i}", it.id), text)),
                )
                .into_any_element(),
            Section::Text { label, text } => {
                let lines: Vec<&str> = text.lines().collect();
                let key = format!("{}#{i}", it.id);
                let full = self.full_outputs.contains(&key) || lines.len() <= SECTION_LINES;
                let shown = if full {
                    text.clone()
                } else {
                    lines[..SECTION_LINES].join("\n")
                };
                let block = code_block(Some(label), &shown, p).when(*label == "Error", |d| d.text_color(p.red));
                block
                    .when(!full, |d| {
                        d.child(
                            Button::new(SharedString::from(format!("all-{key}")))
                                .xsmall()
                                .link()
                                .label(format!("Show all {} lines", lines.len()))
                                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                                    this.full_outputs.insert(key.clone());
                                    this.refresh(cx);
                                })),
                        )
                    })
                    .into_any_element()
            }
        }
    }

    /// A report from a session this one's agent gave work to: rad wrote it,
    /// not the user, so it isn't the user's bubble.
    pub(crate) fn render_report(
        &mut self,
        it: &Rc<Item>,
        text: &str,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let Some(r) = &it.report else {
            return div().into_any_element();
        };
        let (color, icon): (Hsla, L) = match r.status {
            TurnStatus::Failed => (p.red, L::OctagonX),
            TurnStatus::Interrupted => (p.overlay, L::CircleStop),
            _ => (p.green, L::CircleCheck),
        };
        let words = report_words(text);
        let (status, detail) = match words.split_once(": ") {
            Some((s, d)) => (s.to_string(), Some(d.to_string())),
            None => (words, None),
        };
        let session = SessionRef {
            id: r.session_id.clone(),
            title: Some(r.title.clone()),
        };
        let size = prefs(cx).message_size;
        let card = v_flex()
            .gap_0p5()
            .px_3()
            .py_2()
            .rounded_lg()
            .border_1()
            .border_color(color.opacity(0.35))
            .bg(color.opacity(0.07))
            .text_size(px(size))
            .child(
                h_flex()
                    .gap_1p5()
                    .min_w_0()
                    .text_sm()
                    .child(Icon::new(L::MessageSquareReply).small().text_color(p.overlay))
                    .child(div().flex_shrink_0().text_color(p.subtext).child("Report from"))
                    .child(self.pill(&it.id, &session, p, cx))
                    .child(Icon::new(icon).xsmall().text_color(color))
                    .child(div().flex_shrink_0().text_color(color).child(status)),
            )
            .when_some(detail, |d, e| {
                d.child(div().pl_6().text_xs().text_color(p.subtext).line_clamp(3).child(e))
            });
        let mut col = v_flex()
            .items_start()
            .gap_1()
            .when(matches!(it.status, ItemStatus::Queued | ItemStatus::Cancelled), |d| {
                d.opacity(0.75)
            })
            .child(card);
        match it.status {
            ItemStatus::Queued => {
                col = col.child(self.queued_controls(it, text, p, cx));
            }
            ItemStatus::Cancelled => col = col.child(caption("Not sent", p)),
            _ => {}
        }
        col.into_any_element()
    }
}
