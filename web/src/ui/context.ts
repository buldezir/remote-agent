import { createContext, useContext } from "react";
import type { ServerConnection } from "../store/serverConnection";
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
