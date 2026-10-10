import { Observable, useObserved } from "../store/observable";
import { storage } from "../store/storage";

export type Appearance = "system" | "dark" | "light";

/** The body text size at each step of the interface setting. */
export const interfaceSizes = [12, 13, 14, 15, 16, 18, 20];
export const defaultInterface = 2;
export const messageSizes = { min: 11, max: 24 };
export const defaultMessages = 15;

/** Message fonts offered besides any family typed in. */
export const messageFonts: { id: string; name: string; css: string }[] = [
  { id: "", name: "System", css: 'system-ui, -apple-system, "Segoe UI", Roboto, sans-serif' },
  { id: "system:serif", name: "System Serif", css: 'ui-serif, "New York", Georgia, serif' },
  { id: "system:rounded", name: "System Rounded", css: 'ui-rounded, "SF Pro Rounded", system-ui, sans-serif' },
  { id: "system:monospaced", name: "System Mono", css: "" },
];
export const codeFontCSS = 'ui-monospace, "SF Mono", SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace';

/** The settings, saved in this browser. */
class Settings extends Observable {
  appearance: Appearance = "system";
  /** A step of `interfaceSizes`. */
  interface = defaultInterface;
  /** Pixels. */
  messages = defaultMessages;
  /** "" for the system font, "system:<design>", or a family name. */
  messageFont = "";
  /** "" for the system's monospaced font, or a family name. */
  codeFont = "";
  diffWrap = true;

  constructor() {
    super();
    const a = storage.get("appearance");
    if (a === "dark" || a === "light") this.appearance = a;
    this.interface = clamp(int(storage.get("interfaceTextSize"), defaultInterface), 0, interfaceSizes.length - 1);
    this.messages = clamp(int(storage.get("messageTextSize"), defaultMessages), messageSizes.min, messageSizes.max);
    this.messageFont = storage.get("messageFont") ?? "";
    this.codeFont = storage.get("codeFont") ?? "";
    this.diffWrap = storage.get("diffWrap") !== "false";
  }

  update(patch: Partial<Pick<Settings, "appearance" | "interface" | "messages" | "messageFont" | "codeFont" | "diffWrap">>) {
    Object.assign(this, patch);
    storage.set("appearance", this.appearance);
    storage.set("interfaceTextSize", String(this.interface));
    storage.set("messageTextSize", String(this.messages));
    storage.set("messageFont", this.messageFont);
    storage.set("codeFont", this.codeFont);
    storage.set("diffWrap", String(this.diffWrap));
    this.changed();
    this.apply();
  }

  get isDefault(): boolean {
    return (
      this.appearance === "system" &&
      this.interface === defaultInterface &&
      this.messages === defaultMessages &&
      !this.messageFont &&
      !this.codeFont
    );
  }

  restoreDefaults() {
    this.update({ appearance: "system", interface: defaultInterface, messages: defaultMessages, messageFont: "", codeFont: "" });
  }

  private dark = matchMedia("(prefers-color-scheme: dark)");

  /** Sets the theme, sizes and fonts on the page, as CSS variables. */
  apply() {
    const root = document.documentElement;
    const dark = this.appearance === "dark" || (this.appearance === "system" && this.dark.matches);
    root.dataset.theme = dark ? "dark" : "light";
    root.style.colorScheme = dark ? "dark" : "light";
    const code = this.codeFont ? `${quote(this.codeFont)}, ${codeFontCSS}` : codeFontCSS;
    const known = messageFonts.find((f) => f.id === this.messageFont);
    const message = known ? known.css || code : `${quote(this.messageFont)}, ${messageFonts[0].css}`;
    root.style.setProperty("--ui-size", interfaceSizes[this.interface] + "px");
    root.style.setProperty("--message-size", this.messages + "px");
    root.style.setProperty("--message-font", message);
    root.style.setProperty("--code-font", code);
  }

  watchSystem() {
    this.dark.addEventListener("change", () => this.apply());
  }
}

function int(s: string | null, fallback: number): number {
  const n = s === null ? NaN : parseInt(s, 10);
  return Number.isFinite(n) ? n : fallback;
}

function clamp(n: number, lo: number, hi: number) {
  return Math.min(Math.max(n, lo), hi);
}

function quote(family: string) {
  return '"' + family.replace(/["\\]/g, "") + '"';
}

export const settings = new Settings();

export function useSettings() {
  return useObserved(settings);
}
