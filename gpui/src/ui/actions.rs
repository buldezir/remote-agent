//! What a session's menus do, wherever they are: archiving, unarchiving
//! and deleting it, with their confirmations.

use std::rc::Rc;

use gpui_kit::component::button::ButtonVariant;
use gpui_kit::{App, Entity, Window};
use serde_json::json;

use crate::conn::ServerConn;
use crate::protocol::{Session, WorkspaceKind};
use crate::ui::dialogs::{self, Choice};
use crate::ui::session::explain;

fn call_or_alert(
    conn: &Entity<ServerConn>,
    method: &'static str,
    params: serde_json::Value,
    failure: &'static str,
    window: &mut Window,
    cx: &mut App,
) {
    let call = conn.read(cx).call(method, params);
    window
        .spawn(cx, async move |cx| {
            if let Err(e) = call.await {
                let _ = cx.update(|window, cx| dialogs::alert(window, cx, failure, &explain(&e)));
            }
        })
        .detach();
}

/// "Archive session?", with the worktree's choice for a worktree session.
pub fn archive(conn: Entity<ServerConn>, s: Rc<Session>, window: &mut Window, cx: &mut App) {
    let worktree = s.workspace.kind == WorkspaceKind::Worktree;
    let message = if worktree {
        format!(
            "The branch {} is kept either way.",
            s.workspace.branch.clone().unwrap_or_else(|| "of the worktree".into())
        )
    } else {
        "The agent process is stopped.".to_string()
    };
    let mut choices = Vec::new();
    if worktree {
        let (conn, id) = (conn.clone(), s.id.clone());
        choices.push(Choice::new(
            "Archive and delete worktree",
            ButtonVariant::Danger,
            move |window, cx| {
                call_or_alert(
                    &conn,
                    "session.archive",
                    json!({"sessionId": id, "removeWorktree": true}),
                    "Couldn't archive the session",
                    window,
                    cx,
                )
            },
        ));
    }
    let id = s.id.clone();
    choices.push(Choice::new("Archive", ButtonVariant::Primary, move |window, cx| {
        call_or_alert(
            &conn,
            "session.archive",
            json!({"sessionId": id, "removeWorktree": false}),
            "Couldn't archive the session",
            window,
            cx,
        )
    }));
    dialogs::confirm(window, cx, "Archive session?", &message, choices);
}

pub fn unarchive(conn: Entity<ServerConn>, s: Rc<Session>, window: &mut Window, cx: &mut App) {
    call_or_alert(
        &conn,
        "session.unarchive",
        json!({"sessionId": s.id}),
        "Couldn't unarchive the session",
        window,
        cx,
    );
}

pub fn delete(conn: Entity<ServerConn>, s: Rc<Session>, window: &mut Window, cx: &mut App) {
    let worktree = s.workspace.kind == WorkspaceKind::Worktree;
    let mut message = "This can't be undone.".to_string();
    if worktree {
        message += &format!(
            " The branch {} is kept either way.",
            s.workspace.branch.clone().unwrap_or_else(|| "of the worktree".into())
        );
    }
    let mut choices = Vec::new();
    if worktree {
        let (conn, id) = (conn.clone(), s.id.clone());
        choices.push(Choice::new(
            "Delete with worktree",
            ButtonVariant::Danger,
            move |window, cx| {
                call_or_alert(
                    &conn,
                    "session.delete",
                    json!({"sessionId": id, "removeWorktree": true}),
                    "Couldn't delete the session",
                    window,
                    cx,
                )
            },
        ));
    }
    let id = s.id.clone();
    choices.push(Choice::new("Delete", ButtonVariant::Danger, move |window, cx| {
        call_or_alert(
            &conn,
            "session.delete",
            json!({"sessionId": id, "removeWorktree": false}),
            "Couldn't delete the session",
            window,
            cx,
        )
    }));
    dialogs::confirm(window, cx, "Delete session?", &message, choices);
}
