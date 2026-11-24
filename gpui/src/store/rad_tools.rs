//! rad's own tools, which agents call through its MCP server
//! (`server/internal/api/mcp.go`) to start and drive other sessions: what a
//! call did, in words, with the session it acted on, for the transcript to
//! draw as a sentence with that session's pill rather than as a bare
//! `mcp__rad__start_session` and its JSON. Free of UI.

use serde_json::Value;

use crate::protocol::{Item, ItemStatus, ToolCall};

/// One of rad's tools.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RadTool {
    ListProjects,
    ListAgents,
    ListSessions,
    StartSession,
    SendPrompt,
    ReadSession,
    WaitForSession,
    StopSession,
    RenameSession,
    ForkSession,
    SaveDraft,
    ArchiveSession,
    UnarchiveSession,
    SearchSessions,
    ReadDiff,
    ListBranches,
}

impl RadTool {
    fn from_name(name: &str) -> Option<Self> {
        use RadTool::*;
        Some(match name {
            "list_projects" => ListProjects,
            "list_agents" => ListAgents,
            "list_sessions" => ListSessions,
            "start_session" => StartSession,
            "send_prompt" => SendPrompt,
            "read_session" => ReadSession,
            "wait_for_session" => WaitForSession,
            "stop_session" => StopSession,
            "rename_session" => RenameSession,
            "fork_session" => ForkSession,
            "save_draft" => SaveDraft,
            "archive_session" => ArchiveSession,
            "unarchive_session" => UnarchiveSession,
            "search_sessions" => SearchSessions,
            "read_diff" => ReadDiff,
            "list_branches" => ListBranches,
            _ => return None,
        })
    }
}

/// The rad tool a tool call's name names: Claude Code's
/// `mcp__rad__start_session`, Codex's `rad.start_session`, or an ACP
/// agent's `rad/…`, `rad__…` or `rad:…`.
pub fn rad_tool(name: &str) -> Option<RadTool> {
    let rest = ["mcp__rad__", "rad.", "rad/", "rad__", "rad:", "rad_"]
        .iter()
        .find_map(|p| name.strip_prefix(p))?;
    RadTool::from_name(rest.trim())
}

/// A session a call names: its id (or a unique start of one, as agents may
/// pass), and its title where the call says it.
#[derive(Debug, Clone, PartialEq)]
pub struct SessionRef {
    pub id: String,
    pub title: Option<String>,
}

