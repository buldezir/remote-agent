//! What `store::dock` decides, asked of macOS: the Dock tile's badge, the
//! icon's bounce, and the notification center's leave to show the badge.
//!
//! macOS draws an app's badge only with the user's leave, as for its
//! notifications, and GPUI asks for alerts and sounds alone, at the first
//! notification. So the app asks for badges too, at launch while either is
//! on, before GPUI does. An app allowed notifications before it asked for
//! badges keeps them off until the user turns them on in System Settings,
//! which Settings › General says (`badge_blocked`).

use std::ptr::NonNull;

use block2::RcBlock;
use objc2::rc::Retained;
use objc2::runtime::{AnyObject, Bool};
use objc2::{class, msg_send};
use objc2_foundation::{NSBundle, NSError, NSString};
use objc2_user_notifications::{
    UNAuthorizationOptions, UNAuthorizationStatus, UNNotificationSetting, UNNotificationSettings,
    UNUserNotificationCenter,
};

/// NSRequestUserAttentionType.
const CRITICAL_REQUEST: usize = 0;
const INFORMATIONAL_REQUEST: usize = 10;

fn app() -> Retained<AnyObject> {
    // SAFETY: +[NSApplication sharedApplication], on the main thread, where
    // GPUI runs the app.
    unsafe { msg_send![class!(NSApplication), sharedApplication] }
}

/// Shows `count` on the Dock icon, or no badge for 0.
pub fn set_badge(count: usize) {
    let label = (count > 0).then(|| NSString::from_str(&count.to_string()));
    // SAFETY: -[NSApplication dockTile] and -[NSDockTile setBadgeLabel:],
    // which takes nil to remove the badge.
    unsafe {
        let tile: Retained<AnyObject> = msg_send![&*app(), dockTile];
        let _: () = msg_send![&*tile, setBadgeLabel: label.as_deref()];
    }
}

/// Whether the app is in front. macOS bounces the icon only while it isn't.
pub fn app_active() -> bool {
    // SAFETY: -[NSApplication isActive].
    let active: Bool = unsafe { msg_send![&*app(), isActive] };
    active.as_bool()
}

/// Bounces the icon once.
pub fn bounce_once() {
    // SAFETY: -[NSApplication requestUserAttention:].
    let _: isize = unsafe { msg_send![&*app(), requestUserAttention: INFORMATIONAL_REQUEST] };
}

/// Bounces the icon until the user comes to the app or `stop_bouncing`
/// cancels the request this returns.
pub fn bounce_until_active() -> isize {
    // SAFETY: -[NSApplication requestUserAttention:].
    unsafe { msg_send![&*app(), requestUserAttention: CRITICAL_REQUEST] }
}

pub fn stop_bouncing(request: isize) {
    // SAFETY: -[NSApplication cancelUserAttentionRequest:], with a request
    // `bounce_until_active` returned.
    let _: () = unsafe { msg_send![&*app(), cancelUserAttentionRequest: request] };
}

/// The notification center, which aborts the process outside an app bundle,
/// as under `cargo run`; GPUI guards its own the same way.
fn center() -> Option<Retained<UNUserNotificationCenter>> {
    NSBundle::mainBundle().bundleIdentifier()?;
    Some(UNUserNotificationCenter::currentNotificationCenter())
}

/// Asks macOS once for notifications with badges; after that it answers with
/// the user's choice without asking.
pub fn request_authorization() {
    let Some(center) = center() else {
        return;
    };
    let done = RcBlock::new(|_: Bool, _: *mut NSError| {});
    center.requestAuthorizationWithOptions_completionHandler(
        UNAuthorizationOptions::Alert | UNAuthorizationOptions::Sound | UNAuthorizationOptions::Badge,
        &done,
    );
}

/// Whether System Settings holds the badge back: notifications denied, or
/// allowed without badges. None outside an app bundle.
pub fn badge_blocked() -> Option<async_channel::Receiver<bool>> {
    let center = center()?;
    let (tx, rx) = async_channel::bounded(1);
    let done = RcBlock::new(move |settings: NonNull<UNNotificationSettings>| {
        // SAFETY: the center hands a valid settings object for the call.
        let settings = unsafe { settings.as_ref() };
        let blocked = match settings.authorizationStatus() {
            UNAuthorizationStatus::Denied => true,
            UNAuthorizationStatus::NotDetermined => false,
            _ => settings.badgeSetting() != UNNotificationSetting::Enabled,
        };
        let _ = tx.try_send(blocked);
    });
    center.getNotificationSettingsWithCompletionHandler(&done);
    Some(rx)
}

/// System Settings › Notifications, at this app.
pub fn notification_settings_url() -> String {
    let id = NSBundle::mainBundle()
        .bundleIdentifier()
        .map(|s| s.to_string())
        .unwrap_or_default();
    format!("x-apple.systempreferences:com.apple.Notifications-Settings.extension?id={id}")
}
