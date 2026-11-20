//! Remote Agent for macOS, in GPUI: an experimental client. It pairs with
//! rad by link, lists every server's sessions in the Inbox layout, and shows
//! a session's transcript, approvals and composer. See README.md.

mod conn;
mod net;
mod protocol;
mod store;
mod ui;

use gpui_kit::component::TitleBar;
use gpui_kit::{
    App, AppContext as _, Bounds, KeyBinding, Menu, MenuItem, SharedString, WindowBounds, WindowOptions, actions, px,
    size,
};

use crate::ui::app::AppView;

actions!(remote_agent, [Quit, NewSession]);

fn main() {
    // The sockets run on a runtime of their own; GPUI's executor drives the UI.
    let runtime = tokio::runtime::Builder::new_multi_thread()
        .worker_threads(2)
        .thread_name("rad-net")
        .enable_all()
        .build()
        .expect("tokio runtime");
    let handle = runtime.handle().clone();

    gpui_kit::application()
        .with_assets(gpui_kit::assets::Assets)
        .run(move |cx: &mut App| {
            gpui_kit::init(cx);
            ui::palette::install_theme(cx);

            cx.on_action(|_: &Quit, cx| cx.quit());
            cx.bind_keys([
                KeyBinding::new("cmd-q", Quit, None),
                KeyBinding::new("cmd-n", NewSession, None),
            ]);
            cx.set_menus(vec![
                Menu {
                    name: "Remote Agent".into(),
                    items: vec![MenuItem::action("Quit Remote Agent", Quit)],
                    disabled: false,
                },
                Menu {
                    name: "File".into(),
                    items: vec![MenuItem::action("New Session", NewSession)],
                    disabled: false,
                },
            ]);
            cx.on_window_closed(|cx, _| {
                if cx.windows().is_empty() {
                    cx.quit();
                }
            })
            .detach();

            let bounds = Bounds::centered(None, size(px(1180.), px(780.)), cx);
            let options = WindowOptions {
                window_bounds: Some(WindowBounds::Windowed(bounds)),
                window_min_size: Some(size(px(720.), px(480.))),
                app_id: Some("dev.remote-agent.gpui".into()),
                ..TitleBar::window_options()
            };
            let handle = handle.clone();
            let (window, view) = gpui_kit::open_window(options, cx, move |window, cx| {
                window.set_window_title(&SharedString::from("Remote Agent"));
                cx.new(|cx| AppView::new(handle, window, cx))
            })
            .expect("failed to open the window");
            // Also when nothing in the window has focus, as after a session closes.
            cx.on_action(move |_: &NewSession, cx| {
                let view = view.clone();
                let _ = window.update(cx, |_, window, cx| view.update(cx, |v, cx| v.new_session(window, cx)));
            });
            cx.activate(true);
        });
    drop(runtime);
}