/// What a section of an open call shows.
#[derive(Debug, Clone, PartialEq)]
pub enum Section {
    /// Markdown: a prompt, a reply, a list.
    Markdown { label: &'static str, text: String },
    /// Text as it came, in a code block.
    Text { label: &'static str, text: String },
}

/// A call of one of rad's tools, in words: `before`, the session's pill,
/// then `after`, such as "Started" [Fix the tests] " · worktree".
#[derive(Debug, Clone, PartialEq)]
pub struct RadCall {
    pub tool: RadTool,
    pub before: String,
    pub session: Option<SessionRef>,
    pub after: String,
    /// What the call's row shows when it's open.
    pub sections: Vec<Section>,
}

/// A call of rad's tools in words; None for any other tool.
pub fn describe(it: &Item) -> Option<RadCall> {
    let tool = it.tool.as_ref()?;
    let kind = rad_tool(&tool.name)?;
    Some(describe_call(kind, tool, it.status))
}

fn describe_call(kind: RadTool, tool: &ToolCall, status: ItemStatus) -> RadCall {
    use RadTool::*;
    let input = tool.input.clone().unwrap_or(Value::Null);
    let output = tool.output.clone().unwrap_or_default();
    let failed = status == ItemStatus::Failed;
    let running = matches!(status, ItemStatus::InProgress | ItemStatus::Pending);
    // A tool's answer is JSON, or for some a line of text; an error is text.
    let out: Value = if failed {
        Value::Null
    } else {
        serde_json::from_str(output.trim()).unwrap_or(Value::Null)
    };
    let s = |v: &Value, k: &str| -> Option<String> {
        v.get(k)
            .and_then(Value::as_str)
            .map(str::trim)
            .filter(|s| !s.is_empty())
            .map(str::to_string)
    };
    let named = s(&input, "session").map(|id| SessionRef { id, title: None });
    // The session a call made (start, fork), as its answer describes it.
    let made = s(&out, "id").map(|id| SessionRef {
        id,
        title: s(&out, "title"),
    });
    let mut call = RadCall {
        tool: kind,
        before: String::new(),
        session: named.clone(),
        after: String::new(),
        sections: Vec::new(),
    };
    let prompt_section = |key: &str| s(&input, key).map(|text| Section::Markdown { label: "Prompt", text });
    let verb = |done: &str, doing: &str, failing: &str| -> String {
        if failed {
            failing
        } else if running {
            doing
        } else {
            done
        }
        .to_string()
    };
    match kind {
        StartSession => {
            call.before = verb("Started", "Starting", "Couldn't start");
            call.session = made.or_else(|| {
                // Not made yet: the title it asked for, else its prompt's first line.
                Some(SessionRef {
                    id: String::new(),
                    title: s(&input, "title").or_else(|| s(&input, "prompt").map(|p| first_line(&p))),
                })
            });
            call.after = join(&[
                s(&input, "project").map(|p| format!("in {}", base_name(&p))),
                setup(&input, &out),
                (input.get("draft") == Some(&Value::Bool(true))).then(|| "as a draft".into()),
            ]);
            call.sections.extend(prompt_section("prompt"));
        }
        ForkSession => {
            call.before = verb("Forked", "Forking", "Couldn't fork");
            if let Some(m) = made {
                call.session = Some(m);
            }
            call.after = join(&[
                input
                    .get("turn")
                    .and_then(Value::as_i64)
                    .map(|t| format!("from turn {t}")),
                setup(&input, &out),
            ]);
            call.sections.extend(prompt_section("prompt"));
        }
        SendPrompt => {
            let steer = input.get("steer") == Some(&Value::Bool(true));
            call.before = if steer {
                verb("Steered", "Steering", "Couldn't steer")
            } else {
                verb("Sent a prompt to", "Sending a prompt to", "Couldn't send a prompt to")
            };
            // "Queued: the agent gets it once its turn ends."
            if !failed && output.starts_with("Queued:") {
                call.after = "queued".into();
            }
            call.sections.extend(prompt_section("prompt"));
        }
        WaitForSession => {
            call.before = verb("Waited for", "Waiting for", "Couldn't wait for");
            if !running && !failed {
                call.after = match (out.get("done").and_then(Value::as_bool), s(&out, "status")) {
                    (Some(false), _) => "still working".into(),
                    (_, Some(st)) if st == "idle" => "finished".into(),
                    (_, Some(st)) => st,
                    _ => String::new(),
                };
            }
            if let Some(reply) = s(&out, "lastReply") {
                call.sections.push(Section::Markdown {
                    label: "Last reply",
                    text: reply,
                });
            }
        }
        ReadSession => {
            call.before = verb("Read", "Reading", "Couldn't read");
            call.after = match input.get("turns").and_then(Value::as_i64) {
                Some(0) => "whole".into(),
                Some(n) if n > 1 => format!("last {n} turns"),
                _ => String::new(),
            };
        }
        StopSession => call.before = verb("Stopped", "Stopping", "Couldn't stop"),
        RenameSession => {
            call.before = verb("Renamed", "Renaming", "Couldn't rename");
            call.after = match s(&input, "title") {
                Some(t) => format!("to “{}”", first_line(&t)),
                None => "back to rad's title".into(),
            };
        }
        SaveDraft => {
            let removing = s(&input, "text").is_none();
            call.before = if removing {
                verb(
                    "Removed the draft of",
                    "Removing the draft of",
                    "Couldn't remove the draft of",
                )
            } else {
                verb("Saved a draft in", "Saving a draft in", "Couldn't save a draft in")
            };
            call.sections
                .extend(s(&input, "text").map(|text| Section::Markdown { label: "Draft", text }));
        }
        ArchiveSession => call.before = verb("Archived", "Archiving", "Couldn't archive"),
        UnarchiveSession => call.before = verb("Unarchived", "Unarchiving", "Couldn't unarchive"),
        ReadDiff => {
            call.before = verb(
                "Read the changes of",
                "Reading the changes of",
                "Couldn't read the changes of",
            );
            call.after = input
                .get("turn")
                .and_then(Value::as_i64)
                .map(|t| format!("in turn {t}"))
                .unwrap_or_default();
        }
        SearchSessions => {
            let q = s(&input, "query").unwrap_or_default();
            call.before = verb(
                "Searched sessions for",
                "Searching sessions for",
                "Couldn't search sessions for",
            );
            call.before = format!("{} “{q}”", call.before);
            call.session = None;
            let hits = list(&out, "results", |r| {
                let sess = r.get("session")?;
                let title = s(sess, "title")?;
                Some(match s(r, "snippet") {
                    Some(snip) => format!("**{}** — {}", escape(&title), first_line(&snip)),
                    None => format!("**{}**", escape(&title)),
                })
            });
            if let Some(n) = hits.as_ref().map(Vec::len) {
                call.after = count(n, "session", "sessions");
            }
            call.sections.extend(hits.map(|h| Section::Markdown {
                label: "Results",
                text: h.join("\n"),
            }));
        }
        ListSessions => {
            call.before = verb("Listed sessions", "Listing sessions", "Couldn't list sessions");
            call.session = None;
            let rows = list(&out, "sessions", |r| {
                Some(format!(
                    "**{}** — {}",
                    escape(&s(r, "title")?),
                    s(r, "status").unwrap_or_default()
                ))
            });
            if let Some(n) = rows.as_ref().map(Vec::len) {
                call.after = count(n, "session", "sessions");
            }
            call.sections.extend(rows.map(|r| Section::Markdown {
                label: "Sessions",
                text: r.join("\n"),
            }));
        }
        ListProjects => {
            call.before = verb("Listed projects", "Listing projects", "Couldn't list projects");
            call.session = None;
            let rows = list(&out, "projects", |r| {
                Some(format!(
                    "**{}** — `{}`",
                    escape(&s(r, "name")?),
                    s(r, "path").unwrap_or_default()
                ))
            });
            if let Some(n) = rows.as_ref().map(Vec::len) {
                call.after = count(n, "project", "projects");
            }
            call.sections.extend(rows.map(|r| Section::Markdown {
                label: "Projects",
                text: r.join("\n"),
            }));
        }
        ListAgents => {
            call.before = verb("Listed agents", "Listing agents", "Couldn't list agents");
            call.session = None;
            let rows = list(&out, "agents", |r| Some(format!("**{}**", escape(&s(r, "name")?))));
            if let Some(n) = rows.as_ref().map(Vec::len) {
                call.after = count(n, "agent", "agents");
            }
            call.sections.extend(rows.map(|r| Section::Markdown {
                label: "Agents",
                text: r.join("\n"),
            }));
        }
        ListBranches => {
            call.before = verb("Listed branches", "Listing branches", "Couldn't list branches");
            call.session = None;
            call.after = s(&input, "project")
                .map(|p| format!("of {}", base_name(&p)))
                .unwrap_or_default();
        }
    }
    let shown = call.sections.iter().any(|sec| {
        !matches!(
            sec,
            Section::Markdown {
                label: "Prompt" | "Draft",
                ..
            }
        )
    });
    // What the call answered, where the sections don't say it already.
    if failed {
        if !output.trim().is_empty() {
            call.sections.push(Section::Text {
                label: "Error",
                text: output.trim().to_string(),
            });
        }
    } else if !shown && !output.trim().is_empty() && !matches!(kind, StartSession | ForkSession) {
        call.sections.push(Section::Text {
            label: "Answer",
            text: output.trim_end().to_string(),
        });
    }
    call
}

/// The agent, model and mode a session started with, and its worktree.
fn setup(input: &Value, out: &Value) -> Option<String> {
    let get = |k: &str| {
        out.get(k)
            .or_else(|| input.get(k))
            .and_then(Value::as_str)
            .filter(|s| !s.is_empty())
            .map(str::to_string)
    };
    let worktree = out.get("worktree").or_else(|| input.get("worktree")) == Some(&Value::Bool(true));
    let parts: Vec<String> = [
        get("agent"),
        get("model"),
        worktree.then(|| match get("branch") {
            Some(b) => format!("worktree {b}"),
            None => "worktree".into(),
        }),
    ]
    .into_iter()
    .flatten()
    .collect();
    (!parts.is_empty()).then(|| parts.join(" · "))
}

fn join(parts: &[Option<String>]) -> String {
    parts.iter().flatten().cloned().collect::<Vec<_>>().join(" · ")
}

/// The rows of a list in a tool's answer, as Markdown list items.
fn list(out: &Value, key: &str, row: impl Fn(&Value) -> Option<String>) -> Option<Vec<String>> {
    let rows = out.get(key)?.as_array()?;
    Some(rows.iter().filter_map(|r| row(r).map(|t| format!("- {t}"))).collect())
}

fn count(n: usize, one: &str, many: &str) -> String {
    if n == 1 {
        format!("1 {one}")
    } else {
        format!("{n} {many}")
    }
}

fn first_line(s: &str) -> String {
    let line = s.trim().lines().next().unwrap_or_default();
    let mut out: String = line.chars().take(80).collect();
    if line.chars().count() > 80 || s.trim().lines().count() > 1 {
        out.push('…');
    }
    out
}

fn base_name(path: &str) -> String {
    path.trim_end_matches('/')
        .rsplit('/')
        .next()
        .unwrap_or(path)
        .to_string()
}

/// Text as Markdown shows it as it is.
fn escape(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        if matches!(c, '\\' | '`' | '*' | '_' | '[' | ']' | '<' | '>' | '#' | '|') {
            out.push('\\');
        }
        out.push(c);
    }
    out
}

/// A report's words after its session's pill: "finished", "failed: …".
pub fn report_words(text: &str) -> String {
    let rest = match crate::store::mentions::mentions(text).first() {
        Some(m) => &text[m.range.end..],
        None => text,
    };
    rest.trim().trim_end_matches('.').to_string()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::protocol::{ItemKind, ToolKind};
    use serde_json::json;

    fn call(name: &str, status: ItemStatus, input: Value, output: &str) -> Option<RadCall> {
        describe(&Item {
            id: "i".into(),
            kind: ItemKind::ToolCall,
            status,
            tool: Some(ToolCall {
                name: name.into(),
                kind: ToolKind::Other,
                title: Some(name.into()),
                input: Some(input),
                output: (!output.is_empty()).then(|| output.into()),
                exit_code: None,
                paths: vec![],
            }),
            ..Default::default()
        })
    }

    #[test]
    fn knows_the_names_agents_give_them() {
        assert_eq!(rad_tool("mcp__rad__start_session"), Some(RadTool::StartSession));
        assert_eq!(rad_tool("rad.wait_for_session"), Some(RadTool::WaitForSession));
        assert_eq!(rad_tool("rad/read_session"), Some(RadTool::ReadSession));
        assert_eq!(rad_tool("mcp__other__start_session"), None);
        assert_eq!(rad_tool("rad.nope"), None);
        assert_eq!(rad_tool("Bash"), None);
        assert!(call("Bash", ItemStatus::Completed, json!({"command": "ls"}), "").is_none());
    }

    #[test]
    fn a_started_session_is_the_one_made() {
        let input = json!({"project": "/Users/me/app/", "prompt": "Fix the tests\nall of them", "worktree": true});
        let out = r#"{"id": "s2", "title": "Fix the tests", "agent": "claude", "model": "opus", "worktree": true, "branch": "ra/fix", "status": "running"}"#;
        let c = call("mcp__rad__start_session", ItemStatus::Completed, input.clone(), out).unwrap();
        assert_eq!(c.before, "Started");
        assert_eq!(
            c.session,
            Some(SessionRef {
                id: "s2".into(),
                title: Some("Fix the tests".into())
            })
        );
        assert_eq!(c.after, "in app · claude · opus · worktree ra/fix");
        assert_eq!(
            c.sections,
            vec![Section::Markdown {
                label: "Prompt",
                text: "Fix the tests\nall of them".into()
            }]
        );
        // While it starts: the prompt's first line.
        let c = call("rad.start_session", ItemStatus::InProgress, input.clone(), "").unwrap();
        assert_eq!(c.before, "Starting");
        assert_eq!(c.session.unwrap().title.as_deref(), Some("Fix the tests…"));
        // Refused: why.
        let c = call(
            "mcp__rad__start_session",
            ItemStatus::Failed,
            input,
            "no project \"/x\"",
        )
        .unwrap();
        assert_eq!(c.before, "Couldn't start");
        assert_eq!(
            c.sections.last(),
            Some(&Section::Text {
                label: "Error",
                text: "no project \"/x\"".into()
            })
        );
    }

    #[test]
    fn waits_and_prompts_name_their_session() {
        let c = call(
            "mcp__rad__wait_for_session",
            ItemStatus::Completed,
            json!({"session": "s2"}),
            r#"{"session": "s2", "status": "idle", "done": true, "lastReply": "All **green**."}"#,
        )
        .unwrap();
        assert_eq!((c.before.as_str(), c.after.as_str()), ("Waited for", "finished"));
        assert_eq!(c.session.unwrap().id, "s2");
        assert_eq!(
            c.sections,
            vec![Section::Markdown {
                label: "Last reply",
                text: "All **green**.".into()
            }]
        );
        let c = call(
            "rad.wait_for_session",
            ItemStatus::Completed,
            json!({"session": "s2"}),
            r#"{"session": "s2", "status": "working", "done": false}"#,
        )
        .unwrap();
        assert_eq!(c.after, "still working");

        let c = call(
            "mcp__rad__send_prompt",
            ItemStatus::Completed,
            json!({"session": "s2a1b3", "prompt": "and the docs"}),
            "Queued: the agent gets it once its turn ends.",
        )
        .unwrap();
        assert_eq!((c.before.as_str(), c.after.as_str()), ("Sent a prompt to", "queued"));
        assert_eq!(c.session.unwrap().id, "s2a1b3");
        let c = call(
            "mcp__rad__send_prompt",
            ItemStatus::Completed,
            json!({"session": "s2", "prompt": "x", "steer": true}),
            "Steered: …",
        )
        .unwrap();
        assert_eq!((c.before.as_str(), c.after.as_str()), ("Steered", ""));

        let c = call(
            "mcp__rad__read_session",
            ItemStatus::Completed,
            json!({"session": "s2", "turns": 0}),
            "Session s2…",
        )
        .unwrap();
        assert_eq!((c.before.as_str(), c.after.as_str()), ("Read", "whole"));
        assert_eq!(
            c.sections,
            vec![Section::Text {
                label: "Answer",
                text: "Session s2…".into()
            }]
        );
    }

    #[test]
    fn lists_and_searches_say_what_they_found() {
        let c = call(
            "mcp__rad__search_sessions",
            ItemStatus::Completed,
            json!({"query": "flaky"}),
            r#"{"results": [{"session": {"id": "s1", "title": "Fix *it*"}, "snippet": "the **flaky** test"}]}"#,
        )
        .unwrap();
        assert_eq!(c.before, "Searched sessions for “flaky”");
        assert_eq!(c.after, "1 session");
        assert_eq!(c.session, None);
        assert_eq!(
            c.sections,
            vec![Section::Markdown {
                label: "Results",
                text: "- **Fix \\*it\\*** — the **flaky** test".into()
            }]
        );
        let c = call(
            "rad.list_sessions",
            ItemStatus::Completed,
            json!({}),
            r#"{"sessions": [{"id": "a", "title": "A", "status": "idle"}, {"id": "b", "title": "B", "status": "running"}]}"#,
        )
        .unwrap();
        assert_eq!((c.before.as_str(), c.after.as_str()), ("Listed sessions", "2 sessions"));
    }

    #[test]
    fn reports_say_how_the_turn_ended() {
        assert_eq!(report_words("@[Fix it](session:s2) finished."), "finished");
        assert_eq!(report_words("@[Fix it](session:s2) failed: boom."), "failed: boom");
        assert_eq!(report_words("was stopped."), "was stopped");
    }
}
