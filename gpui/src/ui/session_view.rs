//! One session: its header, the transcript (with runs of tool calls folded,
//! as in the other clients), the approvals waiting in it, and the composer.

use std::collections::{BTreeMap, BTreeSet, HashMap, HashSet};
use std::hash::{DefaultHasher, Hash, Hasher};
use std::rc::Rc;

use gpui_kit::component::button::{Button, ButtonVariants as _};
use gpui_kit::component::input::{InputEvent, Textarea, TextareaState};
use gpui_kit::component::text::{MarkdownExtensions, TextView};
use gpui_kit::component::{Disableable as _, Icon, IconName, Sizable as _, h_flex, v_flex};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::{
    AnyElement, App, AppContext as _, ClickEvent, Context, ElementId, Entity, FontWeight, InteractiveElement as _,
    IntoElement, ListAlignment, ListState, ParentElement as _, Render, SharedString, StatefulInteractiveElement as _,
    Styled as _, Subscription, Window, div, list, px, rems,
};
use serde_json::{Value, json};

use crate::conn::ServerConn;
use crate::protocol::{
    Approval, Item, ItemKind, ItemStatus, OptionKind, Session, SessionStatus, ToolKind, Turn, TurnStatus,
};
use crate::store::session::{Outgoing, SessionState};
use crate::store::tool_runs::{ToolRun, TranscriptRow, transcript_rows};
use crate::ui::palette::Palette;

/// A row of the transcript as this view draws it.
#[derive(Clone)]
enum Row {
    Item(Rc<Item>),
    Run { run: Rc<ToolRun>, expanded: bool },
    TurnEnd(Rc<Turn>),
    Outgoing(Outgoing),
    Draft(String),
}

/// What a tool call's output shows at most: its last lines.
const OUTPUT_LINES: usize = 80;

pub struct SessionView {
    conn: Entity<ServerConn>,
    session_id: String,
    list: ListState,
    rows: Vec<Row>,
    signatures: Vec<u64>,
    /// Runs the user opened or shut, by run id.
    expanded_runs: HashMap<String, bool>,
    /// Tool calls and reasoning the user opened.
    open_items: HashSet<String>,
    /// A question approval's chosen options: approval id → per question.
    answers: HashMap<String, Vec<BTreeSet<usize>>>,
    composer: Entity<TextareaState>,
    error: Option<String>,
    /// A markRead is on its way.
    marking_read: bool,
    closed: bool,
    _subscriptions: Vec<Subscription>,
}

impl SessionView {
    pub fn new(conn: Entity<ServerConn>, session_id: String, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let composer = cx.new(|cx| {
            TextareaState::new(window, cx)
                .placeholder("Message the agent… (Return sends, Shift-Return for a new line)")
                .auto_grow(1, 12)
                .submit_on_enter(true)
        });
        let subscriptions = vec![
            cx.observe_in(&conn, window, |this, _, window, cx| {
                this.refresh(cx);
                this.mark_read_if_shown(window, cx);
            }),
            cx.subscribe_in(&composer, window, |this, _, event, window, cx| {
                if let InputEvent::PressEnter { shift: false, .. } = event {
                    this.send(window, cx);
                }
            }),
        ];
        conn.update(cx, |c, cx| c.open_session(&session_id, cx));
        composer.update(cx, |c, cx| c.focus(window, cx));
        let mut this = Self {
            conn,
            session_id,
            list: ListState::new(0, ListAlignment::Bottom, px(2000.)),
            rows: Vec::new(),
            signatures: Vec::new(),
            expanded_runs: HashMap::new(),
            open_items: HashSet::new(),
            answers: HashMap::new(),
            composer,
            error: None,
            marking_read: false,
            closed: false,
            _subscriptions: subscriptions,
        };
        this.refresh(cx);
        this.mark_read_if_shown(window, cx);
        this
    }

    pub fn conn(&self) -> &Entity<ServerConn> {
        &self.conn
    }

