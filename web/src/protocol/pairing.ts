import type { PairResult } from "./models";
import { RPCError } from "./rpc";

/** A parsed `remoteagent://pair?...` link from `rad pair`. */
export interface PairingLink {
  name: string;
  code: string;
  urls: string[];
}

export function parsePairingLink(text: string): PairingLink | undefined {
  const s = text.trim();
  if (!s.startsWith("remoteagent://pair")) return undefined;
  const q = s.indexOf("?");
  if (q < 0) return undefined;
  const params = new URLSearchParams(s.slice(q + 1));
  const code = params.get("code");
  if (!code) return undefined;
  const urls = params
    .getAll("url")
    .map((u) => u.replace(/\/+$/, ""))
    .filter((u) => /^https?:\/\/[^/]+$/.test(u));
  if (urls.length === 0) return undefined;
  return { name: params.get("name") || "Server", code, urls };
}

/** The link in a page address's fragment, as `rad pair --web` prints it:
 *  `#pair=<link, URL-encoded>`. */
export function pairingLinkInHash(hash: string): string | undefined {
  const m = /^#?pair=(.+)$/.exec(hash);
  if (!m) return undefined;
  try {
    return decodeURIComponent(m[1]);
  } catch {
    return undefined;
  }
}

export function pairingLinkString(l: PairingLink): string {
  const p = new URLSearchParams({ name: l.name, code: l.code });
  for (const u of l.urls) p.append("url", u);
  return "remoteagent://pair?" + p.toString().replace(/\+/g, "%20");
}

/** Redeems the pairing code against each URL in order. Returns the result
 *  and the URL that worked. */
export async function pair(link: PairingLink, deviceName: string): Promise<{ result: PairResult; url: string }> {
  let lastError: Error = new RPCError("unreachable", "No server address was reachable");
  for (const base of link.urls) {
    let r: Response;
    try {
      r = await fetch(base + "/v1/pair", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ code: link.code, deviceName }),
        signal: AbortSignal.timeout(6000),
      });
    } catch {
      lastError = new RPCError(
        "unreachable",
        `Could not reach ${link.urls.join(" or ")}. Check that rad is running and allows this page in its web_origins.`,
      );
      continue;
    }
    if (r.status === 200) return { result: await r.json(), url: base };
    const body = await r.json().catch(() => null);
    // The server answered; trying other URLs won't help.
    if (body?.code) throw new RPCError(body.code, body.code === "unauthorized" ? "The pairing code is wrong or has expired. Run `rad pair` again." : body.message);
    lastError = new RPCError("http", `Server returned HTTP ${r.status}`);
  }
  throw lastError;
}
