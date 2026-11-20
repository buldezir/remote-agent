//! Running commands on another machine with `/usr/bin/ssh`, for pulling
//! builds (`build_update`) and installing rad (`ssh_install`). They need
//! `ssh <host>` to work without a password: a key, or one in the agent.

use std::process::{Command, Stdio};

/// Quotes a word for the remote shell, which ssh hands its command to.
pub fn quote(s: &str) -> String {
    format!("'{}'", s.replace('\'', r"'\''"))
}

/// `/usr/bin/ssh`, or `$RA_GPUI_SSH`: tests put in a stand-in that runs
/// the command on this Mac, in a scratch home.
fn program() -> String {
    std::env::var("RA_GPUI_SSH")
        .ok()
        .filter(|p| !p.is_empty())
        .unwrap_or_else(|| "/usr/bin/ssh".into())
}

/// `ssh <host> <command>`, with nothing on its stdin.
pub fn command(host: &str, command: &str) -> Command {
    let mut c = Command::new(program());
    // BatchMode: fail rather than wait for a password nobody types.
    // accept-new: a host never seen before is trusted the first time, as
    // answering "yes" would; a changed key still fails.
    c.args([
        "-o",
        "BatchMode=yes",
        "-o",
        "ConnectTimeout=10",
        "-o",
        "StrictHostKeyChecking=accept-new",
        "--",
        host,
        command,
    ]);
    c.stdin(Stdio::null());
    c
}

/// `ssh <host> sh -c <script>`: a POSIX script whatever the remote user's
/// login shell is (fish, say).
pub fn script(host: &str, script: &str) -> Command {
    command(host, &format!("sh -c {}", quote(script)))
}

/// ssh's own exit status when it couldn't connect or log in, rather than the
/// remote command failing.
pub const FAILED: i32 = 255;

/// The last lines of a command's error output, for an alert.
pub fn tail(stderr: &[u8]) -> String {
    let text = String::from_utf8_lossy(stderr);
    let lines: Vec<&str> = text.lines().filter(|l| !l.trim().is_empty()).collect();
    lines[lines.len().saturating_sub(3)..].join("\n")
}

/// The address `ssh <host>` connects to, after `~/.ssh/config`'s aliases
/// (`ssh -G`, which doesn't connect).
pub fn hostname(host: &str) -> Option<String> {
    let out = Command::new(program())
        .args(["-G", "--", host])
        .stdin(Stdio::null())
        .stderr(Stdio::null())
        .output()
        .ok()?;
    out.status
        .success()
        .then(|| parse_hostname(&String::from_utf8_lossy(&out.stdout)))
        .flatten()
}

fn parse_hostname(config: &str) -> Option<String> {
    config
        .lines()
        .find_map(|l| l.strip_prefix("hostname "))
        .map(|h| h.trim().to_string())
        .filter(|h| !h.is_empty())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn quotes_for_the_remote_shell() {
        assert_eq!(
            quote("/Applications/Remote Agent GPUI.app"),
            "'/Applications/Remote Agent GPUI.app'"
        );
        assert_eq!(quote("it's"), r"'it'\''s'");
    }

    #[test]
    fn reads_the_hostname_from_ssh_g() {
        let config = "user sasha\nhostname box.example.com\nport 22\nhostkeyalias none\n";
        assert_eq!(parse_hostname(config).as_deref(), Some("box.example.com"));
        assert_eq!(parse_hostname("user sasha\n"), None);
    }

    #[test]
    fn keeps_the_last_lines_of_an_error() {
        assert_eq!(tail(b"a\n\nb\nc\nd\n"), "b\nc\nd");
    }
}
