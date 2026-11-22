//! Drawing the transcript's rows.

use std::collections::{BTreeMap, BTreeSet};
use std::rc::Rc;

use gpui_kit::assets::IconName as L;
use gpui_kit::component::button::{Button, ButtonVariants as _};
use gpui_kit::component::input::Input;
use gpui_kit::component::spinner::Spinner;
use gpui_kit::component::{Disableable as _, Icon, IconName, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    AnyElement, App, ClickEvent, Context, Div, FontWeight, Hsla, InteractiveElement as _, IntoElement, ObjectFit,
    ParentElement as _, SharedString, StatefulInteractiveElement as _, Styled as _, StyledImage as _, Window, div, img,
    px,
};
use serde_json::{Value, json};

use super::{Row, SessionView};
use crate::images::Loaded;
use crate::protocol::{
    Approval, BackgroundTask, ImageRef, Item, ItemKind, ItemStatus, OptionKind, ToolKind, Turn, TurnStatus,
    tool_command,
};
use crate::store::limits::{limit_notice, limited_now};
use crate::store::tool_runs::ToolRun;
use crate::ui::common::{caption, code_block, disclosure, markdown};
use crate::ui::palette::Palette;
use crate::ui::prompt_box;
use crate::ui::state::prefs;

/// What a tool call's output shows before "Show all".
const OUTPUT_LINES: usize = 30;

impl SessionView {
    pub(crate) fn render_row(&mut self, ix: usize, window: &mut Window, cx: &mut Context<Self>) -> AnyElement {
        let Some(row) = self.rows.get(ix).cloned() else {
            return div().into_any_element();
        };
        let p = Palette::of(cx);
        let in_run = matches!(&row, Row::Item(it) if self.run_steps.contains(&it.id));
        let (body, lit) = match row {
            // Its approval, just under it, shows what the tool call would do.
            Row::Item(it) if self.asked_about(&it, cx) => return div().into_any_element(),
            Row::Item(it) => {
                let lit = self.highlight(&it.id);
                (self.render_item(&it, &p, window, cx), lit)
            }
            Row::Run { run, expanded } => {
                let lit = run.items.iter().map(|i| self.highlight(&i.id)).max().unwrap_or(0);
                (self.render_run(&run, expanded, &p, cx), if expanded { 0 } else { lit })
            }
            Row::TurnEnd(t) => {
                // An error in the turn has said why it failed.
                let told = self.store(cx).is_some_and(|s| {
                    s.transcript
                        .iter()
                        .any(|i| i.kind == ItemKind::Error && i.turn() == t.id)
                });
                (render_turn_end(&t, told, &p), 0)
            }
            Row::Outgoing(o) => {
                let images = self.gallery("out", &o.images, 120., true, &p, cx);
                (
                    v_flex()
                        .items_end()
                        .pl(px(40.))
                        .gap_1()
                        .when_some(images, |d, g| d.child(g))
                        .when(!o.text.is_empty(), |d| d.child(user_bubble(&o.command_id, &o.text, &p)))
                        .child(caption("Sending…", &p))
                        .into_any_element(),
                    0,
                )
            }
            Row::Draft { text, images } => (self.render_draft(&text, &images, &p, cx), 0),
            Row::Empty => (
                div()
                    .py_6()
                    .text_sm()
                    .text_center()
                    .text_color(p.overlay)
                    .child("No prompts yet. Write the first one below.")
                    .into_any_element(),
                0,
            ),
            Row::Working => (
                h_flex()
                    .gap_2()
                    .py_1()
                    .text_sm()
                    .text_color(p.subtext)
                    .child(Spinner::new().small())
                    .child("Working…")
                    .into_any_element(),
                0,
            ),
            Row::Background(tasks) => (render_background(&tasks, &p), 0),
            Row::Failed(e) => (
                h_flex()
                    .gap_2()
                    .py_1()
                    .text_sm()
                    .text_color(p.red)
                    .child(Icon::new(L::OctagonAlert).small().text_color(p.red))
                    .child(div().flex_1().child(e))
                    .into_any_element(),
                0,
            ),
        };
        let lit_color = match lit {
            2 => Some(p.yellow.opacity(0.2)),
            1 => Some(p.yellow.opacity(0.08)),
            _ => None,
        };
        let body = div()
            .rounded_md()
            .when_some(lit_color, |d, c| d.bg(c).px_1p5().py_0p5())
            .child(body);
        // An open run's steps hang from a rail under its arrow, which runs
        // unbroken from one to the next.
        let content = if in_run {
            div().w_full().child(
                div()
                    .ml(px(5.))
                    .pl_3()
                    .py_1()
                    .border_l_1()
                    .border_color(p.surface0)
                    .child(body),
            )
        } else {
            div().w_full().py_1().child(body)
        };
        // Limited, the rows keep to the prompt box's column.
        div()
            .w_full()
            .px_5()
            .flex()
            .justify_center()
            .child(content.when_some(prompt_box::messages_max_width(cx), |d, w| d.max_w(px(w))))
            .into_any_element()
    }

