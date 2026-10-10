/** localStorage that a private window or blocked site data can't break. */
export const storage = {
  get(key: string): string | null {
    try {
      return localStorage.getItem(key);
    } catch {
      return null;
    }
  },
  set(key: string, value: string) {
    try {
      localStorage.setItem(key, value);
    } catch {
      // not saved
    }
  },
  remove(key: string) {
    try {
      localStorage.removeItem(key);
    } catch {
      // nothing to remove
    }
  },
};