    pub fn session_id(&self) -> &str {
        &self.session_id
    }

    /// The view goes away: its stream may stop.
    pub fn close(&mut self, cx: &mut Context<Self>) {
        if !self.closed {
            self.closed = true;
            let id = self.session_id.clone();
            self.conn.update(cx, |c, _| c.close_session(&id));
        }
    }

    fn session(&self, cx: &App) -> Option<Rc<Session>> {
        self.conn.read(cx).session(&self.session_id)
    }

    /// Tells rad the session was read, while it shows in a window in front.
    pub fn mark_read_if_shown(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(session) = self.session(cx) else { return };
        if !session.unread {
            self.marking_read = false;
            return;
        }
        if self.marking_read || self.closed || !window.is_window_active() {
            return;
        }
        self.marking_read = true;
        drop(
            self.conn
                .read(cx)
                .call("session.markRead", json!({"sessionId": self.session_id})),
        );
    }

    // MARK: Rows

    fn refresh(&mut self, cx: &mut Context<Self>) {
        let rows = {
            let conn = self.conn.read(cx);
            let Some(store) = conn.stores.get(&self.session_id) else {
                return;
            };
            build_rows(store, &self.expanded_runs)
        };
        let signatures: Vec<u64> = rows.iter().map(|r| self.signature(r)).collect();
        let old = self.signatures.len();
        let first = self
            .signatures
            .iter()
            .zip(&signatures)
            .position(|(a, b)| a != b)
            .unwrap_or(old.min(signatures.len()));
        if first < old || signatures.len() != old {
            self.list.splice(first..old, signatures.len() - first);
        }
        self.rows = rows;
        self.signatures = signatures;
        cx.notify();
    }

    /// What a row looks like, so that only the rows that changed are laid out again.
    fn signature(&self, row: &Row) -> u64 {
        let mut h = DefaultHasher::new();
        match row {
            Row::Item(it) => {
                ("item", &it.id, &it.updated_at, it.text.as_ref().map(String::len)).hash(&mut h);
                format!("{:?}", it.status).hash(&mut h);
                self.open_items.contains(&it.id).hash(&mut h);
                if let Some(a) = self.answers.get(&it.id) {
                    a.hash(&mut h);
                }
            }
            Row::Run { run, expanded } => {
                ("run", &run.id, expanded, run.items.len(), run.working()).hash(&mut h);
            }
            Row::TurnEnd(t) => ("end", &t.id, format!("{:?}", t.status)).hash(&mut h),
            Row::Outgoing(o) => ("out", &o.command_id).hash(&mut h),
            Row::Draft(text) => ("draft", text).hash(&mut h),
        }
        h.finish()
    }

    fn toggle_run(&mut self, id: String, expanded: bool, cx: &mut Context<Self>) {
        self.expanded_runs.insert(id, !expanded);
        self.refresh(cx);
    }

    fn toggle_item(&mut self, id: String, cx: &mut Context<Self>) {
        if !self.open_items.remove(&id) {
            self.open_items.insert(id);
        }
        self.refresh(cx);
    }

    // MARK: Commands

    fn run(&mut self, method: &'static str, params: Value, cx: &mut Context<Self>) {
        let call = self.conn.read(cx).call(method, params);
        cx.spawn(async move |this, cx| {
            if let Err(e) = call.await {
                let _ = this.update(cx, |this, cx| {
                    this.error = Some(e.to_string());
                    cx.notify();
                });
            }
        })
        .detach();
    }

    fn send(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let text = self.composer.read(cx).value().trim().to_string();
        if text.is_empty() {
            return;
        }
        self.composer.update(cx, |c, cx| c.set_value("", window, cx));
        self.error = None;
        let id = self.session_id.clone();
        let task = self.conn.update(cx, |c, cx| c.send_prompt(&id, text.clone(), cx));
        cx.spawn_in(window, async move |this, cx| {
            if let Err(e) = task.await {
                let _ = this.update_in(cx, |this, window, cx| {
                    // Give the prompt back, so it isn't lost.
                    if this.composer.read(cx).value().is_empty() {
                        this.composer.update(cx, |c, cx| c.set_value(text, window, cx));
                    }
                    this.error = Some(e.to_string());
                    cx.notify();
                });
            }
        })
        .detach();
    }

