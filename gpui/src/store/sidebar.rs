//! How the sidebar groups sessions, as `SidebarLayout` does in the other
//! clients (`web/src/ui/sidebarLayout.ts`, Android's `ui/SidebarLayout.kt`).
//! Three layouts: Inbox, Projects and Columns, and one that only this client
//! has so far: All Projects, every server's projects in one list.

use chrono::{DateTime, Local, Utc};

use std::collections::HashMap;
use std::rc::Rc;

use crate::protocol::{Project, Session, SessionStatus};
use crate::store::index::IndexState;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum Layout {
    #[default]
    Inbox,
    Projects,
    Columns,
    AllProjects,
}

impl Layout {
    /// The layouts, in the order of the View menu and its ⌘1–4.
    pub const ALL: [Layout; 4] = [Layout::Inbox, Layout::Projects, Layout::Columns, Layout::AllProjects];

    pub fn key(self) -> &'static str {
        match self {
            Layout::Inbox => "inbox",
            Layout::Projects => "projects",
            Layout::Columns => "columns",
            Layout::AllProjects => "allProjects",
        }
    }

    pub fn from_key(s: &str) -> Layout {
        match s {
            "projects" => Layout::Projects,
            "columns" => Layout::Columns,
            "allProjects" => Layout::AllProjects,
            _ => Layout::Inbox,
        }
    }

    pub fn title(self) -> &'static str {
        match self {
            Layout::Inbox => "Inbox",
            Layout::Projects => "Projects",
            Layout::Columns => "Columns",
            Layout::AllProjects => "All Projects",
        }
    }
}

/// How pressing a session is, for the Projects layout's server and project
/// dots: waiting for an answer, then failed, unread, working, background work.
pub fn urgency(s: &Session) -> u8 {
    match s.status {
        SessionStatus::AwaitingApproval => 5,
        SessionStatus::Error => 4,
        _ if s.unread => 3,
        SessionStatus::Running => 2,
        SessionStatus::Idle if !s.background_tasks.is_empty() => 1,
        _ => 0,
    }
}

/// The most pressing of the sessions, if any is pressing at all.
pub fn most_pressing<'a>(sessions: impl IntoIterator<Item = &'a Rc<Session>>) -> Option<&'a Rc<Session>> {
    let mut found: Option<&Rc<Session>> = None;
    for s in sessions {
        let u = urgency(s);
        if u > 0 && found.is_none_or(|f| u > urgency(f)) {
            found = Some(s);
        }
    }
    found
}

/// What needs you first, then the rest, each in the order given.
pub fn needs_you_first(sessions: &[Rc<Session>]) -> Vec<Rc<Session>> {
    let mut out: Vec<Rc<Session>> = sessions.iter().filter(|s| needs_you(s)).cloned().collect();
    out.extend(sessions.iter().filter(|s| !needs_you(s)).cloned());
    out
}

/// Projects with sessions, the one with the latest first, then the rest by name.
pub fn projects_in_order(by_name: &[Rc<Project>], active: &[Rc<Session>]) -> Vec<Rc<Project>> {
    let mut latest: HashMap<&str, usize> = HashMap::new();
    for (i, s) in active.iter().enumerate() {
        latest.entry(s.project_id.as_str()).or_insert(i);
    }
    let (mut with, without): (Vec<_>, Vec<_>) = by_name
        .iter()
        .cloned()
        .partition(|p| latest.contains_key(p.id.as_str()));
    with.sort_by_key(|p| latest[p.id.as_str()]);
    with.extend(without);
    with
}

/// A project of one of the servers, in the All Projects layout.
#[derive(Debug, Clone)]
pub struct ServerProject {
    /// Its server, as its place among the servers given.
    pub server: usize,
    pub project: Rc<Project>,
    /// Its unarchived sessions, the latest first.
    pub sessions: Vec<Rc<Session>>,
}

/// What a server gives the All Projects layout.
#[derive(Debug, Clone, Default)]
pub struct ServerLists {
    /// Its projects, by name.
    pub projects: Vec<Rc<Project>>,
    /// Its unarchived sessions, the latest first.
    pub active: Vec<Rc<Session>>,
}

/// Every server's projects in one list, as the Inbox has every server's
/// sessions: those with sessions, the one with the latest first, then the
/// rest by name. Projects stay their servers' own: two of the same name on
/// two servers are two.
pub fn all_projects(servers: &[ServerLists]) -> Vec<ServerProject> {
    let mut with = Vec::new();
    let mut without = Vec::new();
    for (server, lists) in servers.iter().enumerate() {
        for project in &lists.projects {
            let sessions: Vec<Rc<Session>> = lists
                .active
                .iter()
                .filter(|s| s.project_id == project.id)
                .cloned()
                .collect();
            let entry = ServerProject {
                server,
                project: project.clone(),
                sessions,
            };
            if entry.sessions.is_empty() {
                without.push(entry)
            } else {
                with.push(entry)
            }
        }
    }
    // Both sorts keep ties as they came: by server, then by name.
    with.sort_by(|a, b| b.sessions[0].updated_at.cmp(&a.sessions[0].updated_at));
    without.sort_by_key(|e| e.project.name.to_lowercase());
    with.extend(without);
    with
}

