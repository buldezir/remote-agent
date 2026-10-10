import { useSyncExternalStore } from "react";

/** State that views watch. Subclasses mutate their fields, then `changed()`;
 *  `useObserved` re-renders on each change. */
export class Observable {
  private listeners = new Set<() => void>();
  private version = 0;

  protected changed() {
    this.version++;
    for (const l of [...this.listeners]) l();
  }

  watch = (l: () => void): (() => void) => {
    this.listeners.add(l);
    return () => this.listeners.delete(l);
  };

  getVersion = (): number => this.version;
}

/** Re-renders the component whenever `o` changes. */
export function useObserved<T extends Observable | undefined>(o: T): T {
  useSyncExternalStore(
    o ? o.watch : noopSubscribe,
    o ? o.getVersion : zero,
  );
  return o;
}

const noopSubscribe = () => () => {};
const zero = () => 0;
