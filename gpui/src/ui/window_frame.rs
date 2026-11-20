//! The window opens where it was when the app last quit: on the same display
//! while it's connected, at the same place and size, kept on the screen, and
//! in full screen if it was. The first launch centers it.

use gpui_kit::{App, Bounds, DisplayId, Window, WindowBounds, point, px, size};

use crate::store::prefs::WindowFrame;
use crate::ui::state::{AppPrefs, prefs};

pub const DEFAULT_SIZE: (f32, f32) = (1240., 800.);
pub const MIN_SIZE: (f32, f32) = (720., 480.);

/// The window's bounds at launch, and the display to open it on.
pub fn initial(cx: &App) -> (WindowBounds, Option<DisplayId>) {
    let centered = WindowBounds::Windowed(Bounds::centered(None, size(px(DEFAULT_SIZE.0), px(DEFAULT_SIZE.1)), cx));
    let Some(saved) = prefs(cx).window.clone() else {
        return (centered, None);
    };
    let display = cx
        .displays()
        .into_iter()
        .find(|d| !saved.display.is_empty() && d.uuid().is_ok_and(|u| u.to_string() == saved.display))
        .or_else(|| cx.primary_display());
    let Some(display) = display else {
        return (centered, None);
    };
    let area = display.bounds().size;
    let (x, y, w, h) = fit(&saved, (area.width.as_f32(), area.height.as_f32()));
    let bounds = Bounds::new(point(px(x), px(y)), size(px(w), px(h)));
    let bounds = if saved.fullscreen {
        WindowBounds::Fullscreen(bounds)
    } else {
        WindowBounds::Windowed(bounds)
    };
    (bounds, Some(display.id()))
}

/// The window's frame now; in full screen, the frame it leaves it for.
pub fn current(window: &Window, cx: &App) -> WindowFrame {
    let (bounds, fullscreen) = match window.window_bounds() {
        WindowBounds::Fullscreen(b) => (b, true),
        WindowBounds::Windowed(b) | WindowBounds::Maximized(b) => (b, false),
    };
    WindowFrame {
        x: bounds.origin.x.as_f32(),
        y: bounds.origin.y.as_f32(),
        width: bounds.size.width.as_f32(),
        height: bounds.size.height.as_f32(),
        fullscreen,
        display: window
            .display(cx)
            .and_then(|d| d.uuid().ok())
            .map(|u| u.to_string())
            .unwrap_or_default(),
    }
}

/// Writes the frame, without redrawing: nothing on screen depends on it.
pub fn save(frame: WindowFrame, cx: &mut App) {
    cx.global_mut::<AppPrefs>().0.update(|p| p.window = Some(frame));
}

/// The frame on a display of `area`: no smaller than the window may be, no
/// larger than the display, and wholly on it.
fn fit(frame: &WindowFrame, area: (f32, f32)) -> (f32, f32, f32, f32) {
    let w = frame.width.clamp(MIN_SIZE.0, area.0.max(MIN_SIZE.0));
    let h = frame.height.clamp(MIN_SIZE.1, area.1.max(MIN_SIZE.1));
    let x = frame.x.clamp(0., (area.0 - w).max(0.));
    let y = frame.y.clamp(0., (area.1 - h).max(0.));
    (x, y, w, h)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn frame(x: f32, y: f32, width: f32, height: f32) -> WindowFrame {
        WindowFrame {
            x,
            y,
            width,
            height,
            ..Default::default()
        }
    }

    #[test]
    fn keeps_a_frame_that_fits() {
        assert_eq!(
            fit(&frame(100., 50., 1000., 700.), (1728., 1117.)),
            (100., 50., 1000., 700.)
        );
    }

    #[test]
    fn brings_the_window_back_on_a_smaller_display() {
        // From a large external display to the laptop's.
        assert_eq!(
            fit(&frame(2000., 900., 2400., 1300.), (1512., 982.)),
            (0., 0., 1512., 982.)
        );
        assert_eq!(
            fit(&frame(1400., 800., 1000., 700.), (1512., 982.)),
            (512., 282., 1000., 700.)
        );
        assert_eq!(
            fit(&frame(-300., -40., 900., 600.), (1512., 982.)),
            (0., 0., 900., 600.)
        );
    }

    #[test]
    fn never_smaller_than_the_minimum() {
        assert_eq!(fit(&frame(10., 10., 100., 80.), (1512., 982.)), (10., 10., 720., 480.));
    }
}
