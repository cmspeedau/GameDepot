use crate::api::{ApiClient, ApiError, PairingClaim, PairingInitiate};

/// Where the bearer token lives. The real store is the OS keyring; tests use an
/// in-memory one. The token is never written to the JSON settings file.
pub trait KeyStore: Send + Sync {
    fn get(&self) -> Option<String>;
    fn set(&self, token: &str) -> Result<(), AuthError>;
    fn clear(&self) -> Result<(), AuthError>;
}

#[derive(Debug, thiserror::Error)]
pub enum AuthError {
    #[error("keyring: {0}")]
    Keyring(String),
    #[error("api: {0}")]
    Api(#[from] ApiError),
}

const SERVICE: &str = "gamedepot-desktop";
const ACCOUNT: &str = "token";

pub struct KeyringStore;

impl KeyStore for KeyringStore {
    fn get(&self) -> Option<String> {
        keyring::Entry::new(SERVICE, ACCOUNT)
            .ok()?
            .get_password()
            .ok()
    }

    fn set(&self, token: &str) -> Result<(), AuthError> {
        let e = keyring::Entry::new(SERVICE, ACCOUNT)
            .map_err(|err| AuthError::Keyring(err.to_string()))?;
        e.set_password(token)
            .map_err(|err| AuthError::Keyring(err.to_string()))
    }

    fn clear(&self) -> Result<(), AuthError> {
        let e = keyring::Entry::new(SERVICE, ACCOUNT)
            .map_err(|err| AuthError::Keyring(err.to_string()))?;
        match e.delete_credential() {
            Ok(()) | Err(keyring::Error::NoEntry) => Ok(()),
            Err(err) => Err(AuthError::Keyring(err.to_string())),
        }
    }
}

/// An initiate result to show the user while waiting for dashboard approval.
pub struct PendingPairing {
    pub code: String,
    pub expires_at: String,
}

/// Step 1: ask the server for a pairing code to display.
pub async fn initiate(
    client: &dyn ApiClient,
    name: &str,
    platform: &str,
) -> Result<PendingPairing, AuthError> {
    let PairingInitiate { code, expires_at } = client.initiate_pairing(name, platform).await?;
    Ok(PendingPairing { code, expires_at })
}

/// Step 2: after the code is approved in the dashboard, claim the token and store it.
pub async fn complete(
    client: &dyn ApiClient,
    store: &dyn KeyStore,
    code: &str,
) -> Result<PairingClaim, AuthError> {
    let claim = client.claim_pairing(code).await?;
    store.set(&claim.token)?;
    Ok(claim)
}

/// Build a token-bearing client from the stored token.
pub fn client_for(base_url: &str, store: &dyn KeyStore) -> Result<crate::api::HttpApiClient, AuthError> {
    crate::api::HttpApiClient::new(base_url, store.get())
        .map_err(AuthError::from)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::api::{LibraryItem, Me, ReleaseInfo, SaveManifest, SaveSlot, SaveVersion, PushOutcome};
    use async_trait::async_trait;
    use std::sync::Mutex;

    #[derive(Default)]
    pub struct MemoryStore(Mutex<Option<String>>);

    impl KeyStore for MemoryStore {
        fn get(&self) -> Option<String> {
            self.0.lock().unwrap().clone()
        }
        fn set(&self, token: &str) -> Result<(), AuthError> {
            *self.0.lock().unwrap() = Some(token.to_string());
            Ok(())
        }
        fn clear(&self) -> Result<(), AuthError> {
            *self.0.lock().unwrap() = None;
            Ok(())
        }
    }

    struct MockClient {
        claim: PairingClaim,
    }

    #[async_trait]
    impl ApiClient for MockClient {
        async fn initiate_pairing(&self, _n: &str, _p: &str) -> Result<PairingInitiate, ApiError> {
            Ok(PairingInitiate {
                code: "ABCD1234".into(),
                expires_at: "2026-11-16T12:00:00Z".into(),
            })
        }
        async fn claim_pairing(&self, _c: &str) -> Result<PairingClaim, ApiError> {
            Ok(self.claim.clone())
        }
        async fn me(&self) -> Result<Me, ApiError> {
            unreachable!()
        }
        async fn library(&self) -> Result<Vec<LibraryItem>, ApiError> {
            unreachable!()
        }
        async fn release(&self, _v: &str) -> Result<ReleaseInfo, ApiError> {
            unreachable!()
        }
        async fn get_manifest(&self, _d: &str) -> Result<Vec<u8>, ApiError> {
            unreachable!()
        }
        async fn get_object(&self, _s: &str) -> Result<Vec<u8>, ApiError> {
            unreachable!()
        }
        async fn post_playtime(&self, _g: &str, _s: u64) -> Result<(), ApiError> {
            unreachable!()
        }
        async fn list_saves(&self, _g: Option<&str>) -> Result<Vec<SaveSlot>, ApiError> { unreachable!() }
        async fn save_manifest(&self, _g: &str) -> Result<SaveManifest, ApiError> { unreachable!() }
        async fn push_save(&self, _g: &str, _s: i32, _h: &str, _b: i32, _f: u32, _a: &[u8]) -> Result<PushOutcome, ApiError> { unreachable!() }
        async fn download_save(&self, _g: &str, _s: i32, _seq: Option<i32>) -> Result<Vec<u8>, ApiError> { unreachable!() }
        async fn list_save_versions(&self, _g: &str, _s: i32) -> Result<Vec<SaveVersion>, ApiError> { unreachable!() }
        async fn restore_save(&self, _g: &str, _s: i32, _v: &str) -> Result<SaveVersion, ApiError> { unreachable!() }
        async fn pin_save(&self, _g: &str, _s: i32, _v: &str, _p: bool) -> Result<(), ApiError> { unreachable!() }
        async fn delete_save_version(&self, _g: &str, _s: i32, _v: &str) -> Result<(), ApiError> { unreachable!() }
    }

    #[tokio::test]
    async fn complete_stores_token() {
        let mock = MockClient {
            claim: PairingClaim {
                client_id: "c1".into(),
                token: "secret-token".into(),
            },
        };
        let store = MemoryStore::default();
        assert!(store.get().is_none());
        let claim = complete(&mock, &store, "ABCD1234").await.unwrap();
        assert_eq!(claim.client_id, "c1");
        assert_eq!(store.get().as_deref(), Some("secret-token"));
        store.clear().unwrap();
        assert!(store.get().is_none());
    }
}