    fn interrupt(&mut self, cx: &mut Context<Self>) {
        self.run("session.interrupt", json!({"sessionId": self.session_id}), cx);
    }

    fn respond(
        &mut self,
        approval_id: &str,
        option_id: &str,
        answers: Option<BTreeMap<String, String>>,
        cx: &mut Context<Self>,
    ) {
        let mut params = json!({
            "commandId": uuid::Uuid::new_v4().to_string(),
            "sessionId": self.session_id,
            "approvalId": approval_id,
            "optionId": option_id,
        });
        if let Some(answers) = answers {
            params["answers"] = json!(answers);
        }
        self.run("approval.respond", params, cx);
    }

    fn choose_answer(
        &mut self,
        approval: &str,
        question: usize,
        option: usize,
        multi: bool,
        count: usize,
        cx: &mut Context<Self>,
    ) {
        let chosen = self
            .answers
            .entry(approval.to_string())
            .or_insert_with(|| vec![BTreeSet::new(); count]);
        if chosen.len() < count {
            chosen.resize(count, BTreeSet::new());
        }
        let set = &mut chosen[question];
        if multi {
            if !set.remove(&option) {
                set.insert(option);
            }
        } else {
            set.clear();
            set.insert(option);
        }
        self.refresh(cx);
    }

    // MARK: Drawing

    fn render_row(&mut self, ix: usize, window: &mut Window, cx: &mut Context<Self>) -> AnyElement {
        let Some(row) = self.rows.get(ix).cloned() else {
            return div().into_any_element();
        };
        let p = Palette::of(cx);
        let body = match row {
            Row::Item(it) => self.render_item(&it, &p, window, cx),
            Row::Run { run, expanded } => self.render_run(&run, expanded, &p, cx),
            Row::TurnEnd(t) => render_turn_end(&t, &p),
            Row::Outgoing(o) => trailing(user_bubble(&o.text, &p).child(caption("Sending…", &p))),
            Row::Draft(text) => self.render_draft(&text, &p, cx),
        };
        div()
            .w_full()
            .px_5()
            .py_1()
            .child(div().max_w(rems(52.)).mx_auto().child(body))
            .into_any_element()
    }

