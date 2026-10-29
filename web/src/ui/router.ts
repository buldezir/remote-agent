import { useSyncExternalStore } from "react";

/** A narrow window's tabs, as on a phone: every server's sessions, and the
 *  servers, each with its own sessions. */
export type Tab = "inbox" | "servers";

/** Where the page is: in the hash, so any static host serves it.
 *  - `#/`: nothing open; in a narrow window, the Inbox tab.
 *  - `#/i/<serverId>/<sessionId>`: a session opened from the Inbox tab, whose
 *    Back goes to the Inbox.
 *  - `#/servers`: the Servers tab's list of servers.
 *  - `#/s/<serverId>`: a server's sessions, in the Servers tab.
 *  - `#/s/<serverId>/<sessionId>`: a session; in a narrow window, opened in
 *    the Servers tab, whose Back goes to its server's sessions.
 *  A wide window shows the session of either, and makes `#/s/` links. Each
 *  step within a tab is a history entry, so the browser's Back follows it. */
export interface Route {
  serverID?: string;
  sessionID?: string;
  tab: Tab;
}

export function parseRoute(hash: string): Route {
  const m = /^#\/([is])\/([^/]+)(?:\/([^/]+))?/.exec(hash);
  if (!m) return { tab: hash === "#/servers" ? "servers" : "inbox" };
  return {
    serverID: decodeURIComponent(m[2]),
    sessionID: m[3] ? decodeURIComponent(m[3]) : undefined,
    // A server without a session is only the Servers tab's.
    tab: m[1] === "i" && m[3] ? "inbox" : "servers",
  };
}

/** Without a tab, as a wide window navigates, a session is `#/s/`. */
export function routeHash(r: Partial<Route>): string {
  const session = r.serverID && r.sessionID ? encodeURIComponent(r.serverID) + "/" + encodeURIComponent(r.sessionID) : undefined;
  if (session) return (r.tab === "inbox" ? "#/i/" : "#/s/") + session;
  if (r.serverID) return "#/s/" + encodeURIComponent(r.serverID);
  return r.tab === "servers" ? "#/servers" : "#/";
}

export function navigate(r: Partial<Route>, replace = false) {
  const hash = routeHash(r);
  if (location.hash === hash) return;
  if (replace) {
    history.replaceState(null, "", hash);
    window.dispatchEvent(new HashChangeEvent("hashchange"));
  } else {
    location.hash = hash;
  }
}

function subscribe(l: () => void) {
  window.addEventListener("hashchange", l);
  return () => window.removeEventListener("hashchange", l);
}

const getHash = () => location.hash;

export function useRoute(): Route {
  const hash = useSyncExternalStore(subscribe, getHash);
  return parseRoute(hash);
}

/** Whether the window is wide enough for the sidebar beside the session. */
export function useWide(): boolean {
  return useSyncExternalStore(subscribeWide, () => wide.matches);
}

const wide = matchMedia("(min-width: 860px)");

function subscribeWide(l: () => void) {
  wide.addEventListener("change", l);
  return () => wide.removeEventListener("change", l);
}

/** Whether the page is in front of the user: visible, with the focus. */
export function pageInFront(): boolean {
  return document.visibilityState === "visible" && document.hasFocus();
}

/** `pageInFront`, kept current. */
export function usePageInFront(): boolean {
  return useSyncExternalStore(subscribeInFront, pageInFront);
}

function subscribeInFront(l: () => void) {
  document.addEventListener("visibilitychange", l);
  window.addEventListener("focus", l);
  window.addEventListener("blur", l);
  return () => {
    document.removeEventListener("visibilitychange", l);
    window.removeEventListener("focus", l);
    window.removeEventListener("blur", l);
  };
}
