//! Updating the app from the Mac that builds it, for development: no feed and
//! no Sparkle, only ssh. `scripts/bundle.sh` writes `build.json` into the
//! bundle and installs it in /Applications. The app on another Mac reads that
//! Mac's `build.json` over ssh, pulls the bundle from the same path with tar,
//! checks that its signature is the running app's, and swaps it in once the
//! app has quit.

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

/// The running app's bundle, when it runs from one.
pub fn bundle() -> Option<PathBuf> {
    let exe = std::env::current_exe().ok()?;
    let app = exe.parent()?.parent()?.parent()?;
    (app.extension()? == "app").then(|| app.to_path_buf())
}

/// The running build's `build.json`: a bundle `bundle.sh` made. Without it
/// (`cargo run`, a bare binary) there's nothing to update.
pub fn current() -> Option<BuildInfo> {
    let b = bundle()?;
    let bytes = std::fs::read(info_path(&b)).ok()?;
    serde_json::from_slice(&bytes).ok()
}

fn info_path(bundle: &Path) -> PathBuf {
    bundle.join("Contents/Resources/build.json")
}

/// The build at the same path on `host`.
pub fn remote(host: &str, bundle: &Path) -> Result<BuildInfo, String> {
    let path = info_path(bundle);
    let out = ssh::command(host, &format!("cat {}", quote(&path.to_string_lossy())))
        .output()
        .map_err(|e| format!("Couldn't run ssh: {e}"))?;
    match out.status.code() {
        Some(0) => serde_json::from_slice(&out.stdout).map_err(|e| format!("{host}'s build.json: {e}")),
        Some(ssh::FAILED) => Err(format!("ssh {host} failed: {}", tail(&out.stderr))),
        _ => Err(format!(
            "{host} has no build at {}. Build it there with gpui/scripts/bundle.sh.",
            bundle.display()
        )),
    }
}

/// The folder a build is unpacked into, beside the app so that moving it in
/// is a rename.
fn staging(bundle: &Path) -> PathBuf {
    bundle
        .parent()
        .unwrap_or(Path::new("/Applications"))
        .join(".remote-agent-gpui-update")
}

/// Copies the bundle from `host` and checks it. Returns the new bundle.
fn fetch(host: &str, bundle: &Path) -> Result<PathBuf, String> {
    if bundle.to_string_lossy().contains("/AppTranslocation/") {
        return Err(
            "macOS runs this app from a temporary copy (App Translocation), which can't be replaced. \
             Remove its quarantine with `xattr -dr com.apple.quarantine` on the app and open it again."
                .into(),
        );
    }
    let requirement = designated_requirement(bundle)?;
    let (parent, name) = match (bundle.parent(), bundle.file_name()) {
        (Some(p), Some(n)) => (p, n.to_string_lossy().to_string()),
        _ => return Err(format!("{} isn't in a folder", bundle.display())),
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
                quote(&parent.to_string_lossy()),
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

/// `fetch`, then `relaunch`.
pub fn install(host: &str, bundle: &Path) -> Result<(), String> {
    let new = fetch(host, bundle)?;
    relaunch(&new, bundle)
}

/// Starts a helper that waits for this process to end, moves `new` in place
/// of `bundle` (putting the old one back if that fails) and opens the app.
/// The caller quits the app next.
fn relaunch(new: &Path, bundle: &Path) -> Result<(), String> {
    use std::os::unix::process::CommandExt as _;
    const SCRIPT: &str = r#"
while kill -0 "$1" 2>/dev/null; do sleep 0.2; done
old="$3.old"
rm -rf "$old"
if mv "$3" "$old"; then
    if mv "$2" "$3"; then rm -rf "$old"; else mv "$old" "$3"; fi
fi
rm -rf "$4"
open "$3"
"#;
    Command::new("/bin/sh")
        .args(["-c", SCRIPT, "sh", &std::process::id().to_string()])
        .arg(new)
        .arg(bundle)
        .arg(staging(bundle))
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
}
