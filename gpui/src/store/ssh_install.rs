//! Installing rad on a server over ssh, and pairing with it, for a server
//! this Mac reaches with `ssh <host>` without a password.
//!
//! 1. Look for rad on the server: on the login shell's PATH, or in
//!    `~/.local/bin`, where this puts it.
//! 2. Without one, download the latest release's `rad-<os>-<arch>.tar.gz` on
//!    this Mac, check it against the release's `SHA256SUMS`, and unpack it into
//!    the server's `~/.local/bin` through ssh.
//! 3. `rad pair --print-url` for a pairing code and the server's addresses.
//! 4. Unless rad already answers there, `rad install-service`: systemd on
//!    Linux (with lingering, or rad stops as this ssh session ends), launchd
//!    on a Mac. Then wait for it to answer.
//! 5. Redeem the code from this Mac at the link's addresses, then at the
//!    address ssh connects to, as the pairing page does with a pasted link.

use std::io::Write as _;
use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use std::time::Duration;

use sha2::{Digest as _, Sha256};

use crate::store::servers::{PairingLink, SavedServer, pair, parse_pairing_link};
use crate::store::ssh::{self, quote, tail};

/// Where the latest release's assets are (GitHub redirects to the tag's).
const RELEASES: &str = "https://github.com/buldezir/remote-agent/releases/latest/download";
/// rad's port when the link doesn't say.
const DEFAULT_PORT: u16 = 7421;

/// Installs rad on `host` if it has none, starts it, and pairs. `report`
/// hears each step as it starts ("Downloading rad-linux-amd64.tar.gz…").
/// Blocking.
pub fn install_and_pair(host: &str, report: &dyn Fn(String)) -> Result<SavedServer, String> {
    report(format!("Connecting to {host}…"));
    let probe = probe(host)?;
    let rad = match probe.rad.clone() {
        Some(rad) => {
            report(format!("Found {} at {}", probe.version, tilde(&rad, &probe.home)));
            rad
        }
        None => {
            let asset = asset(&probe.os, &probe.arch)?;
            report(format!("Downloading {asset}…"));
            let download = Download::fetch(&asset)?;
            report(format!("Installing rad in ~/.local/bin on {host}…"));
            let (rad, version) = upload(host, &download.file)?;
            report(format!("Installed {version} at {}", tilde(&rad, &probe.home)));
            rad
        }
    };

    report("Making a pairing code…".into());
    let link = pairing_link(host, &rad)?;
    let port = port_of(&link.urls).unwrap_or(DEFAULT_PORT);
    let own = own_urls(&link.urls, port);

    report("Checking whether rad is running…".into());
    // Whether rad answers there, or the server can't say.
    let mut state = health(host, &own, 1)?;
    if state != Health::Serving {
        report("Starting rad as a background service…".into());
        start(host, &rad, &probe.path)?;
        report("Waiting for rad to answer…".into());
        state = health(host, &own, 30)?;
        if state == Health::Down {
            return Err(format!(
                "rad didn't start on {host}. Its log: `journalctl --user-unit remote-agent` on Linux, \
                 ~/Library/Logs/rad.log on a Mac."
            ));
        }
    }

    let ssh_host = ssh::hostname(host);
    let link = PairingLink {
        urls: pairing_urls(&link.urls, ssh_host.as_deref(), port),
        ..link
    };
    report(format!("Pairing with {}…", link.name));
    // Without curl or wget on the server, rad may not answer yet.
    let tries = if state == Health::Unknown { 10 } else { 1 };
    let mut attempt = 0;
    loop {
        attempt += 1;
        match pair(&link) {
            Ok(saved) => return Ok(saved),
            Err(e) if e.code == "unreachable" && attempt < tries => std::thread::sleep(Duration::from_millis(1500)),
            Err(e) if e.code == "unreachable" => {
                return Err(format!(
                    "rad runs on {host}, but this Mac can't reach it at {}. Open port {port} in the server's \
                     firewall, or put both machines on Tailscale, then try again: rad is installed now, \
                     so that only pairs.",
                    link.urls.join(" or ")
                ));
            }
            Err(e) => return Err(e.to_string()),
        }
    }
}

