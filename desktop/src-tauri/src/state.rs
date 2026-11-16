use std::path::PathBuf;
use std::sync::Arc;

use crate::api::HttpApiClient;
use crate::auth::{KeyStore, KeyringStore};
use crate::config::Settings;
use crate::playtime::{PlaySession, RealClock};
use crate::store::Store;
use std::collections::HashMap;
use std::sync::Mutex;

/// Shared application state managed by Tauri. Holds the SQLite store handle, the
/// settings file location, and the token store. Cheap to clone is not required; commands
/// access it via `State<AppState>`.
pub struct AppState {
    pub store: Arc<Store>,
    pub settings_path: PathBuf,
    pub db_path: PathBuf,
    pub tokens: Box<dyn KeyStore + Send + Sync>,
    pub playtime_sessions: Mutex<HashMap<String, PlaySession<RealClock>>>,
}

impl AppState {
    /// Build state rooted at a directory (the OS app-data dir in production, a temp dir
    /// in tests). Opens or creates the SQLite database and ensures the settings file.
    pub fn new(root: PathBuf) -> Result<Self, String> {
        std::fs::create_dir_all(&root).map_err(|e| e.to_string())?;
        let db_path = root.join("gamedepot.sqlite");
        let store = Store::open(&db_path).map_err(|e| e.to_string())?;
        Ok(Self {
            store: Arc::new(store),
            settings_path: root.join("settings.json"),
            db_path,
            tokens: Box::new(KeyringStore),
            playtime_sessions: Mutex::new(HashMap::new()),
        })
    }

    pub fn settings(&self) -> Settings {
        crate::config::load(&self.settings_path)
    }

    pub fn save_settings(&self, s: &Settings) -> Result<(), String> {
        crate::config::save(&self.settings_path, s).map_err(|e| e.to_string())
    }

    /// A client bound to the configured server and the stored token (if any).
    pub fn client(&self) -> Result<HttpApiClient, String> {
        let url = self.settings().normalized_server_url().map_err(|e| e.to_string())?;
        HttpApiClient::new(url, self.tokens.get()).map_err(|e| e.to_string())
    }

    pub fn playtime_start(&self, game_id: &str) {
        let mut m = self.playtime_sessions.lock().unwrap();
        m.entry(game_id.to_string())
            .or_insert_with(|| PlaySession::new(RealClock))
            .start();
    }

    pub fn playtime_stop(&self, game_id: &str) -> u64 {
        let mut m = self.playtime_sessions.lock().unwrap();
        if let Some(s) = m.get_mut(game_id) {
            s.stop();
            let secs = s.elapsed_secs();
            s.reset();
            secs
        } else {
            0
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn new_creates_db_and_defaults_settings() {
        let root = std::env::temp_dir().join(format!("gd-state-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        let st = AppState::new(root.clone()).unwrap();

        let s = st.settings();
        assert_eq!(s.concurrency, 4);

        let s2 = Settings {
            server_url: "https://games.example".into(),
            ..Default::default()
        };
        st.save_settings(&s2).unwrap();
        assert_eq!(st.settings().server_url, "https://games.example");
        // normalized_server_url is used by client(); a valid URL passes.
        assert_eq!(
            st.settings().normalized_server_url().unwrap(),
            "https://games.example"
        );
        std::fs::remove_dir_all(&root).ok();
    }

    #[test]
    fn client_rejects_unset_server() {
        let root = std::env::temp_dir().join(format!("gd-state-bad-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        let st = AppState::new(root.clone()).unwrap();
        assert!(st.client().is_err());
        std::fs::remove_dir_all(&root).ok();
    }
}
