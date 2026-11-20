//! The socket to one rad: `GET /v1/ws` with the device's token, one JSON
//! frame per text message (PROTOCOL.md, "Frames").
//!
//! It runs on a tokio runtime of its own, beside GPUI's executor: a task per
//! server owns the socket, reconnects with backoff, and gives up after five
//! failures in a row, or at once when rad turns the token away, until
//! `retry`. It hands what it hears to the UI on a channel, and answers calls
//! with a future that any executor can await. Sync (what to subscribe, what
//! to apply) is the UI side's, in `conn.rs`.

use std::collections::HashMap;
use std::future::Future;
use std::time::Duration;

use futures_util::{SinkExt, StreamExt};
use serde_json::Value;
use tokio::sync::{mpsc, oneshot};
use tokio::time::{Instant, timeout};
use tokio_tungstenite::tungstenite::{self, Message, client::IntoClientRequest, http::HeaderValue};

use crate::protocol::{Frame, Notification, RpcError, ServerInfo};

/// What the socket tells the UI.
#[derive(Debug)]
pub enum NetEvent {
    Connecting,
    Connected {
        url: String,
        info: ServerInfo,
    },
    Notification(Notification),
    /// The socket closed; it tries again.
    Closed {
        reason: String,
    },
    /// It gave up, until `retry`.
    Failed(RpcError),
}

enum Command {
    Call {
        method: String,
        params: Value,
        reply: oneshot::Sender<Result<Value, RpcError>>,
    },
    Retry,
}

const MAX_ATTEMPTS: u32 = 5;
const FIRST_RETRY_DELAY: Duration = Duration::from_secs(1);
const CONNECT_TIMEOUT: Duration = Duration::from_secs(6);
/// rad pings every 25 s: a socket silent for longer died, as while the Mac slept.
const SILENCE: Duration = Duration::from_secs(45);
pub const CALL_TIMEOUT: Duration = Duration::from_secs(30);

/// A handle on a server's socket. Dropping every handle closes it.
#[derive(Clone)]
pub struct Rpc {
    commands: mpsc::UnboundedSender<Command>,
    runtime: tokio::runtime::Handle,
}

impl Rpc {
    pub fn spawn(
        runtime: &tokio::runtime::Handle,
        urls: Vec<String>,
        token: String,
    ) -> (Rpc, async_channel::Receiver<NetEvent>) {
        let (commands, rx) = mpsc::unbounded_channel();
        let (events, events_rx) = async_channel::unbounded();
        runtime.spawn(run(urls, token, rx, events));
        (
            Rpc {
                commands,
                runtime: runtime.clone(),
            },
            events_rx,
        )
    }

    /// Sends a request; the result is rad's, or `disconnected` / `timeout`.
    pub fn call(
        &self,
        method: &str,
        params: Value,
        wait: Duration,
    ) -> impl Future<Output = Result<Value, RpcError>> + use<> {
        let (reply, answer) = oneshot::channel();
        let sent = self
            .commands
            .send(Command::Call {
                method: method.into(),
                params,
                reply,
            })
            .is_ok();
        let task = self.runtime.spawn(async move {
            if !sent {
                return Err(disconnected());
            }
            match timeout(wait, answer).await {
                Ok(Ok(result)) => result,
                Ok(Err(_)) => Err(disconnected()),
                Err(_) => Err(RpcError::new("timeout", "rad didn't answer in time.")),
            }
        });
        async move { task.await.unwrap_or_else(|_| Err(disconnected())) }
    }

    /// Connects again now, as after a failure.
    pub fn retry(&self) {
        let _ = self.commands.send(Command::Retry);
    }
}

fn disconnected() -> RpcError {
    RpcError::new("disconnected", "Not connected to rad.")
}

fn unauthorized() -> RpcError {
    RpcError::new("unauthorized", "rad no longer knows this device. Pair it again.")
}

async fn run(
    urls: Vec<String>,
    token: String,
    mut commands: mpsc::UnboundedReceiver<Command>,
    events: async_channel::Sender<NetEvent>,
) {
    let mut urls = urls;
    let mut failures = 0;
    let mut delay = FIRST_RETRY_DELAY;
    loop {
        if events.send(NetEvent::Connecting).await.is_err() {
            return;
        }
        let err = match connect(&urls, &token).await {
            Ok((socket, url)) => {
                failures = 0;
                delay = FIRST_RETRY_DELAY;
                urls.retain(|u| *u != url);
                urls.insert(0, url.clone());
                match serve(socket, url, &mut commands, &events).await {
                    End::Stopped => return,
                    End::Closed(reason) if reason.code == "unauthorized" => reason,
                    End::Closed(reason) => {
                        if events.send(NetEvent::Closed { reason: reason.message }).await.is_err() {
                            return;
                        }
                        continue;
                    }
                }
            }
            Err(e) => e,
        };
        failures += 1;
        // Trying again won't bring back a revoked token.
        if err.code == "unauthorized" || failures >= MAX_ATTEMPTS {
            if events.send(NetEvent::Failed(err)).await.is_err() {
                return;
            }
            if !wait(&mut commands, None).await {
                return;
            }
            failures = 0;
            delay = FIRST_RETRY_DELAY;
            continue;
        }
        if !wait(&mut commands, Some(delay)).await {
            return;
        }
        delay *= 2;
    }
}

