import { pair, type PairingLink } from "../protocol/pairing";
import { Observable } from "./observable";
import { ServerConnection } from "./serverConnection";
import { storage } from "./storage";

export interface SavedServer {
  /** The server's serverId. */
  id: string;
  name: string;
  /** Base URLs in preference order; the last one that worked comes first. */
  urls: string[];
  deviceId: string;
  addedAt: string;
}

const serversKey = "servers.v1";
const tokenKey = (id: string) => "token." + id;

/** The list of paired servers. Tokens are kept in localStorage, beside it. */
export class ServerStore extends Observable {
  servers: SavedServer[] = [];
  private connections = new Map<string, ServerConnection>();

  constructor() {
    super();
    this.load();
    // Another tab paired or removed a server.
    window.addEventListener("storage", (e) => {
      if (e.key === serversKey) {
        this.load();
        this.changed();
      }
    });
  }

  private load() {
    try {
      const list = JSON.parse(storage.get(serversKey) ?? "[]");
      this.servers = Array.isArray(list) ? list : [];
    } catch {
      this.servers = [];
    }
  }

  private save() {
    storage.set(serversKey, JSON.stringify(this.servers));
    this.changed();
  }

  server(id: string | undefined): SavedServer | undefined {
    return this.servers.find((s) => s.id === id);
  }

  connection(server: SavedServer): ServerConnection {
    let c = this.connections.get(server.id);
    if (!c) {
      c = new ServerConnection(server, storage.get(tokenKey(server.id)) ?? "", (url) => this.promote(url, server.id));
      this.connections.set(server.id, c);
    }
    return c;
  }

  /** Pairs with a server from a pairing link and saves it. */
  async pair(link: PairingLink, deviceName: string): Promise<SavedServer> {
    const { result, url } = await pair(link, deviceName);
    const s: SavedServer = {
      id: result.serverId,
      name: result.name,
      urls: [url, ...link.urls.filter((u) => u !== url)],
      deviceId: result.deviceId,
      addedAt: new Date().toISOString(),
    };
    storage.set(tokenKey(s.id), result.token);
    this.connections.get(s.id)?.stop();
    this.connections.delete(s.id);
    this.servers = [...this.servers.filter((x) => x.id !== s.id), s];
    this.save();
    return s;
  }

  /** Forgets a server. Also asks the server to revoke this device's token
   *  (best effort, in the background) so it can't be reused. */
  remove(server: SavedServer) {
    this.connection(server).unpairAndStop();
    this.connections.delete(server.id);
    storage.remove(tokenKey(server.id));
    this.servers = this.servers.filter((s) => s.id !== server.id);
    this.save();
  }

  rename(server: SavedServer, name: string) {
    this.servers = this.servers.map((s) => (s.id === server.id ? { ...s, name } : s));
    const c = this.connections.get(server.id);
    if (c) c.server = { ...c.server, name };
    this.save();
  }

  private promote(url: string, id: string) {
    const s = this.server(id);
    if (!s || s.urls[0] === url) return;
    const urls = [url, ...s.urls.filter((u) => u !== url)];
    this.servers = this.servers.map((x) => (x.id === id ? { ...x, urls } : x));
    const c = this.connections.get(id);
    if (c) c.server = { ...c.server, urls };
    this.save();
  }

  /** Reconnects the connections in use, when the page is shown again. */
  wake() {
    for (const c of this.connections.values()) {
      if (c.state.kind !== "connected") c.retry();
    }
  }
}