    fn render_item(&mut self, it: &Rc<Item>, p: &Palette, _: &mut Window, cx: &mut Context<Self>) -> AnyElement {
        let text = it.text.clone().unwrap_or_default();
        match it.kind {
            ItemKind::UserMessage => {
                let mut bubble = user_bubble(&text, p);
                if !it.images.is_empty() {
                    bubble = bubble.child(caption(&format!("{} image(s) attached", it.images.len()), p));
                }
                if it.status == ItemStatus::Queued {
                    let (now, cancel) = (it.id.clone(), it.id.clone());
                    bubble = bubble.child(
                        h_flex()
                            .gap_1()
                            .justify_end()
                            .child(caption("Queued", p))
                            .child(
                                Button::new(SharedString::from(format!("now-{}", it.id)))
                                    .xsmall()
                                    .ghost()
                                    .label("Send now")
                                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                                        this.run(
                                            "session.sendNow",
                                            json!({"sessionId": this.session_id, "itemId": now}),
                                            cx,
                                        )
                                    })),
                            )
                            .child(
                                Button::new(SharedString::from(format!("cancel-{}", it.id)))
                                    .xsmall()
                                    .ghost()
                                    .label("Cancel")
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
                    bubble = bubble.child(caption("Cancelled", p));
                }
                trailing(bubble)
            }
            ItemKind::AssistantMessage => div()
                .py_1()
                .child(markdown(&it.id, &text))
                .when(!it.images.is_empty(), |d| {
                    d.child(caption(&format!("{} image(s)", it.images.len()), p))
                })
                .into_any_element(),
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
            ItemKind::Approval => self.render_approval(it, p, cx),
            ItemKind::Plan => render_plan(it, p),
            ItemKind::Notice => div()
                .w_full()
                .py_1()
                .text_xs()
                .text_center()
                .text_color(p.overlay)
                .child(text)
                .into_any_element(),
            ItemKind::Error => {
                let limit = it.limit.as_ref().map(|w| format!("{} limit reached", w.label));
                v_flex()
                    .gap_1()
                    .px_3()
                    .py_2()
                    .rounded_md()
                    .border_1()
                    .border_color(p.red.opacity(0.5))
                    .bg(p.red.opacity(0.08))
                    .when_some(limit, |d, l| {
                        d.child(
                            div()
                                .text_sm()
                                .font_weight(FontWeight::SEMIBOLD)
                                .text_color(p.red)
                                .child(l),
                        )
                    })
                    .child(div().text_sm().text_color(p.red).child(text))
                    .into_any_element()
            }
            ItemKind::Unknown => div().into_any_element(),
        }
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
        let children = self
            .conn
            .read(cx)
            .stores
            .get(&self.session_id)
            .map(|s| s.children_of(&it.id).len())
            .unwrap_or(0);
        let id = it.id.clone();
        let command = tool.input.as_ref().and_then(command_of);
        let input = tool.input.as_ref().filter(|_| command.is_none()).map(|v| {
            let s = serde_json::to_string_pretty(v).unwrap_or_default();
            last_lines(&s, 40)
        });
        let output = tool
            .output
            .as_ref()
            .filter(|o| !o.trim().is_empty())
            .map(|o| last_lines(o, OUTPUT_LINES));
        v_flex()
            .child(
                h_flex()
                    .id(SharedString::from(format!("tool-{}", it.id)))
                    .gap_2()
                    .py_0p5()
                    .cursor_pointer()
                    .text_sm()
                    .text_color(p.subtext)
                    .child(Icon::new(tool_icon(tool.kind)).small().text_color(p.overlay))
                    .child(div().flex_1().min_w_0().truncate().child(title))
                    .when(children > 0, |d| d.child(caption(&format!("{children} steps"), p)))
                    .when_some(status_text, |d, s| {
                        d.child(div().text_xs().text_color(status_color).child(s))
                    })
                    .child(
                        Icon::new(if open {
                            IconName::ChevronDown
                        } else {
                            IconName::ChevronRight
                        })
                        .xsmall()
                        .text_color(p.overlay),
                    )
                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.toggle_item(id.clone(), cx))),
            )
            .when(open, |d| {
                d.child(
                    v_flex()
                        .ml_6()
                        .mt_1()
                        .gap_1()
                        .when_some(command, |d, c| d.child(code_block(&format!("$ {c}"), p)))
                        .when_some(input, |d, i| d.child(code_block(&i, p)))
                        .when_some(output, |d, o| d.child(code_block(&o, p)))
                        .when(!it.images.is_empty(), |d| {
                            d.child(caption(&format!("{} image(s)", it.images.len()), p))
                        }),
                )
            })
            .into_any_element()
    }

    fn render_approval(&mut self, it: &Rc<Item>, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let Some(a) = &it.approval else {
            return div().into_any_element();
        };
        if it.status != ItemStatus::Pending {
            return render_decided(it, a, p);
        }
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
                    .child(Icon::new(IconName::TriangleAlert).small().text_color(p.yellow))
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
                            .child(markdown(&format!("plan-{}", it.id), plan)),
                    );
                }
            }
            Some("question") => return self.render_questions(it, a, card, p, cx),
            _ => {
                let command = a.input.as_ref().and_then(command_of);
                if let Some(c) = command {
                    card = card.child(code_block(&format!("$ {c}"), p));
                } else if let Some(input) = &a.input
                    && !input.is_null()
                {
                    card = card.child(code_block(
                        &last_lines(&serde_json::to_string_pretty(input).unwrap_or_default(), 30),
                        p,
                    ));
                }
            }
        }
        let mut buttons = h_flex().gap_2().flex_wrap().justify_end();
        for opt in &a.options {
            let (approval, option) = (it.id.clone(), opt.id.clone());
            let button = Button::new(SharedString::from(format!("opt-{}-{}", it.id, opt.id)))
                .small()
                .label(opt.label.clone())
                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.respond(&approval, &option, None, cx)));
            buttons = buttons.child(match opt.kind {
                OptionKind::AllowOnce => button.primary(),
                OptionKind::Deny => button.danger(),
                _ => button.outline(),
            });
        }
        card.child(buttons).into_any_element()
    }

    fn render_questions(
        &mut self,
        it: &Rc<Item>,
        a: &Approval,
        mut card: gpui_kit::Div,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let count = a.questions.len();
        let chosen = self
            .answers
            .get(&it.id)
            .cloned()
            .unwrap_or_else(|| vec![BTreeSet::new(); count]);
        for (qi, q) in a.questions.iter().enumerate() {
            let mut options = h_flex().gap_1().flex_wrap();
            for (oi, opt) in q.options.iter().enumerate() {
                let selected = chosen.get(qi).is_some_and(|s| s.contains(&oi));
                let (approval, multi) = (it.id.clone(), q.multi_select);
                let mut b = Button::new(SharedString::from(format!("q-{}-{qi}-{oi}", it.id)))
                    .small()
                    .label(opt.label.clone())
                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                        this.choose_answer(&approval, qi, oi, multi, count, cx)
                    }));
                if let Some(d) = &opt.description {
                    b = b.tooltip(d.clone());
                }
                options = options.child(if selected { b.primary() } else { b.outline() });
            }
            card = card.child(
                v_flex()
                    .gap_1()
                    .when_some(q.header.clone(), |d, h| d.child(caption(&h, p)))
                    .child(div().text_sm().child(q.question.clone()))
                    .child(options),
            );
        }
        let complete = a
            .questions
            .iter()
            .enumerate()
            .all(|(qi, _)| chosen.get(qi).is_some_and(|s| !s.is_empty()));
        let allow = a
            .options
            .iter()
            .find(|o| o.kind == OptionKind::AllowOnce)
            .map(|o| o.id.clone());
        let deny = a
            .options
            .iter()
            .find(|o| o.kind == OptionKind::Deny)
            .map(|o| (o.id.clone(), o.label.clone()));
        let answers: BTreeMap<String, String> = a
            .questions
            .iter()
            .enumerate()
            .map(|(qi, q)| {
                let labels: Vec<&str> = chosen
                    .get(qi)
                    .map(|s| {
                        s.iter()
                            .filter_map(|&oi| q.options.get(oi).map(|o| o.label.as_str()))
                            .collect()
                    })
                    .unwrap_or_default();
                (q.question.clone(), labels.join(", "))
            })
            .collect();
        let mut buttons = h_flex().gap_2().justify_end();
        if let Some((deny_id, label)) = deny {
            let approval = it.id.clone();
            buttons = buttons.child(
                Button::new(SharedString::from(format!("deny-{}", it.id)))
                    .small()
                    .outline()
                    .label(label)
                    .on_click(
                        cx.listener(move |this, _: &ClickEvent, _, cx| this.respond(&approval, &deny_id, None, cx)),
                    ),
            );
        }
        if let Some(allow_id) = allow {
            let approval = it.id.clone();
            buttons = buttons.child(
                Button::new(SharedString::from(format!("answer-{}", it.id)))
                    .small()
                    .primary()
                    .label("Answer")
                    .disabled(!complete)
                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                        this.respond(&approval, &allow_id, Some(answers.clone()), cx)
                    })),
            );
        }
        card.child(buttons).into_any_element()
    }

    fn render_draft(&mut self, text: &str, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        v_flex()
            .gap_1()
            .p_3()
            .rounded_lg()
            .border_1()
            .border_dashed()
            .border_color(p.surface1)
            .child(caption("Draft, not sent", p))
            .child(div().text_sm().child(text.to_string()))
            .child(
                h_flex().justify_end().child(
                    Button::new("send-draft")
                        .small()
                        .primary()
                        .label("Send draft")
                        .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                            this.run(
                                "session.sendDraft",
                                json!({"commandId": uuid::Uuid::new_v4().to_string(), "sessionId": this.session_id}),
                                cx,
                            )
                        })),
                ),
            )
            .into_any_element()
    }

    fn render_header(&self, session: Option<&Rc<Session>>, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let conn = self.conn.read(cx);
        let Some(s) = session else {
            return h_flex()
                .px_5()
                .py_3()
                .child(div().text_color(p.overlay).child("Loading…"));
        };
        let project = conn
            .index
            .projects
            .get(&s.project_id)
            .map(|p| p.name.clone())
            .unwrap_or_default();
        let harness = conn
            .harness(&s.harness)
            .map(|h| h.name.clone())
            .unwrap_or_else(|| s.harness.clone());
        let model = s
            .model_info
            .as_ref()
            .and_then(|m| m.name.clone().or_else(|| m.id.clone()))
            .or_else(|| s.model.clone());
        let mut subtitle = vec![project];
        if let Some(b) = &s.workspace.branch {
            subtitle.push(b.clone());
        }
        subtitle.push(match model {
            Some(m) => format!("{harness} · {m}"),
            None => harness,
        });
        let (status, color) = match s.status {
            SessionStatus::Running => ("Working", p.blue),
            SessionStatus::AwaitingApproval => ("Needs you", p.yellow),
            SessionStatus::Error => ("Failed", p.red),
            SessionStatus::Stopped => ("Stopped", p.overlay),
            _ if s.archived => ("Archived", p.overlay),
            _ => ("Idle", p.overlay),
        };
        h_flex()
            .px_5()
            .py_2()
            .gap_3()
            .border_b_1()
            .border_color(p.surface0)
            .child(
                v_flex()
                    .flex_1()
                    .min_w_0()
                    .child(
                        div()
                            .font_weight(FontWeight::SEMIBOLD)
                            .truncate()
                            .child(s.title.clone()),
                    )
                    .child(
                        div()
                            .text_xs()
                            .text_color(p.overlay)
                            .truncate()
                            .child(subtitle.join(" · ")),
                    ),
            )
            .child(
                h_flex()
                    .gap_1p5()
                    .child(div().size(px(7.)).rounded_full().bg(color))
                    .child(div().text_xs().text_color(p.subtext).child(status)),
            )
    }
}