    /// A tool call that waits for the user's answer, while the approval that
    /// asks for it is in the transcript.
    pub(crate) fn asked_about(&self, it: &Item, cx: &App) -> bool {
        it.kind == ItemKind::ToolCall
            && it.status == ItemStatus::Pending
            && self.store(cx).is_some_and(|s| {
                s.transcript.iter().any(|a| {
                    a.kind == ItemKind::Approval
                        && a.status == ItemStatus::Pending
                        && a.approval.as_ref().and_then(|a| a.tool_item_id.as_deref()) == Some(it.id.as_str())
                })
            })
    }

    fn render_item(&mut self, it: &Rc<Item>, p: &Palette, window: &mut Window, cx: &mut Context<Self>) -> AnyElement {
        let text = it.text.clone().unwrap_or_default();
        match it.kind {
            ItemKind::UserMessage => self.render_user(it, &text, p, cx),
            ItemKind::AssistantMessage => self.render_reply(it, &text, p, cx),
            ItemKind::Reasoning => {
                let open = self.open_items.contains(&it.id);
                let working = it.status == ItemStatus::InProgress;
                let id = it.id.clone();
                v_flex()
                    .child(
                        disclosure(
                            format!("reasoning-{}", it.id),
                            open,
                            if working { "Thinking…" } else { "Thought" },
                            p,
                        )
                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.toggle_item(id.clone(), cx))),
                    )
                    .when(open && !text.is_empty(), |d| {
                        d.child(div().pl_5().text_sm().text_color(p.subtext).child(text.clone()))
                    })
                    .into_any_element()
            }
            ItemKind::ToolCall => self.render_tool(it, p, cx),
            ItemKind::Approval => self.render_approval(it, p, window, cx),
            ItemKind::Plan => render_plan(it, p),
            ItemKind::Notice => div()
                .w_full()
                .py_1()
                .text_xs()
                .text_center()
                .text_color(p.overlay)
                .line_clamp(4)
                .child(text)
                .into_any_element(),
            ItemKind::Error => match &it.limit {
                Some(w) => {
                    let now = chrono::Utc::now();
                    let holds = self
                        .conn
                        .read(cx)
                        .session(&self.session_id)
                        .and_then(|s| self.conn.read(cx).index.limits.get(&s.harness).cloned())
                        .is_some_and(|l| limited_now(&l, now))
                        || w.resets_at
                            .as_deref()
                            .and_then(|r| chrono::DateTime::parse_from_rfc3339(r).ok())
                            .is_some_and(|r| r > now);
                    let color = if holds { p.red } else { p.subtext };
                    h_flex()
                        .id(SharedString::from(format!("limit-{}", it.id)))
                        .gap_2()
                        .py_1()
                        .text_sm()
                        .text_color(color)
                        .child(Icon::new(L::Gauge).small().text_color(color))
                        .child(limit_notice(w, now))
                        .tooltip(move |window, cx| {
                            gpui_kit::component::tooltip::Tooltip::new(text.clone()).build(window, cx)
                        })
                        .into_any_element()
                }
                None => h_flex()
                    .gap_2()
                    .items_start()
                    .px_3()
                    .py_2()
                    .rounded_md()
                    .border_1()
                    .border_color(p.red.opacity(0.5))
                    .bg(p.red.opacity(0.08))
                    .child(Icon::new(IconName::TriangleAlert).small().text_color(p.red))
                    .child(div().flex_1().text_sm().text_color(p.red).child(text))
                    .into_any_element(),
            },
            ItemKind::Unknown => div().into_any_element(),
        }
    }

    fn render_user(&mut self, it: &Rc<Item>, text: &str, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let faded = matches!(it.status, ItemStatus::Queued | ItemStatus::Cancelled);
        let images = self.gallery(&it.id, &it.images, 120., true, p, cx);
        let mut col = v_flex()
            .items_end()
            .pl(px(40.))
            .gap_1()
            .when(faded, |d| d.opacity(0.75))
            .when_some(images, |d, g| d.child(g))
            .when(!text.is_empty(), |d| d.child(user_bubble(&it.id, text, p)));
        if it.status == ItemStatus::Queued {
            let steer = self.can_steer(text, cx);
            let (now, steer_id, cancel) = (it.id.clone(), it.id.clone(), it.id.clone());
            col = col.child(
                h_flex()
                    .gap_1()
                    .child(caption("Queued", p))
                    .child(
                        Button::new(SharedString::from(format!("now-{}", it.id)))
                            .xsmall()
                            .ghost()
                            .label("Send now")
                            .tooltip("Stop the current turn and send this prompt now")
                            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                                this.run(
                                    "session.sendNow",
                                    json!({"sessionId": this.session_id, "itemId": now}),
                                    cx,
                                )
                            })),
                    )
                    .when(steer, |d| {
                        d.child(
                            Button::new(SharedString::from(format!("steer-{}", it.id)))
                                .xsmall()
                                .ghost()
                                .label("Steer")
                                .tooltip("Add this prompt to what the agent is doing, without stopping it")
                                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                                    this.run(
                                        "session.steer",
                                        json!({"sessionId": this.session_id, "itemId": steer_id}),
                                        cx,
                                    )
                                })),
                        )
                    })
                    .child(
                        Button::new(SharedString::from(format!("cancel-{}", it.id)))
                            .xsmall()
                            .ghost()
                            .label("Cancel")
                            .tooltip("Don't send this prompt")
                            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                                this.run(
                                    "session.cancelQueued",
                                    json!({"sessionId": this.session_id, "itemId": cancel}),
                                    cx,
                                )
                            })),
                    ),
            );
        } else if it.status == ItemStatus::Cancelled {
            col = col.child(caption("Not sent", p));
        }
        col.into_any_element()
    }

    /// Steering a prompt into the running turn: for agents that take it, and
    /// never a slash command.
    pub(crate) fn can_steer(&self, text: &str, cx: &App) -> bool {
        let conn = self.conn.read(cx);
        let running = self.is_running(cx);
        let steers = conn
            .session(&self.session_id)
            .and_then(|s| conn.harness(&s.harness).map(|h| h.caps.steer))
            .unwrap_or(false);
        running && steers && !text.trim_start().starts_with('/')
    }

    fn render_reply(&mut self, it: &Rc<Item>, text: &str, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let size = prefs(cx).message_size;
        let mut col = v_flex().py_1().gap_2().text_size(px(size));
        for (i, seg) in split_reply(text).into_iter().enumerate() {
            match seg {
                Segment::Text(t) => {
                    if !t.trim().is_empty() {
                        col = col.child(markdown(format!("{}-{i}", it.id), &t));
                    }
                }
                Segment::Images(ids) => {
                    let refs: Vec<ImageRef> = ids
                        .iter()
                        .map(|id| {
                            it.images.iter().find(|r| &r.id == id).cloned().unwrap_or(ImageRef {
                                id: id.clone(),
                                mime_type: String::new(),
                                width: None,
                                height: None,
                                size: 0,
                            })
                        })
                        .collect();
                    if let Some(g) = self.gallery(&format!("{}-{i}", it.id), &refs, 240., false, p, cx) {
                        col = col.child(g);
                    }
                }
            }
        }
        col.into_any_element()
    }

    fn render_run(&mut self, run: &Rc<ToolRun>, expanded: bool, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let id = run.id.clone();
        let title = if run.working() {
            format!("{}…", run.title())
        } else {
            run.title()
        };
        disclosure(format!("run-{}", run.id), expanded, &title, p)
            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.toggle_run(id.clone(), expanded, cx)))
            .into_any_element()
    }

    fn render_tool(&mut self, it: &Rc<Item>, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let Some(tool) = &it.tool else {
            return div().into_any_element();
        };
        let open = self.open_items.contains(&it.id);
        let title = tool
            .title
            .clone()
            .filter(|t| !t.is_empty())
            .unwrap_or_else(|| tool.name.clone());
        let (status_text, status_color) = match it.status {
            ItemStatus::InProgress => (Some("running".to_string()), p.blue),
            ItemStatus::Failed => (Some("failed".to_string()), p.red),
            ItemStatus::Pending => (Some("waiting for you".to_string()), p.yellow),
            _ => match tool.exit_code {
                Some(code) if code != 0 => (Some(format!("exit {code}")), p.red),
                _ => (None, p.overlay),
            },
        };
        let children: Vec<Rc<Item>> = self
            .store(cx)
            .map(|s| s.children_of(&it.id).to_vec())
            .unwrap_or_default();
        let id = it.id.clone();
        let size = prefs(cx).message_size;
        let images = self.gallery(&it.id, &it.images, 160., false, p, cx);
        let mut body = v_flex().ml_6().mt_1().gap_1();
        if open {
            // The row itself shows a command that fits on its line.
            let said = tool.input.as_ref().and_then(tool_command).is_some_and(|c| c == title);
            if let Some(input) = tool.input.as_ref().filter(|v| !v.is_null() && !said) {
                body = body.child(input_preview(&tool.name, input, p));
            }
            if let Some(out) = tool.output.as_ref().filter(|o| !o.trim().is_empty()) {
                let lines: Vec<&str> = out.trim_end().lines().collect();
                let full = self.full_outputs.contains(&it.id) || lines.len() <= OUTPUT_LINES;
                let shown = if full {
                    lines.join("\n")
                } else {
                    lines[..OUTPUT_LINES].join("\n")
                };
                let title = match tool.exit_code {
                    Some(c) => format!("Output (exit {c})"),
                    None => "Output".into(),
                };
                let id2 = it.id.clone();
                body = body.child(code_block(Some(&title), &shown, p).when(!full, |d| {
                    d.child(
                        Button::new(SharedString::from(format!("all-{}", it.id)))
                            .xsmall()
                            .link()
                            .label(format!("Show all {} lines", lines.len()))
                            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                                this.full_outputs.insert(id2.clone());
                                this.refresh(cx);
                            })),
                    )
                }));
            }
            for child in &children {
                body = body.child(self.render_child(child, p));
            }
        }
        v_flex()
            .text_size(px(size))
            .child(
                h_flex()
                    .id(SharedString::from(format!("tool-{}", it.id)))
                    .gap_2()
                    .py_0p5()
                    .cursor_pointer()
                    .text_sm()
                    .text_color(p.subtext)
                    .child(Icon::new(tool_icon(tool.kind)).small().text_color(p.overlay))
                    .child(div().min_w_0().truncate().child(title))
                    .when(!children.is_empty() && !open, |d| {
                        d.child(caption(
                            if children.len() == 1 {
                                "1 sub-agent step".to_string()
                            } else {
                                format!("{} sub-agent steps", children.len())
                            },
                            p,
                        ))
                    })
                    .when_some(status_text, |d, s| {
                        d.child(div().flex_shrink_0().text_xs().text_color(status_color).child(s))
                    })
                    // Beside the title, not across the window from it.
                    .child(
                        Icon::new(if open {
                            IconName::ChevronDown
                        } else {
                            IconName::ChevronRight
                        })
                        .xsmall()
                        .text_color(p.overlay),
                    )
                    .child(div().flex_1())
                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.toggle_item(id.clone(), cx))),
            )
            .when(open, |d| d.child(body))
            .when_some(images, |d, g| d.child(div().ml_6().mt_1().child(g)))
            .into_any_element()
    }

    /// A sub-agent's step, inside its tool call.
    fn render_child(&self, it: &Item, p: &Palette) -> AnyElement {
        let text = match it.kind {
            ItemKind::ToolCall => it
                .tool
                .as_ref()
                .map(|t| {
                    t.title
                        .clone()
                        .filter(|s| !s.is_empty())
                        .unwrap_or_else(|| t.name.clone())
                })
                .unwrap_or_default(),
            _ => it.text.clone().unwrap_or_default(),
        };
        let icon = match (&it.kind, &it.tool) {
            (ItemKind::ToolCall, Some(t)) => tool_icon(t.kind),
            (ItemKind::Reasoning, _) => L::Brain.into(),
            _ => L::MessageSquare.into(),
        };
        h_flex()
            .gap_2()
            .pl_2()
            .border_l_2()
            .border_color(p.surface0)
            .text_xs()
            .text_color(p.subtext)
            .child(Icon::new(icon).xsmall().text_color(p.overlay))
            .child(div().flex_1().min_w_0().line_clamp(2).child(text))
            .into_any_element()
    }

    fn render_approval(
        &mut self,
        it: &Rc<Item>,
        p: &Palette,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let Some(a) = &it.approval else {
            return div().into_any_element();
        };
        if it.status != ItemStatus::Pending {
            return render_decided(it, a, p);
        }
        let question = a.special.as_deref() == Some("question");
        let mut card = v_flex()
            .gap_2()
            .p_3()
            .rounded_lg()
            .border_1()
            .border_color(p.yellow.opacity(0.7))
            .bg(p.mantle)
            .child(
                h_flex()
                    .gap_2()
                    .child(
                        if question {
                            Icon::new(L::CircleQuestionMark)
                        } else {
                            Icon::new(IconName::TriangleAlert)
                        }
                        .small()
                        .text_color(p.yellow),
                    )
                    .child(div().text_sm().font_weight(FontWeight::SEMIBOLD).child(a.title.clone())),
            )
            .when_some(a.detail.clone().filter(|d| !d.is_empty()), |d, detail| {
                d.child(div().text_sm().text_color(p.subtext).child(detail))
            });
        match a.special.as_deref() {
            Some("plan") => {
                if let Some(plan) = &a.plan_text {
                    card = card.child(
                        div()
                            .p_2()
                            .rounded_md()
                            .bg(p.base)
                            .child(markdown(format!("plan-{}", it.id), plan)),
                    );
                }
            }
            Some("question") => return self.render_questions(it, a, card, p, window, cx),
            _ => {
                if let Some(input) = a.input.as_ref().filter(|v| !v.is_null()) {
                    card = card.child(input_preview(a.tool_name.as_deref().unwrap_or("Input"), input, p));
                }
            }
        }
        // Under what they answer, the first to allow it the default.
        let mut buttons = h_flex().gap_2().flex_wrap();
        let mut allowed = false;
        for opt in &a.options {
            let (approval, option) = (it.id.clone(), opt.id.clone());
            let deny = opt.kind == OptionKind::Deny;
            let button = Button::new(SharedString::from(format!("opt-{}-{}", it.id, opt.id)))
                .small()
                .label(opt.label.clone())
                .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| {
                    if deny {
                        this.deny_with_reason(approval.clone(), option.clone(), window, cx);
                    } else {
                        this.respond(&approval, &option, None, None, cx);
                    }
                }));
            buttons = buttons.child(if deny {
                button.danger()
            } else if !std::mem::replace(&mut allowed, true) {
                button.primary()
            } else {
                button.outline()
            });
        }
        card.child(buttons).into_any_element()
    }

    fn render_questions(
        &mut self,
        it: &Rc<Item>,
        a: &Approval,
        mut card: Div,
        p: &Palette,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let count = a.questions.len();
        let chosen = self
            .answers
            .get(&it.id)
            .cloned()
            .unwrap_or_else(|| vec![BTreeSet::new(); count]);
        let mut answers: BTreeMap<String, String> = BTreeMap::new();
        let mut complete = true;
        for (qi, q) in a.questions.iter().enumerate() {
            let mut options = h_flex().gap_1().flex_wrap();
            for (oi, opt) in q.options.iter().enumerate() {
                let selected = chosen.get(qi).is_some_and(|s| s.contains(&oi));
                let (approval, multi) = (it.id.clone(), q.multi_select);
                let mut b = Button::new(SharedString::from(format!("q-{}-{qi}-{oi}", it.id)))
                    .small()
                    .label(opt.label.clone())
                    .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| {
                        this.choose_answer(&approval, qi, oi, multi, count, window, cx)
                    }));
                if let Some(d) = &opt.description {
                    b = b.tooltip(d.clone());
                }
                options = options.child(if selected { b.primary() } else { b.outline() });
            }
            let own = (!q.options_only).then(|| {
                let placeholder = if q.options.is_empty() {
                    "Your answer…"
                } else {
                    "Other…"
                };
                self.answer_input(&it.id, qi, placeholder, window, cx)
            });
            let typed = own
                .as_ref()
                .map(|i| i.read(cx).value().trim().to_string())
                .unwrap_or_default();
            let mut labels: Vec<String> = chosen
                .get(qi)
                .map(|s| {
                    s.iter()
                        .filter_map(|&oi| q.options.get(oi).map(|o| o.label.clone()))
                        .collect()
                })
                .unwrap_or_default();
            if !typed.is_empty() {
                if q.multi_select {
                    labels.push(typed);
                } else {
                    labels = vec![typed];
                }
            }
            if labels.is_empty() {
                complete = false;
            }
            answers.insert(q.question.clone(), labels.join(", "));
            card = card.child(
                v_flex()
                    .gap_1()
                    .when_some(q.header.clone(), |d, h| d.child(caption(h, p)))
                    .child(div().text_sm().child(q.question.clone()))
                    .when(!q.options.is_empty(), |d| d.child(options))
                    .when_some(own, |d, input| {
                        d.child(div().w_full().max_w(px(480.)).child(Input::new(&input).small()))
                    }),
            );
        }
        let mut buttons = h_flex().gap_2().flex_wrap();
        for opt in &a.options {
            let (approval, option) = (it.id.clone(), opt.id.clone());
            if opt.kind == OptionKind::Deny {
                buttons = buttons.child(
                    Button::new(SharedString::from(format!("deny-{}", it.id)))
                        .small()
                        .outline()
                        .label(opt.label.clone())
                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                            this.respond(&approval, &option, None, None, cx)
                        })),
                );
            } else {
                let answers = answers.clone();
                buttons = buttons.child(
                    Button::new(SharedString::from(format!("answer-{}-{}", it.id, opt.id)))
                        .small()
                        .primary()
                        .label(if opt.kind == OptionKind::AllowOnce {
                            "Answer".to_string()
                        } else {
                            opt.label.clone()
                        })
                        .disabled(!complete)
                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                            this.respond(&approval, &option, Some(answers.clone()), None, cx)
                        })),
                );
            }
        }
        card.child(buttons).into_any_element()
    }

    fn render_draft(&mut self, text: &str, images: &[ImageRef], p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let archived = self.session(cx).is_some_and(|s| s.archived);
        let blocked = self.read_only(cx) || self.unarchiving;
        let gallery = self.gallery("draft", images, 120., true, p, cx);
        v_flex()
            .items_end()
            .pl(px(40.))
            .gap_1()
            .when_some(gallery, |d, g| d.child(g))
            .child(
                v_flex()
                    .gap_1()
                    .px_3()
                    .py_2()
                    .rounded_lg()
                    .border_1()
                    .border_dashed()
                    .border_color(p.accent)
                    .bg(p.accent.opacity(0.12))
                    .when(!text.is_empty(), |d| d.child(div().text_sm().child(text.to_string())))
                    .child(
                        h_flex()
                            .gap_1()
                            .child(Icon::new(L::Pencil).xsmall().text_color(p.overlay))
                            .child(caption("Draft · not sent", p)),
                    ),
            )
            .child(
                h_flex()
                    .gap_1()
                    .child(
                        Button::new("edit-draft")
                            .xsmall()
                            .ghost()
                            .label("Edit")
                            .tooltip("Take the draft back into the prompt to change it")
                            .disabled(archived || blocked)
                            .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.edit_draft(window, cx))),
                    )
                    .child(
                        Button::new("send-draft")
                            .xsmall()
                            .primary()
                            .label("Send")
                            .disabled(blocked)
                            .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.send_draft(window, cx))),
                    ),
            )
            .into_any_element()
    }

    /// A row of images, from rad or just attached; a click opens them.
    pub(crate) fn gallery(
        &mut self,
        key: &str,
        refs: &[ImageRef],
        height: f32,
        trailing: bool,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> Option<AnyElement> {
        if refs.is_empty() {
            return None;
        }
        let mut row = h_flex().gap_1p5().flex_wrap().when(trailing, |d| d.justify_end());
        for (i, r) in refs.iter().enumerate() {
            let loaded = self.conn.update(cx, |c, cx| c.image(&r.id, cx));
            let aspect = match (r.width, r.height) {
                (Some(w), Some(h)) if h > 0 => (w as f32 / h as f32).clamp(0.5, 2.),
                _ => match &loaded {
                    Loaded::Ready(_) => 1.33,
                    _ => 1.,
                },
            };
            let frame = div()
                .id(SharedString::from(format!("img-{key}-{i}")))
                .h(px(height))
                .w(px(height * aspect))
                .rounded_md()
                .overflow_hidden()
                .bg(p.crust)
                .cursor_pointer();
            let refs_all = refs.to_vec();
            let conn = self.conn.clone();
            let frame = frame.on_click(move |_: &ClickEvent, window, cx| {
                crate::ui::viewer::open(conn.clone(), refs_all.clone(), i, window, cx)
            });
            row = row.child(match loaded {
                Loaded::Ready(image) => frame
                    .child(img(image).size_full().object_fit(ObjectFit::Cover))
                    .into_any_element(),
                Loaded::Loading => frame
                    .flex()
                    .items_center()
                    .justify_center()
                    .child(Spinner::new().small())
                    .into_any_element(),
                Loaded::Failed(_) => frame
                    .flex()
                    .items_center()
                    .justify_center()
                    .child(Icon::new(L::ImageOff).text_color(p.overlay))
                    .into_any_element(),
            });
        }
        Some(row.into_any_element())
    }
}

