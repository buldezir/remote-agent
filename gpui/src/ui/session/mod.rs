//! One session: its header and menus, the transcript (with runs of tool
//! calls folded, as in the other clients), find, the approvals waiting in
//! it, the composer, and its terminal.

mod composer;
mod dock;
mod header;
mod transcript;

use std::collections::{BTreeSet, HashMap, HashSet};
use std::hash::{DefaultHasher, Hash, Hasher};
use std::rc::Rc;
use std::sync::Arc;
use std::time::Duration;

use gpui_kit::component::input::{InputEvent, InputState, TextareaState};
use gpui_kit::component::speech::{SpeechEvent, SpeechState};
use gpui_kit::{
    App, AppContext as _, Context, Entity, Focusable as _, Image, ListAlignment, ListState, Subscription, Task, Window,
    px,
};
use serde_json::{Value, json};

use crate::conn::ServerConn;
use crate::images::Prepared;
use crate::protocol::{BackgroundTask, Command, ImageRef, Item, Project, RpcError, Session, SessionStatus, Turn};
use crate::store::search::find_matches;
use crate::store::session::{Outgoing, SessionState};
use crate::store::tool_runs::{ToolRun, TranscriptRow, tool_runs, transcript_rows};
use crate::ui::dialogs;
use crate::ui::state::{AppPrefs, prefs, update_prefs};
use crate::ui::terminal::TerminalView;

/// A row of the transcript as this view draws it.
#[derive(Clone)]
pub(crate) enum Row {
    Item(Rc<Item>),
    Run { run: Rc<ToolRun>, expanded: bool },
    TurnEnd(Rc<Turn>),
    Outgoing(Outgoing),
    Draft { text: String, images: Vec<ImageRef> },
    Empty,
    Working,
    Background(Vec<BackgroundTask>),
    Failed(String),
}

pub(crate) enum AttachState {
    Uploading,
    Done(ImageRef),
    Failed(String),
}

/// An image the user attached to the prompt.
pub(crate) struct Attachment {
    pub key: u64,
    /// The prepared image, to show and to upload again; none for one taken
    /// back from a draft, which shows from rad.
    pub prepared: Option<(Prepared, Arc<Image>)>,
    pub state: AttachState,
}

/// The composer's slash-command suggestions, fetched as a `/` starts the prompt.
#[derive(Default)]
pub(crate) struct Commands {
    pub fetched: bool,
    pub loading: bool,
    pub failed: bool,
    pub list: Vec<Command>,
}

/// Find in the session.
pub(crate) struct Find {
    pub input: Entity<InputState>,
    pub matches: Vec<String>,
    pub current: Option<usize>,
}

pub struct SessionView {
    pub(crate) conn: Entity<ServerConn>,
    pub(crate) session_id: String,
    pub(crate) list: ListState,
    pub(crate) rows: Vec<Row>,
    signatures: Vec<u64>,
    /// The prefs and theme that rows were laid out with (`look`).
    look: String,
    /// Runs the user opened or shut, by run id.
    pub(crate) expanded_runs: HashMap<String, bool>,
    /// The steps of the runs that are open, which hang under their run.
    pub(crate) run_steps: HashSet<String>,
    /// Tool calls and reasoning the user opened.
    pub(crate) open_items: HashSet<String>,
    /// Tool outputs shown whole.
    pub(crate) full_outputs: HashSet<String>,
    /// A question approval's chosen options: approval id → per question.
    pub(crate) answers: HashMap<String, Vec<BTreeSet<usize>>>,
    /// The answers in the user's own words: (approval id, question) → field.
    pub(crate) answer_inputs: HashMap<(String, usize), Entity<InputState>>,
    pub(crate) composer: Entity<TextareaState>,
    pub(crate) attachments: Vec<Attachment>,
    next_attachment: u64,
    pub(crate) commands: Commands,
    pub(crate) command_index: usize,
    /// The prompt as it was when Esc put the suggestions away.
    pub(crate) commands_dismissed: Option<String>,
    pub(crate) find: Option<Find>,
    /// The item a search opened the session at, lit for a moment.
    pub(crate) flash: Option<String>,
    /// An item to show once the transcript is in.
    jump_to: Option<String>,
    pub(crate) terminal: Option<Entity<TerminalView>>,
    pub(crate) terminal_shown: bool,
    /// The docked terminal's height or width while a drag sets it, until it's saved.
    terminal_height: Option<f32>,
    terminal_width: Option<f32>,
    terminal_size_save: Option<Task<()>>,
    pub(crate) speech: Entity<SpeechState>,
    /// The prompt as it was when dictation started.
    dictation_base: Option<String>,
    pub(crate) error: Option<String>,
    /// The session's changes, for its bar: files, lines added and removed.
    pub(crate) changes: Option<(usize, i64, i64)>,
    /// What the changes were counted for (see `count_changes`).
    changes_for: String,
    changes_task: Option<Task<()>>,
    /// A dialog was open at the last draw: Changes and Commit alter the changes.
    pub(crate) had_dialog: bool,
    pub(crate) placeholder: &'static str,
    pub(crate) saving_draft: bool,
    pub(crate) unarchiving: bool,
    marking_read: bool,
    closed: bool,
    draft_save: Option<Task<()>>,
    _subscriptions: Vec<Subscription>,
}

