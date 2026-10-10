import { useCallback, useEffect, useRef, useState } from "react";

// The Web Speech API's recognizer, which TypeScript's DOM types leave out.
interface Recognition extends EventTarget {
  lang: string;
  continuous: boolean;
  interimResults: boolean;
  start(): void;
  stop(): void;
  abort(): void;
  onresult: ((e: { resultIndex: number; results: ArrayLike<ArrayLike<{ transcript: string }> & { isFinal: boolean }> }) => void) | null;
  onerror: ((e: { error: string }) => void) | null;
  onend: (() => void) | null;
}

type RecognitionClass = new () => Recognition;

const Recognizer: RecognitionClass | undefined =
  typeof window === "undefined" ? undefined : ((window as any).SpeechRecognition ?? (window as any).webkitSpeechRecognition);

export type DictationPhase = "idle" | "listening";

/** Dictation with the browser's speech recognizer, where it has one
 *  (Chrome, Edge, Safari). The apps use on-device models instead. */
export function useDictation() {
  const [phase, setPhase] = useState<DictationPhase>("idle");
  const [error, setError] = useState<string>();
  const rec = useRef<Recognition | null>(null);

  const stop = useCallback(() => {
    rec.current?.stop();
  }, []);

  useEffect(() => () => rec.current?.abort(), []);

  /** Listens until stopped, giving the text heard so far each time it changes. */
  const start = useCallback((onText: (text: string) => void) => {
    if (!Recognizer || rec.current) return;
    const r = new Recognizer();
    r.lang = navigator.language;
    r.continuous = true;
    r.interimResults = true;
    let final = "";
    r.onresult = (e) => {
      let interim = "";
      for (let i = e.resultIndex; i < e.results.length; i++) {
        const res = e.results[i];
        if (res.isFinal) final += res[0].transcript;
        else interim += res[0].transcript;
      }
      onText((final + interim).trim());
    };
    r.onerror = (e) => {
      if (e.error === "no-speech" || e.error === "aborted") return;
      setError(
        e.error === "not-allowed" || e.error === "service-not-allowed"
          ? "The browser isn't allowed to use the microphone."
          : `Dictation failed (${e.error}).`,
      );
    };
    r.onend = () => {
      rec.current = null;
      setPhase("idle");
    };
    rec.current = r;
    try {
      r.start();
      setPhase("listening");
    } catch (e) {
      rec.current = null;
      setError((e as Error).message);
    }
  }, []);

  return { supported: !!Recognizer, phase, start, stop, error, clearError: () => setError(undefined) };
}