/// A reply's Markdown, cut where a paragraph is only images rad keeps
/// (`rad-image:<id>`), which show as images between the texts. Such images
/// within a line of text show after its paragraph.
pub(crate) enum Segment {
    Text(String),
    Images(Vec<String>),
}

pub(crate) fn split_reply(text: &str) -> Vec<Segment> {
    let mut out: Vec<Segment> = Vec::new();
    let mut para: Vec<&str> = Vec::new();
    let mut text_acc = String::new();
    let mut in_fence = false;
    let flush = |para: &mut Vec<&str>, text_acc: &mut String, out: &mut Vec<Segment>| {
        if para.is_empty() {
            return;
        }
        let joined = para.join("\n");
        para.clear();
        let (rest, ids) = take_rad_images(&joined);
        if ids.is_empty() {
            text_acc.push_str(&joined);
            text_acc.push_str("\n\n");
            return;
        }
        if !rest.trim().is_empty() {
            text_acc.push_str(&rest);
            text_acc.push_str("\n\n");
        }
        if !text_acc.trim().is_empty() {
            out.push(Segment::Text(std::mem::take(text_acc)));
        } else {
            text_acc.clear();
        }
        out.push(Segment::Images(ids));
    };
    for line in text.lines() {
        let fence = line.trim_start().starts_with("```") || line.trim_start().starts_with("~~~");
        if in_fence {
            text_acc.push_str(line);
            text_acc.push('\n');
            if fence {
                in_fence = false;
                text_acc.push('\n');
            }
            continue;
        }
        if fence {
            flush(&mut para, &mut text_acc, &mut out);
            in_fence = true;
            text_acc.push_str(line);
            text_acc.push('\n');
            continue;
        }
        if line.trim().is_empty() {
            flush(&mut para, &mut text_acc, &mut out);
        } else {
            para.push(line);
        }
    }
    flush(&mut para, &mut text_acc, &mut out);
    if !text_acc.trim().is_empty() {
        out.push(Segment::Text(text_acc));
    }
    out
}