fn build_rows(store: &SessionState, expanded: &HashMap<String, bool>) -> Vec<Row> {
    let turns: HashMap<String, Rc<Turn>> = store.turns.clone();
    let mut rows: Vec<Row> = transcript_rows(&store.transcript, &turns, expanded, true)
        .into_iter()
        .map(|r| match r {
            TranscriptRow::Item(it) => Row::Item(it),
            TranscriptRow::Run { run, expanded } => Row::Run { run, expanded },
            TranscriptRow::TurnEnd(t) => Row::TurnEnd(t),
        })
        .collect();
    rows.extend(store.outbox.iter().cloned().map(Row::Outgoing));
    if let Some(draft) = store.session.as_ref().and_then(|s| s.draft.as_ref())
        && !store.outbox.iter().any(|o| o.text == draft.text)
    {
        rows.push(Row::Draft(draft.text.clone()));
    }
    rows
}

fn markdown(id: &str, text: &str) -> TextView {
    TextView::markdown(
        ElementId::Name(SharedString::from(format!("md-{id}"))),
        SharedString::from(text.to_string()),
    )
    // As the other clients do: raw HTML in a reply stays text.
    .markdown_extensions(MarkdownExtensions::default().raw_html(false))
    .selectable(true)
}

/// A row that puts its child at the trailing edge, as wide as its content.
fn trailing(child: impl IntoElement) -> AnyElement {
    h_flex().w_full().justify_end().child(child).into_any_element()
}

