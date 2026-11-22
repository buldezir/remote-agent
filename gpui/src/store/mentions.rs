//! Mentions of other sessions in a prompt: typing `@` lists the server's
//! sessions, and picking one writes `@[title](session:<id>)`, which rad tells
//! the agent to read with its read_session tool. Transcripts show a mention
//! as `@title`. The rules are every client's, and their tests share
//! `protocol/vectors/session_mentions.json`.

use std::ops::Range;

/// The most sessions the picker lists.
pub const MAX_SUGGESTIONS: usize = 8;
/// A longer query is prose, not a session's name.
const MAX_QUERY: usize = 50;

/// An `@` being typed: where it starts (a byte offset) and what follows it,
/// up to the cursor.
#[derive(Debug, Clone, PartialEq)]
pub struct MentionQuery {
    pub start: usize,
    pub query: String,
}

/// The `@` before the cursor, on its line, at the start or after a space,
/// with no space right after it and not yet a mention.
pub fn mention_query(text: &str, cursor: usize) -> Option<MentionQuery> {
    let before = text.get(..cursor)?;
    let line = before.rfind('\n').map_or(0, |i| i + 1);
    let at = line + before[line..].rfind('@')?;
    if before[..at].chars().next_back().is_some_and(|c| !c.is_whitespace()) {
        return None;
    }
    let query = &before[at + 1..];
    if query.starts_with(|c: char| c.is_whitespace() || c == '[') || query.chars().count() > MAX_QUERY {
        return None;
    }
    Some(MentionQuery {
        start: at,
        query: query.to_string(),
    })
}

/// The mention of a session: its title, on one line and without brackets.
pub fn mention_text(title: &str, id: &str) -> String {
    let title: String = title
        .chars()
        .map(|c| match c {
            '[' => '(',
            ']' => ')',
            c if c.is_whitespace() => ' ',
            c => c,
        })
        .collect();
    let title = title.trim();
    let title = if title.is_empty() { "Untitled" } else { title };
    format!("@[{title}](session:{id})")
}

/// The text with the query replaced by the mention and a space, and the
/// cursor after that space.
pub fn complete_mention(text: &str, cursor: usize, q: &MentionQuery, title: &str, id: &str) -> (String, usize) {
    let mention = mention_text(title, id);
    let after = &text[cursor..];
    let space = if after.starts_with(char::is_whitespace) {
        ""
    } else {
        " "
    };
    let result = format!("{}{mention}{space}{after}", &text[..q.start]);
    (result, q.start + mention.len() + 1)
}

/// The sessions that match a query, best first: the title starts with it,
/// a word of the title does, or the title holds it; then the latest. All
/// but the session itself, archived ones too.
pub fn mention_suggestions<'a, T: Candidate>(query: &str, sessions: &'a [T], self_id: &str) -> Vec<&'a T> {
    let q = query.trim().to_lowercase();
    let mut ranked: Vec<(u8, &str, usize, &T)> = Vec::new();
    for (i, s) in sessions.iter().enumerate() {
        if s.id() == self_id {
            continue;
        }
        let Some(rank) = rank(&s.title().to_lowercase(), &q) else {
            continue;
        };
        ranked.push((rank, s.updated_at(), i, s));
    }
    ranked.sort_by(|a, b| a.0.cmp(&b.0).then(b.1.cmp(a.1)).then(a.2.cmp(&b.2)));
    ranked.into_iter().take(MAX_SUGGESTIONS).map(|(.., s)| s).collect()
}

/// A session the picker can list.
pub trait Candidate {
    fn id(&self) -> &str;
    fn title(&self) -> &str;
    fn updated_at(&self) -> &str;
}

impl<T: Candidate> Candidate for std::rc::Rc<T> {
    fn id(&self) -> &str {
        (**self).id()
    }
    fn title(&self) -> &str {
        (**self).title()
    }
    fn updated_at(&self) -> &str {
        (**self).updated_at()
    }
}

impl Candidate for crate::protocol::Session {
    fn id(&self) -> &str {
        &self.id
    }
    fn title(&self) -> &str {
        &self.title
    }
    fn updated_at(&self) -> &str {
        &self.updated_at
    }
}

fn rank(title: &str, q: &str) -> Option<u8> {
    if q.is_empty() || title.starts_with(q) {
        return Some(0);
    }
    let mut prev: Option<char> = None;
    let mut contains = false;
    for (i, c) in title.char_indices() {
        if title[i..].starts_with(q) {
            if prev.is_some_and(|p| !p.is_alphanumeric()) {
                return Some(1);
            }
            contains = true;
        }
        prev = Some(c);
    }
    contains.then_some(2)
}

/// A mention in a text.
#[derive(Debug, Clone, PartialEq)]
pub struct Mention {
    pub range: Range<usize>,
    pub title: String,
    pub id: String,
}

