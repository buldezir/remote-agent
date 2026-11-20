//! Finding in a transcript, searching sessions on the servers, and the
//! composer's slash-command suggestions, as RAKit's TranscriptFind,
//! SessionSearch and CommandSuggestions have them (ra's
//! `internal/client/search.go` and `commands.go`).

use std::rc::Rc;

use crate::protocol::{Command, Item, SearchResult};

/// The ids of the items whose text holds the query, ignoring case, in the
/// transcript's order. The query is trimmed and taken whole; an empty one
/// finds nothing.
pub fn find_matches(items: &[Rc<Item>], query: &str) -> Vec<String> {
    let q = query.trim().to_lowercase();
    if q.is_empty() {
        return Vec::new();
    }
    items
        .iter()
        .filter(|it| it.search_text().to_lowercase().contains(&q))
        .map(|it| it.id.clone())
        .collect()
}

/// The shortest query, in characters, that the session search searches for.
pub const SEARCH_MIN_LENGTH: usize = 2;

pub fn search_active(query: &str) -> bool {
    query.trim().chars().count() >= SEARCH_MIN_LENGTH
}

/// A session a search found, on the server with id `server_id`.
#[derive(Debug, Clone)]
pub struct Hit {
    pub server_id: String,
    pub result: SearchResult,
}

impl Hit {
    #[cfg(test)]
    pub fn session_id(&self) -> &str {
        self.result.session.as_ref().map(|s| s.id.as_str()).unwrap_or("")
    }
}

/// Interleaves the servers' results, given in the app's order of servers:
/// every server's first, then every server's second, and so on. One
/// server's scores mean nothing to another's, so round-robin is the fair merge.
pub fn merge_search_results(lists: &[Vec<Hit>]) -> Vec<Hit> {
    let mut out = Vec::new();
    for rank in 0.. {
        let mut took = false;
        for l in lists {
            if let Some(h) = l.get(rank) {
                out.push(h.clone());
                took = true;
            }
        }
        if !took {
            break;
        }
    }
    out
}

/// What a composer's text asks to complete: only a leading command, before
/// any whitespace, so arguments and prose stay as they are.
pub fn command_query(draft: &str) -> Option<String> {
    let rest = draft.strip_prefix('/')?;
    if draft.chars().any(char::is_whitespace) {
        return None;
    }
    Some(rest.to_lowercase())
}

/// Ranks the commands that match the draft's query: an exact name, then a
/// prefix (of the name or of its part after the last ":"), then a part of
/// the name, then a part of the description. The first of each name wins,
/// so put the server's before the harness's.
pub fn command_suggestions(draft: &str, commands: &[Command]) -> Vec<Command> {
    let Some(query) = command_query(draft) else {
        return Vec::new();
    };
    let mut seen = std::collections::HashSet::new();
    let mut ranked: Vec<(usize, usize, &Command)> = Vec::new();
    for (i, c) in commands.iter().enumerate() {
        if !seen.insert(c.name.clone()) {
            continue;
        }
        let name = c.name.to_lowercase();
        let short = name.rsplit(':').next().unwrap_or(&name).to_string();
        let desc = c.description.as_deref().unwrap_or("").to_lowercase();
        let rank = if name == query || short == query {
            0
        } else if name.starts_with(&query) || short.starts_with(&query) {
            1
        } else if name.contains(&query) {
            2
        } else if desc.contains(&query) {
            3
        } else {
            continue;
        };
        ranked.push((rank, i, c));
    }
    ranked.sort_by_key(|(rank, i, _)| (*rank, *i));
    ranked.into_iter().map(|(_, _, c)| c.clone()).collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::protocol::{Frame, Notification};

    #[test]
    fn find_matches_the_shared_vectors() {
        let text = std::fs::read_to_string(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../protocol/fixtures/notification_events_session.json"
        ))
        .unwrap();
        let Frame::Notification(Notification::Events { events, .. }) = Frame::parse(&text).unwrap() else {
            panic!()
        };
        let items: Vec<Rc<Item>> = events.into_iter().filter_map(|e| e.item.map(Rc::new)).collect();
        let cases: [(&str, &[&str]); 8] = [
            ("TESTS", &["i1", "i4", "i8"]),
            ("which", &["i6"]),
            ("go test", &["i3"]),
            ("  go test  ", &["i3"]),
            ("test output", &[]),
            ("sub-agent", &[]),
            ("", &[]),
            ("   ", &[]),
        ];
        for (q, want) in cases {
            assert_eq!(find_matches(&items, q), want, "{q:?}");
        }
    }

    fn cmd(name: &str, description: &str) -> Command {
        Command {
            name: name.into(),
            description: Some(description.into()),
            kind: None,
            scope: None,
        }
    }

    fn names(draft: &str, cmds: &[Command]) -> Vec<String> {
        command_suggestions(draft, cmds).into_iter().map(|c| c.name).collect()
    }

    #[test]
    fn suggests_commands() {
        let cmds = [
            cmd("compact", "Summarize the conversation"),
            cmd("skill:prototype", "Prototype to answer a design question"),
            cmd("review", "Review a pull request"),
            cmd("compact", "a duplicate"),
        ];
        assert_eq!(names("/", &cmds), ["compact", "skill:prototype", "review"]);
        assert_eq!(names("/proto", &cmds), ["skill:prototype"]);
        assert_eq!(names("/design", &cmds), ["skill:prototype"]);
        assert!(names("/compact now", &cmds).is_empty());
        assert!(names("compact", &cmds).is_empty());
        let ranked = [
            cmd("a-review", ""),
            cmd("x", "review"),
            cmd("reviewer", ""),
            cmd("review", ""),
        ];
        assert_eq!(names("/review", &ranked), ["review", "reviewer", "a-review", "x"]);
    }

    #[test]
    fn merges_round_robin() {
        let hit = |s: &str, id: &str| Hit {
            server_id: s.into(),
            result: serde_json::from_value(serde_json::json!({
                "session": {"id": id, "projectId": "p", "harness": "fake"}, "snippet": [], "matches": 1
            }))
            .unwrap(),
        };
        let merged = merge_search_results(&[vec![hit("a", "1"), hit("a", "2"), hit("a", "3")], vec![hit("b", "4")]]);
        let ids: Vec<&str> = merged.iter().map(|h| h.session_id()).collect();
        assert_eq!(ids, ["1", "4", "2", "3"]);
        assert!(search_active(" ab "));
        assert!(!search_active(" a "));
    }
}
