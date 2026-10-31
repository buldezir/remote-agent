import { AArrowDown, AArrowUp } from "lucide-react";
import { useState } from "react";
import { UserBubble } from "./items/ItemView";
import { Markdown } from "./Markdown";
import { notifications, useNotifications } from "./notifications";
import { Modal } from "./primitives";
import { interfaceSizes, messageFonts, messageSizes, settings, terminalPlacements, useSettings, type Appearance, type TerminalPlacement } from "./settings";
import { sidebarLayouts, type SidebarLayout } from "./sidebarLayout";

const codeFonts = ["", "Menlo", "SF Mono", "Monaco", "JetBrains Mono", "Fira Code", "Cascadia Code", "Consolas", "Source Code Pro", "IBM Plex Mono"];
const otherFont = "__other";

export function SettingsView({ onClose }: { onClose: () => void }) {
  const s = useSettings();
  return (
    <Modal
      title="Settings"
      onClose={onClose}
      leading={<span />}
      trailing={
        <button className="link strong" onClick={onClose}>
          Done
        </button>
      }
    >
      <div className="form">
        <h4 className="form-header">Appearance</h4>
        <section className="form-section">
          <label className="form-row">
            <span>Theme</span>
            <select value={s.appearance} onChange={(e) => settings.update({ appearance: e.target.value as Appearance })}>
              <option value="system">System</option>
              <option value="dark">Dark</option>
              <option value="light">Light</option>
            </select>
          </label>
        </section>
        <p className="form-footer">Catppuccin Latte when light, Frappé when dark.</p>

        <h4 className="form-header">Interface</h4>
        <section className="form-section">
          <SizeSlider
            value={s.interface}
            min={0}
            max={interfaceSizes.length - 1}
            text={interfaceSizes[s.interface] + " px"}
            onChange={(v) => settings.update({ interface: v })}
          />
        </section>
        <p className="form-footer">Lists and labels.</p>

        <h4 className="form-header">Messages</h4>
        <section className="form-section">
          <FontPicker
            label="Font"
            value={s.messageFont}
            options={messageFonts.map((f) => [f.id, f.name])}
            onChange={(v) => settings.update({ messageFont: v })}
          />
          <FontPicker
            label="Code font"
            value={s.codeFont}
            options={codeFonts.map((f) => [f, f || "System Mono"])}
            onChange={(v) => settings.update({ codeFont: v })}
          />
          <SizeSlider value={s.messages} min={messageSizes.min} max={messageSizes.max} text={s.messages + " px"} onChange={(v) => settings.update({ messages: v })} />
        </section>
        <p className="form-footer">Your prompts, the agent's replies and its tool calls. Tool calls and diffs use the code font.</p>

        <h4 className="form-header">Preview</h4>
        <section className="form-section">
          <div className="form-row preview">
            <UserBubble text="Add tests for the parser." />
            <div className="assistant">
              <Markdown text={"Done. The new tests cover **empty input** and `\\r\\n` line endings, and all 42 pass:\n\n```\nnpm test -- parser\n```"} />
            </div>
          </div>
        </section>

        <h4 className="form-header">Transcript</h4>
        <section className="form-section">
          <label className="form-row">
            <span>Group tool calls</span>
            <input type="checkbox" className="switch" checked={s.groupToolCalls} onChange={(e) => settings.update({ groupToolCalls: e.target.checked })} />
          </label>
        </section>
        <p className="form-footer">Folds each run of the agent's tool calls into one line, such as “Ran 4 commands, edited 3 files”, which opens to show them.</p>

        <h4 className="form-header">Sidebar</h4>
        <section className="form-section">
          <label className="form-row">
            <span>Layout</span>
            <select value={s.sidebarLayout} onChange={(e) => settings.update({ sidebarLayout: e.target.value as SidebarLayout })}>
              {sidebarLayouts.map((l) => (
                <option key={l.id} value={l.id}>
                  {l.name}
                </option>
              ))}
            </select>
          </label>
        </section>
        <p className="form-footer">
          {sidebarLayouts.find((l) => l.id === s.sidebarLayout)?.summary} A narrow window has the Inbox and the Servers as tabs.
        </p>

        <h4 className="form-header">Terminal</h4>
        <section className="form-section">
          <label className="form-row">
            <span>Opens</span>
            <select value={s.terminalPlacement} onChange={(e) => settings.update({ terminalPlacement: e.target.value as TerminalPlacement })}>
              {terminalPlacements.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </label>
          <label className="form-row">
            <span>Copy on select</span>
            <input type="checkbox" className="switch" checked={s.copyOnSelect} onChange={(e) => settings.update({ copyOnSelect: e.target.checked })} />
          </label>
        </section>
        <p className="form-footer">
          Where a session's terminal opens. In a narrow window it takes the place of the messages. Copy on select puts the text you select on the clipboard.
        </p>

        <NotificationsSection />

        <section className="form-section">
          <button className="form-row link" disabled={s.isDefault} onClick={() => settings.restoreDefaults()}>
            Restore Defaults
          </button>
        </section>
      </div>
    </Modal>
  );
}

/** Turns notifications on and off, and says when the browser holds them back. */
function NotificationsSection() {
  const n = useNotifications();
  const blocked = n.supported && n.permission === "denied";
  return (
    <>
      <h4 className="form-header">Notifications</h4>
      <section className="form-section">
        <label className="form-row">
          <span>Show notifications</span>
          <input
            type="checkbox"
            className="switch"
            checked={n.enabled}
            disabled={!n.supported || blocked}
            onChange={(e) => void notifications.setEnabled(e.target.checked)}
          />
        </label>
      </section>
      <p className="form-footer">
        {!n.supported
          ? "This browser can't show notifications from this page. Pages need https or localhost."
          : blocked
            ? "This browser blocks notifications from this page. Allow them in its site settings."
            : "When a session needs you, finishes or fails, unless this page shows it in front. Only while the page is open."}
      </p>
    </>
  );
}

function SizeSlider({ value, min, max, text, onChange }: { value: number; min: number; max: number; text: string; onChange: (v: number) => void }) {
  return (
    <label className="form-row">
      <span>Text size</span>
      <span className="slider">
        <AArrowDown size={16} className="c-subtext" />
        <input type="range" min={min} max={max} step={1} value={value} onChange={(e) => onChange(Number(e.target.value))} aria-valuetext={text} />
        <AArrowUp size={18} className="c-subtext" />
        <span className="slider-value hint">{text}</span>
      </span>
    </label>
  );
}

/** A choice of fonts, or any family installed, typed in. */
function FontPicker({ label, value, options, onChange }: { label: string; value: string; options: [string, string][]; onChange: (v: string) => void }) {
  const known = options.some(([id]) => id === value);
  const [other, setOther] = useState(!known);
  return (
    <>
      <label className="form-row">
        <span>{label}</span>
        <select
          value={other ? otherFont : value}
          onChange={(e) => {
            if (e.target.value === otherFont) setOther(true);
            else {
              setOther(false);
              onChange(e.target.value);
            }
          }}
        >
          {options.map(([id, name]) => (
            <option key={id} value={id}>
              {name}
            </option>
          ))}
          <option value={otherFont}>Other…</option>
        </select>
      </label>
      {other && (
        <label className="form-row">
          <span>Family</span>
          <input className="text-field" placeholder="Installed font family" defaultValue={known ? "" : value} onChange={(e) => onChange(e.target.value.trim())} spellCheck={false} />
        </label>
      )}
    </>
  );
}
