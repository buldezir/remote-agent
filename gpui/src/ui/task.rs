//! Following a task on the server (an update, a sign-in, a commit) until it
//! ends: `task.get` about once a second, as RAKit's ServerTaskProgress. An
//! update of rad ends with rad restarting, so that one waits for the new
//! version to answer.

use std::time::{Duration, Instant};

use gpui_kit::{Context, Entity, EventEmitter};
use serde_json::json;

use crate::conn::ServerConn;
use crate::protocol::{ServerTask, TaskStatus};

#[derive(Debug, Clone, PartialEq)]
pub enum Phase {
    Running,
    /// rad installed its update and restarts.
    Restarting,
    Done,
    Failed(String),
    Cancelled,
}

pub struct TaskProgress {
    pub conn: Entity<ServerConn>,
    pub task: ServerTask,
    pub phase: Phase,
    start_version: String,
    deadline: Option<Instant>,
}

pub struct TaskEnded;

enum Step<F> {
    Stop,
    Wait,
    Call(F),
}

impl EventEmitter<TaskEnded> for TaskProgress {}

impl TaskProgress {
    pub fn new(conn: Entity<ServerConn>, task: ServerTask, cx: &mut Context<Self>) -> Self {
        let start_version = conn
            .read(cx)
            .info
            .as_ref()
            .map(|i| i.version.clone())
            .unwrap_or_default();
        let mut this = Self {
            conn,
            task,
            phase: Phase::Running,
            start_version,
            deadline: None,
        };
        this.settle(cx);
        this.poll(cx);
        this
    }

    pub fn active(&self) -> bool {
        matches!(self.phase, Phase::Running | Phase::Restarting)
    }

    fn poll(&mut self, cx: &mut Context<Self>) {
        cx.spawn(async move |this, cx| {
            loop {
                cx.background_executor().timer(Duration::from_secs(1)).await;
                let step = this.update(cx, |this, cx| {
                    if !this.active() {
                        return Step::Stop;
                    }
                    if this.phase == Phase::Restarting {
                        this.check_restart(cx);
                        return Step::Wait;
                    }
                    Step::Call(this.conn.read(cx).call("task.get", json!({"id": this.task.id})))
                });
                let call = match step {
                    Err(_) | Ok(Step::Stop) => break,
                    Ok(Step::Wait) => continue,
                    Ok(Step::Call(c)) => c,
                };
                let result = call.await;
                let _ = this.update(cx, |this, cx| {
                    match result {
                        Ok(v) => {
                            if let Ok(t) = serde_json::from_value::<ServerTask>(v) {
                                this.task = t;
                                this.settle(cx);
                            }
                        }
                        Err(e) if e.code == "not_found" || e.code == "method_not_found" => {
                            let version = this
                                .conn
                                .read(cx)
                                .info
                                .as_ref()
                                .map(|i| i.version.clone())
                                .unwrap_or_default();
                            if this.task.kind == "update_server" && version != this.start_version {
                                this.phase = Phase::Done;
                            } else {
                                this.phase = Phase::Failed("rad restarted before the task finished".into());
                            }
                            cx.emit(TaskEnded);
                        }
                        // Disconnected or slow: ask again.
                        Err(_) => {}
                    }
                    cx.notify();
                });
            }
        })
        .detach();
    }

    /// The phase the task's status gives.
    fn settle(&mut self, cx: &mut Context<Self>) {
        let before = self.phase.clone();
        self.phase = match self.task.status {
            TaskStatus::Running | TaskStatus::Unknown => Phase::Running,
            TaskStatus::Succeeded if self.task.kind == "update_server" => {
                self.deadline.get_or_insert(Instant::now() + Duration::from_secs(120));
                Phase::Restarting
            }
            TaskStatus::Succeeded => Phase::Done,
            TaskStatus::Cancelled => Phase::Cancelled,
            TaskStatus::Failed => Phase::Failed(self.task.error.clone().unwrap_or_else(|| "The task failed".into())),
        };
        if before != self.phase && !self.active() {
            if matches!(self.task.kind.as_str(), "update_agent" | "login") {
                self.conn.update(cx, |c, cx| c.refresh_harnesses_now(cx));
            }
            cx.emit(TaskEnded);
        }
    }

    fn check_restart(&mut self, cx: &mut Context<Self>) {
        let conn = self.conn.read(cx);
        if matches!(conn.state, crate::conn::ConnState::Failed(_)) {
            conn.retry();
        }
        let version = conn.info.as_ref().map(|i| i.version.clone()).unwrap_or_default();
        let want = self.task.version.clone();
        if conn.is_connected()
            && (want.as_deref() == Some(version.as_str()) || (want.is_none() && version != self.start_version))
        {
            self.phase = Phase::Done;
            cx.emit(TaskEnded);
        } else if self.deadline.is_some_and(|d| Instant::now() > d) {
            self.phase = Phase::Failed(format!(
                "rad hasn't come back as {}",
                want.unwrap_or_else(|| "the new version".into())
            ));
            cx.emit(TaskEnded);
        }
        cx.notify();
    }

    pub fn cancel(&self, cx: &mut Context<Self>) {
        drop(self.conn.read(cx).call("task.cancel", json!({"id": self.task.id})));
    }

    pub fn input(
        &self,
        text: String,
        cx: &mut Context<Self>,
    ) -> impl std::future::Future<Output = Result<serde_json::Value, crate::protocol::RpcError>> + use<> {
        self.conn
            .read(cx)
            .call("task.input", json!({"id": self.task.id, "text": text}))
    }
}
