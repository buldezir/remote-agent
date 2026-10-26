import { useSyncExternalStore } from "react";

/** Where the page is: in the hash, so any static host serves it.
 *  `#/s/<serverId>` is a server's sessions, `#/s/<serverId>/<sessionId>` a session. */
export interface Route {
  serverID?: string;
  sessionID?: string;
}

export function parseRoute(hash: string): Route {
  const m = /^#\/s\/([^/]+)(?:\/([^/]+))?/.exec(hash);
  if (!m) return {};
  return { serverID: decodeURIComponent(m[1]), sessionID: m[2] ? decodeURIComponent(m[2]) : undefined };
}

export function routeHash(r: Route): string {
  if (!r.serverID) return "#/";
  return "#/s/" + encodeURIComponent(r.serverID) + (r.sessionID ? "/" + encodeURIComponent(r.sessionID) : "");
}

export function navigate(r: Route, replace = false) {
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
