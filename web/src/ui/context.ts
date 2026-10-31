import { createContext, useContext } from "react";
import type { ServerConnection } from "../store/serverConnection";
import type { SessionSearch } from "../store/sessionSearch";
import type { ServerStore } from "../store/serverStore";

export const ServerStoreContext = createContext<ServerStore | null>(null);

export function useServerStore(): ServerStore {
  const s = useContext(ServerStoreContext);
  if (!s) throw new Error("no ServerStore");
  return s;
}

/** The server whose sessions and images the views below show. */
export const ConnectionContext = createContext<ServerConnection | null>(null);

export function useConnection(): ServerConnection {
  const c = useContext(ConnectionContext);
  if (!c) throw new Error("no ServerConnection");
  return c;
}

/** The search across every server's sessions, one for the app, so that it
 *  outlives the sidebar that shows it: a phone's Back returns to it. */
export const SessionSearchContext = createContext<SessionSearch | null>(null);

export function useSessionSearch(): SessionSearch {
  const s = useContext(SessionSearchContext);
  if (!s) throw new Error("no SessionSearch");
  return s;
}
