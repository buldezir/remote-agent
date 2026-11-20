//! The small dialogs the views share: a confirmation with its own buttons,
//! a one-line text prompt, and an error.

use std::cell::Cell;
use std::rc::Rc;

use gpui_kit::component::WindowExt as _;
use gpui_kit::component::button::{Button, ButtonVariant, ButtonVariants as _};
use gpui_kit::component::input::{Input, InputEvent, InputState};
use gpui_kit::component::{Sizable as _, h_flex, v_flex};
use gpui_kit::{
    App, AppContext as _, ClickEvent, Focusable as _, ParentElement as _, SharedString, Styled as _, Window, div, px,
};

use crate::ui::palette::Palette;

type Action = Rc<dyn Fn(&mut Window, &mut App)>;
type OnText = Rc<dyn Fn(String, &mut Window, &mut App)>;

/// A button of a confirmation.
pub struct Choice {
    pub label: SharedString,
    pub variant: ButtonVariant,
    pub action: Action,
}

impl Choice {
    pub fn new(
        label: impl Into<SharedString>,
        variant: ButtonVariant,
        f: impl Fn(&mut Window, &mut App) + 'static,
    ) -> Self {
        Self {
            label: label.into(),
            variant,
            action: Rc::new(f),
        }
    }
}

/// A question with buttons of its own beside Cancel, the last one the default.
pub fn confirm(window: &mut Window, cx: &mut App, title: &str, message: &str, choices: Vec<Choice>) {
    let title = SharedString::from(title.to_string());
    let message = SharedString::from(message.to_string());
    let choices = Rc::new(choices);
    window.open_dialog(cx, move |dialog, _, cx| {
        let p = Palette::of(cx);
        let mut buttons = h_flex().gap_2().justify_end().child(
            Button::new("cancel")
                .small()
                .outline()
                .label("Cancel")
                .on_click(|_: &ClickEvent, window, cx| window.close_dialog(cx)),
        );
        for (i, c) in choices.iter().enumerate() {
            let action = c.action.clone();
            buttons = buttons.child(
                Button::new(("choice", i))
                    .small()
                    .with_variant(c.variant)
                    .label(c.label.clone())
                    .on_click(move |_: &ClickEvent, window, cx| {
                        window.close_dialog(cx);
                        action(window, cx);
                    }),
            );
        }
        // Return takes the last choice, the default.
        let default = choices.last().map(|c| c.action.clone());
        dialog
            .w(px(440.))
            .title(title.clone())
            .on_ok(move |_, window, cx| {
                window.close_dialog(cx);
                if let Some(action) = &default {
                    action(window, cx);
                }
                false
            })
            .child(div().text_sm().text_color(p.subtext).child(message.clone()))
            .footer(buttons)
    });
}

/// Asks for a line of text: `on_done` gets it, trimmed, unless cancelled.
#[allow(clippy::too_many_arguments)]
pub fn prompt_text(
    window: &mut Window,
    cx: &mut App,
    title: &str,
    message: &str,
    placeholder: &str,
    initial: &str,
    ok: &str,
    ok_variant: ButtonVariant,
    on_done: impl Fn(String, &mut Window, &mut App) + 'static,
) {
    let input = cx.new(|cx| {
        let mut s = InputState::new(window, cx).placeholder(placeholder.to_string());
        s.set_value(initial.to_string(), window, cx);
        s
    });
    let on_done: OnText = Rc::new(on_done);
    let done = Rc::new(Cell::new(false));
    let submit = {
        let (input, on_done, done) = (input.clone(), on_done.clone(), done.clone());
        Rc::new(move |window: &mut Window, cx: &mut App| {
            if done.replace(true) {
                return;
            }
            let text = input.read(cx).value().trim().to_string();
            window.close_dialog(cx);
            on_done(text, window, cx);
        })
    };
    // Return submits.
    let sub = {
        let submit = submit.clone();
        window.subscribe(&input, cx, move |_, event: &InputEvent, window, cx| {
            if let InputEvent::PressEnter { .. } = event {
                submit(window, cx);
            }
        })
    };
    let sub = Rc::new(Cell::new(Some(sub)));
    let first = Cell::new(true);
    let (title, message, ok) = (
        SharedString::from(title.to_string()),
        SharedString::from(message.to_string()),
        SharedString::from(ok.to_string()),
    );
    window.open_dialog(cx, move |dialog, window, cx| {
        if first.replace(false) {
            input.focus_handle(cx).focus(window, cx);
        }
        let p = Palette::of(cx);
        let submit = submit.clone();
        let on_return = submit.clone();
        let sub = sub.clone();
        dialog
            .w(px(440.))
            .title(title.clone())
            // Return confirms the dialog; it submits what was typed.
            .on_ok(move |_, window, cx| {
                on_return(window, cx);
                false
            })
            .on_close(move |_, _, _| drop(sub.take()))
            .child(
                v_flex()
                    .gap_2()
                    .child(Input::new(&input))
                    .when(!message.is_empty(), |d| {
                        d.child(div().text_xs().text_color(p.overlay).child(message.clone()))
                    }),
            )
            .footer(
                h_flex()
                    .gap_2()
                    .justify_end()
                    .child(
                        Button::new("cancel")
                            .small()
                            .outline()
                            .label("Cancel")
                            .on_click(|_: &ClickEvent, window, cx| window.close_dialog(cx)),
                    )
                    .child(
                        Button::new("ok")
                            .small()
                            .with_variant(ok_variant)
                            .label(ok.clone())
                            .on_click(move |_: &ClickEvent, window, cx| submit(window, cx)),
                    ),
            )
    });
}

/// An error, with OK.
pub fn alert(window: &mut Window, cx: &mut App, title: &str, message: &str) {
    let (title, message) = (
        SharedString::from(title.to_string()),
        SharedString::from(message.to_string()),
    );
    window.open_alert_dialog(cx, move |alert, _, _| {
        alert.title(title.clone()).description(message.clone())
    });
}

use gpui_kit::prelude::FluentBuilder as _;
