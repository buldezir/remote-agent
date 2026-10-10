import { ChevronLeft, ChevronRight, ImageOff, ImagePlus, RotateCw, X } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { aspectRatio, type ImageRef } from "../protocol/models";
import type { Attachments } from "../store/attachments";
import { useObserved } from "../store/observable";
import { useConnection } from "./context";
import { showModal, Spinner } from "./primitives";

/** An object URL for one of rad's images, once it has loaded. */
export function useImageURL(id: string): { url?: string; failed: boolean } {
  const connection = useConnection();
  useObserved(connection);
  const connected = connection.isConnected;
  const [state, setState] = useState<{ id: string; url?: string; failed: boolean }>({ id, failed: false });
  useEffect(() => {
    if (!connected) return;
    let live = true;
    connection.imageURL(id).then(
      (url) => live && setState({ id, url, failed: false }),
      () => live && setState({ id, failed: true }),
    );
    return () => {
      live = false;
    };
  }, [connection, id, connected]);
  return state.id === id ? state : { failed: false };
}

/** An image at a fixed height, as wide as its aspect ratio allows (between
 *  half and twice the height). */
export function RemoteImage({ image, height, onOpen }: { image: ImageRef; height: number; onOpen?: () => void }) {
  const { url, failed } = useImageURL(image.id);
  const ratio = Math.min(Math.max(aspectRatio(image) ?? 1, 0.5), 2);
  return (
    <button className="remote-image" style={{ height, width: height * ratio }} onClick={onOpen} disabled={!url} aria-label="Open image">
      {url ? <img src={url} alt="" /> : failed ? <ImageOff size={20} className="c-overlay" /> : <span className="placeholder" />}
    </button>
  );
}

/** Thumbnails that wrap onto rows; one opens a viewer that pages through all. */
export function ImageGallery({ images, height, align = "start" }: { images: ImageRef[]; height: number; align?: "start" | "end" }) {
  const [open, setOpen] = useState<number>();
  return (
    <div className="image-gallery" style={{ justifyContent: align === "end" ? "flex-end" : "flex-start" }}>
      {images.map((img, i) => (
        <RemoteImage key={img.id + i} image={img} height={height} onOpen={() => setOpen(i)} />
      ))}
      {open !== undefined && <Lightbox images={images} start={open} onClose={() => setOpen(undefined)} />}
    </div>
  );
}

/** A full-window viewer for an item's images, like Quick Look. */
export function Lightbox({ images, start, onClose }: { images: ImageRef[]; start: number; onClose: () => void }) {
  const [i, setI] = useState(start);
  const ref = useRef<HTMLDialogElement>(null);
  const image = images[i];
  const { url } = useImageURL(image.id);
  const close = useRef(onClose);
  close.current = onClose;
  useEffect(() => {
    const d = ref.current;
    if (d) showModal(d);
    const cancel = (e: Event) => {
      e.preventDefault();
      close.current();
    };
    d?.addEventListener("cancel", cancel);
    return () => {
      d?.removeEventListener("cancel", cancel);
      d?.close();
    };
  }, []);
  const step = (d: number) => setI((n) => (n + d + images.length) % images.length);
  return (
    <dialog
      ref={ref}
      tabIndex={-1}
      className="lightbox"
      onKeyDown={(e) => {
        if (e.key === "ArrowRight") step(1);
        if (e.key === "ArrowLeft") step(-1);
      }}
      onMouseDown={(e) => e.target === e.currentTarget && onClose()}
    >
      {url ? <img src={url} alt="" /> : <Spinner size={28} />}
      <button className="lightbox-close icon-button" onClick={onClose} aria-label="Close">
        <X />
      </button>
      {images.length > 1 && (
        <>
          <button className="lightbox-prev icon-button" onClick={() => step(-1)} aria-label="Previous">
            <ChevronLeft />
          </button>
          <button className="lightbox-next icon-button" onClick={() => step(1)} aria-label="Next">
            <ChevronRight />
          </button>
          <div className="lightbox-count">
            {i + 1} / {images.length}
          </div>
        </>
      )}
      {url && (
        <a className="lightbox-open" href={url} target="_blank" rel="noreferrer">
          Open in new tab
        </a>
      )}
    </dialog>
  );
}

