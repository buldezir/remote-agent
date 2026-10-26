import type { Session } from "../protocol/models";
import { Observable, useObserved } from "../store/observable";
import { sessionAlert, type SessionAlert } from "../store/sessionAlert";
import type { ServerConnection } from "../store/serverConnection";
import type { ServerStore } from "../store/serverStore";
import { storage } from "../store/storage";
import { navigate, pageInFront, parseRoute } from "./router";

/** A turn that ends starts the next queued prompt at once: wait this long,
 *  in ms, before saying it finished. */
const finishDelay = 1000;

/** The browser's notifications when a session on any server needs the user,
 *  finishes or fails, as the Mac app's Notifier. None comes for the session
 *  the page shows while it has the focus. A click opens the session. A
 *  session that no longer needs the user, because they answered on another
 *  device, takes its notification back. They come while the page is open,
 *  once the user turns them on in Settings: browsers ask for permission only
 *  after a click. */
class Notifications extends Observable {
  /** The user turned them on in this browser. */
  private on = storage.get("notifications") === "true";
  private store: ServerStore | undefined;
  /** The notifications showing, by session. */
  private showing = new Map<string, { notification: Notification; needsYou: boolean }>();

  /** The browser has them on this page: it needs https or localhost. */
  get supported(): boolean {
    return typeof Notification !== "undefined" && window.isSecureContext;
  }

  get permission(): NotificationPermission {
    return this.supported ? Notification.permission : "denied";
  }

  /** Turned on, and allowed. */
  get enabled(): boolean {
    return this.on && this.permission === "granted";
  }

  attach(store: ServerStore) {
    this.store = store;
    store.onSessionChange = (c, old, next) => this.sessionChanged(c, old, next);
    // The session in front of the user takes its notification back.
    const seen = () => {
      const r = parseRoute(location.hash);
      if (this.focused && r.serverID && r.sessionID) this.close(key(r.serverID, r.sessionID));
    };
    window.addEventListener("hashchange", seen);
    window.addEventListener("focus", seen);
    // The user can change the permission in the browser's site settings.
    void navigator.permissions
      ?.query({ name: "notifications" })
      .then((status) => status.addEventListener("change", () => this.changed()))
      .catch(() => {});
  }

  /** Turns them on, asking the browser first if it hasn't been asked. */
  async setEnabled(on: boolean) {
    if (on && this.supported && Notification.permission === "default") await Notification.requestPermission();
    this.on = on && this.permission === "granted";
    storage.set("notifications", String(this.on));
    this.changed();
  }

  private get focused(): boolean {
    return pageInFront();
  }

  private sessionChanged(c: ServerConnection, old: Session, next: Session) {
    const k = key(c.server.id, next.id);
    if (old.status === "awaiting_approval" && next.status !== "awaiting_approval" && this.showing.get(k)?.needsYou) this.close(k);
    // Read on another device.
    if (old.unread && !next.unread) this.close(k);
    const alert = sessionAlert(old, next);
    if (!alert || !this.enabled) return;
    if (alert.kind === "finished") {
      setTimeout(() => {
        const now = c.sessions.get(next.id);
        if (now?.status === "idle") this.post(alert, now, c);
      }, finishDelay);
    } else {
      this.post(alert, next, c);
    }
  }

  private post(alert: SessionAlert, s: Session, c: ServerConnection) {
    const route = parseRoute(location.hash);
    if (this.focused && route.serverID === c.server.id && route.sessionID === s.id) return;
    const agent = c.harness(s.harness)?.name ?? s.harness;
    const message =
      alert.kind === "needsYou"
        ? `${agent} needs you.`
        : alert.kind === "finished"
          ? `${agent} finished.`
          : alert.error
            ? `${agent} failed: ${alert.error}`
            : `${agent} failed.`;
    // The project, and the server when there are several.
    let place = c.projects.get(s.projectId)?.name ?? "";
    if ((this.store?.servers.length ?? 0) > 1) place += place ? ` on ${c.server.name}` : c.server.name;
    const k = key(c.server.id, s.id);
    this.close(k);
    let n: Notification;
    try {
      // One per session: the tag makes a newer one replace it, in other tabs too.
      n = new Notification(s.title || "Session", { body: place ? `${message}\n${place}` : message, tag: k, icon: "/icon.png" });
    } catch {
      return; // Android's Chrome shows them only from a service worker.
    }
    n.onclick = () => {
      window.focus();
      navigate({ serverID: c.server.id, sessionID: s.id });
      n.close();
    };
    n.onclose = () => {
      if (this.showing.get(k)?.notification === n) this.showing.delete(k);
    };
    this.showing.set(k, { notification: n, needsYou: alert.kind === "needsYou" });
  }

  private close(k: string) {
    this.showing.get(k)?.notification.close();
    this.showing.delete(k);
  }
}

function key(serverID: string, sessionID: string) {
  return serverID + " " + sessionID;
}

export const notifications = new Notifications();

export function useNotifications() {
  return useObserved(notifications);
}
