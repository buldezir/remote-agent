//! Images beside the socket (PROTOCOL.md, "Images"): uploading what the
//! user attaches, after scaling it down for the agents, and fetching what
//! sessions show, cached on disk by id (an id names its content) and in
//! memory as GPUI images.

use std::collections::HashMap;
use std::io::Cursor;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use gpui_kit::{App, AppContext as _, Global, Image, ImageFormat};
use image::{DynamicImage, GenericImageView as _, ImageFormat as Format, imageops::FilterType};

use crate::protocol::{ImageRef, RpcError};
use crate::store::servers::home_dir;

/// The long side of an image sent with a prompt.
const MAX_PIXELS: u32 = 2048;
/// Under 5 MB once base64-encoded (Claude's limit).
const MAX_IMAGE_BYTES: usize = 3_500_000;

/// An image ready to upload: its bytes, re-encoded, and its type.
#[derive(Clone)]
pub struct Prepared {
    pub bytes: Vec<u8>,
    pub format: ImageFormat,
}

/// Readies an image for agents, whose APIs reject large ones: it scales it
/// down to 2048 pixels on the long side and re-encodes it without metadata.
/// PNG when it was one or has transparency, so screenshots stay sharp, else
/// JPEG. As the other clients do.
pub fn prepare(data: &[u8]) -> Result<Prepared, String> {
    let format = image::guess_format(data).map_err(|_| not_image())?;
    let img = image::load_from_memory_with_format(data, format).map_err(|_| not_image())?;
    let (w, h) = img.dimensions();
    let img = if w.max(h) > MAX_PIXELS {
        img.resize(MAX_PIXELS, MAX_PIXELS, FilterType::Lanczos3)
    } else {
        img
    };
    if format == Format::Png || img.color().has_alpha() {
        let mut out = Vec::new();
        if img.write_to(&mut Cursor::new(&mut out), Format::Png).is_ok() && out.len() <= MAX_IMAGE_BYTES {
            return Ok(Prepared {
                bytes: out,
                format: ImageFormat::Png,
            });
        }
    }
    // JPEG has no transparency: put the image on white.
    let flat = DynamicImage::ImageRgb8(flatten(&img));
    for quality in [85u8, 70, 55, 40] {
        let mut out = Vec::new();
        let encoder = image::codecs::jpeg::JpegEncoder::new_with_quality(&mut out, quality);
        if flat.write_with_encoder(encoder).is_ok() && out.len() <= MAX_IMAGE_BYTES {
            return Ok(Prepared {
                bytes: out,
                format: ImageFormat::Jpeg,
            });
        }
    }
    Err("The image is too large to send.".into())
}

fn flatten(img: &DynamicImage) -> image::RgbImage {
    let rgba = img.to_rgba8();
    let mut out = image::RgbImage::new(rgba.width(), rgba.height());
    for (x, y, p) in rgba.enumerate_pixels() {
        let a = p[3] as u32;
        let blend = |c: u8| ((c as u32 * a + 255 * (255 - a)) / 255) as u8;
        out.put_pixel(x, y, image::Rgb([blend(p[0]), blend(p[1]), blend(p[2])]));
    }
    out
}

fn not_image() -> String {
    "That isn't an image this app can read (PNG, JPEG, GIF or WebP).".into()
}

/// The GPUI format of bytes, from their first bytes.
pub fn format_of(data: &[u8]) -> Option<ImageFormat> {
    Some(match image::guess_format(data).ok()? {
        Format::Png => ImageFormat::Png,
        Format::Jpeg => ImageFormat::Jpeg,
        Format::Gif => ImageFormat::Gif,
        Format::WebP => ImageFormat::Webp,
        _ => return None,
    })
}

fn agent() -> ureq::Agent {
    ureq::Agent::config_builder()
        .timeout_global(Some(Duration::from_secs(60)))
        .http_status_as_error(false)
        .build()
        .into()
}

/// `POST /v1/images` (blocking).
pub fn upload(base: &str, token: &str, bytes: &[u8]) -> Result<ImageRef, RpcError> {
    let mut resp = agent()
        .post(&format!("{base}/v1/images"))
        .header("Authorization", &format!("Bearer {token}"))
        .send(bytes)
        .map_err(|e| RpcError::new("unreachable", e.to_string()))?;
    let status = resp.status().as_u16();
    let text = resp.body_mut().read_to_string().unwrap_or_default();
    match status {
        200 => serde_json::from_str(&text).map_err(|e| RpcError::new("invalid", e.to_string())),
        413 => Err(RpcError::new("invalid", "The image is too large for rad.")),
        _ => Err(serde_json::from_str(&text)
            .unwrap_or_else(|_| RpcError::new("http", format!("rad answered HTTP {status}")))),
    }
}

