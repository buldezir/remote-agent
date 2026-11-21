//! A live connection to one rad, as a GPUI entity: it keeps the index in
//! sync, owns the session stores, and subscribes to a session's stream while
//! a view shows it. RAKit's `ServerConnection`, on GPUI's main thread.
//! Views observe it and redraw when it notifies.

use std::collections::{HashMap, HashSet};
use std::future::Future;
use std::rc::Rc;
use std::time::Duration;

use gpui_kit::{AppContext as _, Context, EventEmitter, Task};
use serde_json::{Value, json};

use crate::images::{Images, Prepared};
use crate::net::{CALL_TIMEOUT, NetEvent, Rpc};
use crate::protocol::ImageRef;
use crate::protocol::{
    HarnessInfo, HarnessList, INDEX_STREAM, Notification, RpcError, ServerInfo, Session, TerminalInfo, TerminalList,
    session_id_of_stream, session_stream,
};
use crate::store::alert::{SessionAlert, alert};
use crate::store::index::IndexState;
use crate::store::servers::SavedServer;
use crate::store::session::SessionState;

#[derive(Debug, Clone, PartialEq)]
pub enum ConnState {
    Connecting(Option<String>),
    Connected,
    Failed(RpcError),
}

/// What the app hears from a connection.
pub enum ConnEvent {
    /// This URL connected; the app saves it first.
    UrlWorked(String),
    /// A session changed in a way the user wants to hear about.
    Alert { session: Rc<Session>, alert: SessionAlert },
}

pub struct ServerConn {
    pub server_id: String,
    pub name: String,
    pub state: ConnState,
    pub info: Option<ServerInfo>,
    pub index: IndexState,
    /// None until loaded.
    pub harnesses: Option<Vec<HarnessInfo>>,
    pub stores: HashMap<String, SessionState>,
    /// The base URL that connected last, for images and the terminal.
    pub base_url: Option<String>,
    pub token: String,
    /// The sockets' runtime, which the terminal's socket runs on too.
    pub runtime: tokio::runtime::Handle,
    /// The server's running shells by session id (`terminal.list`): asked
    /// on connecting, every minute while connected, and after this app
    /// opens or ends one. None until known, or when rad can't say.
    pub terminals: Option<HashMap<String, TerminalInfo>>,
    /// The sessions whose terminal panel is shown, so that it shows again
    /// when the user comes back to the session.
    pub terminals_shown: HashSet<String>,
    rpc: Rpc,
    _events: Task<()>,
    _terminal_poll: Task<()>,
}

/// How often the running shells are asked for while connected.
const TERMINAL_POLL: Duration = Duration::from_secs(60);

impl EventEmitter<ConnEvent> for ServerConn {}

impl ServerConn {
    pub fn new(saved: &SavedServer, runtime: &tokio::runtime::Handle, cx: &mut Context<Self>) -> Self {
        let (rpc, events) = Rpc::spawn(runtime, saved.urls.clone(), saved.token.clone());
        let task = cx.spawn(async move |this, cx| {
            while let Ok(ev) = events.recv().await {
                if this.update(cx, |conn, cx| conn.handle(ev, cx)).is_err() {
                    break;
                }
            }
        });
        let poll = cx.spawn(async move |this, cx| {
            loop {
                cx.background_executor().timer(TERMINAL_POLL).await;
                if this.update(cx, |conn, cx| conn.refresh_terminals(cx)).is_err() {
                    break;
                }
            }
        });
        Self {
            server_id: saved.id.clone(),
            name: saved.name.clone(),
            state: ConnState::Connecting(None),
            info: None,
            index: IndexState::default(),
            harnesses: None,
            stores: HashMap::new(),
            base_url: saved.urls.first().cloned(),
            token: saved.token.clone(),
            runtime: runtime.clone(),
            terminals: None,
            terminals_shown: HashSet::new(),
            rpc,
            _events: task,
            _terminal_poll: poll,
        }
    }

    pub fn is_connected(&self) -> bool {
        self.state == ConnState::Connected
    }

    pub fn retry(&self) {
        self.rpc.retry();
    }

    /// Calls a method; the future runs on any executor.
    pub fn call(&self, method: &str, params: Value) -> impl Future<Output = Result<Value, RpcError>> + use<> {
        self.rpc.call(method, params, CALL_TIMEOUT)
    }

    pub fn call_with_timeout(
        &self,
        method: &str,
        params: Value,
        wait: Duration,
    ) -> impl Future<Output = Result<Value, RpcError>> + use<> {
        self.rpc.call(method, params, wait)
    }

    pub fn harness(&self, id: &str) -> Option<&HarnessInfo> {
        self.harnesses.as_ref()?.iter().find(|h| h.id == id)
    }

