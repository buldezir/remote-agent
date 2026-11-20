//! Updating the app from the Mac that builds it, for development: no feed and
//! no Sparkle, only ssh. `scripts/bundle.sh` writes `build.json` into the
//! bundle and installs it in /Applications. The app on another Mac reads that
//! Mac's `build.json` over ssh, from the development app in the folder this
//! app is in, pulls that bundle with tar, checks that its signature is the
//! running app's, and swaps it in once the app has quit.

use std::io::Read as _;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

use chrono::{DateTime, Utc};
use serde::Deserialize;

use crate::store::sidebar::short_age_of;
use crate::store::ssh::{self, quote, tail};

/// What `bundle.sh` writes to `Contents/Resources/build.json`.
#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct BuildInfo {
    /// The commit's short hash.
    pub commit: String,
    /// Built with uncommitted changes.
    #[serde(default)]
    pub dirty: bool,
    #[serde(default)]
    pub branch: String,
    /// RFC 3339; what tells two builds apart, since one commit builds many times.
    pub built_at: String,
}

impl BuildInfo {
    /// "3f2a1c4 (with changes) on ra/foo, built 5m ago".
    pub fn summary(&self, now: DateTime<Utc>) -> String {
        let mut s = self.commit.clone();
        if self.dirty {
            s += " (with changes)";
        }
        if !self.branch.is_empty() {
            s += &format!(" on {}", self.branch);
        }
        let age = short_age_of(&self.built_at, now);
        match age.as_str() {
            "" => s,
            "now" => s + ", built just now",
            a if a.ends_with(|c: char| c.is_ascii_digit()) => s + &format!(", built {a}"),
            a => s + &format!(", built {a} ago"),
        }
    }
}

pub use crate::store::identity::bundle;

/// The running build's `build.json`: a development bundle `bundle.sh` made.
/// Without it (`cargo run`, a bare binary, the release build) there's nothing
/// to update from.
pub fn current() -> Option<BuildInfo> {
    let b = bundle()?;
    let bytes = std::fs::read(info_path(&b)).ok()?;
    serde_json::from_slice(&bytes).ok()
}

fn info_path(bundle: &Path) -> PathBuf {
    bundle.join("Contents/Resources/build.json")
}

/// The development app's name, as `bundle.sh` installs it.
pub const APP_NAME: &str = "Remote Agent Dev.app";

/// Where to look for the build on the host: the development app beside this
/// one, then this one's own path. A build installed under an earlier name
/// ("Remote Agent GPUI.app") would otherwise keep reading a copy there that
/// `bundle.sh` no longer writes.
fn candidates(bundle: &Path) -> Vec<PathBuf> {
    let current = bundle.with_file_name(APP_NAME);
    if current == bundle {
        vec![current]
    } else {
        vec![current, bundle.to_path_buf()]
    }
}

/// A build on the host, and where it is there.
#[derive(Debug, Clone, PartialEq)]
pub struct RemoteBuild {
    pub info: BuildInfo,
    pub path: PathBuf,
}

/// The first of `candidates` that holds a build on `host`.
pub fn remote(host: &str, bundle: &Path) -> Result<RemoteBuild, String> {
    let paths = candidates(bundle);
    // Prints the build's path on a line of its own, then its build.json.
    let script = paths
        .iter()
        .map(|p| {
            let p = quote(&p.to_string_lossy());
            format!("if [ -f {p}/Contents/Resources/build.json ]; then echo {p}; cat {p}/Contents/Resources/build.json; exit 0; fi; ")
        })
        .collect::<String>()
        + "exit 1";
    let out = ssh::script(host, &script)
        .output()
        .map_err(|e| format!("Couldn't run ssh: {e}"))?;
    match out.status.code() {
        Some(0) => parse_remote(&out.stdout).map_err(|e| format!("{host}'s build.json: {e}")),
        Some(ssh::FAILED) => Err(format!("ssh {host} failed: {}", tail(&out.stderr))),
        _ => Err(format!(
            "{host} has no build at {}. Build it there with gpui/scripts/bundle.sh.",
            paths[0].display()
        )),
    }
}

