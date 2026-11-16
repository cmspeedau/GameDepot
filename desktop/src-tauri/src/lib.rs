pub mod api;
pub mod auth;
pub mod commands;
pub mod config;
pub mod depot;
pub mod download;
pub mod install;
pub mod installer;
pub mod launch;
pub mod saves;
pub mod state;
pub mod store;

use state::AppState;

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    let root = dirs::config_dir()
        .unwrap_or_else(std::env::temp_dir)
        .join("gamedepot");
    let app_state = AppState::new(root).expect("failed to initialise desktop state");

    tauri::Builder::default()
        .manage(app_state)
        .setup(|app| {
            if cfg!(debug_assertions) {
                app.handle().plugin(
                    tauri_plugin_log::Builder::default()
                        .level(log::LevelFilter::Info)
                        .build(),
                )?;
            }
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            commands::settings_get,
            commands::settings_set,
            commands::pair_initiate,
            commands::pair_complete,
            commands::sign_out,
            commands::signed_in,
            commands::library_list,
            commands::install_version,
            commands::playtime_report
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