/// The text without its `![…](rad-image:<id>)` images, and their ids.
fn take_rad_images(text: &str) -> (String, Vec<String>) {
    let mut rest = String::new();
    let mut ids = Vec::new();
    let mut s = text;
    while let Some(start) = s.find("![") {
        let after = &s[start..];
        let Some(close) = after.find("](rad-image:") else { break };
        // The alt text mustn't span a link of its own.
        if after[2..close].contains(']') {
            rest.push_str(&s[..start + 2]);
            s = &s[start + 2..];
            continue;
        }
        let id_start = close + "](rad-image:".len();
        let Some(end) = after[id_start..].find(')') else { break };
        let id = after[id_start..id_start + end]
            .split_whitespace()
            .next()
            .unwrap_or("")
            .to_string();
        rest.push_str(&s[..start]);
        if !id.is_empty() {
            ids.push(id);
        }
        s = &after[id_start + end + 1..];
    }
    rest.push_str(s);
    (rest, ids)
}

/// A tool's input as a reviewer wants it: the command, a mini diff for
/// edits, the content for writes, JSON otherwise.
pub(crate) fn input_preview(name: &str, input: &Value, p: &Palette) -> AnyElement {
    if let Some(cmd) = tool_command(input) {
        return code_block(Some("Command"), &cmd, p).into_any_element();
    }
    let pair = |v: &Value| -> Option<(String, String)> {
        Some((
            v.get("old_string")?.as_str()?.to_string(),
            v.get("new_string")?.as_str()?.to_string(),
        ))
    };
    let edits: Vec<(String, String)> = if let Some(e) = pair(input) {
        vec![e]
    } else if let Some(arr) = input.get("edits").and_then(Value::as_array) {
        arr.iter().filter_map(pair).collect()
    } else {
        Vec::new()
    };
    if !edits.is_empty() {
        let lines: Vec<(String, bool)> = edits
            .iter()
            .flat_map(|(old, new)| {
                old.split('\n')
                    .map(|l| (format!("- {l}"), false))
                    .chain(new.split('\n').map(|l| (format!("+ {l}"), true)))
                    .collect::<Vec<_>>()
            })
            .collect();
        let mut col = v_flex().w_full().p_1p5().rounded_md().bg(p.crust).child(
            div()
                .text_xs()
                .font_weight(FontWeight::SEMIBOLD)
                .text_color(p.overlay)
                .pb_1()
                .child("Change"),
        );
        for (text, added) in lines.iter().take(60) {
            col = col.child(
                div()
                    .w_full()
                    .text_size(px(crate::ui::fonts::code_size()))
                    .font_family(crate::ui::fonts::code())
                    .bg(if *added {
                        p.green.opacity(0.15)
                    } else {
                        p.red.opacity(0.15)
                    })
                    .child(if text.is_empty() { " ".to_string() } else { text.clone() }),
            );
        }
        if lines.len() > 60 {
            col = col.child(caption(format!("… {} more lines", lines.len() - 60), p));
        }
        return col.into_any_element();
    }
    if let Some(content) = input.get("content").and_then(Value::as_str) {
        return code_block(Some("New content"), &last_lines(content, 40), p).into_any_element();
    }
    let pretty = serde_json::to_string_pretty(input).unwrap_or_default();
    code_block(Some(name), &last_lines(&pretty, 40), p).into_any_element()
}