    /// The freshest copy of a session: its stream's, else the index's.
    pub fn session(&self, id: &str) -> Option<Rc<Session>> {
        self.stores
            .get(id)
            .and_then(|s| s.session.clone())
            .or_else(|| self.index.sessions.get(id).cloned())
    }

    fn handle(&mut self, ev: NetEvent, cx: &mut Context<Self>) {
        match ev {
            NetEvent::Connecting => {
                if !matches!(self.state, ConnState::Connecting(_)) {
                    self.state = ConnState::Connecting(None);
                }
            }
            NetEvent::Connected { url, info } => {
                self.name = info.name.clone();
                self.info = Some(info);
                self.state = ConnState::Connected;
                self.base_url = Some(url.clone());
                cx.emit(ConnEvent::UrlWorked(url));
                // The index first, then each store a view shows.
                self.subscribe(INDEX_STREAM.to_string(), cx);
                let shown: Vec<String> = self
                    .stores
                    .values()
                    .filter(|s| s.viewers > 0)
                    .map(|s| session_stream(&s.id))
                    .collect();
                for stream in shown {
                    self.subscribe(stream, cx);
                }
                self.refresh_harnesses(cx);
                self.refresh_terminals(cx);
            }
            NetEvent::Notification(n) => self.notification(n, cx),
            NetEvent::Closed { reason } => {
                eprintln!("{}: {reason}", self.name);
                self.index.sync.closed();
                for s in self.stores.values_mut() {
                    s.sync.closed();
                }
                self.state = ConnState::Connecting(Some("Reconnecting…".into()));
            }
            NetEvent::Failed(e) => {
                self.index.sync.closed();
                for s in self.stores.values_mut() {
                    s.sync.closed();
                }
                self.state = ConnState::Failed(e);
            }
        }
        cx.notify();
    }

    fn notification(&mut self, n: Notification, cx: &mut Context<Self>) {
        match n {
            Notification::Events { stream, events } => {
                let gap = if stream == INDEX_STREAM {
                    // Live changes only: a replay is old news.
                    let before: HashMap<String, Rc<Session>> = if self.index.sync.live {
                        events
                            .iter()
                            .filter_map(|e| e.session.as_ref())
                            .filter_map(|s| self.index.sessions.get(&s.id).map(|old| (s.id.clone(), old.clone())))
                            .collect()
                    } else {
                        HashMap::new()
                    };
                    let gap = self.index.apply(events);
                    self.copy_index_sessions();
                    for (id, old) in before {
                        if let Some(next) = self.index.sessions.get(&id).cloned()
                            && let Some(alert) = alert(&old, &next)
                        {
                            cx.emit(ConnEvent::Alert { session: next, alert });
                        }
                    }
                    gap
                } else if let Some(store) = session_id_of_stream(&stream).and_then(|id| self.stores.get_mut(id)) {
                    store.apply(events)
                } else {
                    false
                };
                if gap {
                    self.subscribe(stream, cx);
                }
            }
            Notification::Synchronized { stream, seq, reset } => {
                if stream == INDEX_STREAM {
                    self.index.synchronized(seq, reset);
                    self.copy_index_sessions();
                } else if let Some(store) = session_id_of_stream(&stream).and_then(|id| self.stores.get_mut(id)) {
                    store.synchronized(seq, reset);
                }
            }
            Notification::Resync { stream } => self.subscribe(stream, cx),
        }
    }

    /// Stores whose stream hasn't brought the session yet take the index's.
    fn copy_index_sessions(&mut self) {
        for (id, store) in self.stores.iter_mut() {
            if !store.sync.synced
                && let Some(s) = self.index.sessions.get(id)
                && store.session.as_ref() != Some(s)
            {
                store.set_session(s.clone());
            }
        }
    }

    fn subscribe(&mut self, stream: String, cx: &mut Context<Self>) {
        let after = if stream == INDEX_STREAM {
            self.index.sync.begin_replay()
        } else if let Some(store) = session_id_of_stream(&stream).and_then(|id| self.stores.get_mut(id)) {
            store.sync.begin_replay()
        } else {
            return;
        };
        let call = self.call("subscribe", json!({"stream": stream, "afterSeq": after}));
        cx.spawn(async move |this, cx| {
            if call.await.is_err() {
                let _ = this.update(cx, |conn, _| {
                    if stream == INDEX_STREAM {
                        conn.index.sync.subscription_failed();
                    } else if let Some(store) = session_id_of_stream(&stream).and_then(|id| conn.stores.get_mut(id)) {
                        store.sync.subscription_failed();
                    }
                });
            }
        })
        .detach();
    }

