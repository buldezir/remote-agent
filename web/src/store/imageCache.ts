/** Object URLs for rad's images, by id. An id names its content, so a URL
 *  never goes stale; loads of the same id are shared. The browser's HTTP
 *  cache keeps the bytes across reloads (rad marks them immutable). */
class ImageCache {
  private urls = new Map<string, Promise<string>>();

  url(id: string, load: () => Promise<Blob>): Promise<string> {
    let u = this.urls.get(id);
    if (!u) {
      u = load().then((b) => URL.createObjectURL(b));
      u.catch(() => this.urls.delete(id)); // try again next time
      this.urls.set(id, u);
    }
    return u;
  }

  /** Keeps bytes just uploaded, so showing them doesn't download them again. */
  store(id: string, data: Blob) {
    if (!this.urls.has(id)) this.urls.set(id, Promise.resolve(URL.createObjectURL(data)));
  }
}

export const imageCache = new ImageCache();