/// The user's prompt, selectable like the agent's replies.
pub(crate) fn user_bubble(id: &str, text: &str, p: &Palette) -> Div {
    v_flex()
        .font_family(crate::ui::fonts::message())
        .gap_1()
        .px_3()
        .py_2()
        .rounded_lg()
        .bg(p.user_bubble)
        .child(div().text_sm().child(gpui_kit::base::SelectableText::new(
            SharedString::from(format!("prompt-{id}")),
            text.to_string(),
        )))
}

pub(crate) fn tool_icon(kind: ToolKind) -> gpui_kit::component::Icon {
    match kind {
        ToolKind::Execute => Icon::new(L::Terminal),
        ToolKind::Edit => Icon::new(L::Pencil),
        ToolKind::Read => Icon::new(L::FileText),
        ToolKind::Search => Icon::new(L::Search),
        ToolKind::Fetch => Icon::new(L::Globe),
        ToolKind::Think => Icon::new(L::Users),
        ToolKind::Other => Icon::new(L::Wrench),
    }
}

fn last_lines(text: &str, n: usize) -> String {
    let lines: Vec<&str> = text.trim_end().lines().collect();
    if lines.len() <= n {
        return lines.join("\n");
    }
    format!("{}\n… {} more lines", lines[..n].join("\n"), lines.len() - n)
}

