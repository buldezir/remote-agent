//! Remote Agent for macOS, in GPUI. It pairs with
//! rad by link, lists every server's sessions in the Inbox, Projects or
//! Columns layout, and shows a session with everything the other clients
//! do with it: its transcript, approvals, composer, diffs, commits, forks
//! and terminal. See README.md.

mod conn;
mod images;
mod net;
mod protocol;
mod store;
mod ui;

use gpui_kit::component::TitleBar;
use gpui_kit::component::input::{Copy, Cut, Paste, Redo, SelectAll, Undo};
use gpui_kit::{
    Action, App, AppContext as _, KeyBinding, Menu, MenuItem, OsAction, SharedString, WindowOptions, actions, px, size,
};

use crate::store::prefs::PrefsStore;
use crate::store::servers::home_dir;
use crate::ui::app::AppView;
use crate::ui::state::{AppHandle, AppPrefs, apply_text_size};

actions!(
    remote_agent,
    [
        Quit,
        NewSession,
        PairServer,
        OpenSettings,
        CheckForUpdate,
        SearchSessions,
        FindInSession,
        FindNext,
        FindPrevious,
        RenameSession,
        ShowChanges,
        CommitSession,
        ForkSession,
        ToggleTerminal,
        StopAgent,
        SaveDraft,
        LayoutInbox,
        LayoutProjects,
        LayoutColumns,
        LayoutAllProjects,
        ToggleSidebar,
    ]
);

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
        .with_assets(gpui_kit::assets::AllAssets)
        .run(move |cx: &mut App| {
            gpui_kit::init(cx);
            cx.set_global(AppPrefs(PrefsStore::load(&home_dir())));
            ui::palette::install_theme(cx);
            apply_text_size(cx);

            cx.on_action(|_: &Quit, cx| cx.quit());
            cx.bind_keys([
                KeyBinding::new("cmd-q", Quit, None),
                KeyBinding::new("cmd-n", NewSession, None),
                KeyBinding::new("cmd-,", OpenSettings, None),
                KeyBinding::new("shift-cmd-f", SearchSessions, None),
                KeyBinding::new("cmd-f", FindInSession, None),
                KeyBinding::new("cmd-g", FindNext, None),
                KeyBinding::new("shift-cmd-g", FindPrevious, None),
                KeyBinding::new("alt-cmd-c", CommitSession, None),
                // The later one is what menus show.
                KeyBinding::new("cmd-`", ToggleTerminal, None),
                KeyBinding::new("cmd-j", ToggleTerminal, None),
                KeyBinding::new("cmd-.", StopAgent, None),
                KeyBinding::new("cmd-s", SaveDraft, None),
                KeyBinding::new("cmd-1", LayoutInbox, None),
                KeyBinding::new("cmd-2", LayoutProjects, None),
                KeyBinding::new("cmd-3", LayoutColumns, None),
                KeyBinding::new("cmd-4", LayoutAllProjects, None),
                KeyBinding::new("ctrl-cmd-s", ToggleSidebar, None),
            ]);
            ui::terminal::bind_keys(cx);
            ui::viewer::bind_keys(cx);
            // The release build updates through Sparkle; the development
            // bundle from the Mac that built it, over ssh.
            ui::sparkle::start();
            let mut app_menu = vec![MenuItem::action("Settings…", OpenSettings)];
            if ui::sparkle::available() {
                app_menu.push(MenuItem::action("Check for Updates…", CheckForUpdate));
            } else if ui::build_update::available() {
                app_menu.push(MenuItem::action("Check for Update…", CheckForUpdate));
            }
            app_menu.extend([MenuItem::separator(), MenuItem::action("Quit Remote Agent", Quit)]);
            cx.set_menus(vec![
                Menu {
                    name: "Remote Agent".into(),
                    items: app_menu,
                    disabled: false,
                },
                Menu {
                    name: "File".into(),
                    items: vec![
                        MenuItem::action("New Session", NewSession),
                        MenuItem::separator(),
                        MenuItem::action("Pair a Server…", PairServer),
                    ],
                    disabled: false,
                },
                Menu {
                    name: "Edit".into(),
                    items: vec![
                        MenuItem::os_action("Undo", Undo, OsAction::Undo),
                        MenuItem::os_action("Redo", Redo, OsAction::Redo),
                        MenuItem::separator(),
                        MenuItem::os_action("Cut", Cut, OsAction::Cut),
                        MenuItem::os_action("Copy", Copy, OsAction::Copy),
                        MenuItem::os_action("Paste", Paste, OsAction::Paste),
                        MenuItem::os_action("Select All", SelectAll, OsAction::SelectAll),
                        MenuItem::separator(),
                        MenuItem::submenu(Menu {
                            name: "Find".into(),
                            items: vec![
                                MenuItem::action("Search Sessions", SearchSessions),
                                MenuItem::separator(),
                                MenuItem::action("Find in Session", FindInSession),
                                MenuItem::action("Find Next", FindNext),
                                MenuItem::action("Find Previous", FindPrevious),
                            ],
                            disabled: false,
                        }),
                    ],
                    disabled: false,
                },
                Menu {
                    name: "Session".into(),
                    items: vec![
                        MenuItem::action("Rename…", RenameSession),
                        MenuItem::separator(),
                        MenuItem::action("Changes", ShowChanges),
                        MenuItem::action("Commit…", CommitSession),
                        MenuItem::action("Fork…", ForkSession),
                        MenuItem::separator(),
                        MenuItem::action("Terminal", ToggleTerminal),
                        MenuItem::action("Stop", StopAgent),
                        MenuItem::action("Save as Draft", SaveDraft),
                    ],
                    disabled: false,
                },
                Menu {
                    name: "View".into(),
                    items: vec![
                        MenuItem::action("as Inbox", LayoutInbox),
                        MenuItem::action("as Projects", LayoutProjects),
                        MenuItem::action("as Columns", LayoutColumns),
                        MenuItem::action("as All Projects", LayoutAllProjects),
                        MenuItem::separator(),
                        MenuItem::action("Show or Hide Sidebar", ToggleSidebar),
                    ],
                    disabled: false,
                },
            ]);
            cx.on_window_closed(|cx, _| {
                if cx.windows().is_empty() {
                    cx.quit();
                }
            })
            .detach();

            let (bounds, display_id) = ui::window_frame::initial(cx);
            let (min_w, min_h) = ui::window_frame::MIN_SIZE;
            let options = WindowOptions {
                window_bounds: Some(bounds),
                display_id,
                window_min_size: Some(size(px(min_w), px(min_h))),
                app_id: Some(store::identity::current().id.clone()),
                // The traffic lights sit in the first column's bar, centred in it.
                titlebar: Some(gpui_kit::TitlebarOptions {
                    traffic_light_position: Some(gpui_kit::point(px(14.), px(16.))),
                    ..TitleBar::title_bar_options()
                }),
                ..TitleBar::window_options()
            };
            let handle = handle.clone();
            let (window, view) = gpui_kit::open_window(options, cx, move |window, cx| {
                window.set_window_title(&SharedString::from("Remote Agent"));
                cx.new(|cx| AppView::new(handle, window, cx))
            })
            .expect("failed to open the window");
            cx.set_global(AppHandle(view.downgrade()));
            // With nothing in the window focused, as after a session closes,
            // the window's own view takes its commands.
            fn forward<A: Action + Clone>(
                window: gpui_kit::AnyWindowHandle,
                view: gpui_kit::Entity<AppView>,
                cx: &mut App,
            ) {
                cx.on_action(move |a: &A, cx| {
                    let (a, view) = (a.clone(), view.clone());
                    let _ = window.update(cx, |_, window, cx| {
                        view.update(cx, |v, cx| v.focus_root(window, cx));
                        window.dispatch_action(Box::new(a), cx);
                    });
                });
            }
            forward::<NewSession>(window, view.clone(), cx);
            forward::<PairServer>(window, view.clone(), cx);
            forward::<OpenSettings>(window, view.clone(), cx);
            forward::<CheckForUpdate>(window, view.clone(), cx);
            forward::<SearchSessions>(window, view.clone(), cx);
            forward::<FindInSession>(window, view.clone(), cx);
            forward::<FindNext>(window, view.clone(), cx);
            forward::<FindPrevious>(window, view.clone(), cx);
            forward::<RenameSession>(window, view.clone(), cx);
            forward::<ShowChanges>(window, view.clone(), cx);
            forward::<CommitSession>(window, view.clone(), cx);
            forward::<ForkSession>(window, view.clone(), cx);
            forward::<ToggleTerminal>(window, view.clone(), cx);
            forward::<StopAgent>(window, view.clone(), cx);
            forward::<SaveDraft>(window, view.clone(), cx);
            forward::<LayoutInbox>(window, view.clone(), cx);
            forward::<LayoutProjects>(window, view.clone(), cx);
            forward::<LayoutColumns>(window, view.clone(), cx);
            forward::<LayoutAllProjects>(window, view.clone(), cx);
            forward::<ToggleSidebar>(window, view.clone(), cx);
            cx.activate(true);
        });
    drop(runtime);
}
