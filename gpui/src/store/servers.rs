//! Paired servers, and pairing.
//!
//! The servers live in `servers.json` in `~/Library/Application
//! Support/Remote Agent GPUI` (or `$RA_GPUI_HOME`), and their tokens in the
//! login keychain (`tokens.rs`). A file from before the Keychain held its
//! tokens itself; loading it moves them over.

use std::path::{Path, PathBuf};
use std::time::Duration;

use serde::{Deserialize, Serialize};

use crate::protocol::{PairResult, RpcError};
use crate::store::tokens::Tokens;

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct SavedServer {
    pub id: String,
    pub name: String,
    /// Base URLs, the one that worked last first.
    pub urls: Vec<String>,
    pub device_id: String,
    /// In the Keychain, never in the file (an older file may have it).
    #[serde(default, skip_serializing)]
    pub token: String,
}

#[derive(Debug, Default, Serialize, Deserialize)]
struct ServersFile {
    #[serde(default)]
    servers: Vec<SavedServer>,
}

/// `$RA_GPUI_HOME`, when set.
pub fn custom_home() -> Option<String> {
    std::env::var("RA_GPUI_HOME").ok().filter(|h| !h.is_empty())
}

pub fn home_dir() -> PathBuf {
    if let Some(dir) = custom_home() {
        return PathBuf::from(dir);
    }
    let home = std::env::var_os("HOME")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."));
    home.join("Library/Application Support/Remote Agent GPUI")
}

pub struct ServerStore {
    path: PathBuf,
    tokens: Tokens,
    pub servers: Vec<SavedServer>,
}

impl ServerStore {
    pub fn load(dir: &Path, tokens: Tokens) -> Self {
        let path = dir.join("servers.json");
        let mut servers = std::fs::read(&path)
            .ok()
            .and_then(|b| serde_json::from_slice::<ServersFile>(&b).ok())
            .map(|f| f.servers)
            .unwrap_or_default();
        let mut migrated = false;
        for s in &mut servers {
            if !s.token.is_empty() {
                // An older file's token: into the Keychain, and out of the file.
                match tokens.set(&s.id, &s.token) {
                    Ok(()) => migrated = true,
                    Err(e) => eprintln!("{}: {e}", s.name),
                }
                continue;
            }
            match tokens.get(&s.id) {
                Ok(Some(token)) => s.token = token,
                // rad turns the connection away, and the sidebar says to pair again.
                Ok(None) => eprintln!("{}: no token in the Keychain", s.name),
                Err(e) => eprintln!("{}: {e}", s.name),
            }
        }
        let store = Self { path, tokens, servers };
        if migrated && let Err(e) = store.write() {
            eprintln!("servers.json: {e}");
        }
        store
    }

    /// Adds the server, or replaces the one with its id.
    pub fn save(&mut self, server: SavedServer) -> std::io::Result<()> {
        self.tokens
            .set(&server.id, &server.token)
            .map_err(std::io::Error::other)?;
        match self.servers.iter_mut().find(|s| s.id == server.id) {
            Some(s) => *s = server,
            None => self.servers.push(server),
        }
        self.write()
    }

    pub fn remove(&mut self, id: &str) -> std::io::Result<()> {
        self.tokens.delete(id);
        self.servers.retain(|s| s.id != id);
        self.write()
    }

    /// Puts the URL that worked first.
    pub fn promote(&mut self, id: &str, url: &str) -> std::io::Result<()> {
        let Some(s) = self.servers.iter_mut().find(|s| s.id == id) else {
            return Ok(());
        };
        if s.urls.first().map(String::as_str) == Some(url) {
            return Ok(());
        }
        s.urls.retain(|u| u != url);
        s.urls.insert(0, url.to_string());
        self.write()
    }

    fn write(&self) -> std::io::Result<()> {
        use std::io::Write;
        use std::os::unix::fs::OpenOptionsExt;
        if let Some(dir) = self.path.parent() {
            std::fs::create_dir_all(dir)?;
        }
        let body = serde_json::to_vec_pretty(&ServersFile {
            servers: self.servers.clone(),
        })?;
        let tmp = self.path.with_extension("json.tmp");
        let mut f = std::fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .mode(0o600)
            .open(&tmp)?;
        f.write_all(&body)?;
        f.sync_all()?;
        std::fs::rename(&tmp, &self.path)
    }
}

/// A parsed `remoteagent://pair?…` link from `rad pair`.
#[derive(Debug, Clone, PartialEq)]
pub struct PairingLink {
    pub name: String,
    pub code: String,
    pub urls: Vec<String>,
}

pub fn parse_pairing_link(text: &str) -> Option<PairingLink> {
    let s = text.trim();
    if !s.starts_with("remoteagent://pair") {
        return None;
    }
    let (_, query) = s.split_once('?')?;
    let mut name = String::new();
    let mut code = String::new();
    let mut urls = Vec::new();
    for (k, v) in url::form_urlencoded::parse(query.as_bytes()) {
        match k.as_ref() {
            "name" => name = v.into_owned(),
            "code" => code = v.into_owned(),
            "url" => {
                let u = v.trim_end_matches('/').to_string();
                if is_base_url(&u) {
                    urls.push(u);
                }
            }
            _ => {}
        }
    }
    if code.is_empty() || urls.is_empty() {
        return None;
    }
    if name.is_empty() {
        name = "Server".into();
    }
    Some(PairingLink { name, code, urls })
}

fn is_base_url(u: &str) -> bool {
    let rest = u.strip_prefix("http://").or_else(|| u.strip_prefix("https://"));
    rest.is_some_and(|r| !r.is_empty() && !r.contains('/'))
}