fn render_plan(it: &Item, p: &Palette) -> AnyElement {
    let Some(plan) = &it.plan else {
        return div().into_any_element();
    };
    let mut col = v_flex()
        .gap_0p5()
        .p_3()
        .rounded_lg()
        .bg(p.mantle)
        .child(div().text_sm().font_weight(FontWeight::SEMIBOLD).child("Plan"));
    if let Some(text) = plan.text.as_ref().filter(|t| !t.is_empty()) {
        col = col.child(markdown(it.id.clone(), text));
    }
    for e in &plan.entries {
        let (icon, color, done): (Icon, Hsla, bool) = match e.status.as_str() {
            "completed" => (Icon::new(IconName::CircleCheck), p.green, true),
            "in_progress" => (Icon::new(L::CircleDashed), p.peach, false),
            _ => (Icon::new(L::Circle), p.overlay, false),
        };
        col = col.child(
            h_flex()
                .gap_2()
                .items_start()
                .text_sm()
                .child(icon.small().text_color(color))
                .child(
                    div()
                        .flex_1()
                        .when(done, |d| d.line_through().text_color(p.subtext))
                        .child(e.content.clone()),
                ),
        );
    }
    col.into_any_element()
}

fn render_decided(it: &Item, a: &Approval, p: &Palette) -> AnyElement {
    let chosen = a
        .decision
        .as_ref()
        .and_then(|d| a.options.iter().find(|o| o.id == d.option_id));
    let (icon, color, verb) = match (it.status, chosen) {
        (ItemStatus::Resolved, Some(o)) => (
            if o.kind == OptionKind::Deny {
                IconName::CircleX
            } else {
                IconName::CircleCheck
            },
            if o.kind == OptionKind::Deny { p.red } else { p.green },
            o.label.clone(),
        ),
        (ItemStatus::Resolved, None) => (IconName::CircleCheck, p.green, "Allowed".into()),
        (ItemStatus::Expired, _) => (IconName::CircleX, p.overlay, "Expired".into()),
        _ => (IconName::CircleX, p.overlay, "Cancelled".into()),
    };
    let mut col = v_flex().gap_0p5().child(
        h_flex()
            .gap_2()
            .text_sm()
            .text_color(p.subtext)
            .child(Icon::new(icon).small().text_color(color))
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .truncate()
                    .child(format!("{verb}: {}", a.title)),
            ),
    );
    if let Some(d) = &a.decision {
        for (q, ans) in &d.answers {
            col = col.child(caption(format!("{q} → {ans}"), p).pl_6());
        }
        if let Some(m) = d.message.as_ref().filter(|m| !m.is_empty()) {
            col = col.child(caption(format!("“{m}”"), p).pl_6());
        }
    }
    col.into_any_element()
}