/// A collection of the Columns layout.
#[derive(Debug, Clone, PartialEq, Eq, Hash)]
pub enum Collection {
    NeedsYou,
    Running,
    All,
    Archived,
    Project(String),
}

impl Collection {
    pub fn key(&self) -> String {
        match self {
            Collection::NeedsYou => "needsYou".into(),
            Collection::Running => "running".into(),
            Collection::All => "all".into(),
            Collection::Archived => "archived".into(),
            Collection::Project(id) => format!("project {id}"),
        }
    }

    pub fn parse(stored: &str) -> Option<Collection> {
        let parts: Vec<&str> = stored.split_whitespace().collect();
        match parts.as_slice() {
            ["needsYou"] => Some(Collection::NeedsYou),
            ["running"] => Some(Collection::Running),
            ["all"] => Some(Collection::All),
            ["archived"] => Some(Collection::Archived),
            ["project", id] => Some(Collection::Project(id.to_string())),
            _ => None,
        }
    }

    pub fn title(&self, index: &IndexState) -> String {
        match self {
            Collection::NeedsYou => "Needs You".into(),
            Collection::Running => "Running".into(),
            Collection::All => "All Sessions".into(),
            Collection::Archived => "Archived".into(),
            Collection::Project(id) => index
                .projects
                .get(id)
                .map(|p| p.name.clone())
                .unwrap_or_else(|| "Project".into()),
        }
    }

    /// The collection's sessions on one server.
    pub fn sessions(&self, index: &IndexState) -> Vec<Rc<Session>> {
        let active = index.active_sessions();
        match self {
            Collection::NeedsYou => active.into_iter().filter(|s| needs_you(s)).collect(),
            Collection::Running => active.into_iter().filter(|s| is_busy(s)).collect(),
            Collection::All => needs_you_first(&active),
            Collection::Archived => index.archived_sessions(),
            Collection::Project(id) => active.into_iter().filter(|s| &s.project_id == id).collect(),
        }
    }

    /// The collection that shows a session: its project's, or the archive.
    pub fn revealing(s: &Session, index: &IndexState) -> Collection {
        if s.archived {
            Collection::Archived
        } else if index.projects.contains_key(&s.project_id) {
            Collection::Project(s.project_id.clone())
        } else {
            Collection::All
        }
    }
}

/// Waiting for an answer, or failed: what a person has to look at.
pub fn needs_you(s: &Session) -> bool {
    matches!(s.status, SessionStatus::AwaitingApproval | SessionStatus::Error)
}

/// Needs you or is unread: its title is bold in the lists.
pub fn stands_out(s: &Session) -> bool {
    needs_you(s) || s.unread
}

/// Working, or done with tasks left running in the background.
pub fn is_busy(s: &Session) -> bool {
    s.status == SessionStatus::Running || (s.status == SessionStatus::Idle && !s.background_tasks.is_empty())
}

/// The open session, and whether it stood out when it was opened.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct OpenedSession {
    pub id: String,
    pub stood_out: bool,
}

/// Splits sessions into the Inbox's groups, each in the order given: what
/// needs you or is unread, then the rest. The open session counts as unread
/// if it was when it was opened: reading it leaves its row where it is, and
/// so does a turn that ends in view.
pub fn inbox_groups<T: AsRef<Session> + Clone>(active: &[T], opened: &OpenedSession) -> (Vec<T>, Vec<T>) {
    let mut needs = Vec::new();
    let mut recent = Vec::new();
    for s in active {
        let session = s.as_ref();
        let stands = needs_you(session)
            || if !opened.id.is_empty() && session.id == opened.id {
                opened.stood_out
            } else {
                session.unread
            };
        if stands {
            needs.push(s.clone())
        } else {
            recent.push(s.clone())
        }
    }
    (needs, recent)
}

/// A session's age where a row has little room: "now", "4m", "3h", "2d",
/// then the date.
pub fn short_age(t: DateTime<Utc>, now: DateTime<Utc>) -> String {
    let s = (now - t).num_seconds().max(0);
    match s {
        s if s < 60 => "now".into(),
        s if s < 3600 => format!("{}m", s / 60),
        s if s < 86_400 => format!("{}h", s / 3600),
        s if s < 7 * 86_400 => format!("{}d", s / 86_400),
        _ => t.with_timezone(&Local).format("%b %-d").to_string(),
    }
}

