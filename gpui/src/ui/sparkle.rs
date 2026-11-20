//! The release build's updates, through Sparkle, as the SwiftUI Mac app had
//! them: it reads the latest release's feed (`SUFeedURL`, written by
//! `apple/scripts/appcast.sh`) once a day, asks before installing unless told
//! to install by itself, and replaces the app when it quits.
//!
//! `scripts/bundle.sh --release <version>` embeds `Sparkle.framework`, which
//! this loads at launch rather than linking, so that `cargo run`, the tests
//! and the development build (which updates over ssh, `build_update`) run
//! without it. Sparkle's own windows show the update and its progress.

use std::cell::RefCell;
use std::ptr;

use objc2::msg_send;
use objc2::rc::{Allocated, Retained};
use objc2::runtime::{AnyClass, AnyObject, Bool};
use objc2_foundation::{NSBundle, NSString};

use crate::store::identity;

thread_local! {
    /// The `SPUStandardUpdaterController`, made on the main thread at launch
    /// and kept for the app's life.
    static CONTROLLER: RefCell<Option<Retained<AnyObject>>> = const { RefCell::new(None) };
}

/// Loads Sparkle and starts its updater, which checks on its schedule. Only
/// a release build with a feed has it. Call once, on the main thread, once
/// the app has launched.
pub fn start() {
    let Some(controller) = make_controller() else {
        return;
    };
    CONTROLLER.with(|c| *c.borrow_mut() = Some(controller));
}

fn make_controller() -> Option<Retained<AnyObject>> {
    identity::current().feed.as_ref()?;
    let path = identity::bundle()?.join("Contents/Frameworks/Sparkle.framework");
    let framework = NSBundle::bundleWithPath(&NSString::from_str(&path.to_string_lossy()))?;
    // SAFETY: Sparkle.framework is the one bundle.sh embedded and signed.
    if !unsafe { framework.load() } {
        eprintln!("Couldn't load {}", path.display());
        return None;
    }
    let class = AnyClass::get(c"SPUStandardUpdaterController")?;
    // SAFETY: -[SPUStandardUpdaterController
    // initWithStartingUpdater:updaterDelegate:userDriverDelegate:], with no
    // delegates, on the main thread.
    unsafe {
        let alloc: Allocated<AnyObject> = msg_send![class, alloc];
        msg_send![
            alloc,
            initWithStartingUpdater: Bool::YES,
            updaterDelegate: ptr::null::<AnyObject>(),
            userDriverDelegate: ptr::null::<AnyObject>()
        ]
    }
}

fn with_controller<R>(f: impl FnOnce(&AnyObject) -> R) -> Option<R> {
    CONTROLLER.with(|c| c.borrow().as_deref().map(f))
}

fn with_updater<R>(f: impl FnOnce(&AnyObject) -> R) -> Option<R> {
    with_controller(|controller| {
        // SAFETY: -[SPUStandardUpdaterController updater], never nil.
        let updater: Retained<AnyObject> = unsafe { msg_send![controller, updater] };
        f(&updater)
    })
}

/// Sparkle runs: Check for Updates… and the Updates settings show.
pub fn available() -> bool {
    with_controller(|_| ()).is_some()
}

/// Remote Agent › Check for Updates…: Sparkle's window says what it finds.
pub fn check() {
    // SAFETY: -[SPUStandardUpdaterController checkForUpdates:], a sender-taking action.
    with_controller(|controller| unsafe {
        let _: () = msg_send![controller, checkForUpdates: ptr::null::<AnyObject>()];
    });
}

/// It looks for updates once a day.
pub fn checks_automatically() -> bool {
    // SAFETY: SPUUpdater's automaticallyChecksForUpdates, a BOOL property.
    with_updater(|u| unsafe { msg_send![u, automaticallyChecksForUpdates] }).is_some_and(|b: Bool| b.as_bool())
}

pub fn set_checks_automatically(on: bool) {
    // SAFETY: SPUUpdater's automaticallyChecksForUpdates setter.
    with_updater(|u| unsafe {
        let _: () = msg_send![u, setAutomaticallyChecksForUpdates: Bool::new(on)];
    });
}

/// It downloads an update it finds, and installs it when the app quits.
pub fn downloads_automatically() -> bool {
    // SAFETY: SPUUpdater's automaticallyDownloadsUpdates, a BOOL property.
    with_updater(|u| unsafe { msg_send![u, automaticallyDownloadsUpdates] }).is_some_and(|b: Bool| b.as_bool())
}

pub fn set_downloads_automatically(on: bool) {
    // SAFETY: SPUUpdater's automaticallyDownloadsUpdates setter.
    with_updater(|u| unsafe {
        let _: () = msg_send![u, setAutomaticallyDownloadsUpdates: Bool::new(on)];
    });
}