    /// A view shows the session: its stream is subscribed until every
    /// `open_session` has had its `close_session`.
    pub fn open_session(&mut self, id: &str, cx: &mut Context<Self>) {
        let session = self.index.sessions.get(id).cloned();
        let store = self
            .stores
            .entry(id.to_string())
            .or_insert_with(|| SessionState::new(id, session));
        store.viewers += 1;
        if store.viewers == 1 && self.state == ConnState::Connected {
            self.subscribe(session_stream(id), cx);
        }
        cx.notify();
    }

    pub fn close_session(&mut self, id: &str) {
        let Some(store) = self.stores.get_mut(id) else { return };
        store.viewers = store.viewers.saturating_sub(1);
        if store.viewers == 0 {
            store.sync.live = false;
            if self.state == ConnState::Connected {
                // Sent as it's called; nobody waits for the answer.
                drop(self.call("unsubscribe", json!({"stream": session_stream(id)})));
            }
        }
    }

    /// Asks rad which shells run, where it can say (`terminals` in its info).
    pub fn refresh_terminals(&mut self, cx: &mut Context<Self>) {
        if !self.is_connected() || !self.info.as_ref().is_some_and(|i| i.terminals) {
            return;
        }
        let call = self.call("terminal.list", json!({}));
        cx.spawn(async move |this, cx| {
            if let Ok(value) = call.await
                && let Ok(list) = serde_json::from_value::<TerminalList>(value)
            {
                let _ = this.update(cx, |conn, cx| {
                    let terminals = list.terminals.into_iter().map(|t| (t.session_id.clone(), t)).collect();
                    if conn.terminals.as_ref() != Some(&terminals) {
                        conn.terminals = Some(terminals);
                        cx.notify();
                    }
                });
            }
        })
        .detach();
    }

    pub fn refresh_harnesses(&mut self, cx: &mut Context<Self>) {
        self.load_harnesses(false, cx);
    }

    /// Asks the agents again, as after one was updated or signed in.
    pub fn refresh_harnesses_now(&mut self, cx: &mut Context<Self>) {
        self.load_harnesses(true, cx);
    }

    fn load_harnesses(&mut self, refresh: bool, cx: &mut Context<Self>) {
        let call = self.call_with_timeout("harness.list", json!({"refresh": refresh}), Duration::from_secs(45));
        cx.spawn(async move |this, cx| {
            if let Ok(value) = call.await
                && let Ok(list) = serde_json::from_value::<HarnessList>(value)
            {
                let _ = this.update(cx, |conn, cx| {
                    conn.harnesses = Some(list.harnesses);
                    cx.notify();
                });
            }
        })
        .detach();
    }

    /// Sends a prompt: it shows in the outbox until the transcript has it,
    /// and leaves it again if rad never takes it. `steer` gives it to the
    /// running turn where the agent takes that.
    pub fn send_prompt(
        &mut self,
        session_id: &str,
        text: String,
        images: Vec<ImageRef>,
        steer: bool,
        cx: &mut Context<Self>,
    ) -> Task<Result<(), RpcError>> {
        let command_id = uuid::Uuid::new_v4().to_string();
        if let Some(store) = self.stores.get_mut(session_id) {
            store.add_outgoing(&command_id, &text, images.clone());
        }
        cx.notify();
        let mut params = json!({"commandId": command_id, "sessionId": session_id, "text": text});
        if !images.is_empty() {
            params["images"] = json!(images.iter().map(|i| i.id.clone()).collect::<Vec<_>>());
        }
        if steer {
            params["steer"] = json!(true);
        }
        let call = self.call("session.prompt", params);
        let session_id = session_id.to_string();
        cx.spawn(async move |this, cx| {
            let result = call.await.map(|_| ());
            if result.is_err() {
                let _ = this.update(cx, |conn, cx| {
                    if let Some(store) = conn.stores.get_mut(&session_id) {
                        store.drop_outgoing(&command_id);
                    }
                    cx.notify();
                });
            }
            result
        })
    }

    /// Uploads an image the user attached; it's in the cache once it's in.
    pub fn upload_image(&self, prepared: Prepared, cx: &mut Context<Self>) -> Task<Result<ImageRef, RpcError>> {
        let Some(base) = self.base_url.clone() else {
            return Task::ready(Err(RpcError::new("disconnected", "Not connected to rad.")));
        };
        let token = self.token.clone();
        let bytes = prepared.bytes.clone();
        let upload = cx.background_spawn(async move { crate::images::upload(&base, &token, &bytes) });
        cx.spawn(async move |_, cx| {
            let image = upload.await?;
            cx.update(|cx| Images::store(&image.id, &prepared, cx));
            Ok(image)
        })
    }

    /// Loads an image of this server's (`Images::get`).
    pub fn image(&self, id: &str, cx: &mut gpui_kit::App) -> crate::images::Loaded {
        Images::get(id, self.base_url.as_deref(), &self.token, cx)
    }
}