/// What the first ssh learns about the server.
#[derive(Debug, Default, PartialEq)]
struct Probe {
    /// `uname -s`, `uname -m`.
    os: String,
    arch: String,
    /// The login shell's PATH, which `install-service` keeps for the agents.
    path: String,
    /// rad's path, when it has one.
    rad: Option<String>,
    /// "rad 0.29.0".
    version: String,
    /// `$HOME`, to show paths in it with `~`.
    home: String,
}

/// Prints `@@key=value` lines: the login shell's PATH (a non-interactive
/// ssh gets a shorter one, without `~/.local/bin` or the agents' folders),
/// and rad, when it's on it or in `~/.local/bin`. A program called rad that
/// isn't ours doesn't count.
const PROBE: &str = r#"
printf '@@os=%s\n' "$(uname -s)"
printf '@@arch=%s\n' "$(uname -m)"
printf '@@home=%s\n' "$HOME"
p=$("${SHELL:-/bin/sh}" -lc 'printf "\n@@PATH=%s\n" "$PATH"' </dev/null 2>/dev/null | sed -n 's/^@@PATH=//p' | tail -n 1)
[ -n "$p" ] && PATH=$p
printf '@@path=%s\n' "$PATH"
for rad in "$(command -v rad 2>/dev/null)" "$HOME/.local/bin/rad"; do
    [ -n "$rad" ] && [ -x "$rad" ] || continue
    v=$("$rad" version 2>/dev/null) || continue
    case "$v" in rad\ *) ;; *) continue ;; esac
    printf '@@rad=%s\n@@version=%s\n' "$rad" "$v"
    break
done
"#;

fn probe(host: &str) -> Result<Probe, String> {
    let out = run(ssh::script(host, PROBE), host)?;
    if !out.status.success() {
        return Err(format!("Looking around on {host} failed: {}", tail(&out.stderr)));
    }
    Ok(parse_probe(&String::from_utf8_lossy(&out.stdout)))
}

fn parse_probe(text: &str) -> Probe {
    let mut p = Probe::default();
    for line in text.lines() {
        let Some((key, value)) = line.strip_prefix("@@").and_then(|l| l.split_once('=')) else {
            continue;
        };
        let value = value.trim().to_string();
        match key {
            "os" => p.os = value,
            "arch" => p.arch = value,
            "path" => p.path = value,
            "rad" => p.rad = Some(value),
            "version" => p.version = value,
            "home" => p.home = value,
            _ => {}
        }
    }
    p
}

/// The release's tarball for `uname -s` and `uname -m`.
fn asset(os: &str, arch: &str) -> Result<String, String> {
    let goos = match os {
        "Linux" => "linux",
        "Darwin" => "darwin",
        _ => {
            return Err(format!(
                "rad's releases are for Linux and macOS, and this server runs {os}."
            ));
        }
    };
    let goarch = match arch {
        "x86_64" | "amd64" => "amd64",
        "aarch64" | "arm64" => "arm64",
        _ => {
            return Err(format!(
                "rad's releases are for x86-64 and arm64, and this server is {arch}."
            ));
        }
    };
    Ok(format!("rad-{goos}-{goarch}.tar.gz"))
}

/// A release asset downloaded to a folder of its own, removed when dropped.
struct Download {
    dir: PathBuf,
    file: PathBuf,
}

impl Download {
    /// Downloads `asset` and checks it against `SHA256SUMS`. curl, since this
    /// app's HTTP client speaks only plain http, as rad does.
    fn fetch(asset: &str) -> Result<Self, String> {
        let dir = std::env::temp_dir().join(format!("ra-gpui-rad-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).map_err(|e| format!("Can't make {}: {e}", dir.display()))?;
        let download = Self {
            file: dir.join(asset),
            dir,
        };
        let sums = curl(&format!("{RELEASES}/SHA256SUMS"), None)?;
        let sums = String::from_utf8_lossy(&sums);
        let want = checksum(&sums, asset).ok_or_else(|| format!("The release's SHA256SUMS has no {asset}."))?;
        curl(&format!("{RELEASES}/{asset}"), Some(&download.file))?;
        let bytes = std::fs::read(&download.file).map_err(|e| e.to_string())?;
        let got = hex(&Sha256::digest(&bytes));
        if !got.eq_ignore_ascii_case(want) {
            return Err(format!(
                "{asset} doesn't match its checksum in SHA256SUMS, so it wasn't installed."
            ));
        }
        Ok(download)
    }
}

impl Drop for Download {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.dir);
    }
}