/// The name this client pairs as: "GPUI on <host>", without the host's
/// domain (`.local`, `.internal`, …).
pub fn device_name() -> String {
    let full = gethostname::gethostname().to_string_lossy().into_owned();
    let host = full.split('.').next().unwrap_or_default();
    if host.is_empty() {
        "Remote Agent GPUI".into()
    } else {
        format!("GPUI on {host}")
    }
}

/// Redeems the link's code at each of its URLs in order (blocking), and
/// returns the server to save.
pub fn pair(link: &PairingLink) -> Result<SavedServer, RpcError> {
    let agent: ureq::Agent = ureq::Agent::config_builder()
        .timeout_global(Some(Duration::from_secs(6)))
        .http_status_as_error(false)
        .build()
        .into();
    let body = serde_json::json!({"code": link.code, "deviceName": device_name()});
    let mut last = RpcError::new(
        "unreachable",
        format!("rad isn't reachable at {}", link.urls.join(" or ")),
    );
    for base in &link.urls {
        let Ok(mut resp) = agent.post(&format!("{base}/v1/pair")).send_json(&body) else {
            continue;
        };
        let status = resp.status().as_u16();
        let text = resp.body_mut().read_to_string().unwrap_or_default();
        if status == 200 {
            let res: PairResult = serde_json::from_str(&text)
                .map_err(|e| RpcError::new("invalid", format!("rad's answer didn't parse: {e}")))?;
            let mut urls = vec![base.clone()];
            urls.extend(link.urls.iter().filter(|u| *u != base).cloned());
            return Ok(SavedServer {
                id: res.server_id,
                name: res.name,
                urls,
                device_id: res.device_id,
                token: res.token,
            });
        }
        // rad answered; other URLs won't do better.
        let e: Option<RpcError> = serde_json::from_str(&text).ok();
        match e {
            Some(e) if e.code == "unauthorized" => {
                return Err(RpcError::new(
                    "unauthorized",
                    "The pairing code is wrong or has expired. Run `rad pair` again.",
                ));
            }
            Some(e) if !e.code.is_empty() => return Err(e),
            _ => last = RpcError::new("http", format!("rad answered HTTP {status}")),
        }
    }
    Err(last)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_pairing_links() {
        let link = parse_pairing_link(
            " remoteagent://pair?v=1&name=studio&code=abc&url=http%3A%2F%2F100.1.2.3%3A7421%2F&url=http://127.0.0.1:7421&url=ftp://x ",
        )
        .unwrap();
        assert_eq!(link.name, "studio");
        assert_eq!(link.code, "abc");
        assert_eq!(link.urls, ["http://100.1.2.3:7421", "http://127.0.0.1:7421"]);
        assert!(parse_pairing_link("remoteagent://pair?code=abc").is_none(), "no URLs");
        assert!(
            parse_pairing_link("remoteagent://pair?url=http://h:1").is_none(),
            "no code"
        );
        assert!(parse_pairing_link("https://example.com/?code=a&url=http://h:1").is_none());
        assert_eq!(
            parse_pairing_link("remoteagent://pair?code=a&url=http://h:1")
                .unwrap()
                .name,
            "Server"
        );
    }

    fn scratch() -> std::path::PathBuf {
        std::env::temp_dir().join(format!("ra-gpui-test-{}", uuid::Uuid::new_v4()))
    }

    type Memory = std::rc::Rc<std::cell::RefCell<std::collections::HashMap<String, String>>>;

    #[test]
    fn keeps_servers_in_the_file_and_tokens_out_of_it() {
        use std::os::unix::fs::PermissionsExt;
        let dir = scratch();
        let memory = Memory::default();
        let mut store = ServerStore::load(&dir, Tokens::Memory(memory.clone()));
        assert!(store.servers.is_empty());
        let server = SavedServer {
            id: "srv1".into(),
            name: "studio".into(),
            urls: vec!["http://a:1".into(), "http://b:1".into()],
            device_id: "dev1".into(),
            token: "secret".into(),
        };
        store.save(server.clone()).unwrap();
        store.promote("srv1", "http://b:1").unwrap();
        let file = std::fs::read_to_string(dir.join("servers.json")).unwrap();
        assert!(!file.contains("secret"), "the token is in the file: {file}");
        assert_eq!(memory.borrow().get("srv1").map(String::as_str), Some("secret"));
        let again = ServerStore::load(&dir, Tokens::Memory(memory.clone()));
        assert_eq!(again.servers[0].urls, ["http://b:1", "http://a:1"]);
        assert_eq!(again.servers[0].token, "secret");
        let mode = std::fs::metadata(dir.join("servers.json"))
            .unwrap()
            .permissions()
            .mode();
        assert_eq!(mode & 0o777, 0o600);
        store.remove("srv1").unwrap();
        assert!(memory.borrow().is_empty());
        assert!(ServerStore::load(&dir, Tokens::Memory(memory)).servers.is_empty());
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn moves_an_older_files_tokens_into_the_keychain() {
        let dir = scratch();
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(
            dir.join("servers.json"),
            r#"{"servers":[{"id":"srv1","name":"studio","urls":["http://a:1"],"deviceId":"dev1","token":"old"}]}"#,
        )
        .unwrap();
        let memory = Memory::default();
        let store = ServerStore::load(&dir, Tokens::Memory(memory.clone()));
        assert_eq!(store.servers[0].token, "old");
        assert_eq!(memory.borrow().get("srv1").map(String::as_str), Some("old"));
        let file = std::fs::read_to_string(dir.join("servers.json")).unwrap();
        assert!(!file.contains("old"), "the token stayed in the file: {file}");
        std::fs::remove_dir_all(&dir).unwrap();
    }
}