fn parse_remote(out: &[u8]) -> Result<RemoteBuild, String> {
    let text = String::from_utf8_lossy(out);
    let (path, json) = text.split_once('\n').ok_or("no build")?;
    let info = serde_json::from_str(json).map_err(|e| e.to_string())?;
    Ok(RemoteBuild {
        info,
        path: PathBuf::from(path),
    })
}

/// The folder a build is unpacked into, beside the app so that moving it in
/// is a rename.
fn staging(bundle: &Path) -> PathBuf {
    bundle
        .parent()
        .unwrap_or(Path::new("/Applications"))
        .join(".remote-agent-gpui-update")
}

/// Copies the bundle at `from` on `host` and checks it against the running
/// `bundle`. Returns the new bundle.
fn fetch(host: &str, from: &Path, bundle: &Path) -> Result<PathBuf, String> {
    if bundle.to_string_lossy().contains("/AppTranslocation/") {
        return Err(
            "macOS runs this app from a temporary copy (App Translocation), which can't be replaced. \
             Remove its quarantine with `xattr -dr com.apple.quarantine` on the app and open it again."
                .into(),
        );
    }
    let requirement = designated_requirement(bundle)?;
    let Some(parent) = bundle.parent() else {
        return Err(format!("{} isn't in a folder", bundle.display()));
    };
    let (from_parent, name) = match (from.parent(), from.file_name()) {
        (Some(p), Some(n)) => (p, n.to_string_lossy().to_string()),
        _ => return Err(format!("{host}'s {} isn't in a folder", from.display())),
    };
    let dir = staging(bundle);
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir(&dir).map_err(|e| format!("Can't write to {}: {e}", parent.display()))?;
    let result = (|| {
        // COPYFILE_DISABLE: no AppleDouble files for the extended attributes.
        let mut ssh = ssh::command(
            host,
            &format!(
                "COPYFILE_DISABLE=1 tar -czf - -C {} {}",
                quote(&from_parent.to_string_lossy()),
                quote(&name)
            ),
        )
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|e| format!("Couldn't run ssh: {e}"))?;
        let stdout = ssh.stdout.take().expect("piped");
        let untar = Command::new("/usr/bin/tar")
            .args(["-xzf", "-", "-C"])
            .arg(&dir)
            .stdin(stdout)
            .stderr(Stdio::piped())
            .output()
            .map_err(|e| format!("Couldn't run tar: {e}"))?;
        let mut ssh_err = Vec::new();
        if let Some(mut e) = ssh.stderr.take() {
            let _ = e.read_to_end(&mut ssh_err);
        }
        let ssh_status = ssh.wait().map_err(|e| e.to_string())?;
        if !ssh_status.success() {
            return Err(format!("Copying from {host} failed: {}", tail(&ssh_err)));
        }
        if !untar.status.success() {
            return Err(format!("Unpacking the build failed: {}", tail(&untar.stderr)));
        }
        let new = dir.join(&name);
        verify(&new, &requirement)?;
        Ok(new)
    })();
    if result.is_err() {
        let _ = std::fs::remove_dir_all(&dir);
    }
    result
}

/// The running app's designated requirement: its identifier and the
/// certificate it was signed with. A new build must meet it, so that only
/// one of the same developer's builds replaces this one, and the Keychain
/// lets it read the tokens this one saved.
fn designated_requirement(bundle: &Path) -> Result<String, String> {
    let out = Command::new("/usr/bin/codesign")
        .args(["-d", "-r-"])
        .arg(bundle)
        .output()
        .map_err(|e| format!("Couldn't run codesign: {e}"))?;
    let text = String::from_utf8_lossy(&out.stdout).to_string() + &String::from_utf8_lossy(&out.stderr);
    let requirement = text
        .lines()
        .find_map(|l| l.strip_prefix("designated => "))
        .ok_or_else(|| {
            format!(
                "This app has no signature to check a new build against: {}",
                tail(&out.stderr)
            )
        })?;
    if requirement.starts_with("cdhash") {
        return Err(
            "This app is signed ad hoc, so a new build can't be checked against it. \
             Install one signed with your Apple Development identity first (gpui/scripts/bundle.sh)."
                .into(),
        );
    }
    Ok(requirement.to_string())
}

