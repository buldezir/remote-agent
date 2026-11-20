//! Where device tokens live: the login keychain, one generic password per
//! server (service `dev.remote-agent.gpui`, account the server's id), as the
//! other clients keep theirs.
//!
//! macOS lets a later build read, update and delete these items without
//! asking only when every build is signed with the same Apple-issued
//! certificate (a Team ID), which `scripts/sign.sh` does. This goes through
//! the older SecKeychain API on purpose: with SecItem, a rebuilt binary can
//! read an item an earlier build wrote but not delete or replace it
//! (`errSecInvalidOwnerEdit`).

use std::cell::RefCell;
use std::collections::HashMap;
use std::rc::Rc;

use security_framework::os::macos::keychain::SecKeychain;
use security_framework::os::macos::passwords::find_generic_password;

const SERVICE: &str = "dev.remote-agent.gpui";
const NOT_FOUND: i32 = -25300; // errSecItemNotFound

pub enum Tokens {
    Keychain {
        service: String,
    },
    /// For tests, which never touch the developer's keychain.
    #[cfg_attr(not(test), allow(dead_code))]
    Memory(Rc<RefCell<HashMap<String, String>>>),
}

impl Tokens {
    /// The login keychain. A home of its own (`RA_GPUI_HOME`, as for a
    /// scratch rad) gets a service of its own, so that its tokens stay apart.
    pub fn keychain(custom_home: Option<&str>) -> Self {
        let service = match custom_home {
            Some(home) => format!("{SERVICE} ({home})"),
            None => SERVICE.to_string(),
        };
        Tokens::Keychain { service }
    }

    pub fn get(&self, server_id: &str) -> Result<Option<String>, String> {
        match self {
            Tokens::Keychain { service } => match find_generic_password(None, service, server_id) {
                Ok((password, _)) => Ok(Some(String::from_utf8_lossy(&password).into_owned())),
                Err(e) if e.code() == NOT_FOUND => Ok(None),
                Err(e) => Err(format!("Keychain: {e}")),
            },
            Tokens::Memory(map) => Ok(map.borrow().get(server_id).cloned()),
        }
    }

    pub fn set(&self, server_id: &str, token: &str) -> Result<(), String> {
        match self {
            Tokens::Keychain { service } => SecKeychain::default()
                .and_then(|kc| kc.set_generic_password(service, server_id, token.as_bytes()))
                .map_err(|e| format!("Keychain: {e}")),
            Tokens::Memory(map) => {
                map.borrow_mut().insert(server_id.into(), token.into());
                Ok(())
            }
        }
    }

    pub fn delete(&self, server_id: &str) {
        match self {
            Tokens::Keychain { service } => {
                if let Ok((_, item)) = find_generic_password(None, service, server_id) {
                    item.delete();
                }
            }
            Tokens::Memory(map) => {
                map.borrow_mut().remove(server_id);
            }
        }
    }
}
