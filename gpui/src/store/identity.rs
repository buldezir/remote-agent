//! Which app this is. `scripts/bundle.sh` builds two, which can sit side by
//! side in /Applications:
//!
//! - **Remote Agent Dev** (`dev.remote-agent.gpui`), the development build,
//!   which `cargo run` and bare builds are too. It updates from the Mac that
//!   built it, over ssh (`build_update`), and keeps its servers in
//!   `~/Library/Application Support/Remote Agent GPUI` and its tokens under
//!   the Keychain service `dev.remote-agent.gpui`.
//! - **Remote Agent**, the release build (`dev.remote-agent.app`, or the
//!   bundle ID a test build was given). It updates through Sparkle
//!   (`ui::sparkle`), and keeps its servers in `~/Library/Application
//!   Support/<bundle ID>` and its tokens under the bundle ID, so that the two
//!   never share a pairing.

use std::path::PathBuf;
use std::sync::OnceLock;

use core_foundation::bundle::CFBundle;
use core_foundation::string::CFString;

/// The development build's identifier: `cargo run`'s and `bundle.sh`'s.
pub const DEV_ID: &str = "dev.remote-agent.gpui";

#[derive(Debug, Clone, PartialEq)]
pub struct Identity {
    /// The bundle identifier, which names the Keychain service too.
    pub id: String,
    pub release: bool,
    /// Sparkle's feed, from Info.plist: a release build with a version.
    pub feed: Option<String>,
    /// The bundle's version (CFBundleShortVersionString).
    pub version: Option<String>,
}

impl Identity {
    /// From the bundle's identifier and feed. A build outside a bundle, or
    /// in the development bundle, is the development app.
    pub fn of(bundle_id: Option<&str>, feed: Option<&str>) -> Self {
        match bundle_id {
            Some(id) if !id.is_empty() && id != DEV_ID => Identity {
                id: id.to_string(),
                release: true,
                feed: feed.filter(|f| !f.is_empty()).map(str::to_string),
                version: None,
            },
            _ => Identity {
                id: DEV_ID.to_string(),
                release: false,
                feed: None,
                version: None,
            },
        }
    }

    /// The folder in `~/Library/Application Support` that holds its servers
    /// and prefs. The development app's predates the release build.
    pub fn data_folder(&self) -> &str {
        if self.release { &self.id } else { "Remote Agent GPUI" }
    }

    /// What it pairs as on `host`.
    pub fn device_name(&self, host: &str) -> String {
        let app = if self.release {
            "Remote Agent"
        } else {
            "Remote Agent Dev"
        };
        if host.is_empty() {
            app.to_string()
        } else {
            format!("{app} on {host}")
        }
    }
}

/// The running app's identity.
pub fn current() -> &'static Identity {
    static CURRENT: OnceLock<Identity> = OnceLock::new();
    CURRENT.get_or_init(|| {
        let info = CFBundle::main_bundle().info_dictionary();
        let string = |key: &'static str| {
            info.find(CFString::from_static_string(key))
                .and_then(|v| v.downcast::<CFString>())
                .map(|s| s.to_string())
        };
        let id = bundle().and(string("CFBundleIdentifier"));
        Identity {
            version: id.as_ref().and(string("CFBundleShortVersionString")),
            ..Identity::of(id.as_deref(), string("SUFeedURL").as_deref())
        }
    })
}

/// The running app's bundle, when it runs from one.
pub fn bundle() -> Option<PathBuf> {
    let exe = std::env::current_exe().ok()?;
    let app = exe.parent()?.parent()?.parent()?;
    (app.extension()? == "app").then(|| app.to_path_buf())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tells_the_release_build_from_the_development_one() {
        let dev = Identity::of(None, None);
        assert!(!dev.release);
        assert_eq!(dev.id, DEV_ID);
        assert_eq!(dev.data_folder(), "Remote Agent GPUI");
        assert_eq!(dev.device_name("studio"), "Remote Agent Dev on studio");
        assert_eq!(Identity::of(Some(DEV_ID), None), dev);

        let release = Identity::of(Some("dev.remote-agent.app"), Some("https://example.com/feed.xml"));
        assert!(release.release);
        assert_eq!(release.data_folder(), "dev.remote-agent.app");
        assert_eq!(release.feed.as_deref(), Some("https://example.com/feed.xml"));
        assert_eq!(release.device_name(""), "Remote Agent");

        let unversioned = Identity::of(Some("dev.remote-agent.updatetest"), None);
        assert!(unversioned.release && unversioned.feed.is_none());
    }
}