fn curl(url: &str, to: Option<&Path>) -> Result<Vec<u8>, String> {
    let mut c = Command::new("/usr/bin/curl");
    c.args(["-fsSL", "--max-time", "300", url]);
    if let Some(to) = to {
        c.arg("-o").arg(to);
    }
    let out = c.output().map_err(|e| format!("Couldn't run curl: {e}"))?;
    if !out.status.success() {
        return Err(format!("Downloading {url} failed: {}", tail(&out.stderr)));
    }
    Ok(out.stdout)
}

/// The hash `SHA256SUMS` gives `name` (`sha256sum`'s format; `*` marks
/// binary mode).
fn checksum<'a>(sums: &'a str, name: &str) -> Option<&'a str> {
    sums.lines().find_map(|l| {
        let (hash, file) = l.split_once(char::is_whitespace)?;
        (file.trim().trim_start_matches('*') == name).then_some(hash)
    })
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

/// Unpacks the tarball on stdin into `~/.local/bin`, through a folder of
/// its own, and checks that it runs before moving it in.
const UPLOAD: &str = r#"
set -e
d=$HOME/.local/bin
mkdir -p "$d"
t=$(mktemp -d "$d/.rad-install.XXXXXX")
trap 'rm -rf "$t"' EXIT
tar -xzf - -C "$t" rad
"$t/rad" version >/dev/null
mv -f "$t/rad" "$d/rad"
printf '@@rad=%s\n@@version=%s\n' "$d/rad" "$("$d/rad" version)"
"#;

/// Copies the tarball to `host` and unpacks it. Returns rad's path and version.
fn upload(host: &str, tarball: &Path) -> Result<(String, String), String> {
    let bytes = std::fs::read(tarball).map_err(|e| e.to_string())?;
    let mut child = ssh::script(host, UPLOAD)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|e| format!("Couldn't run ssh: {e}"))?;
    let mut stdin = child.stdin.take().expect("piped");
    // From a thread of its own, so that a full stdout pipe can't stall it.
    let writer = std::thread::spawn(move || stdin.write_all(&bytes));
    let out = child.wait_with_output().map_err(|e| e.to_string())?;
    let _ = writer.join();
    if !out.status.success() {
        return Err(format!("Installing rad on {host} failed: {}", tail(&out.stderr)));
    }
    let p = parse_probe(&String::from_utf8_lossy(&out.stdout));
    match p.rad {
        Some(rad) => Ok((rad, p.version)),
        None => Err(format!(
            "Installing rad on {host} failed: it didn't say where rad went."
        )),
    }
}

fn pairing_link(host: &str, rad: &str) -> Result<PairingLink, String> {
    let out = run(ssh::script(host, &format!("{} pair --print-url", quote(rad))), host)?;
    let text = String::from_utf8_lossy(&out.stdout);
    let link = text.lines().rev().find_map(parse_pairing_link);
    match link {
        Some(link) if out.status.success() => Ok(link),
        _ => Err(format!("`rad pair` failed on {host}: {}", tail(&out.stderr))),
    }
}

#[derive(Debug, PartialEq)]
enum Health {
    Serving,
    Down,
    /// The server has neither curl nor wget to ask with.
    Unknown,
}

/// Asks rad's `/v1/health` at `urls` from the server itself, up to `tries`
/// times a second apart. Exits 0 when one answers, 2 without curl or wget.
fn health(host: &str, urls: &[String], tries: u32) -> Result<Health, String> {
    let urls: Vec<String> = urls.iter().map(|u| quote(&format!("{u}/v1/health"))).collect();
    let script = format!(
        r#"
get() {{
    if command -v curl >/dev/null 2>&1; then curl -fsS -m 3 "$1" >/dev/null 2>&1
    elif command -v wget >/dev/null 2>&1; then wget -q -O /dev/null -T 3 "$1" 2>/dev/null
    else exit 2; fi
}}
i=0
while :; do
    for u in {urls}; do get "$u" && exit 0; done
    i=$((i + 1))
    [ "$i" -ge {tries} ] && exit 1
    sleep 1
done
"#,
        urls = urls.join(" ")
    );
    let out = run(ssh::script(host, &script), host)?;
    Ok(match out.status.code() {
        Some(0) => Health::Serving,
        Some(2) => Health::Unknown,
        _ => Health::Down,
    })
}