fn verify(new: &Path, requirement: &str) -> Result<(), String> {
    let out = Command::new("/usr/bin/codesign")
        .args(["--verify", "--deep", "--strict", "-R"])
        .arg(format!("={requirement}"))
        .arg(new)
        .output()
        .map_err(|e| format!("Couldn't run codesign: {e}"))?;
    if out.status.success() {
        Ok(())
    } else {
        Err(format!(
            "The new build isn't signed as this one is, so it wasn't installed: {}",
            tail(&out.stderr)
        ))
    }
}

/// `fetch`, then `relaunch`. The build goes in beside the running app under
/// its name on the host, and takes the running app's place.
pub fn install(host: &str, from: &Path, bundle: &Path) -> Result<(), String> {
    let new = fetch(host, from, bundle)?;
    let target = match from.file_name() {
        Some(name) => bundle.with_file_name(name),
        None => bundle.to_path_buf(),
    };
    relaunch(&new, &target, bundle)
}

/// Starts a helper that waits for this process to end, moves `new` to
/// `target` (putting what was there back if that fails), removes the running
/// `bundle` when it's elsewhere, and opens the app. The caller quits the app
/// next.
fn relaunch(new: &Path, target: &Path, bundle: &Path) -> Result<(), String> {
    use std::os::unix::process::CommandExt as _;
    const SCRIPT: &str = r#"
while kill -0 "$1" 2>/dev/null; do sleep 0.2; done
old="$3.old"
rm -rf "$old"
if [ ! -e "$3" ] || mv "$3" "$old"; then
    if mv "$2" "$3"; then
        rm -rf "$old"
        [ "$5" != "$3" ] && rm -rf "$5"
    elif [ -e "$old" ]; then
        mv "$old" "$3"
    fi
fi
rm -rf "$4"
if [ -e "$3" ]; then open "$3"; else open "$5"; fi
"#;
    Command::new("/bin/sh")
        .args(["-c", SCRIPT, "sh", &std::process::id().to_string()])
        .arg(new)
        .arg(target)
        .arg(staging(bundle))
        .arg(bundle)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        // Its own process group, so nothing that ends the app's ends it.
        .process_group(0)
        .spawn()
        .map(|_| ())
        .map_err(|e| format!("Couldn't start the installer: {e}"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::TimeZone;

    #[test]
    fn reads_and_sums_up_a_build() {
        let info: BuildInfo = serde_json::from_str(
            r#"{"commit":"3f2a1c4","dirty":true,"branch":"ra/gpui","builtAt":"2026-11-20T10:00:00Z"}"#,
        )
        .unwrap();
        let now = Utc.with_ymd_and_hms(2026, 11, 20, 10, 5, 0).unwrap();
        assert_eq!(info.summary(now), "3f2a1c4 (with changes) on ra/gpui, built 5m ago");
        let just = Utc.with_ymd_and_hms(2026, 11, 20, 10, 0, 20).unwrap();
        let clean: BuildInfo =
            serde_json::from_str(r#"{"commit":"3f2a1c4","builtAt":"2026-11-20T10:00:00Z"}"#).unwrap();
        assert_eq!(clean.summary(just), "3f2a1c4, built just now");
    }

    #[test]
    fn looks_for_the_build_under_its_current_name_first() {
        let old = Path::new("/Applications/Remote Agent GPUI.app");
        assert_eq!(
            candidates(old),
            vec![PathBuf::from("/Applications/Remote Agent Dev.app"), old.to_path_buf()]
        );
        let current = Path::new("/Users/me/Apps/Remote Agent Dev.app");
        assert_eq!(candidates(current), vec![current.to_path_buf()]);
    }

    #[test]
    fn reads_where_the_build_is_and_its_info() {
        let out = b"/Applications/Remote Agent Dev.app\n{\"commit\":\"f491983\",\"branch\":\"main\",\"builtAt\":\"2026-11-20T21:27:26Z\"}\n";
        let build = parse_remote(out).unwrap();
        assert_eq!(build.path, PathBuf::from("/Applications/Remote Agent Dev.app"));
        assert_eq!(build.info.branch, "main");
        assert!(parse_remote(b"").is_err());
    }
}