/// Waits for the delay, or for `retry` (forever without one), failing calls
/// meanwhile. Returns false once every handle is gone.
async fn wait(commands: &mut mpsc::UnboundedReceiver<Command>, delay: Option<Duration>) -> bool {
    let deadline = delay.map(|d| Instant::now() + d);
    loop {
        let next = async {
            match deadline {
                Some(d) => timeout(d.saturating_duration_since(Instant::now()), commands.recv())
                    .await
                    .ok(),
                None => Some(commands.recv().await),
            }
        };
        match next.await {
            None => return true, // the delay is over
            Some(None) => return false,
            Some(Some(Command::Retry)) => return true,
            Some(Some(Command::Call { reply, .. })) => {
                let _ = reply.send(Err(disconnected()));
            }
        }
    }
}

type Socket = tokio_tungstenite::WebSocketStream<tokio_tungstenite::MaybeTlsStream<tokio::net::TcpStream>>;

async fn connect(urls: &[String], token: &str) -> Result<(Socket, String), RpcError> {
    let mut last = RpcError::new("unreachable", format!("rad isn't reachable at {}", urls.join(" or ")));
    for base in urls {
        let ws = if let Some(rest) = base.strip_prefix("https://") {
            format!("wss://{rest}/v1/ws")
        } else if let Some(rest) = base.strip_prefix("http://") {
            format!("ws://{rest}/v1/ws")
        } else {
            continue;
        };
        let Ok(mut request) = ws.into_client_request() else {
            continue;
        };
        let Ok(auth) = HeaderValue::from_str(&format!("Bearer {token}")) else {
            continue;
        };
        request.headers_mut().insert("Authorization", auth);
        match timeout(CONNECT_TIMEOUT, tokio_tungstenite::connect_async(request)).await {
            Ok(Ok((socket, _))) => return Ok((socket, base.clone())),
            Ok(Err(tungstenite::Error::Http(resp))) if resp.status().as_u16() == 401 => return Err(unauthorized()),
            Ok(Err(e)) => last = RpcError::new("unreachable", format!("{base}: {e}")),
            Err(_) => last = RpcError::new("unreachable", format!("{base}: timed out")),
        }
    }
    Err(last)
}

enum End {
    Stopped,
    Closed(RpcError),
}

/// Runs a connected socket until it closes. `server.info` goes first; its
/// answer is the `Connected` event.
async fn serve(
    socket: Socket,
    url: String,
    commands: &mut mpsc::UnboundedReceiver<Command>,
    events: &async_channel::Sender<NetEvent>,
) -> End {
    let (mut sink, mut stream) = socket.split();
    let mut pending: HashMap<u64, oneshot::Sender<Result<Value, RpcError>>> = HashMap::new();
    const INFO: u64 = 0;
    let mut next_id: u64 = 1;
    let mut heard = Instant::now();
    let mut ticker = tokio::time::interval(Duration::from_secs(5));
    let info_request = serde_json::json!({"id": INFO, "method": "server.info"}).to_string();
    if sink.send(Message::text(info_request)).await.is_err() {
        return End::Closed(RpcError::new("disconnected", "The connection dropped."));
    }
    let end = loop {
        tokio::select! {
            message = stream.next() => {
                heard = Instant::now();
                match message {
                    Some(Ok(Message::Text(text))) => match Frame::parse(&text) {
                        Ok(Frame::Response { id: INFO, result }) => match result.and_then(|v| {
                            serde_json::from_value::<ServerInfo>(v).map_err(|e| RpcError::new("invalid", e.to_string()))
                        }) {
                            Ok(info) => {
                                if events.send(NetEvent::Connected { url: url.clone(), info }).await.is_err() {
                                    break End::Stopped;
                                }
                            }
                            Err(e) => break End::Closed(e),
                        },
                        Ok(Frame::Response { id, result }) => {
                            if let Some(reply) = pending.remove(&id) {
                                let _ = reply.send(result);
                            }
                        }
                        Ok(Frame::Notification(n)) => {
                            if events.send(NetEvent::Notification(n)).await.is_err() {
                                break End::Stopped;
                            }
                        }
                        Ok(Frame::Ignored) | Err(_) => {}
                    },
                    Some(Ok(Message::Close(frame))) => {
                        let code: u16 = frame.as_ref().map(|f| f.code.into()).unwrap_or(1005);
                        break End::Closed(if code == 4001 {
                            unauthorized()
                        } else {
                            RpcError::new("disconnected", "rad closed the connection.")
                        });
                    }
                    Some(Ok(_)) => {} // pings are answered by tungstenite
                    Some(Err(e)) => break End::Closed(RpcError::new("disconnected", e.to_string())),
                    None => break End::Closed(RpcError::new("disconnected", "The connection dropped.")),
                }
            }
            command = commands.recv() => match command {
                None => break End::Stopped,
                Some(Command::Retry) => {}
                Some(Command::Call { method, params, reply }) => {
                    let id = next_id;
                    next_id += 1;
                    let mut frame = serde_json::json!({"id": id, "method": method});
                    if !params.is_null() {
                        frame["params"] = params;
                    }
                    pending.insert(id, reply);
                    if let Err(e) = sink.send(Message::text(frame.to_string())).await {
                        break End::Closed(RpcError::new("disconnected", e.to_string()));
                    }
                }
            },
            _ = ticker.tick() => {
                if heard.elapsed() > SILENCE {
                    break End::Closed(RpcError::new("disconnected", "rad stopped answering."));
                }
            }
        }
    };
    for (_, reply) in pending {
        let _ = reply.send(Err(disconnected()));
    }
    let _ = sink.close().await;
    end
}
