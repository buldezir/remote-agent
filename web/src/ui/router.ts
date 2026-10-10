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
