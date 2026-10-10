import { ClipboardPaste, TriangleAlert } from "lucide-react";
import { useState } from "react";
import { parsePairingLink } from "../protocol/pairing";
import type { SavedServer } from "../store/serverStore";
import { useServerStore } from "./context";
import { Modal, Spinner } from "./primitives";

/** "Browser on macOS", for the server's list of devices. */
function defaultDeviceName(): string {
  const ua = navigator.userAgent;
  const os = /iPhone|iPad/.test(ua) ? "iOS" : /Mac/.test(ua) ? "macOS" : /Android/.test(ua) ? "Android" : /Windows/.test(ua) ? "Windows" : /Linux/.test(ua) ? "Linux" : "";
  const browser = /Edg\//.test(ua) ? "Edge" : /Firefox\//.test(ua) ? "Firefox" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : "Browser";
  return os ? `${browser} on ${os}` : browser;
}

export function AddServer({ initialLink, onClose, onPaired }: { initialLink?: string; onClose: () => void; onPaired: (s: SavedServer) => void }) {
  const store = useServerStore();
  const [linkText, setLinkText] = useState(initialLink ?? "");
  const [deviceName, setDeviceName] = useState(defaultDeviceName);
  const [pairing, setPairing] = useState(false);
  const [error, setError] = useState<string>();
  const link = parsePairingLink(linkText);

  const pair = async () => {
    if (!link || pairing) return;
    setPairing(true);
    setError(undefined);
    try {
      const s = await store.pair(link, deviceName.trim() || defaultDeviceName());
      onClose();
      onPaired(s);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setPairing(false);
    }
  };

  return (
    <Modal
      title="Pair a server"
      onClose={onClose}
      trailing={
        pairing ? (
          <Spinner />
        ) : (
          <button className="link strong" disabled={!link} onClick={pair}>
            Pair
          </button>
        )
      }
    >
      <form
        className="form"
        onSubmit={(e) => {
          e.preventDefault();
          void pair();
        }}
      >
        <h4 className="form-header">Pairing link</h4>
        <section className="form-section">
          <div className="form-row">
            <textarea
              className="link-field mono"
              rows={3}
              placeholder="remoteagent://pair?…"
              value={linkText}
              onChange={(e) => setLinkText(e.target.value)}
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              data-autofocus={initialLink ? undefined : true}
            />
            {navigator.clipboard?.readText && (
              <button
                type="button"
                className="icon-button"
                title="Paste"
                aria-label="Paste"
                onClick={async () => setLinkText(await navigator.clipboard.readText().catch(() => linkText))}
              >
                <ClipboardPaste size={18} />
              </button>
            )}
          </div>
        </section>
        <p className="form-footer">
          On the computer with rad, run <code>rad pair</code> and paste the link it prints, or open the one <code>rad pair --web {location.origin}</code> prints. The
          code is valid for 10 minutes and works once.
        </p>

        {link && (
          <>
            <h4 className="form-header">Server</h4>
            <section className="form-section">
              <div className="form-row">
                <span>Name</span>
                <span className="hint">{link.name}</span>
              </div>
              {link.urls.map((u) => (
                <div key={u} className="form-row mono small hint">
                  {u}
                </div>
              ))}
            </section>
          </>
        )}

        <h4 className="form-header">This device</h4>
        <section className="form-section">
          <label className="form-row">
            <span>Device name</span>
            <input className="text-field" value={deviceName} onChange={(e) => setDeviceName(e.target.value)} />
          </label>
        </section>

        {error && (
          <div className="form-message c-red">
            <TriangleAlert size={15} /> {error}
          </div>
        )}
        <button type="submit" hidden />
      </form>
    </Modal>
  );
}
