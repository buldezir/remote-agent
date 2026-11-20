//! Remote Agent › Check for Update…: asks the Mac that builds the app for
//! its latest build over ssh, and installs it on request (see
//! `store::build_update`). Only a bundle made by `scripts/bundle.sh` offers it.

use gpui_kit::component::WindowExt as _;
use gpui_kit::component::button::ButtonVariant;
use gpui_kit::{App, AppContext as _, ParentElement as _, Styled as _, Window, div, px};

use crate::store::build_update::{self, BuildInfo};
use crate::ui::dialogs::{self, Choice};
use crate::ui::palette::Palette;
use crate::ui::state::{prefs, update_prefs};

/// The bundle can update itself.
pub fn available() -> bool {
    build_update::current().is_some()
}

/// Asks for the host builds come from, then checks it.
pub fn ask_host(window: &mut Window, cx: &mut App) {
    let host = prefs(cx).update_host.clone();
    dialogs::prompt_text(
        window,
        cx,
        "Pull Builds From",
        "The Mac that builds the app, as `ssh <host>` reaches it without a password. \
         Its build in the same folder as this app is the update.",
        "my-mac",
        &host,
        "Check",
        ButtonVariant::Primary,
        |host, window, cx| {
            if host.is_empty() {
                return;
            }
            update_prefs(cx, |p| p.update_host = host);
            check(window, cx);
        },
    );
}

pub fn check(window: &mut Window, cx: &mut App) {
    let (Some(bundle), Some(current)) = (build_update::bundle(), build_update::current()) else {
        dialogs::alert(
            window,
            cx,
            "No Updates for This Build",
            "Only the app gpui/scripts/bundle.sh builds can update itself.",
        );
        return;
    };
    let host = prefs(cx).update_host.clone();
    if host.is_empty() {
        ask_host(window, cx);
        return;
    }
    let task = cx.background_spawn({
        let host = host.clone();
        async move { build_update::remote(&host, &bundle) }
    });
    window
        .spawn(cx, async move |cx| {
            let result = task.await;
            let _ = cx.update(|window, cx| show_result(host, current, result, window, cx));
        })
        .detach();
}

fn show_result(host: String, current: BuildInfo, result: Result<BuildInfo, String>, window: &mut Window, cx: &mut App) {
    let now = chrono::Utc::now();
    match result {
        Err(e) => dialogs::confirm(
            window,
            cx,
            "Couldn't Check for an Update",
            &e,
            vec![Choice::new("Change Host…", ButtonVariant::Primary, ask_host)],
        ),
        Ok(remote) if remote == current => dialogs::confirm(
            window,
            cx,
            "This Is the Latest Build",
            &format!("{} has no newer build than this one: {}.", host, current.summary(now)),
            vec![Choice::new("Change Host…", ButtonVariant::Secondary, ask_host)],
        ),
        Ok(remote) => {
            let message = format!(
                "{}: {}.\nThis one: {}.\n\nThe app quits, installs it and opens again.",
                host,
                remote.summary(now),
                current.summary(now)
            );
            dialogs::confirm(
                window,
                cx,
                "A New Build Is Ready",
                &message,
                vec![Choice::new(
                    "Install and Restart",
                    ButtonVariant::Primary,
                    move |window, cx| install(host.clone(), remote.clone(), window, cx),
                )],
            )
        }
    }
}

fn install(host: String, remote: BuildInfo, window: &mut Window, cx: &mut App) {
    let Some(bundle) = build_update::bundle() else {
        return;
    };
    let text = format!("Copying {} from {host}…", remote.commit);
    window.open_dialog(cx, move |dialog, _, cx| {
        let p = Palette::of(cx);
        dialog
            .w(px(380.))
            .title("Installing the Build")
            .close_button(false)
            .overlay_closable(false)
            .keyboard(false)
            .child(div().text_sm().text_color(p.subtext).child(text.clone()))
    });
    let task = cx.background_spawn(async move { build_update::install(&host, &bundle) });
    window
        .spawn(cx, async move |cx| {
            let result = task.await;
            let _ = cx.update(|window, cx| match result {
                Ok(()) => cx.quit(),
                Err(e) => {
                    window.close_dialog(cx);
                    dialogs::alert(window, cx, "Couldn't Install the Build", &e);
                }
            });
        })
        .detach();
}