fn user_bubble(text: &str, p: &Palette) -> gpui_kit::Div {
    v_flex()
        .max_w(rems(36.))
        .gap_1()
        .px_3()
        .py_2()
        .rounded_lg()
        .bg(p.user_bubble)
        .child(div().text_sm().child(text.to_string()))
}

fn caption(text: &str, p: &Palette) -> gpui_kit::Div {
    div().text_xs().text_color(p.overlay).child(text.to_string())
}

fn disclosure(id: String, open: bool, title: &str, p: &Palette) -> gpui_kit::Stateful<gpui_kit::Div> {
    h_flex()
        .id(SharedString::from(id))
        .gap_1p5()
        .py_0p5()
        .cursor_pointer()
        .text_sm()
        .text_color(p.subtext)
        .hover(|d| d.text_color(p.text))
        .child(
            Icon::new(if open {
                IconName::ChevronDown
            } else {
                IconName::ChevronRight
            })
            .xsmall()
            .text_color(p.overlay),
        )
        .child(title.to_string())
}

fn code_block(text: &str, p: &Palette) -> impl IntoElement {
    div()
        .w_full()
        .px_2()
        .py_1p5()
        .rounded_md()
        .bg(p.crust)
        .text_xs()
        .font_family("Menlo")
        .text_color(p.subtext)
        .child(text.to_string())
}