/// How a turn ended, when not well; `told` when an error above says why.
fn render_turn_end(t: &Turn, told: bool, p: &Palette) -> AnyElement {
    let failed = t.status == TurnStatus::Failed;
    let text = if failed {
        match &t.error {
            Some(e) if !told => format!("Failed: {e}"),
            _ => "Failed".to_string(),
        }
    } else {
        "Interrupted".to_string()
    };
    let color = if failed { p.red } else { p.overlay };
    h_flex()
        .w_full()
        .justify_center()
        .gap_1p5()
        .py_1()
        .text_xs()
        .text_color(color)
        .child(
            Icon::new(if failed { L::OctagonX } else { L::Square })
                .xsmall()
                .text_color(color),
        )
        .child(text)
        .into_any_element()
}

fn render_background(tasks: &[BackgroundTask], p: &Palette) -> AnyElement {
    let mut col = v_flex().gap_1().py_1().child(
        h_flex()
            .gap_2()
            .text_sm()
            .text_color(p.subtext)
            .child(Spinner::new().small())
            .child("Running in the background"),
    );
    for t in tasks {
        let icon = match t.kind.as_str() {
            "command" => L::Terminal,
            "agent" => L::Users,
            _ => L::CircleDashed,
        };
        col = col.child(
            h_flex()
                .gap_2()
                .pl_6()
                .text_xs()
                .text_color(p.subtext)
                .child(Icon::new(icon).xsmall().text_color(p.overlay))
                .child(div().flex_1().line_clamp(2).child(t.description.clone())),
        );
    }
    col.into_any_element()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn splits_replies_at_their_images() {
        let text = "Here it is:\n\n![Shot](rad-image:abc.png)\n\nAnd inline ![x](rad-image:def.png) too.\n\n```\n![no](rad-image:zzz.png)\n```\n";
        let segs = split_reply(text);
        let kinds: Vec<String> = segs
            .iter()
            .map(|s| match s {
                Segment::Text(t) => format!("T:{}", t.trim()),
                Segment::Images(ids) => format!("I:{}", ids.join(",")),
            })
            .collect();
        assert_eq!(kinds[0], "T:Here it is:");
        assert_eq!(kinds[1], "I:abc.png");
        assert_eq!(kinds[2], "T:And inline  too.");
        assert_eq!(kinds[3], "I:def.png");
        assert!(kinds[4].contains("rad-image:zzz.png"), "code stays as it is: {kinds:?}");
        assert_eq!(take_rad_images("![a](https://x/y.png)").1.len(), 0);
    }
}