impl SessionView {
    pub fn new(
        conn: Entity<ServerConn>,
        session_id: String,
        jump_to: Option<String>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> Self {
        let composer = cx.new(|cx| {
            TextareaState::new(window, cx)
                .placeholder("Prompt")
                .auto_grow(1, 12)
                .submit_on_enter(true)
        });
        let speech = cx.new(SpeechState::new);
        let subscriptions = vec![
            cx.observe_in(&conn, window, |this, _, window, cx| {
                this.refresh(cx);
                this.mark_read_if_shown(window, cx);
                this.try_jump(cx);
                this.update_placeholder(window, cx);
                this.count_changes(false, cx);
            }),
            cx.subscribe_in(&composer, window, |this, _, event, window, cx| match event {
                InputEvent::PressEnter { shift, secondary } => this.composer_enter(*shift, *secondary, window, cx),
                InputEvent::Change => this.composer_changed(cx),
                _ => {}
            }),
            cx.subscribe_in(&speech, window, |this, _, event: &SpeechEvent, window, cx| {
                this.dictation(event, window, cx)
            }),
            cx.observe_global::<AppPrefs>(|this, cx| this.restyle(cx)),
            cx.observe_global::<gpui_kit::component::Theme>(|this, cx| this.restyle(cx)),
        ];
        conn.update(cx, |c, cx| c.open_session(&session_id, cx));
        // The prompt written here before, and not sent.
        if let Some(text) = prefs(cx).drafts.get(&session_id).cloned() {
            composer.update(cx, |c, cx| c.set_value(text, window, cx));
        }
        composer.update(cx, |c, cx| c.focus(window, cx));
        let mut this = Self {
            conn,
            session_id,
            list: ListState::new(0, ListAlignment::Bottom, px(2000.)),
            rows: Vec::new(),
            signatures: Vec::new(),
            look: look(cx),
            expanded_runs: HashMap::new(),
            run_steps: HashSet::new(),
            open_items: HashSet::new(),
            full_outputs: HashSet::new(),
            answers: HashMap::new(),
            answer_inputs: HashMap::new(),
            composer,
            attachments: Vec::new(),
            next_attachment: 0,
            commands: Commands::default(),
            command_index: 0,
            commands_dismissed: None,
            find: None,
            flash: None,
            jump_to,
            terminal: None,
            terminal_shown: false,
            speech,
            dictation_base: None,
            error: None,
            changes: None,
            changes_for: String::new(),
            changes_task: None,
            had_dialog: false,
            placeholder: "Prompt",
            saving_draft: false,
            unarchiving: false,
            marking_read: false,
            closed: false,
            draft_save: None,
            terminal_height: None,
            terminal_width: None,
            terminal_size_save: None,
            _subscriptions: subscriptions,
        };
        this.refresh(cx);
        this.mark_read_if_shown(window, cx);
        this.update_placeholder(window, cx);
        this.try_jump(cx);
        this.count_changes(false, cx);
        this
    }

    pub fn conn(&self) -> &Entity<ServerConn> {
        &self.conn
    }

    pub fn session_id(&self) -> &str {
        &self.session_id
    }

    /// The view goes away: its stream may stop, and its prompt is kept.
    pub fn close(&mut self, cx: &mut Context<Self>) {
        if !self.closed {
            self.closed = true;
            self.save_local_draft(cx);
            let id = self.session_id.clone();
            self.conn.update(cx, |c, _| c.close_session(&id));
            if let Some(t) = &self.terminal {
                t.update(cx, |t, cx| t.detach(cx));
            }
        }
    }

    pub(crate) fn session(&self, cx: &App) -> Option<Rc<Session>> {
        self.conn.read(cx).session(&self.session_id)
    }

    pub(crate) fn store<'a>(&self, cx: &'a App) -> Option<&'a SessionState> {
        self.conn.read(cx).stores.get(&self.session_id)
    }

    pub(crate) fn project(&self, cx: &App) -> Option<Rc<Project>> {
        let s = self.session(cx)?;
        self.conn.read(cx).index.projects.get(&s.project_id).cloned()
    }

    pub(crate) fn is_running(&self, cx: &App) -> bool {
        self.session(cx)
            .is_some_and(|s| matches!(s.status, SessionStatus::Running | SessionStatus::AwaitingApproval))
    }

    /// Archived on a rad that can't unarchive: nothing can be sent.
    pub(crate) fn read_only(&self, cx: &App) -> bool {
        let unarchive = self.conn.read(cx).info.as_ref().is_some_and(|i| i.unarchive);
        self.session(cx).is_some_and(|s| s.archived) && !unarchive
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

    pub(crate) fn refresh(&mut self, cx: &mut Context<Self>) {
        let group = prefs(cx).group_tool_calls;
        let rows = {
            let conn = self.conn.read(cx);
            let Some(store) = conn.stores.get(&self.session_id) else {
                return;
            };
            // Find and a search show the run that holds their item open.
            let mut expanded = self.expanded_runs.clone();
            if let Some(target) = self.current_match().or(self.flash.clone()) {
                for run in tool_runs(&store.transcript, &store.turns) {
                    if run.items.iter().any(|i| i.id == target) {
                        expanded.insert(run.id.clone(), true);
                    }
                }
            }
            build_rows(store, &expanded, group)
        };
        self.run_steps = rows
            .iter()
            .filter_map(|r| match r {
                Row::Run { run, expanded: true } => Some(run.items.iter().map(|i| i.id.clone())),
                _ => None,
            })
            .flatten()
            .collect();
        let signatures: Vec<u64> = rows.iter().map(|r| self.signature(r, cx)).collect();
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

    /// The prefs or the theme changed. When the grouping, fonts, sizes or
    /// colours did, rows are built again and every one is laid out again.
    fn restyle(&mut self, cx: &mut Context<Self>) {
        // The prompt box's width is drawn afresh; the transcript's look only
        // when it changed.
        cx.notify();
        let look = look(cx);
        if look != self.look {
            self.look = look;
            self.refresh(cx);
            let n = self.list.item_count();
            self.list.splice(0..n, n);
            cx.notify();
        }
    }

    /// What a row looks like, so that only the rows that changed are laid out again.
    fn signature(&self, row: &Row, cx: &App) -> u64 {
        let mut h = DefaultHasher::new();
        match row {
            Row::Item(it) => {
                ("item", &it.id, &it.updated_at, it.text.as_ref().map(String::len)).hash(&mut h);
                format!("{:?}", it.status).hash(&mut h);
                it.images.len().hash(&mut h);
                self.open_items.contains(&it.id).hash(&mut h);
                self.full_outputs.contains(&it.id).hash(&mut h);
                self.highlight(&it.id).hash(&mut h);
                self.run_steps.contains(&it.id).hash(&mut h);
                self.asked_about(it, cx).hash(&mut h);
                if let Some(a) = self.answers.get(&it.id) {
                    a.hash(&mut h);
                }
                if let Some(store) = self.store(cx) {
                    store.children_of(&it.id).len().hash(&mut h);
                }
            }
            Row::Run { run, expanded } => {
                ("run", &run.id, expanded, run.items.len(), run.working()).hash(&mut h);
                run.items.iter().any(|i| self.highlight(&i.id) > 0).hash(&mut h);
            }
            Row::TurnEnd(t) => ("end", &t.id, format!("{:?}", t.status), &t.error).hash(&mut h),
            Row::Outgoing(o) => ("out", &o.command_id).hash(&mut h),
            Row::Draft { text, images } => ("draft", text, images.len()).hash(&mut h),
            Row::Empty => "empty".hash(&mut h),
            Row::Working => "working".hash(&mut h),
            Row::Background(tasks) => {
                "bg".hash(&mut h);
                for t in tasks {
                    (&t.id, &t.description).hash(&mut h);
                }
            }
            Row::Failed(e) => ("failed", e).hash(&mut h),
        }
        h.finish()
    }

    /// How an item is lit: 2 for find's current match or a search's item, 1
    /// for another match, 0 for none.
    pub(crate) fn highlight(&self, id: &str) -> u8 {
        if self.flash.as_deref() == Some(id) || self.current_match().as_deref() == Some(id) {
            2
        } else if self.find.as_ref().is_some_and(|f| f.matches.iter().any(|m| m == id)) {
            1
        } else {
            0
        }
    }

    pub(crate) fn toggle_run(&mut self, id: String, expanded: bool, cx: &mut Context<Self>) {
        self.expanded_runs.insert(id, !expanded);
        self.refresh(cx);
    }

    pub(crate) fn toggle_item(&mut self, id: String, cx: &mut Context<Self>) {
        if !self.open_items.remove(&id) {
            self.open_items.insert(id);
        }
        self.refresh(cx);
    }

    /// The row that shows an item: its own, or its run's while that's shut.
    fn row_of(&self, id: &str) -> Option<usize> {
        self.rows
            .iter()
            .position(|r| matches!(r, Row::Item(it) if it.id == id))
            .or_else(|| {
                self.rows.iter().position(
                    |r| matches!(r, Row::Run { run, expanded: false } if run.items.iter().any(|i| i.id == id)),
                )
            })
    }

    pub(crate) fn reveal(&mut self, id: &str) {
        if let Some(ix) = self.row_of(id) {
            self.list.scroll_to_reveal_item(ix);
        }
    }

    /// Opens the session at the item a search found, once the transcript is
    /// in, and lights it for a moment.
    fn try_jump(&mut self, cx: &mut Context<Self>) {
        let Some(target) = self.jump_to.clone() else { return };
        let Some(store) = self.store(cx) else { return };
        if !store.sync.live {
            return;
        }
        self.jump_to = None;
        // A sub-agent's item shows in its tool call.
        let top = |id: &str| store.transcript.iter().any(|i| i.id == id);
        let target = if top(&target) {
            Some(target)
        } else {
            store
                .items
                .get(&target)
                .and_then(|i| i.parent_item_id.clone())
                .filter(|p| top(p))
        };
        let Some(target) = target else { return };
        self.flash = Some(target.clone());
        self.refresh(cx);
        self.reveal(&target);
        cx.spawn(async move |this, cx| {
            cx.background_executor().timer(Duration::from_millis(2400)).await;
            let _ = this.update(cx, |this, cx| {
                this.flash = None;
                this.refresh(cx);
            });
        })
        .detach();
    }

    // MARK: Find

    pub(crate) fn current_match(&self) -> Option<String> {
        let f = self.find.as_ref()?;
        f.matches.get(f.current?).cloned()
    }

    pub fn open_find(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if let Some(f) = &self.find {
            f.input.focus_handle(cx).focus(window, cx);
            return;
        }
        let input = cx.new(|cx| InputState::new(window, cx).placeholder("Find in session"));
        let sub = cx.subscribe_in(&input, window, |this, _, event: &InputEvent, window, cx| match event {
            InputEvent::Change => this.find_changed(cx),
            InputEvent::PressEnter { shift, .. } => this.find_step(!*shift, cx),
            _ => {
                let _ = window;
            }
        });
        self._subscriptions.push(sub);
        input.focus_handle(cx).focus(window, cx);
        self.find = Some(Find {
            input,
            matches: Vec::new(),
            current: None,
        });
        cx.notify();
    }

    fn find_changed(&mut self, cx: &mut Context<Self>) {
        let Some(f) = &self.find else { return };
        let query = f.input.read(cx).value().to_string();
        let matches = self
            .store(cx)
            .map(|s| find_matches(&s.transcript, &query))
            .unwrap_or_default();
        let current = if matches.is_empty() {
            None
        } else {
            Some(matches.len() - 1)
        };
        if let Some(f) = &mut self.find {
            f.matches = matches;
            f.current = current;
        }
        self.refresh(cx);
        if let Some(id) = self.current_match() {
            self.reveal(&id);
        }
    }

    /// The next match (or the previous), round.
    pub fn find_step(&mut self, forward: bool, cx: &mut Context<Self>) {
        let Some(f) = &mut self.find else { return };
        let n = f.matches.len();
        if n == 0 {
            return;
        }
        let i = f.current.unwrap_or(n - 1);
        f.current = Some(if forward { (i + 1) % n } else { (i + n - 1) % n });
        self.refresh(cx);
        if let Some(id) = self.current_match() {
            self.reveal(&id);
        }
    }

    pub fn close_find(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        // The run that holds the match stays open.
        if let Some(target) = self.current_match()
            && let Some(store) = self.store(cx)
        {
            for run in tool_runs(&store.transcript, &store.turns) {
                if run.items.iter().any(|i| i.id == target) {
                    self.expanded_runs.insert(run.id.clone(), true);
                }
            }
        }
        self.find = None;
        self.refresh(cx);
        self.composer.update(cx, |c, cx| c.focus(window, cx));
    }

    // MARK: Commands

    /// Calls a method, showing what went wrong under the transcript.
    pub(crate) fn run(&mut self, method: &'static str, params: Value, cx: &mut Context<Self>) {
        let call = self.conn.read(cx).call(method, params);
        cx.spawn(async move |this, cx| {
            if let Err(e) = call.await {
                let _ = this.update(cx, |this, cx| {
                    this.error = Some(explain(&e));
                    cx.notify();
                });
            }
        })
        .detach();
    }

    pub fn interrupt(&mut self, force: bool, cx: &mut Context<Self>) {
        if !self.is_running(cx) {
            return;
        }
        self.run(
            "session.interrupt",
            json!({"sessionId": self.session_id, "force": force}),
            cx,
        );
    }

    pub(crate) fn respond(
        &mut self,
        approval_id: &str,
        option_id: &str,
        answers: Option<std::collections::BTreeMap<String, String>>,
        message: Option<String>,
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
        if let Some(m) = message.filter(|m| !m.is_empty()) {
            params["message"] = json!(m);
        }
        self.run("approval.respond", params, cx);
    }

    /// Denies with a reason the user may give.
    pub(crate) fn deny_with_reason(
        &mut self,
        approval_id: String,
        option_id: String,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let this = cx.entity().downgrade();
        dialogs::prompt_text(
            window,
            cx,
            "Reason (optional)",
            "",
            "Tell the agent why",
            "",
            "Deny",
            gpui_kit::component::button::ButtonVariant::Danger,
            move |reason, _, cx| {
                let _ = this.update(cx, |this, cx| {
                    this.respond(&approval_id, &option_id, None, Some(reason), cx)
                });
            },
        );
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) fn choose_answer(
        &mut self,
        approval: &str,
        question: usize,
        option: usize,
        multi: bool,
        count: usize,
        window: &mut Window,
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
            // A chip replaces the user's own words.
            if let Some(input) = self.answer_inputs.get(&(approval.to_string(), question)) {
                input.update(cx, |i, cx| i.set_value("", window, cx));
            }
        }
        self.refresh(cx);
    }

    /// The field for a question's answer in the user's own words.
    pub(crate) fn answer_input(
        &mut self,
        approval: &str,
        question: usize,
        placeholder: &str,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> Entity<InputState> {
        let key = (approval.to_string(), question);
        if let Some(i) = self.answer_inputs.get(&key) {
            return i.clone();
        }
        let input = cx.new(|cx| InputState::new(window, cx).placeholder(placeholder.to_string()));
        let (approval_id, multi_key) = (approval.to_string(), question);
        let sub = cx.subscribe_in(&input, window, move |this, input, event: &InputEvent, _, cx| {
            if let InputEvent::Change = event {
                // Typing replaces a single choice.
                let typed = !input.read(cx).value().trim().is_empty();
                let single = this
                    .session_question_multi(&approval_id, multi_key, cx)
                    .is_some_and(|multi| !multi);
                if typed
                    && single
                    && let Some(chosen) = this.answers.get_mut(&approval_id)
                    && let Some(set) = chosen.get_mut(multi_key)
                {
                    set.clear();
                }
                this.refresh(cx);
            }
        });
        self._subscriptions.push(sub);
        self.answer_inputs.insert(key, input.clone());
        input
    }

    fn session_question_multi(&self, approval: &str, question: usize, cx: &App) -> Option<bool> {
        let store = self.store(cx)?;
        let it = store.items.get(approval)?;
        Some(it.approval.as_ref()?.questions.get(question)?.multi_select)
    }

    /// Counts the session's changes for its bar: when it opens, when a turn
    /// ends, and when it moves to another branch or folder; `again` counts
    /// them whatever happened.
    pub(crate) fn count_changes(&mut self, again: bool, cx: &mut Context<Self>) {
        let (Some(s), true) = (self.session(cx), self.is_git(cx)) else {
            return;
        };
        let ended = self.store(cx).map_or(0, |store| {
            store
                .turns
                .values()
                .filter(|t| t.status != crate::protocol::TurnStatus::Running)
                .count()
        });
        let key = format!("{ended} {:?} {:?}", s.workspace.branch, s.workspace.agent_path);
        if key == self.changes_for && !again {
            return;
        }
        self.changes_for = key;
        let call = self
            .conn
            .read(cx)
            .call("git.sessionDiff", json!({"sessionId": self.session_id}));
        self.changes_task = Some(cx.spawn(async move |this, cx| {
            let counted = call
                .await
                .ok()
                .and_then(|v| serde_json::from_value::<crate::protocol::Diff>(v).ok())
                .map(|d| {
                    (
                        d.files.len(),
                        d.files.iter().map(|f| f.additions).sum(),
                        d.files.iter().map(|f| f.deletions).sum(),
                    )
                });
            let _ = this.update(cx, |this, cx| {
                this.changes = counted;
                cx.notify();
            });
        }));
    }

    // MARK: Session actions

    pub fn rename(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(s) = self.session(cx) else { return };
        let conn = self.conn.clone();
        rename_session(conn, s, window, cx);
    }

    // MARK: Terminal

    pub fn can_terminal(&self, cx: &App) -> bool {
        let terminal = self.conn.read(cx).info.as_ref().is_some_and(|i| i.terminal);
        terminal && self.session(cx).is_some_and(|s| !s.archived)
    }

    pub fn toggle_terminal(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if !self.can_terminal(cx) {
            return;
        }
        self.terminal_shown = !self.terminal_shown;
        if self.terminal_shown {
            let terminal = match &self.terminal {
                Some(t) => t.clone(),
                None => {
                    let (conn, id) = (self.conn.clone(), self.session_id.clone());
                    let t = cx.new(|cx| TerminalView::new(conn, id, window, cx));
                    self.terminal = Some(t.clone());
                    t
                }
            };
            terminal.update(cx, |t, cx| t.attach(window, cx));
            terminal.read(cx).focus_handle(cx).focus(window, cx);
        } else {
            if let Some(t) = &self.terminal {
                t.update(cx, |t, cx| t.detach(cx));
            }
            self.composer.update(cx, |c, cx| c.focus(window, cx));
        }
        cx.notify();
    }

    // MARK: Local drafts

    fn composer_changed(&mut self, cx: &mut Context<Self>) {
        self.update_commands(cx);
        // Saved a moment after the typing stops.
        self.draft_save = Some(cx.spawn(async move |this, cx| {
            cx.background_executor().timer(Duration::from_millis(800)).await;
            let _ = this.update(cx, |this, cx| this.save_local_draft(cx));
        }));
        cx.notify();
    }

    fn save_local_draft(&mut self, cx: &mut Context<Self>) {
        let text = self.composer.read(cx).value().to_string();
        let id = self.session_id.clone();
        update_prefs(cx, |p| {
            if text.trim().is_empty() {
                p.drafts.remove(&id);
            } else {
                p.drafts.insert(id, text);
            }
        });
    }

    // MARK: Dictation

    pub(crate) fn toggle_dictation(&mut self, cx: &mut Context<Self>) {
        self.speech.update(cx, |s, cx| s.toggle(cx));
    }

    fn dictation(&mut self, event: &SpeechEvent, window: &mut Window, cx: &mut Context<Self>) {
        match event {
            SpeechEvent::Started => {
                self.dictation_base = Some(self.composer.read(cx).value().to_string());
            }
            SpeechEvent::Partial(text) | SpeechEvent::Final(text) => {
                let base = self.dictation_base.clone().unwrap_or_default();
                let joined = if base.is_empty() || base.ends_with(char::is_whitespace) || text.is_empty() {
                    format!("{base}{text}")
                } else {
                    format!("{base} {text}")
                };
                self.composer.update(cx, |c, cx| c.set_value(joined, window, cx));
                if matches!(event, SpeechEvent::Final(_)) {
                    self.dictation_base = None;
                }
            }
            SpeechEvent::Cancelled => {
                if let Some(base) = self.dictation_base.take() {
                    self.composer.update(cx, |c, cx| c.set_value(base, window, cx));
                }
            }
            SpeechEvent::Error(e) => {
                self.dictation_base = None;
                self.error = Some(e.to_string());
            }
        }
        self.update_placeholder(window, cx);
        cx.notify();
    }

    fn next_key(&mut self) -> u64 {
        self.next_attachment += 1;
        self.next_attachment
    }
}

/// The rows: the transcript, then what waits at its foot.
fn build_rows(store: &SessionState, expanded: &HashMap<String, bool>, group: bool) -> Vec<Row> {
    let mut rows: Vec<Row> = transcript_rows(&store.transcript, &store.turns, expanded, group)
        .into_iter()
        .map(|r| match r {
            TranscriptRow::Item(it) => Row::Item(it),
            TranscriptRow::Run { run, expanded } => Row::Run { run, expanded },
            TranscriptRow::TurnEnd(t) => Row::TurnEnd(t),
        })
        .collect();
    let mut outbox = store.outbox.clone();
    outbox.sort_by(|a, b| a.command_id.cmp(&b.command_id));
    let session = store.session.as_ref();
    let draft = session.and_then(|s| s.draft.as_ref()).filter(|d| {
        !store
            .outbox
            .iter()
            .any(|o| o.text == d.text && o.images.len() == d.images.len())
    });
    rows.extend(outbox.into_iter().map(Row::Outgoing));
    if let Some(d) = draft {
        rows.push(Row::Draft {
            text: d.text.clone(),
            images: d.images.clone(),
        });
    } else if store.sync.synced && store.transcript.is_empty() && store.outbox.is_empty() {
        rows.push(Row::Empty);
    }
    if let Some(s) = session {
        if s.status == SessionStatus::Running {
            rows.push(Row::Working);
        }
        if !s.background_tasks.is_empty() {
            rows.push(Row::Background(s.background_tasks.clone()));
        }
        if s.status == SessionStatus::Error {
            rows.push(Row::Failed(s.error.clone().unwrap_or_else(|| "Failed".into())));
        }
    }
    rows
}

/// rad's errors as the user reads them: an older rad lacks newer methods.
pub(crate) fn explain(e: &RpcError) -> String {
    if e.code == "method_not_found" {
        "This server's rad is too old for that. Update rad on it.".into()
    } else {
        e.to_string()
    }
}

/// Asks for a session's new title (empty: rad names it again).
pub fn rename_session(conn: Entity<ServerConn>, s: Rc<Session>, window: &mut Window, cx: &mut App) {
    let (id, current) = (s.id.clone(), s.title.clone());
    dialogs::prompt_text(
        window,
        cx,
        "Rename Session",
        "Leave it empty to name it automatically.",
        "Title",
        &current.clone(),
        "Rename",
        gpui_kit::component::button::ButtonVariant::Primary,
        move |title, window, cx| {
            if title == current {
                return;
            }
            let call = conn
                .read(cx)
                .call("session.rename", json!({"sessionId": id, "title": title}));
            window
                .spawn(cx, async move |cx| {
                    if let Err(e) = call.await {
                        let _ = cx.update(|window, cx| {
                            dialogs::alert(window, cx, "Couldn't rename the session", &explain(&e))
                        });
                    }
                })
                .detach();
        },
    );
}

/// What rows' layout depends on besides their own content.
fn look(cx: &App) -> String {
    let p = prefs(cx);
    format!(
        "{} {} {} {} {} {} {} {:?}",
        p.group_tool_calls,
        p.limit_message_width,
        p.text_size,
        p.message_size,
        p.code_size,
        crate::ui::fonts::message(),
        crate::ui::fonts::code(),
        gpui_kit::component::ActiveTheme::theme(cx).mode
    )
}
