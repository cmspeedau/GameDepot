use std::sync::Arc;

use tauri::State;

use crate::api::{ApiClient, LibraryItem, Me, PairingClaim, PairingInitiate};
use crate::auth;
use crate::config::Settings;
use crate::download::SystemSleeper;
use crate::install::InstallReport;
use crate::installer::Installer;
use crate::prefix;
use crate::runner;
use crate::state::AppState;

fn e<E: std::fmt::Display>(err: E) -> String {
    err.to_string()
}

#[tauri::command]
pub async fn settings_get(state: State<'_, AppState>) -> Result<Settings, String> {
    Ok(state.settings())
}

#[tauri::command]
pub async fn settings_set(
    state: State<'_, AppState>,
    settings: Settings,
) -> Result<(), String> {
    state.save_settings(&settings)
}

#[tauri::command]
pub async fn pair_initiate(
    state: State<'_, AppState>,
    name: String,
    platform: String,
) -> Result<PairingInitiate, String> {
    let client = state.client()?;
    client.initiate_pairing(&name, &platform).await.map_err(e)
}

#[tauri::command]
pub async fn pair_complete(
    state: State<'_, AppState>,
    code: String,
) -> Result<PairingClaim, String> {
    let client = state.client()?;
    auth::complete(&client, &*state.tokens, &code).await.map_err(e)
}

#[tauri::command]
pub async fn sign_out(state: State<'_, AppState>) -> Result<(), String> {
    state.tokens.clear().map_err(e)
}

#[tauri::command]
pub async fn signed_in(state: State<'_, AppState>) -> Result<Option<Me>, String> {
    let client = state.client()?;
    match client.me().await {
        Ok(me) => Ok(Some(me)),
        Err(crate::api::ApiError::Api { status: 401, .. }) => Ok(None),
        Err(other) => Err(e(other)),
    }
}

#[tauri::command]
pub async fn library_list(state: State<'_, AppState>) -> Result<Vec<LibraryItem>, String> {
    state.client()?.library().await.map_err(e)
}

#[tauri::command]
pub async fn install_version(
    state: State<'_, AppState>,
    game_id: String,
    version_id: String,
) -> Result<InstallReport, String> {
    let settings = state.settings();
    let root = state
        .db_path
        .parent()
        .ok_or_else(|| "no state root".to_string())?;
    let dest = settings.download_dir.join(&game_id);

    let installer = Installer {
        client: Arc::new(state.client()?),
        store: state.store.clone(),
        cache_dir: root.join("cache"),
        concurrency: settings.concurrency.max(1),
        max_attempts: 5,
        sleeper: Arc::new(SystemSleeper),
    };
    installer
        .install_version(&game_id, &version_id, &dest)
        .await
        .map_err(e)
}

#[tauri::command]
pub async fn launch_game(
    executable: String,
    args: Vec<String>,
    working_dir: Option<String>,
    env: std::collections::BTreeMap<String, String>,
) -> Result<u32, String> {
    let mut spec = crate::launch::LaunchSpec::new(executable);
    for a in args { spec = spec.arg(a); }
    if let Some(d) = working_dir { spec = spec.cwd(d); }
    for (k, v) in env { spec = spec.env(k, v); }
    crate::launch::spawn_detached(&spec).map_err(|e| e.to_string())
}
#[tauri::command]
pub async fn playtime_report(
    state: State<'_, AppState>,
    game_id: String,
    seconds: u64,
) -> Result<(), String> {
    state
        .client()?
        .post_playtime(&game_id, seconds)
        .await
        .map_err(e)
}

#[tauri::command]
pub async fn playtime_start(state: State<'_, AppState>, game_id: String) -> Result<(), String> {
    state.playtime_start(&game_id);
    Ok(())
}

#[tauri::command]
pub async fn playtime_stop(state: State<'_, AppState>, game_id: String) -> Result<u64, String> {
    Ok(state.playtime_stop(&game_id))
}

#[derive(serde::Serialize)]
pub struct RunnerDiscovery {
    pub umu: Option<String>,
    pub builds: Vec<runner::RunnerBuild>,
}

#[tauri::command]
pub async fn runner_discover() -> Result<RunnerDiscovery, String> {
    let (umu, builds) = runner::discover();
    Ok(RunnerDiscovery {
        umu: umu.map(|p| p.display().to_string()),
        builds,
    })
}

#[tauri::command]
pub async fn prefix_path_for(game_id: String, profile: String) -> Result<String, String> {
    prefix::prefix_path(&game_id, &profile)
        .map(|p| p.display().to_string())
        .map_err(|err| err.to_string())
}

#[tauri::command]
pub async fn prefix_size_for(path: String) -> Result<u64, String> {
    Ok(prefix::prefix_size(std::path::Path::new(&path)))
}

#[tauri::command]
pub async fn runner_install_ge_proton() -> Result<runner::RunnerBuild, String> {
    runner::install_latest_ge_proton().await.map_err(|e| e.to_string())
}
