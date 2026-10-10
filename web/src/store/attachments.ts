import type { ImageRef } from "../protocol/models";
import { Observable } from "./observable";
import type { ServerConnection } from "./serverConnection";

/** The long side of an image sent with a prompt. */
const maxPixels = 2048;
/** Under 5 MB once base64-encoded (Claude's limit). */
const maxBytes = 3_500_000;

export interface Prepared {
  data: Blob;
  thumbnail: string;
}

/** Readies an image for agents, whose APIs reject large ones: scales it down
 *  to `maxPixels`, upright, and re-encodes it without metadata (no
 *  location). PNG when it was a PNG or has transparency, so screenshots stay
 *  sharp, else JPEG. As apple/Shared/ImageCoding.swift does. */
export async function prepareImage(file: Blob): Promise<Prepared | undefined> {
  let bitmap: ImageBitmap;
  try {
    bitmap = await createImageBitmap(file);
  } catch {
    return undefined;
  }
  const scale = Math.min(1, maxPixels / Math.max(bitmap.width, bitmap.height));
  const w = Math.max(1, Math.round(bitmap.width * scale));
  const h = Math.max(1, Math.round(bitmap.height * scale));
  const canvas = document.createElement("canvas");
  canvas.width = w;
  canvas.height = h;
  const ctx = canvas.getContext("2d", { willReadFrequently: true });
  if (!ctx) return undefined;
  ctx.drawImage(bitmap, 0, 0, w, h);
  bitmap.close();

  const encode = (type: string, quality?: number) =>
    new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, type, quality));
  let data: Blob | null = null;
  if (file.type === "image/png" || hasAlpha(ctx, w, h)) {
    const png = await encode("image/png");
    if (png && png.size <= maxBytes) data = png;
  }
  if (!data) {
    // JPEG has no transparency: put the image on white.
    ctx.globalCompositeOperation = "destination-over";
    ctx.fillStyle = "#fff";
    ctx.fillRect(0, 0, w, h);
    for (const q of [0.85, 0.6]) {
      const jpeg = await encode("image/jpeg", q);
      if (jpeg && jpeg.size <= maxBytes) {
        data = jpeg;
        break;
      }
    }
  }
  if (!data) return undefined;
  return { data, thumbnail: URL.createObjectURL(data) };
}

function hasAlpha(ctx: CanvasRenderingContext2D, w: number, h: number): boolean {
  const px = ctx.getImageData(0, 0, w, h).data;
  for (let i = 3; i < px.length; i += 4) if (px[i] < 255) return true;
  return false;
}

export type AttachmentState = { kind: "uploading" } | { kind: "uploaded"; ref: ImageRef } | { kind: "failed"; message: string };

export interface Attachment {
  id: string;
  thumbnail: string;
  state: AttachmentState;
}

/** Images attached to a prompt being written. Each uploads as soon as it is
 *  added, so sending doesn't wait. */
export class Attachments extends Observable {
  entries: Attachment[] = [];
  /** Something couldn't be attached, e.g. it wasn't an image. */
  error: string | undefined;
  private data = new Map<string, Blob>();

  constructor(private connection: ServerConnection) {
    super();
  }

  get isEmpty() {
    return this.entries.length === 0;
  }

  /** Every image is on the server, so the prompt can go. */
  get ready() {
    return this.entries.every((e) => e.state.kind === "uploaded");
  }

  get refs(): ImageRef[] {
    return this.entries.flatMap((e) => (e.state.kind === "uploaded" ? [e.state.ref] : []));
  }

  clear() {
    this.entries = [];
    this.data.clear();
    this.changed();
  }

  remove(id: string) {
    this.entries = this.entries.filter((e) => e.id !== id);
    this.data.delete(id);
    this.changed();
  }

  clearError() {
    this.error = undefined;
    this.changed();
  }

  async add(files: Iterable<Blob>) {
    for (const f of files) {
      if (f.type && !f.type.startsWith("image/")) {
        this.error = "That isn't an image.";
        this.changed();
        continue;
      }
      const prepared = await prepareImage(f);
      if (!prepared) {
        this.error = "That isn't an image this browser can read.";
        this.changed();
        continue;
      }
      const entry: Attachment = { id: crypto.randomUUID(), thumbnail: prepared.thumbnail, state: { kind: "uploading" } };
      this.entries = [...this.entries, entry];
      this.data.set(entry.id, prepared.data);
      this.changed();
      void this.upload(entry.id);
    }
  }

  retry(id: string) {
    this.update(id, { kind: "uploading" });
    void this.upload(id);
  }

  private async upload(id: string) {
    const bytes = this.data.get(id);
    if (!bytes) return;
    let state: AttachmentState;
    try {
      state = { kind: "uploaded", ref: await this.connection.uploadImage(bytes) };
    } catch (e) {
      state = { kind: "failed", message: (e as Error).message };
    }
    this.update(id, state);
  }

  private update(id: string, state: AttachmentState) {
    this.entries = this.entries.map((e) => (e.id === id ? { ...e, state } : e));
    this.changed();
  }
}