/** An image in a reply, `rad-image:<id>`: the largest of a few heights that
 *  fits the width. */
export function ReplyImage({ id, alt, images }: { id: string; alt?: string; images: ImageRef[] }) {
  const ref = images.find((r) => r.id === id) ?? { id, mimeType: "", size: 0 };
  const [open, setOpen] = useState(false);
  const box = useRef<HTMLSpanElement>(null);
  const [height, setHeight] = useState(240);
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ratio = Math.min(Math.max(aspectRatio(ref) ?? 1, 0.5), 2);
    const fit = () => {
      const w = el.parentElement?.clientWidth ?? 600;
      setHeight([320, 240, 180, 120].find((h) => h * ratio <= w) ?? 120);
    };
    fit();
    const ro = new ResizeObserver(fit);
    if (el.parentElement) ro.observe(el.parentElement);
    return () => ro.disconnect();
  }, [ref.width, ref.height]);
  const all = images.some((r) => r.id === id) ? images : [ref];
  return (
    <span ref={box} className="reply-image" title={alt}>
      <RemoteImage image={ref} height={height} onOpen={() => setOpen(true)} />
      {open && <Lightbox images={all} start={Math.max(0, all.findIndex((r) => r.id === id))} onClose={() => setOpen(false)} />}
    </span>
  );
}

/** The images attached to a prompt being written. */
export function AttachmentStrip({ attachments }: { attachments: Attachments }) {
  useObserved(attachments);
  if (attachments.isEmpty) return null;
  return (
    <div className="attachment-strip">
      {attachments.entries.map((e) => (
        <div key={e.id} className="attachment">
          <img src={e.thumbnail} alt="" style={{ opacity: e.state.kind === "uploaded" ? 1 : 0.5 }} />
          {e.state.kind === "uploading" && (
            <span className="attachment-overlay">
              <Spinner size={14} />
            </span>
          )}
          {e.state.kind === "failed" && (
            <button className="attachment-overlay" title={e.state.message} onClick={() => attachments.retry(e.id)} aria-label="Retry upload">
              <RotateCw size={16} className="c-red" />
            </button>
          )}
          <button className="attachment-remove" onClick={() => attachments.remove(e.id)} aria-label="Remove image">
            <X size={11} strokeWidth={3} />
          </button>
        </div>
      ))}
    </div>
  );
}

/** Opens the file picker for images. */
export function AttachButton({ attachments, children, className, disabled }: { attachments: Attachments; children?: ReactNode; className?: string; disabled?: boolean }) {
  const input = useRef<HTMLInputElement>(null);
  return (
    <>
      <button className={className ?? "round-button c-overlay"} onClick={() => input.current?.click()} aria-label="Add images" title="Add images" disabled={disabled}>
        {children ?? <ImagePlus size={20} />}
      </button>
      <input
        ref={input}
        type="file"
        accept="image/*"
        multiple
        hidden
        onChange={(e) => {
          const files = [...(e.target.files ?? [])];
          e.target.value = "";
          void attachments.add(files);
        }}
      />
    </>
  );
}

/** Images from a paste or a drop. */
export function imageFiles(data: DataTransfer | null): File[] {
  if (!data) return [];
  return [...data.files].filter((f) => f.type.startsWith("image/"));
}

/** Lets images be dropped on an element, adding them to `attachments`. */
export function useImageDrop(attachments: Attachments | undefined) {
  const [over, setOver] = useState(false);
  if (!attachments) return { over: false, handlers: {} };
  return {
    over,
    handlers: {
      onDragOver: (e: React.DragEvent) => {
        if ([...e.dataTransfer.items].some((i) => i.kind === "file")) {
          e.preventDefault();
          setOver(true);
        }
      },
      onDragLeave: (e: React.DragEvent) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node)) setOver(false);
      },
      onDrop: (e: React.DragEvent) => {
        e.preventDefault();
        setOver(false);
        const files = imageFiles(e.dataTransfer);
        if (files.length) void attachments.add(files);
      },
    },
  };
}