/// `rad install-service` with the login shell's PATH, after turning on
/// lingering on Linux where it can without a password (`sudo -n`), which
/// keeps rad running once this ssh session ends.
fn start(host: &str, rad: &str, path: &str) -> Result<(), String> {
    let script = format!(
        r#"
PATH={path}; export PATH
if [ "$(uname -s)" = Linux ] && command -v loginctl >/dev/null 2>&1; then
    u=$(id -un)
    if [ "$(loginctl show-user "$u" -p Linger --value 2>/dev/null)" != yes ]; then
        loginctl --no-ask-password enable-linger "$u" 2>/dev/null || sudo -n loginctl enable-linger "$u" 2>/dev/null || true
    fi
fi
{rad} install-service 2>&1
"#,
        path = quote(path),
        rad = quote(rad)
    );
    let out = run(ssh::script(host, &script), host)?;
    let text = String::from_utf8_lossy(&out.stdout);
    if !out.status.success() {
        // A rad we couldn't ask (no curl or wget) that runs already.
        if text.contains("rad is already running") {
            return Ok(());
        }
        return Err(format!("`rad install-service` failed on {host}: {}", tail(&out.stdout)));
    }
    if text.contains("rad stops when you log out") {
        let user = text
            .lines()
            .find_map(|l| l.trim().strip_prefix("sudo loginctl enable-linger "))
            .unwrap_or("<you>")
            .to_string();
        return Err(format!(
            "rad's service is installed, but systemd stops it when you log out of {host}, as this install \
             does. Run `sudo loginctl enable-linger {user}` there, then try again."
        ));
    }
    Ok(())
}

/// Runs an ssh command, turning ssh's own failure into the error.
fn run(mut c: Command, host: &str) -> Result<Output, String> {
    let out = c.output().map_err(|e| format!("Couldn't run ssh: {e}"))?;
    if out.status.code() == Some(ssh::FAILED) {
        return Err(format!("ssh {host} failed: {}", tail(&out.stderr)));
    }
    Ok(out)
}

/// `path` with `home` written as `~`.
fn tilde(path: &str, home: &str) -> String {
    match path.strip_prefix(home) {
        Some(rest) if !home.is_empty() && rest.starts_with('/') => format!("~{rest}"),
        _ => path.to_string(),
    }
}

/// A base URL's host and port: `http://[::1]:7421` is `::1` and 7421.
fn host_port(url: &str) -> (&str, Option<u16>) {
    let rest = url.split_once("://").map_or(url, |(_, r)| r);
    if let Some(v6) = rest.strip_prefix('[') {
        let (host, after) = v6.split_once(']').unwrap_or((v6, ""));
        return (host, after.strip_prefix(':').and_then(|p| p.parse().ok()));
    }
    match rest.rsplit_once(':') {
        Some((host, port)) => (host, port.parse().ok()),
        None => (rest, None),
    }
}

fn port_of(urls: &[String]) -> Option<u16> {
    urls.iter().find_map(|u| host_port(u).1)
}

fn is_loopback(host: &str) -> bool {
    host == "localhost" || host.parse::<std::net::IpAddr>().is_ok_and(|ip| ip.is_loopback())
}

fn base_url(host: &str, port: u16) -> String {
    if host.contains(':') {
        format!("http://[{host}]:{port}")
    } else {
        format!("http://{host}:{port}")
    }
}

/// Where the server asks itself for rad's health: its own addresses from
/// the link, and loopback.
fn own_urls(link_urls: &[String], port: u16) -> Vec<String> {
    let mut urls = vec![base_url("127.0.0.1", port)];
    for u in link_urls {
        if !urls.contains(u) {
            urls.push(u.clone());
        }
    }
    urls
}

