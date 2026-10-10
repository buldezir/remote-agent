import { describe, expect, it } from "vitest";
import { pairingLinkInHash, pairingLinkString, parsePairingLink } from "./pairing";

describe("pairing links", () => {
  it("parses a link", () => {
    const l = parsePairingLink("remoteagent://pair?v=1&name=studio&code=abc&url=http%3A%2F%2F100.64.1.2%3A7421&url=http%3A%2F%2F127.0.0.1%3A7421");
    expect(l).toEqual({ name: "studio", code: "abc", urls: ["http://100.64.1.2:7421", "http://127.0.0.1:7421"] });
    expect(parsePairingLink("https://example.com")).toBeUndefined();
    expect(parsePairingLink("remoteagent://pair?code=abc")).toBeUndefined();
  });

  it("decodes a name with spaces", () => {
    // As written by netinfo.PairingLink for `name = "Studio Mac + iPad"`.
    const l = parsePairingLink("remoteagent://pair?code=abc&name=Studio%20Mac%20%2B%20iPad&url=http%3A%2F%2F127.0.0.1%3A7421&v=1");
    expect(l?.name).toBe("Studio Mac + iPad");
  });

  it("round-trips", () => {
    const l = { name: "Studio Mac + iPad", code: "abc", urls: ["http://127.0.0.1:7421"] };
    expect(parsePairingLink(pairingLinkString(l))).toEqual(l);
  });

  it("reads the link from a page's fragment", () => {
    // As `rad pair --web` prints it: Go's url.QueryEscape of the link.
    const link = "remoteagent://pair?code=abc&name=Studio%20Mac&url=http%3A%2F%2F127.0.0.1%3A7421&v=1";
    const hash = "#pair=" + encodeURIComponent(link);
    expect(pairingLinkInHash(hash)).toBe(link);
    expect(pairingLinkInHash("#/s/x")).toBeUndefined();
  });
});