/// `short_age` of an RFC 3339 time, or "" when it doesn't parse.
pub fn short_age_of(time: &str, now: DateTime<Utc>) -> String {
    DateTime::parse_from_rfc3339(time)
        .map(|t| short_age(t.with_timezone(&Utc), now))
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::protocol::BackgroundTask;
    use chrono::TimeZone;
    use std::rc::Rc;

    fn sess(id: &str, status: SessionStatus, unread: bool, background: bool) -> Rc<Session> {
        let mut s: Session = serde_json::from_value(serde_json::json!({
            "id": id, "projectId": "p", "harness": "fake", "title": id
        }))
        .unwrap();
        s.status = status;
        s.unread = unread;
        if background {
            s.background_tasks = vec![BackgroundTask {
                id: "b".into(),
                kind: "command".into(),
                description: "".into(),
            }];
        }
        Rc::new(s)
    }

    fn ids(list: &[Rc<Session>]) -> Vec<&str> {
        list.iter().map(|s| s.id.as_str()).collect()
    }

    #[test]
    fn groups_sessions_by_what_they_need() {
        use SessionStatus::*;
        assert!(needs_you(&sess("s", AwaitingApproval, false, false)));
        assert!(needs_you(&sess("s", Error, false, false)));
        assert!(!needs_you(&sess("s", Running, false, false)));
        assert!(is_busy(&sess("s", Running, false, false)));
        assert!(is_busy(&sess("s", Idle, false, true)));
        assert!(!is_busy(&sess("s", Idle, false, false)));
        assert!(!is_busy(&sess("s", Stopped, false, true)));
    }

    #[test]
    fn inbox_puts_what_needs_you_or_is_unread_first() {
        use SessionStatus::*;
        let items = vec![
            sess("running", Running, false, false),
            sess("unread", Idle, true, false),
            sess("background", Idle, false, true),
            sess("waiting", AwaitingApproval, false, false),
            sess("idle", Idle, false, false),
            sess("failed", Error, false, false),
        ];
        assert!(stands_out(&sess("s", Idle, true, false)));
        assert!(!stands_out(&sess("s", Running, false, false)));
        let (needs, recent) = inbox_groups(&items, &OpenedSession::default());
        assert_eq!(ids(&needs), ["unread", "waiting", "failed"]);
        assert_eq!(ids(&recent), ["running", "background", "idle"]);
        // The open session stays where it was when it was opened.
        let read: Vec<Rc<Session>> = items
            .iter()
            .map(|s| {
                let mut c = (**s).clone();
                c.unread = false;
                Rc::new(c)
            })
            .collect();
        let (needs, _) = inbox_groups(
            &read,
            &OpenedSession {
                id: "unread".into(),
                stood_out: true,
            },
        );
        assert_eq!(ids(&needs), ["unread", "waiting", "failed"]);
    }

    #[test]
    fn layouts_keep_their_keys() {
        for layout in Layout::ALL {
            assert_eq!(Layout::from_key(layout.key()), layout);
        }
        assert_eq!(Layout::from_key("allProjects"), Layout::AllProjects);
        assert_eq!(Layout::from_key("something else"), Layout::Inbox);
    }

    #[test]
    fn all_projects_mixes_the_servers() {
        let project = |id: &str, name: &str| -> Rc<Project> {
            Rc::new(
                serde_json::from_value(serde_json::json!({"id": id, "path": format!("/work/{name}"), "name": name}))
                    .unwrap(),
            )
        };
        let session = |id: &str, project: &str, at: &str| -> Rc<Session> {
            Rc::new(
                serde_json::from_value(serde_json::json!({
                    "id": id, "projectId": project, "harness": "fake", "title": id, "updatedAt": at
                }))
                .unwrap(),
            )
        };
        // Each server's projects by name, and its sessions, the latest first.
        let servers = vec![
            ServerLists {
                projects: vec![project("a1", "api"), project("a2", "Docs"), project("a3", "web")],
                active: vec![
                    session("s3", "a3", "2026-10-09T12:03:00Z"),
                    session("s1", "a1", "2026-10-09T12:01:00Z"),
                    session("s0", "a3", "2026-10-09T12:00:00Z"),
                ],
            },
            ServerLists {
                projects: vec![project("b1", "api"), project("b2", "cli")],
                active: vec![session("s2", "b1", "2026-10-09T12:02:00Z")],
            },
        ];
        let list = all_projects(&servers);
        let order: Vec<(usize, &str)> = list.iter().map(|e| (e.server, e.project.id.as_str())).collect();
        // The latest session's project first; the same name on two servers
        // stays two projects; the empty ones last, by name.
        assert_eq!(order, [(0, "a3"), (1, "b1"), (0, "a1"), (1, "b2"), (0, "a2")]);
        assert_eq!(ids(&list[0].sessions), ["s3", "s0"]);
        assert_eq!(ids(&list[1].sessions), ["s2"]);
        assert!(list[3].sessions.is_empty());
        assert!(all_projects(&[]).is_empty());
    }

    #[test]
    fn short_ages() {
        let now = Utc.with_ymd_and_hms(2026, 10, 9, 12, 0, 0).unwrap();
        let ago = |s: i64| short_age(now - chrono::Duration::seconds(s), now);
        assert_eq!(ago(10), "now");
        assert_eq!(ago(4 * 60), "4m");
        assert_eq!(ago(3 * 3600), "3h");
        assert_eq!(ago(2 * 86_400), "2d");
        assert!(ago(30 * 86_400).starts_with("Sep"));
        assert_eq!(short_age_of("2026-10-09T11:56:00Z", now), "4m");
        assert_eq!(short_age_of("bogus", now), "");
    }
}