/// `GET /v1/images/<id>` (blocking).
pub fn download(base: &str, token: &str, id: &str) -> Result<Vec<u8>, RpcError> {
    let mut resp = agent()
        .get(&format!("{base}/v1/images/{id}"))
        .header("Authorization", &format!("Bearer {token}"))
        .call()
        .map_err(|e| RpcError::new("unreachable", e.to_string()))?;
    let status = resp.status().as_u16();
    if status != 200 {
        return Err(RpcError::new("http", format!("rad answered HTTP {status}")));
    }
    resp.body_mut()
        .with_config()
        .limit(64 << 20)
        .read_to_vec()
        .map_err(|e| RpcError::new("unreachable", e.to_string()))
}

fn cache_dir() -> PathBuf {
    home_dir().join("images")
}

/// An id kept to a file name, whatever rad sent.
fn safe_id(id: &str) -> String {
    id.replace(['/', '\\'], "_").replace("..", "_")
}

#[derive(Clone)]
pub enum Loaded {
    Loading,
    Ready(Arc<Image>),
    Failed(String),
}

/// The images loaded so far, by id. Views ask with `get`, which starts a
/// load the first time, and redraw once it's done.
#[derive(Default)]
pub struct Images {
    images: HashMap<String, Loaded>,
}

impl Global for Images {}

impl Images {
    /// The image, loading it from the disk cache or rad (`base`, `token`) the
    /// first time; `Loading` meanwhile. The window redraws when it's in.
    pub fn get(id: &str, base: Option<&str>, token: &str, cx: &mut App) -> Loaded {
        if !cx.has_global::<Images>() {
            cx.set_global(Images::default());
        }
        if let Some(l) = cx.global::<Images>().images.get(id) {
            return l.clone();
        }
        let Some(base) = base else {
            return Loaded::Loading;
        };
        cx.global_mut::<Images>().images.insert(id.to_string(), Loaded::Loading);
        let (id, base, token) = (id.to_string(), base.to_string(), token.to_string());
        let task = cx.background_spawn({
            let id = id.clone();
            async move {
                let path = cache_dir().join(safe_id(&id));
                if let Ok(data) = std::fs::read(&path) {
                    return Ok(data);
                }
                let data = download(&base, &token, &id)?;
                let _ = std::fs::create_dir_all(cache_dir());
                let _ = std::fs::write(&path, &data);
                Ok::<_, RpcError>(data)
            }
        });
        cx.spawn(async move |cx| {
            let result = task.await;
            cx.update(|cx| {
                let loaded = match result {
                    Ok(data) => match format_of(&data) {
                        Some(f) => Loaded::Ready(Arc::new(Image::from_bytes(f, data))),
                        None => Loaded::Failed("Not an image".into()),
                    },
                    Err(e) => Loaded::Failed(e.to_string()),
                };
                cx.global_mut::<Images>().images.insert(id, loaded);
                cx.refresh_windows();
            });
        })
        .detach();
        Loaded::Loading
    }

    /// Keeps bytes just uploaded, so showing them doesn't fetch them again.
    pub fn store(id: &str, prepared: &Prepared, cx: &mut App) {
        if !cx.has_global::<Images>() {
            cx.set_global(Images::default());
        }
        let image = Arc::new(Image::from_bytes(prepared.format, prepared.bytes.clone()));
        cx.global_mut::<Images>()
            .images
            .insert(id.to_string(), Loaded::Ready(image));
        let (path, bytes) = (cache_dir().join(safe_id(id)), prepared.bytes.clone());
        cx.background_spawn(async move {
            let _ = std::fs::create_dir_all(cache_dir());
            let _ = std::fs::write(path, bytes);
        })
        .detach();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn encode(img: &DynamicImage, format: Format) -> Vec<u8> {
        let mut out = Vec::new();
        img.write_to(&mut Cursor::new(&mut out), format).unwrap();
        out
    }

    #[test]
    fn prepares_images() {
        let big = DynamicImage::new_rgba8(4096, 1024);
        let out = prepare(&encode(&big, Format::Png)).unwrap();
        assert!(matches!(out.format, ImageFormat::Png));
        let img = image::load_from_memory(&out.bytes).unwrap();
        assert_eq!(img.dimensions(), (2048, 512));
        // A photo stays a JPEG.
        let photo = DynamicImage::new_rgb8(10, 10);
        let out = prepare(&encode(&photo, Format::Jpeg)).unwrap();
        assert!(matches!(out.format, ImageFormat::Jpeg));
        assert!(prepare(b"not an image").is_err());
    }
}