/// Where this Mac redeems the code: the link's addresses but loopback (the
/// server's, not this Mac's), then the address ssh reaches the server at.
/// When that is loopback, the server is this Mac, and loopback stays.
fn pairing_urls(link_urls: &[String], ssh_host: Option<&str>, port: u16) -> Vec<String> {
    let local = ssh_host.is_some_and(is_loopback);
    let mut urls: Vec<String> = link_urls
        .iter()
        .filter(|u| local || !is_loopback(host_port(u).0))
        .cloned()
        .collect();
    if let Some(h) = ssh_host {
        let u = base_url(h, port);
        if !urls.contains(&u) {
            urls.push(u);
        }
    }
    urls
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reads_what_the_probe_prints() {
        let out = "motd from .profile\n@@os=Linux\n@@arch=x86_64\n@@path=/home/me/.local/bin:/usr/bin\n\
                   @@rad=/home/me/.local/bin/rad\n@@version=rad 0.29.0\n";
        assert_eq!(
            parse_probe(out),
            Probe {
                os: "Linux".into(),
                arch: "x86_64".into(),
                path: "/home/me/.local/bin:/usr/bin".into(),
                rad: Some("/home/me/.local/bin/rad".into()),
                version: "rad 0.29.0".into(),
                home: String::new(),
            }
        );
        assert_eq!(parse_probe("@@os=Darwin\n@@arch=arm64\n").rad, None);
    }

    #[test]
    fn shows_paths_in_the_home_folder_with_a_tilde() {
        assert_eq!(tilde("/home/me/.local/bin/rad", "/home/me"), "~/.local/bin/rad");
        assert_eq!(tilde("/home/meg/rad", "/home/me"), "/home/meg/rad");
        assert_eq!(tilde("/usr/local/bin/rad", ""), "/usr/local/bin/rad");
    }

    #[test]
    fn names_the_release_for_the_server() {
        assert_eq!(asset("Linux", "x86_64").unwrap(), "rad-linux-amd64.tar.gz");
        assert_eq!(asset("Linux", "aarch64").unwrap(), "rad-linux-arm64.tar.gz");
        assert_eq!(asset("Darwin", "arm64").unwrap(), "rad-darwin-arm64.tar.gz");
        assert!(asset("FreeBSD", "amd64").is_err());
        assert!(asset("Linux", "armv7l").is_err());
    }

    #[test]
    fn finds_a_checksum() {
        let sums = "aaa  rad-linux-amd64.tar.gz\nbbb *rad-darwin-arm64.tar.gz\nccc  ra-linux-amd64.tar.gz\n";
        assert_eq!(checksum(sums, "rad-linux-amd64.tar.gz"), Some("aaa"));
        assert_eq!(checksum(sums, "rad-darwin-arm64.tar.gz"), Some("bbb"));
        assert_eq!(checksum(sums, "rad-linux-arm64.tar.gz"), None);
        assert_eq!(hex(&[0x0f, 0xa0]), "0fa0");
    }

    #[test]
    fn splits_base_urls() {
        assert_eq!(host_port("http://100.1.2.3:7421"), ("100.1.2.3", Some(7421)));
        assert_eq!(host_port("http://[fd7a::1]:7421"), ("fd7a::1", Some(7421)));
        assert_eq!(host_port("https://box.ts.net"), ("box.ts.net", None));
        assert_eq!(port_of(&["https://a".into(), "http://b:7499".into()]), Some(7499));
    }

    #[test]
    fn pairs_at_the_links_addresses_then_sshs() {
        let link = vec![
            "http://100.1.2.3:7421".to_string(),
            "http://192.168.1.5:7421".into(),
            "http://127.0.0.1:7421".into(),
        ];
        assert_eq!(
            pairing_urls(&link, Some("box.example.com"), 7421),
            [
                "http://100.1.2.3:7421",
                "http://192.168.1.5:7421",
                "http://box.example.com:7421"
            ]
        );
        assert_eq!(
            pairing_urls(&link, Some("192.168.1.5"), 7421),
            ["http://100.1.2.3:7421", "http://192.168.1.5:7421"],
            "the address ssh uses is in the link already"
        );
        assert_eq!(
            pairing_urls(&["http://127.0.0.1:7421".into()], Some("2001:db8::5"), 7421),
            ["http://[2001:db8::5]:7421"]
        );
        assert_eq!(
            pairing_urls(&["http://127.0.0.1:7499".into()], Some("localhost"), 7499),
            ["http://127.0.0.1:7499", "http://localhost:7499"],
            "a server on this Mac keeps loopback"
        );
        assert_eq!(pairing_urls(&link[..1], None, 7421), ["http://100.1.2.3:7421"]);
    }

    #[test]
    fn asks_the_server_itself_at_loopback_first() {
        assert_eq!(
            own_urls(&["http://100.1.2.3:7421".into(), "http://127.0.0.1:7421".into()], 7421),
            ["http://127.0.0.1:7421", "http://100.1.2.3:7421"]
        );
    }
}