/// The mentions in a text, in order.
pub fn mentions(text: &str) -> Vec<Mention> {
    let mut out = Vec::new();
    let mut from = 0;
    while let Some(i) = text[from..].find("@[") {
        let start = from + i;
        from = start + 2;
        let rest = &text[from..];
        let Some(close) = rest.find(['[', ']', '\n']) else {
            break;
        };
        if !rest[close..].starts_with("](session:") {
            continue;
        }
        let title = &rest[..close];
        let id_from = close + "](session:".len();
        let id_len = rest[id_from..]
            .find(|c: char| !(c.is_ascii_alphanumeric() || c == '-'))
            .unwrap_or(rest.len() - id_from);
        if id_len == 0 || !rest[id_from + id_len..].starts_with(')') {
            continue;
        }
        let end = from + id_from + id_len + 1;
        out.push(Mention {
            range: start..end,
            title: title.to_string(),
            id: rest[id_from..id_from + id_len].to_string(),
        });
        from = end;
    }
    out
}

/// A text as transcripts show it: each mention as `@title`.
pub fn mention_display(text: &str) -> String {
    let list = mentions(text);
    if list.is_empty() {
        return text.to_string();
    }
    let mut out = String::with_capacity(text.len());
    let mut last = 0;
    for m in list {
        out.push_str(&text[last..m.range.start]);
        out.push('@');
        out.push_str(&m.title);
        last = m.range.end;
    }
    out.push_str(&text[last..]);
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde::Deserialize;

    #[derive(Deserialize)]
    #[serde(rename_all = "camelCase")]
    struct Vectors {
        #[serde(rename = "self")]
        self_id: String,
        sessions: Vec<S>,
        query: Vec<QueryCase>,
        suggestions: Vec<SuggestCase>,
        complete: Vec<CompleteCase>,
        mention_text: Vec<TextCase>,
        display: Vec<DisplayCase>,
    }
    #[derive(Deserialize)]
    #[serde(rename_all = "camelCase")]
    struct S {
        id: String,
        title: String,
        updated_at: String,
    }
    #[derive(Deserialize)]
    struct QueryCase {
        text: String,
        query: Option<String>,
    }
    #[derive(Deserialize)]
    struct SuggestCase {
        query: String,
        ids: Vec<String>,
    }
    #[derive(Deserialize)]
    struct CompleteCase {
        text: String,
        id: String,
        result: String,
    }
    #[derive(Deserialize)]
    struct TextCase {
        title: String,
        id: String,
        text: String,
    }
    #[derive(Deserialize)]
    struct DisplayCase {
        text: String,
        display: String,
    }

    impl Candidate for S {
        fn id(&self) -> &str {
            &self.id
        }
        fn title(&self) -> &str {
            &self.title
        }
        fn updated_at(&self) -> &str {
            &self.updated_at
        }
    }

    fn cursor(marked: &str) -> (String, usize) {
        let at = marked.find('|').unwrap();
        (marked.replacen('|', "", 1), at)
    }

    #[test]
    fn shared_vectors() {
        let text = std::fs::read_to_string(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../protocol/vectors/session_mentions.json"
        ))
        .unwrap();
        let v: Vectors = serde_json::from_str(&text).unwrap();
        for c in &v.query {
            let (text, at) = cursor(&c.text);
            assert_eq!(mention_query(&text, at).map(|q| q.query), c.query, "{}", c.text);
        }
        for c in &v.suggestions {
            let ids: Vec<&str> = mention_suggestions(&c.query, &v.sessions, &v.self_id)
                .iter()
                .map(|s| s.id.as_str())
                .collect();
            assert_eq!(ids, c.ids, "{:?}", c.query);
        }
        for c in &v.complete {
            let (text, at) = cursor(&c.text);
            let q = mention_query(&text, at).unwrap();
            let s = v.sessions.iter().find(|s| s.id == c.id).unwrap();
            let (result, at) = complete_mention(&text, at, &q, &s.title, &s.id);
            assert_eq!(format!("{}|{}", &result[..at], &result[at..]), c.result);
        }
        for c in &v.mention_text {
            assert_eq!(mention_text(&c.title, &c.id), c.text);
        }
        for c in &v.display {
            assert_eq!(mention_display(&c.text), c.display, "{}", c.text);
        }
    }

    #[test]
    fn finds_mentions() {
        let text = "a @[Fix](session:s-1) b @[x](other:1) @[y](session:) @[Ünï](session:s-2)";
        let m = mentions(text);
        assert_eq!(m.len(), 2);
        assert_eq!(&text[m[0].range.clone()], "@[Fix](session:s-1)");
        assert_eq!((m[1].title.as_str(), m[1].id.as_str()), ("Ünï", "s-2"));
    }
}
