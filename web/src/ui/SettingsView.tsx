import { AArrowDown, AArrowUp } from "lucide-react";
import { useState } from "react";
import { UserBubble } from "./items/ItemView";
import { Markdown } from "./Markdown";
import { Modal } from "./primitives";
import { interfaceSizes, messageFonts, messageSizes, settings, useSettings, type Appearance } from "./settings";

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

        <section className="form-section">
          <button className="form-row link" disabled={s.isDefault} onClick={() => settings.restoreDefaults()}>
            Restore Defaults
          </button>
        </section>
      </div>
    </Modal>
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
