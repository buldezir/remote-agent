//! Runs of tool calls in a transcript, each folded into one row that sums it
//! up ("Ran 4 commands, edited 3 files, read 2 files") and opens to show the
//! calls, as `ToolRuns` does in the other clients (RAKit's
//! `Store/ToolRuns.swift`, `web/src/store/toolRuns.ts`, and Android's
//! core), whose test vectors these tests share.
//!
//! A run is the tool calls and the agent's thinking between two pieces of
//! conversation (a prompt, a reply, an approval, a plan, a notice, an error),
//! within one turn. It folds with two tool calls or more. A call that waits
//! for approval, failed, shows images or is one of rad's own tools (it starts
//! or drives another session, `store::rad_tools`) stands on its own, between
//! runs. A
//! run is live while its turn runs and nothing has come after it: it shows
//! open, unless the user shut it, and folds once the agent replies or the
//! turn ends. A prompt queued while the agent works isn't conversation yet:
//! it doesn't end the run it was sent during, and waits after it.

use std::collections::{HashMap, HashSet};
use std::rc::Rc;

use crate::protocol::{Item, ItemKind, ItemStatus, ToolKind, Turn, TurnStatus};
use crate::store::rad_tools::rad_tool;

/// The fewest tool calls that fold: one on its own stays as it is.
pub const TOOL_RUN_MINIMUM: usize = 2;

#[derive(Debug, Clone)]
pub struct ToolRun {
    /// "tools:" and the first item's id, which stays as the run grows.
    pub id: String,
    /// Its tool calls, and the reasoning between them.
    pub items: Vec<Rc<Item>>,
    /// Its turn runs and nothing has come after it.
    pub live: bool,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ToolRunPart {
    pub kind: ToolKind,
    pub count: usize,
    pub text: String,
}

/// A row of the transcript: an item, a run's summary (followed by its items
/// when it's open), or the end of a turn that was interrupted or failed.
#[derive(Debug, Clone)]
pub enum TranscriptRow {
    Item(Rc<Item>),
    Run { run: Rc<ToolRun>, expanded: bool },
    TurnEnd(Rc<Turn>),
}

impl ToolRun {
    pub fn tools(&self) -> Vec<&Rc<Item>> {
        self.items.iter().filter(|it| it.kind == ItemKind::ToolCall).collect()
    }

    pub fn parts(&self) -> Vec<ToolRunPart> {
        tool_run_parts(&self.tools())
    }

    pub fn title(&self) -> String {
        tool_run_title(&self.parts())
    }