fn tool_icon(kind: ToolKind) -> IconName {
    match kind {
        ToolKind::Execute => IconName::SquareTerminal,
        ToolKind::Edit => IconName::File,
        ToolKind::Read => IconName::FileText,
        ToolKind::Search => IconName::Search,
        ToolKind::Fetch => IconName::Globe,
        ToolKind::Think => IconName::Bot,
        ToolKind::Other => IconName::Settings,
    }
}

/// A shell command in a tool's input, as most agents put it.
fn command_of(input: &Value) -> Option<String> {
    match input.get("command")? {
        Value::String(s) => Some(s.clone()),
        Value::Array(parts) => Some(parts.iter().filter_map(Value::as_str).collect::<Vec<_>>().join(" ")),
        _ => None,
    }
}

fn last_lines(text: &str, n: usize) -> String {
    let lines: Vec<&str> = text.trim_end().lines().collect();
    if lines.len() <= n {
        return lines.join("\n");
    }
    format!(
        "… {} lines above\n{}",
        lines.len() - n,
        lines[lines.len() - n..].join("\n")
    )
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
        .child(caption("Plan", p));
    for e in &plan.entries {
        let (mark, color) = match e.status.as_str() {
            "completed" => ("☑", p.green),
            "in_progress" => ("◐", p.blue),
            _ => ("☐", p.overlay),
        };
        col = col.child(
            h_flex()
                .gap_2()
                .items_start()
                .text_sm()
                .child(div().text_color(color).child(mark))
                .child(div().flex_1().child(e.content.clone())),
        );
    }
    if let Some(text) = plan.text.as_ref().filter(|t| !t.is_empty()) {
        col = col.child(markdown(&it.id, text));
    }
    col.into_any_element()
}

