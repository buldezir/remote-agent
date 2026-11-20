//! The app's own settings and what it remembers between launches, in
//! `prefs.json` beside `servers.json`: the sidebar's layout, the transcript's
//! options, notifications, the Dock icon, the agent and commit scope last
//! chosen, the Columns layout's collection, prompts written and not sent,
//! the columns' widths and where the window was.

use std::collections::HashMap;
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

/// The agent a new session form starts with when the server names no default.
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct AgentChoice {
    pub harness: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub model: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub effort: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub mode: Option<String>,
}

fn yes() -> bool {
    true
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase", default)]
pub struct Prefs {
    /// "inbox", "projects" or "columns".
    pub sidebar_layout: String,
    /// Fold runs of tool calls into one row.
    #[serde(default = "yes")]
    pub group_tool_calls: bool,
    /// Post a notification when a session needs you, finishes or fails while
    /// the window isn't in front.
    #[serde(default = "yes")]
    pub notifications: bool,
    /// The Dock icon counts the sessions that need you (`store::dock`).
    pub dock_badge: bool,
    /// How the Dock icon bounces: "off", "once" or "untilActive".
    pub dock_bounce: String,
    /// Archived sessions in search results.
    #[serde(default = "yes")]
    pub search_archived: bool,
    /// The interface text size, in points.
    pub text_size: f32,
    /// Agent replies' text size, in points.
    pub message_size: f32,
    /// The font family of prompts and replies; empty for the system font.
    pub message_font: String,
    /// The font family of code: in replies, tool calls, diffs and the
    /// terminal; empty for the default, Menlo.
    pub code_font: String,
    /// Code's text size, in points: in replies, tool calls, diffs and the
    /// terminal.
    pub code_size: f32,
    /// The agent last used, per server id.
    pub last_agent: HashMap<String, AgentChoice>,
    /// The project last used, per server id.
    pub last_project: HashMap<String, String>,
    pub commit_scope: String,
    /// The Columns layout's collection: "<serverId> <collection key>".
    pub column: String,
    /// Projects folded shut in the Projects layout: "<serverId>/<projectId>".
    pub folded_projects: Vec<String>,
    /// Prompts written and not sent, by session id.
    pub drafts: HashMap<String, String>,
    /// Where a session's terminal opens: "over" the messages, "below" or "right".
    pub terminal_placement: String,
    /// The terminal's height below the messages, and its width on their right, in points.
    pub terminal_height: f32,
    pub terminal_width: f32,
    #[serde(default = "yes")]
    pub terminal_copy_on_select: bool,
    /// Diffs wrap their lines.
    #[serde(default = "yes")]
    pub diff_wrap: bool,
    /// "system", "light" or "dark".
    pub appearance: String,
    /// Servers folded shut in the Projects layout, by id.
    pub collapsed_servers: Vec<String>,
    /// The session open when the app quit: "<serverId> <sessionId>".
    pub open_session: String,
    /// Sessions opened lately, the latest first: "<serverId> <sessionId>".
    pub recent_sessions: Vec<String>,
    /// The sidebar's width, in points.
    pub sidebar_width: f32,
    /// The Columns layout's middle column's width, in points.
    pub column_width: f32,
    /// The sidebar, and the Columns layout's middle column, are hidden.
    pub sidebar_hidden: bool,
    /// The prompt box keeps to `prompt_box::LIMITED_WIDTH`, in a centred
    /// column; otherwise it's as wide as the window lets it.
    pub limit_prompt_width: bool,
    /// The transcript's messages keep to the same width, in the same column.
    pub limit_message_width: bool,
    /// The window's frame when it last moved; none until it has.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub window: Option<WindowFrame>,
    /// The ssh host that Check for Update pulls builds from.
    pub update_host: String,
}

/// Where the window was: its top-left corner on its display, in points from
/// the display's top-left, and its size.
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase", default)]
pub struct WindowFrame {
    pub x: f32,
    pub y: f32,
    pub width: f32,
    pub height: f32,
    pub fullscreen: bool,
    /// The display's UUID, which outlives a restart; empty when unknown.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub display: String,
}

impl Default for Prefs {
    fn default() -> Self {
        Self {
            sidebar_layout: "inbox".into(),
            group_tool_calls: true,
            notifications: true,
            dock_badge: false,
            dock_bounce: "off".into(),
            search_archived: true,
            text_size: 14.,
            message_size: 14.,
            message_font: String::new(),
            code_font: String::new(),
            code_size: 13.,
            last_agent: HashMap::new(),
            last_project: HashMap::new(),
            commit_scope: String::new(),
            column: String::new(),
            folded_projects: Vec::new(),
            drafts: HashMap::new(),
            terminal_placement: "over".into(),
            terminal_height: 280.,
            terminal_width: 440.,
            terminal_copy_on_select: true,
            diff_wrap: true,
            appearance: "system".into(),
            collapsed_servers: Vec::new(),
            open_session: String::new(),
            recent_sessions: Vec::new(),
            sidebar_width: 290.,
            column_width: 330.,
            sidebar_hidden: false,
            limit_prompt_width: false,
            limit_message_width: false,
            window: None,
            update_host: String::new(),
        }
    }
}

pub struct PrefsStore {
    path: PathBuf,
    pub prefs: Prefs,
}

impl PrefsStore {
    pub fn load(dir: &Path) -> Self {
        let path = dir.join("prefs.json");
        let prefs = std::fs::read(&path)
            .ok()
            .and_then(|b| serde_json::from_slice(&b).ok())
            .unwrap_or_default();
        Self { path, prefs }
    }

    /// Changes the prefs and writes them; errors only go to the log, since
    /// prefs are a convenience.
    pub fn update(&mut self, f: impl FnOnce(&mut Prefs)) {
        let before = self.prefs.clone();
        f(&mut self.prefs);
        if self.prefs != before
            && let Err(e) = self.write()
        {
            eprintln!("prefs.json: {e}");
        }
    }

    fn write(&self) -> std::io::Result<()> {
        if let Some(dir) = self.path.parent() {
            std::fs::create_dir_all(dir)?;
        }
        let tmp = self.path.with_extension("json.tmp");
        std::fs::write(&tmp, serde_json::to_vec_pretty(&self.prefs)?)?;
        std::fs::rename(&tmp, &self.path)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn round_trips_and_defaults() {
        let dir = std::env::temp_dir().join(format!("ra-gpui-prefs-{}", uuid::Uuid::new_v4()));
        let mut store = PrefsStore::load(&dir);
        assert!(store.prefs.group_tool_calls && store.prefs.notifications);
        store.update(|p| {
            p.sidebar_layout = "columns".into();
            p.drafts.insert("s1".into(), "half a thought".into());
        });
        let again = PrefsStore::load(&dir);
        assert_eq!(again.prefs.sidebar_layout, "columns");
        assert_eq!(again.prefs.drafts["s1"], "half a thought");
        // An older file without newer keys takes their defaults.
        std::fs::write(dir.join("prefs.json"), r#"{"sidebarLayout":"projects"}"#).unwrap();
        let old = PrefsStore::load(&dir);
        assert!(old.prefs.group_tool_calls);
        assert_eq!(old.prefs.text_size, 14.);
        std::fs::remove_dir_all(&dir).unwrap();
    }
}