    /// The agent is at work in the run.
    pub fn working(&self) -> bool {
        self.live || self.items.iter().any(|it| it.status == ItemStatus::InProgress)
    }
}

/// An item can be part of a run: reasoning, and tool calls that neither wait
/// for approval, failed, show images, nor are rad's own.
pub fn folds(it: &Item) -> bool {
    match it.kind {
        ItemKind::Reasoning => true,
        ItemKind::ToolCall => {
            it.status != ItemStatus::Pending
                && it.status != ItemStatus::Failed
                && it.images.is_empty()
                && !it.tool.as_ref().is_some_and(|t| rad_tool(&t.name).is_some())
        }
        _ => false,
    }
}

enum Segment {
    Item(Rc<Item>),
    Run(Rc<ToolRun>),
}

fn segments(items: &[Rc<Item>], turns: &HashMap<String, Rc<Turn>>) -> Vec<Segment> {
    let mut last_of_turn: HashMap<&str, usize> = HashMap::new();
    for (i, it) in items.iter().enumerate() {
        last_of_turn.insert(it.turn(), i);
    }
    let mut out = Vec::new();
    let mut i = 0;
    while i < items.len() {
        let first = &items[i];
        if !folds(first) {
            out.push(Segment::Item(first.clone()));
            i += 1;
            continue;
        }
        // The run's last item is j; k looks past the prompts queued meanwhile.
        let mut j = i;
        let mut k = i;
        while k + 1 < items.len() {
            let next = &items[k + 1];
            if next.status == ItemStatus::Queued {
                k += 1;
            } else if folds(next) && next.turn() == first.turn() {
                k += 1;
                j = k;
            } else {
                break;
            }
        }
        let span = &items[i..=j];
        let mut run = Vec::new();
        let mut queued = Vec::new();
        let mut tools = 0;
        for it in span {
            if it.status == ItemStatus::Queued {
                queued.push(it.clone());
                continue;
            }
            if it.kind == ItemKind::ToolCall {
                tools += 1;
            }
            run.push(it.clone());
        }
        if tools >= TOOL_RUN_MINIMUM {
            let turn = turns.get(first.turn());
            let live = !first.turn().is_empty()
                && turn.is_some_and(|t| t.status == TurnStatus::Running)
                && last_of_turn.get(first.turn()) == Some(&j);
            out.push(Segment::Run(Rc::new(ToolRun {
                id: format!("tools:{}", first.id),
                items: run,
                live,
            })));
            out.extend(queued.into_iter().map(Segment::Item));
        } else {
            out.extend(span.iter().cloned().map(Segment::Item));
        }
        i = j + 1;
    }
    out
}

/// The transcript's rows. A run is open as `expanded` says, by its id, else
/// while it's live. Without grouping, every item is a row.
pub fn transcript_rows(
    items: &[Rc<Item>],
    turns: &HashMap<String, Rc<Turn>>,
    expanded: &HashMap<String, bool>,
    grouping: bool,
) -> Vec<TranscriptRow> {
    // A prompt queued during a turn sits among its items, so the last is looked up.
    let mut last: HashMap<&str, &str> = HashMap::new();
    for it in items {
        if !it.turn().is_empty() {
            last.insert(it.turn(), &it.id);
        }
    }
    let mut out = Vec::new();
    let end = |it: &Item, out: &mut Vec<TranscriptRow>| {
        if let Some(t) = turns.get(it.turn())
            && last.get(it.turn()) == Some(&it.id.as_str())
            && matches!(t.status, TurnStatus::Interrupted | TurnStatus::Failed)
        {
            out.push(TranscriptRow::TurnEnd(t.clone()));
        }
    };
    let segs = if grouping {
        segments(items, turns)
    } else {
        items.iter().cloned().map(Segment::Item).collect()
    };
    for s in segs {
        match s {
            Segment::Item(it) => {
                out.push(TranscriptRow::Item(it.clone()));
                end(&it, &mut out);
            }
            Segment::Run(run) => {
                let open = expanded.get(&run.id).copied().unwrap_or(run.live);
                out.push(TranscriptRow::Run {
                    run: run.clone(),
                    expanded: open,
                });
                if open {
                    out.extend(run.items.iter().cloned().map(TranscriptRow::Item));
                }
                if let Some(last_item) = run.items.last() {
                    end(last_item, &mut out);
                }
            }
        }
    }
    out
}

/// The runs that fold, in order.
pub fn tool_runs(items: &[Rc<Item>], turns: &HashMap<String, Rc<Turn>>) -> Vec<Rc<ToolRun>> {
    segments(items, turns)
        .into_iter()
        .filter_map(|s| match s {
            Segment::Run(r) => Some(r),
            Segment::Item(_) => None,
        })
        .collect()
}

/// The order the kinds are summed up in.
const KINDS: [ToolKind; 7] = [
    ToolKind::Execute,
    ToolKind::Edit,
    ToolKind::Read,
    ToolKind::Search,
    ToolKind::Fetch,
    ToolKind::Think,
    ToolKind::Other,
];

fn kind_of(it: &Item) -> ToolKind {
    it.tool.as_ref().map(|t| t.kind).unwrap_or(ToolKind::Other)
}

/// The calls of each kind: files edited and read count once each, by their
/// paths (a call without paths is a file of its own); the rest by calls.
pub fn tool_run_parts(tools: &[&Rc<Item>]) -> Vec<ToolRunPart> {
    let mut out = Vec::new();
    for kind in KINDS {
        let calls: Vec<&&Rc<Item>> = tools.iter().filter(|it| kind_of(it) == kind).collect();
        if calls.is_empty() {
            continue;
        }
        let mut n = calls.len();
        if matches!(kind, ToolKind::Edit | ToolKind::Read) {
            let mut files: HashSet<String> = HashSet::new();
            for c in &calls {
                let paths = c.tool.as_ref().map(|t| t.paths.as_slice()).unwrap_or(&[]);
                if paths.is_empty() {
                    files.insert(format!("\0{}", c.id));
                }
                files.extend(paths.iter().cloned());
            }
            n = files.len();
        }
        out.push(ToolRunPart {
            kind,
            count: n,
            text: phrase(kind, n),
        });
    }
    out
}

fn phrase(kind: ToolKind, n: usize) -> String {
    let of = |one: &str, many: &str| format!("{n} {}", if n == 1 { one } else { many });
    match kind {
        ToolKind::Execute => format!("ran {}", of("command", "commands")),
        ToolKind::Edit => format!("edited {}", of("file", "files")),
        ToolKind::Read => format!("read {}", of("file", "files")),
        ToolKind::Search => format!("searched {}", of("time", "times")),
        ToolKind::Fetch => format!("fetched {}", of("page", "pages")),
        ToolKind::Think => format!("started {}", of("agent", "agents")),
        ToolKind::Other => format!("used {}", of("tool", "tools")),
    }
}

/// The parts as a sentence: "Ran 4 commands, edited 3 files".
pub fn tool_run_title(parts: &[ToolRunPart]) -> String {
    capitalized(&parts.iter().map(|p| p.text.as_str()).collect::<Vec<_>>().join(", "))
}

pub fn capitalized(text: &str) -> String {
    let mut chars = text.chars();
    match chars.next() {
        Some(c) => c.to_uppercase().chain(chars).collect(),
        None => String::new(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::protocol::{ImageRef, ToolCall};

    fn item(id: &str, kind: ItemKind, turn: &str, status: ItemStatus) -> Rc<Item> {
        Rc::new(Item {
            id: id.into(),
            session_id: "s".into(),
            turn_id: (!turn.is_empty()).then(|| turn.into()),
            kind,
            status,
            ..Default::default()
        })
    }

    fn tool(id: &str, kind: ToolKind, turn: &str, status: ItemStatus, paths: &[&str]) -> Rc<Item> {
        let mut it = (*item(id, ItemKind::ToolCall, turn, status)).clone();
        it.tool = Some(ToolCall {
            name: id.into(),
            kind,
            title: Some(id.into()),
            paths: paths.iter().map(|p| p.to_string()).collect(),
            ..Default::default()
        });
        Rc::new(it)
    }

    fn turn(id: &str, status: TurnStatus) -> Rc<Turn> {
        Rc::new(Turn {
            id: id.into(),
            session_id: "s".into(),
            n: 1,
            status,
            checkpoint_before: None,
            checkpoint_after: None,
            usage: None,
            error: None,
            started_at: String::new(),
            ended_at: None,
        })
    }

    use ItemKind as K;
    use ItemStatus as S;
    use ToolKind as T;
    const DONE: ItemStatus = S::Completed;

    /// A finished turn with a run, a failed command, a lone edit and a reply,
    /// and a running one with a run still going.
    fn transcript() -> Vec<Rc<Item>> {
        vec![
            item("u1", K::UserMessage, "t1", DONE),
            item("r1", K::Reasoning, "t1", DONE),
            tool("a", T::Execute, "t1", DONE, &[]),
            tool("b", T::Read, "t1", DONE, &["README.md"]),
            item("r2", K::Reasoning, "t1", DONE),
            tool("c", T::Search, "t1", DONE, &[]),
            tool("d", T::Read, "t1", DONE, &["README.md"]),
            tool("e", T::Edit, "t1", DONE, &["main.go"]),
            tool("f", T::Edit, "t1", DONE, &["main.go", "main_test.go"]),
            tool("g", T::Execute, "t1", S::Failed, &[]),
            tool("h", T::Edit, "t1", DONE, &["main.go"]),
            item("m1", K::AssistantMessage, "t1", DONE),
            item("u2", K::UserMessage, "t2", DONE),
            tool("i", T::Fetch, "t2", DONE, &[]),
            tool("j", T::Think, "t2", DONE, &[]),
            tool("k", T::Other, "t2", S::InProgress, &[]),
        ]
    }

    fn turns() -> HashMap<String, Rc<Turn>> {
        HashMap::from([
            ("t1".to_string(), turn("t1", TurnStatus::Completed)),
            ("t2".to_string(), turn("t2", TurnStatus::Running)),
        ])
    }

    fn row_ids(rows: &[TranscriptRow]) -> Vec<String> {
        rows.iter()
            .map(|r| match r {
                TranscriptRow::Item(it) => it.id.clone(),
                TranscriptRow::Run { run, expanded: true } => format!("{} open", run.id),
                TranscriptRow::Run { run, expanded: false } => format!("{} shut", run.id),
                TranscriptRow::TurnEnd(t) => format!("end {}", t.id),
            })
            .collect()
    }

    fn ids(items: &[Rc<Item>]) -> Vec<String> {
        items.iter().map(|i| i.id.clone()).collect()
    }

    fn expanded(pairs: &[(&str, bool)]) -> HashMap<String, bool> {
        pairs.iter().map(|(k, v)| (k.to_string(), *v)).collect()
    }

    #[test]
    fn fold_runs_of_tool_calls() {
        let runs = tool_runs(&transcript(), &turns());
        assert_eq!(runs.len(), 2);
        assert_eq!(runs[0].id, "tools:r1");
        assert_eq!(runs[1].id, "tools:i");
        assert_eq!(ids(&runs[0].items), ["r1", "a", "b", "r2", "c", "d", "e", "f"]);
        assert_eq!(runs[0].tools().len(), 6);
        assert!(!runs[0].live && !runs[0].working());
        assert_eq!(
            runs[0].title(),
            "Ran 1 command, edited 2 files, read 1 file, searched 1 time"
        );
        let parts = runs[0].parts();
        assert_eq!(
            parts.iter().map(|p| p.kind).collect::<Vec<_>>(),
            [T::Execute, T::Edit, T::Read, T::Search]
        );
        assert_eq!(parts.iter().map(|p| p.count).collect::<Vec<_>>(), [1, 2, 1, 1]);
        assert_eq!(ids(&runs[1].items), ["i", "j", "k"]);
        assert!(runs[1].live && runs[1].working());
        assert_eq!(runs[1].title(), "Fetched 1 page, started 1 agent, used 1 tool");
    }

    #[test]
    fn show_shut_unless_live() {
        let none = HashMap::new();
        assert_eq!(
            row_ids(&transcript_rows(&transcript(), &turns(), &none, true)),
            [
                "u1",
                "tools:r1 shut",
                "g",
                "h",
                "m1",
                "u2",
                "tools:i open",
                "i",
                "j",
                "k"
            ]
        );
        assert_eq!(
            row_ids(&transcript_rows(
                &transcript(),
                &turns(),
                &expanded(&[("tools:r1", true), ("tools:i", false)]),
                true
            )),
            [
                "u1",
                "tools:r1 open",
                "r1",
                "a",
                "b",
                "r2",
                "c",
                "d",
                "e",
                "f",
                "g",
                "h",
                "m1",
                "u2",
                "tools:i shut"
            ]
        );
        assert_eq!(
            row_ids(&transcript_rows(&transcript(), &turns(), &none, false)),
            ids(&transcript())
        );
    }

    #[test]
    fn fold_once_the_agent_replies_or_the_turn_ends() {
        let mut items = transcript();
        let mut ts = turns();
        assert!(tool_runs(&items, &ts)[1].live);
        items.push(item("q", K::UserMessage, "", S::Queued));
        assert!(tool_runs(&items, &ts)[1].live, "a queued prompt isn't the turn's");
        items.push(item("m2", K::AssistantMessage, "t2", S::InProgress));
        assert!(!tool_runs(&items, &ts)[1].live);
        let rows = row_ids(&transcript_rows(&items, &ts, &HashMap::new(), true));
        assert_eq!(rows[rows.len() - 4..], ["u2", "tools:i shut", "q", "m2"]);
        ts.insert("t2".into(), turn("t2", TurnStatus::Completed));
        assert!(!tool_runs(&transcript(), &ts)[1].live);
    }

    #[test]
    fn show_a_prompt_queued_mid_run_after_it() {
        let ts = turns();
        let mut items = transcript();
        let at = items.len() - 1;
        items.insert(at, item("q", K::UserMessage, "", S::Queued));
        let runs = tool_runs(&items, &ts);
        assert_eq!(ids(&runs[1].items), ["i", "j", "k"]);
        assert!(runs[1].live);
        let rows = row_ids(&transcript_rows(&items, &ts, &HashMap::new(), true));
        assert_eq!(rows[rows.len() - 6..], ["u2", "tools:i open", "i", "j", "k", "q"]);
        let rows = row_ids(&transcript_rows(&items, &ts, &expanded(&[("tools:i", false)]), true));
        assert_eq!(rows[rows.len() - 3..], ["u2", "tools:i shut", "q"]);
        let queued = item("q", K::UserMessage, "", S::Queued);
        assert_eq!(
            row_ids(&transcript_rows(
                &[
                    tool("a", T::Execute, "t1", DONE, &[]),
                    queued.clone(),
                    item("m", K::AssistantMessage, "t1", DONE)
                ],
                &ts,
                &HashMap::new(),
                true
            )),
            ["a", "q", "m"]
        );
        assert_eq!(
            row_ids(&transcript_rows(
                &[
                    tool("a", T::Execute, "t1", DONE, &[]),
                    queued,
                    tool("b", T::Read, "t1", DONE, &[])
                ],
                &ts,
                &HashMap::new(),
                true
            )),
            ["tools:a shut", "q"]
        );
        // Steered into the turn, it's conversation: the run ends before it.
        let n = items.len();
        items[n - 2] = item("q", K::UserMessage, "t2", DONE);
        let rows = row_ids(&transcript_rows(&items, &ts, &HashMap::new(), true));
        assert_eq!(rows[rows.len() - 4..], ["u2", "tools:i shut", "q", "k"]);
    }

    #[test]
    fn leave_calls_that_need_attention_alone() {
        let mut shot = (*tool("s", T::Other, "t1", DONE, &[])).clone();
        shot.images = vec![ImageRef {
            id: "img".into(),
            mime_type: "image/png".into(),
            width: None,
            height: None,
            size: 1,
        }];
        let items = vec![
            tool("a", T::Execute, "t1", DONE, &[]),
            tool("b", T::Edit, "t1", S::Pending, &[]),
            tool("c", T::Execute, "t1", DONE, &[]),
            Rc::new(shot),
            tool("d", T::Read, "t1", DONE, &[]),
            item("r", K::Reasoning, "t1", DONE),
            tool("e", T::Read, "t1", DONE, &[]),
        ];
        assert_eq!(
            row_ids(&transcript_rows(&items, &turns(), &HashMap::new(), true)),
            ["a", "b", "c", "s", "tools:d shut"]
        );
        assert_eq!(tool_runs(&items, &turns())[0].title(), "Read 2 files");
        let alone = vec![
            item("r1", K::Reasoning, "t1", DONE),
            item("r2", K::Reasoning, "t1", DONE),
            tool("x", T::Read, "t1", DONE, &[]),
        ];
        assert!(tool_runs(&alone, &turns()).is_empty());
    }

    #[test]
    fn leave_rads_own_calls_alone() {
        let items = vec![
            tool("a", T::Execute, "t1", DONE, &[]),
            tool("mcp__rad__start_session", T::Other, "t1", DONE, &[]),
            tool("rad.wait_for_session", T::Other, "t1", DONE, &[]),
            tool("b", T::Read, "t1", DONE, &[]),
            tool("mcp__other__start_session", T::Other, "t1", DONE, &[]),
            tool("rad.nope", T::Other, "t1", DONE, &[]),
        ];
        // rad's calls start and drive other sessions, so they show between
        // runs; other servers' tools, and names rad doesn't have, fold.
        assert_eq!(
            row_ids(&transcript_rows(&items, &turns(), &HashMap::new(), true)),
            ["a", "mcp__rad__start_session", "rad.wait_for_session", "tools:b shut"]
        );
    }

    #[test]
    fn stay_within_a_turn() {
        let items = vec![
            tool("a", T::Execute, "t1", DONE, &[]),
            tool("b", T::Execute, "t2", DONE, &[]),
            tool("c", T::Execute, "t2", DONE, &[]),
        ];
        assert_eq!(
            row_ids(&transcript_rows(&items, &turns(), &HashMap::new(), true)),
            ["a", "tools:b open", "b", "c"]
        );
    }

    #[test]
    fn end_a_turn_that_ended_badly_after_the_run() {
        let items = vec![
            item("u", K::UserMessage, "t1", DONE),
            tool("a", T::Execute, "t1", DONE, &[]),
            tool("b", T::Read, "t1", DONE, &[]),
        ];
        let failed = HashMap::from([("t1".to_string(), turn("t1", TurnStatus::Failed))]);
        assert_eq!(
            row_ids(&transcript_rows(&items, &failed, &HashMap::new(), true)),
            ["u", "tools:a shut", "end t1"]
        );
        assert_eq!(
            row_ids(&transcript_rows(&items, &failed, &expanded(&[("tools:a", true)]), true)),
            ["u", "tools:a open", "a", "b", "end t1"]
        );
        let interrupted = HashMap::from([("t1".to_string(), turn("t1", TurnStatus::Interrupted))]);
        assert_eq!(
            row_ids(&transcript_rows(&items, &interrupted, &HashMap::new(), false)),
            ["u", "a", "b", "end t1"]
        );
    }

    #[test]
    fn parts_count_files_by_path_and_the_rest_by_call() {
        let spec: [(&str, ToolKind, &[&str]); 14] = [
            ("a", T::Read, &[]),
            ("b", T::Read, &[]),
            ("c", T::Edit, &["x"]),
            ("d", T::Edit, &["x"]),
            ("e", T::Search, &[]),
            ("f", T::Search, &[]),
            ("g", T::Fetch, &[]),
            ("h", T::Fetch, &[]),
            ("i", T::Think, &[]),
            ("j", T::Think, &[]),
            ("k", T::Other, &[]),
            ("l", T::Other, &[]),
            ("m", T::Execute, &[]),
            ("n", T::Execute, &[]),
        ];
        let items: Vec<Rc<Item>> = spec.iter().map(|(id, k, p)| tool(id, *k, "t1", DONE, p)).collect();
        let refs: Vec<&Rc<Item>> = items.iter().collect();
        assert_eq!(
            tool_run_title(&tool_run_parts(&refs)),
            "Ran 2 commands, edited 1 file, read 2 files, searched 2 times, fetched 2 pages, started 2 agents, used 2 tools"
        );
        assert_eq!(tool_run_title(&[]), "");
    }
}