fn render_decided(it: &Item, a: &Approval, p: &Palette) -> AnyElement {
    let chosen = a
        .decision
        .as_ref()
        .and_then(|d| a.options.iter().find(|o| o.id == d.option_id));
    let (icon, color, verb) = match (it.status, chosen.map(|o| o.kind)) {
        (ItemStatus::Resolved, Some(OptionKind::Deny)) => (IconName::CircleX, p.red, chosen.map(|o| o.label.clone())),
        (ItemStatus::Resolved, _) => (IconName::CircleCheck, p.green, chosen.map(|o| o.label.clone())),
        (ItemStatus::Expired, _) => (IconName::CircleX, p.overlay, Some("Expired".into())),
        _ => (IconName::CircleX, p.overlay, Some("Cancelled".into())),
    };
    let answers = a
        .decision
        .as_ref()
        .map(|d| d.answers.values().cloned().collect::<Vec<_>>().join("; "))
        .unwrap_or_default();
    h_flex()
        .gap_2()
        .text_sm()
        .text_color(p.subtext)
        .child(Icon::new(icon).small().text_color(color))
        .child(div().flex_1().min_w_0().truncate().child(a.title.clone()))
        .when_some(verb, |d, v| d.child(div().text_xs().text_color(p.overlay).child(v)))
        .when(!answers.is_empty(), |d| {
            d.child(div().text_xs().text_color(p.overlay).truncate().child(answers))
        })
        .into_any_element()
}

fn render_turn_end(t: &Turn, p: &Palette) -> AnyElement {
    let text = match t.status {
        TurnStatus::Interrupted => "Interrupted".to_string(),
        _ => match &t.error {
            Some(e) => format!("Failed: {e}"),
            None => "Failed".to_string(),
        },
    };
    let color = if t.status == TurnStatus::Failed {
        p.red
    } else {
        p.overlay
    };
    div()
        .w_full()
        .py_1()
        .text_xs()
        .text_center()
        .text_color(color)
        .child(text)
        .into_any_element()
}

impl Render for SessionView {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::of(cx);
        let session = self.session(cx);
        let running = session
            .as_ref()
            .is_some_and(|s| matches!(s.status, SessionStatus::Running | SessionStatus::AwaitingApproval));
        let connected = self.conn.read(cx).is_connected();
        let archived = session.as_ref().is_some_and(|s| s.archived);
        let removed = self
            .conn
            .read(cx)
            .stores
            .get(&self.session_id)
            .is_some_and(|s| s.removed);
        v_flex()
            .size_full()
            .child(self.render_header(session.as_ref(), cx))
            .child(
                list(
                    self.list.clone(),
                    cx.processor(|this, ix, window, cx| this.render_row(ix, window, cx)),
                )
                .flex_1()
                .min_h_0()
                .py_3(),
            )
            .when_some(self.error.clone(), |d, e| {
                d.child(
                    h_flex()
                        .mx_5()
                        .mb_1()
                        .gap_2()
                        .text_sm()
                        .text_color(p.red)
                        .child(div().flex_1().child(e))
                        .child(
                            Button::new("dismiss-error")
                                .xsmall()
                                .ghost()
                                .icon(IconName::Close)
                                .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                                    this.error = None;
                                    cx.notify();
                                })),
                        ),
                )
            })
            .child(
                v_flex().px_5().pb_4().pt_1().child(
                    v_flex()
                        .max_w(rems(52.))
                        .w_full()
                        .mx_auto()
                        .gap_2()
                        .p_2()
                        .rounded_lg()
                        .border_1()
                        .border_color(p.surface0)
                        .bg(p.mantle)
                        .child(
                            Textarea::new(&self.composer)
                                .appearance(false)
                                .disabled(archived || removed),
                        )
                        .child(
                            h_flex()
                                .gap_2()
                                .justify_end()
                                .when(!connected, |d| d.child(caption("Not connected", &p).mr_auto()))
                                .when(archived, |d| d.child(caption("Archived", &p).mr_auto()))
                                .when(running, |d| {
                                    d.child(
                                        Button::new("stop")
                                            .small()
                                            .outline()
                                            .icon(IconName::Square)
                                            .label("Stop")
                                            .on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.interrupt(cx))),
                                    )
                                })
                                .child(
                                    Button::new("send")
                                        .small()
                                        .primary()
                                        .icon(IconName::ArrowUp)
                                        .label(if running { "Queue" } else { "Send" })
                                        .disabled(!connected || archived || removed)
                                        .on_click(
                                            cx.listener(|this, _: &ClickEvent, window, cx| this.send(window, cx)),
                                        ),
                                ),
                        ),
                ),
            )
    }
}
