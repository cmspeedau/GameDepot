use std::{
    collections::HashMap,
    fs::{OpenOptions, create_dir_all},
    io,
    path::{Component, Path, PathBuf},
    process::{Command, ExitStatus},
    sync::Arc,
    thread::spawn,
    time::{Duration, SystemTime},
};

use database::{
    ApplicationTransientStatus, Database, DownloadableMetadata, GameDownloadStatus, GameVersion,
    borrow_db_checked, borrow_db_mut_checked, db::DATA_ROOT_DIR, models::data::InstalledGameType,
    platform::Platform,
};
use dynfmt::Format;
use dynfmt::SimpleCurlyFormat;
use games::{
    library::{push_game_update, report_playtime},
    state::GameStatusManager,
};
use log::{debug, info, warn};
use serde::Serialize;
use shared_child::SharedChild;
use tauri::{AppHandle, Emitter as _};

use crate::{
    PROCESS_MANAGER,
    error::ProcessError,
    format::DropFormatArgs,
    parser::{LaunchParameters, ParsedCommand},
    process_handlers::{
        AsahiMuvmLauncher, LinuxNativeLauncher, MacLauncher, UMUCompatLauncher, UMUNativeLauncher,
        WindowsCmdLauncher, WindowsDirectLauncher, WindowsLauncher, WindowsPowershellLauncher,
    },
};

pub struct RunningProcess {
    handle: Arc<SharedChild>,
    start: SystemTime,
    manually_killed: bool,
}

pub struct ProcessManager<'a> {
    current_platform: Platform,
    log_output_dir: PathBuf,
    processes: HashMap<String, RunningProcess>,
    game_launchers: Vec<(
        (Platform, Platform),
        &'a (dyn ProcessHandler + Sync + Send + 'static),
    )>,
    app_handle: AppHandle,
}

#[derive(Serialize)]
pub struct LaunchOption {
    name: String,
}

#[derive(Serialize)]
pub struct ProcessHandlerOption {
    id: String,
    name: String,
    description: String,
}

impl ProcessManager<'_> {
    pub fn new(app_handle: AppHandle) -> Self {
        let log_output_dir = DATA_ROOT_DIR.join("logs");

        ProcessManager {
            #[cfg(target_os = "windows")]
            current_platform: Platform::Windows,

            #[cfg(target_os = "macos")]
            current_platform: Platform::macOS,

            #[cfg(target_os = "linux")]
            current_platform: Platform::Linux,

            processes: HashMap::new(),
            log_output_dir,
            game_launchers: vec![
                // Current platform to target platform
                (
                    (Platform::Windows, Platform::Windows),
                    &WindowsLauncher {} as &(dyn ProcessHandler + Sync + Send + 'static),
                ),
                (
                    (Platform::Windows, Platform::Windows),
                    &WindowsDirectLauncher {} as &(dyn ProcessHandler + Sync + Send + 'static),
                ),
                (
                    (Platform::Windows, Platform::Windows),
                    &WindowsCmdLauncher {} as &(dyn ProcessHandler + Sync + Send + 'static),
                ),
                (
                    (Platform::Windows, Platform::Windows),
                    &WindowsPowershellLauncher {} as &(dyn ProcessHandler + Sync + Send + 'static),
                ),
                (
                    (Platform::Linux, Platform::Linux),
                    &LinuxNativeLauncher {} as &(dyn ProcessHandler + Sync + Send + 'static),
                ),
                (
                    (Platform::Linux, Platform::Linux),
                    &UMUNativeLauncher {} as &(dyn ProcessHandler + Sync + Send + 'static),
                ),
                (
                    (Platform::macOS, Platform::macOS),
                    &MacLauncher {} as &(dyn ProcessHandler + Sync + Send + 'static),
                ),
                (
                    (Platform::Linux, Platform::Windows),
                    &AsahiMuvmLauncher {} as &(dyn ProcessHandler + Sync + Send + 'static),
                ),
                (
                    (Platform::Linux, Platform::Windows),
                    &UMUCompatLauncher {} as &(dyn ProcessHandler + Sync + Send + 'static),
                ),
            ],
            app_handle,
        }
    }

    pub fn kill_game(&mut self, game_id: String) -> Result<(), io::Error> {
        match self.processes.get_mut(&game_id) {
            Some(process) => {
                process.manually_killed = true;
                kill_process_tree(&process.handle)?;
                let exit_status = process.handle.wait()?;
                info!("exit status: {:?}", exit_status);
                Ok(())
            }
            None => Err(io::Error::new(
                io::ErrorKind::NotFound,
                "Game ID not running",
            )),
        }
    }

    pub fn get_log_dir(&self, game_id: String) -> PathBuf {
        self.log_output_dir.join(game_id)
    }

    fn on_process_finish(
        &mut self,
        game_id: String,
        result: Result<ExitStatus, std::io::Error>,
    ) -> Result<(), ProcessError> {
        if !self.processes.contains_key(&game_id) {
            warn!(
                "process on_finish was called, but game_id is no longer valid. finished with result: {result:?}"
            );
            return Ok(());
        }

        debug!("process for {:?} exited with {:?}", &game_id, result);

        let process = match self.processes.remove(&game_id) {
            Some(process) => process,
            None => {
                info!("Attempted to stop process {game_id} which didn't exist");
                return Ok(());
            }
        };

        let mut db_handle = borrow_db_mut_checked();
        let meta = db_handle
            .applications
            .installed_game_version
            .get(&game_id)
            .cloned()
            .unwrap_or_else(|| panic!("Could not get installed version of {}", &game_id));
        db_handle.applications.transient_statuses.remove(&meta);

        let current_state = db_handle.applications.game_statuses.get_mut(&game_id);
        if let Some(GameDownloadStatus::Installed { install_type, .. }) = current_state
            && let Ok(exit_code) = result
            && exit_code.success()
        {
            *install_type = InstalledGameType::Installed;
        }

        let elapsed = process.start.elapsed().unwrap_or(Duration::ZERO);
        // If we started and ended really quickly, something might've gone wrong
        // Or if the status isn't 0
        // Or if it's an error
        if !process.manually_killed
            && (elapsed.as_secs() <= 2 || result.map_or(true, |r| !r.success()))
        {
            warn!("drop detected that the game {game_id} may have failed to launch properly");
            let _ = self.app_handle.emit("launch_external_error", &game_id);
        }

        let version_data = match db_handle.applications.game_versions.get(&meta.version) {
            // This unwrap here should be resolved by just making the hashmap accept an option rather than just a String
            Some(res) => res,
            None => todo!(),
        };

        let status = GameStatusManager::fetch_state(&game_id, &db_handle);

        push_game_update(
            &self.app_handle,
            &game_id,
            Some(version_data.clone()),
            status,
        );

        let elapsed_seconds = elapsed.as_secs();
        if elapsed_seconds > 2 {
            let playtime_game_id = game_id.clone();
            tauri::async_runtime::spawn(async move {
                if let Err(error) = report_playtime(playtime_game_id.clone(), elapsed_seconds).await
                {
                    warn!("failed to report playtime for game {playtime_game_id}: {error}");
                }
            });
        }

        // Cloud saves: the game is done writing its saves - back them up now.
        // Same detached, best-effort pattern as playtime: a sync failure must
        // never affect the exit path, and games without a curated manifest
        // (the overwhelmingly common case) return None without any network
        // traffic at all.
        let saves_game_id = game_id.clone();
        let saves_app_handle = self.app_handle.clone();
        tauri::async_runtime::spawn(async move {
            if let Some(outcome) = cloud_saves::engine::post_exit(&saves_game_id).await {
                let _ = saves_app_handle.emit(
                    "saves_state",
                    cloud_saves::engine::SaveStateEvent {
                        game_id: saves_game_id,
                        slot: cloud_saves::engine::AUTO_SLOT,
                        outcome,
                    },
                );
            }
        });

        Ok(())
    }

    fn fetch_process_handler(
        &self,
        db_lock: &Database,
        target_platform: &Platform,
        override_id: Option<&str>,
    ) -> Result<&(dyn ProcessHandler + Send + Sync), ProcessError> {
        // An explicit override wins, as long as it's valid for the current platform.
        if let Some(override_id) = override_id
            && let Some(handler) = self.game_launchers.iter().find(|e| {
                let (e_current, e_target) = e.0;
                e_current == self.current_platform
                    && e_target == *target_platform
                    && e.1.id() == override_id
                    && e.1.valid_for_platform(db_lock, target_platform)
            })
        {
            return Ok(handler.1);
        }

        Ok(self
            .game_launchers
            .iter()
            .find(|e| {
                let (e_current, e_target) = e.0;
                e_current == self.current_platform
                    && e_target == *target_platform
                    && e.1.valid_for_platform(db_lock, target_platform)
            })
            .ok_or(ProcessError::InvalidPlatform)?
            .1)
    }

    pub fn valid_platform(&self, platform: &Platform) -> bool {
        let db_lock = borrow_db_checked();
        let process_handler = self.fetch_process_handler(&db_lock, platform, None);
        process_handler.is_ok()
    }

    pub fn get_process_handlers(
        &self,
        game_id: String,
    ) -> Result<Vec<ProcessHandlerOption>, ProcessError> {
        let db_lock = borrow_db_checked();

        let meta = db_lock
            .applications
            .installed_game_version
            .get(&game_id)
            .cloned()
            .ok_or(ProcessError::NotInstalled)?;

        let target_platform = meta.target_platform;

        let handlers = self
            .game_launchers
            .iter()
            .filter(|e| {
                let (e_current, e_target) = e.0;
                e_current == self.current_platform
                    && e_target == target_platform
                    && e.1.valid_for_platform(&db_lock, &target_platform)
            })
            .map(|e| ProcessHandlerOption {
                id: e.1.id().to_string(),
                name: e.1.name().to_string(),
                description: e.1.description().to_string(),
            })
            .collect();

        Ok(handlers)
    }

    pub fn get_launch_options(game_id: String) -> Result<Vec<LaunchOption>, ProcessError> {
        let db_lock = borrow_db_checked();

        let meta = db_lock
            .applications
            .installed_game_version
            .get(&game_id)
            .cloned()
            .ok_or(ProcessError::NotInstalled)?;

        let game_version = db_lock
            .applications
            .game_versions
            .get(&meta.version)
            .ok_or(ProcessError::InvalidVersion)?;

        let launch_options = game_version
            .launches
            .iter()
            .filter(|v| v.platform == meta.target_platform)
            .map(|v| LaunchOption {
                name: v.name.clone(),
            })
            .collect::<Vec<LaunchOption>>();

        Ok(launch_options)
    }

    pub fn launch_process(
        &mut self,
        game_id: String,
        launch_process_index: usize,
    ) -> Result<(), ProcessError> {
        if self.processes.contains_key(&game_id) {
            return Err(ProcessError::AlreadyRunning);
        }

        let mut db_lock = borrow_db_mut_checked();

        let meta = db_lock
            .applications
            .installed_game_version
            .get(&game_id)
            .cloned()
            .ok_or(ProcessError::NotInstalled)?;

        let game_status = db_lock
            .applications
            .game_statuses
            .get(&game_id)
            .ok_or(ProcessError::NotInstalled)?;

        let (version_name, install_dir) = match game_status {
            GameDownloadStatus::Installed {
                version_id: version_name,
                install_dir,
                install_type: InstalledGameType::Installed | InstalledGameType::SetupRequired,
                ..
            } => (version_name, install_dir),
            _ => return Err(ProcessError::NotInstalled),
        };

        debug!(
            "Launching process {:?} with version {:?}",
            &game_id,
            db_lock.applications.game_versions.get(version_name)
        );

        let game_version = db_lock
            .applications
            .game_versions
            .get(version_name)
            .ok_or(ProcessError::InvalidVersion)?;

        // Launcher handoff: a build the publisher declared as needing its
        // store client is not started by plain direct exec here. The play
        // hands off to the storefront runner, which runs the content in place
        // under the compatibility layer and owns the session from there. A
        // launcher's own install still plays through the detected-copy path,
        // which bypasses this manager entirely. The runner works under this
        // same guard - taking it twice from one thread would park forever.
        if !game_version.runs_standalone {
            let version_name = version_name.clone();
            let install_dir = install_dir.clone();
            return self.hand_off_to_storefront(&mut db_lock, meta, version_name, install_dir);
        }

        let game_log_folder = &self.get_log_dir(game_id);
        create_dir_all(game_log_folder)?;

        let current_time = chrono::offset::Local::now();
        let log_file = OpenOptions::new()
            .write(true)
            .truncate(true)
            .read(true)
            .create(true)
            .open(game_log_folder.join(format!(
                "{}-{}.log",
                &meta.version,
                current_time.timestamp()
            )))?;

        let error_file = OpenOptions::new()
            .write(true)
            .truncate(true)
            .read(true)
            .create(true)
            .open(game_log_folder.join(format!(
                "{}-{}-error.log",
                &meta.version,
                current_time.timestamp()
            )))?;

        let target_platform = meta.target_platform;

        let process_handler = self.fetch_process_handler(
            &db_lock,
            &target_platform,
            game_version.user_configuration.override_handler.as_deref(),
        )?;
        debug!("using process handler {:?}", process_handler.id());

        let (target_command, emulator) = match game_status {
            GameDownloadStatus::Installed {
                install_type: InstalledGameType::Installed,
                ..
            } => {
                let (_, launch_config) = game_version
                    .launches
                    .iter()
                    .filter(|v| v.platform == target_platform)
                    .enumerate()
                    .find(|(i, _)| *i == launch_process_index)
                    .ok_or(ProcessError::NotInstalled)?;
                (
                    launch_config.command.clone(),
                    launch_config.emulator.as_ref(),
                )
            }
            GameDownloadStatus::Installed {
                install_type: InstalledGameType::SetupRequired,
                ..
            } => {
                let setup_config = game_version
                    .setups
                    .iter()
                    .find(|v| v.platform == target_platform)
                    .ok_or(ProcessError::NotInstalled)?;

                (setup_config.command.clone(), None)
            }
            _ => unreachable!("Game registered as 'Partially Installed'"),
        };

        let mut target_command = ParsedCommand::parse(target_command)?;

        let target_launch_string = if let Some(emulator) = emulator {
            let err = ProcessError::RequiredDependency(
                emulator.game_id.clone(),
                emulator.version_id.clone(),
            );

            let emulator_metadata = db_lock
                .applications
                .installed_game_version
                .get(&emulator.game_id)
                .ok_or(err.clone())?;

            let emulator_game_status = db_lock
                .applications
                .game_statuses
                .get(&emulator.game_id)
                .ok_or(err.clone())?;

            let emulator_install_dir = match emulator_game_status {
                GameDownloadStatus::Installed {
                    install_type: InstalledGameType::Installed,
                    install_dir,
                    ..
                } => Ok(install_dir),
                GameDownloadStatus::Installed {
                    install_type: InstalledGameType::SetupRequired,
                    ..
                } => todo!(),
                _ => Err(err.clone()),
            }?;

            let emulator_game_version = db_lock
                .applications
                .game_versions
                .get(&emulator.version_id)
                .ok_or(err.clone())?;

            let emulator_launch_config = emulator_game_version
                .launches
                .iter()
                .find(|v| v.launch_id == emulator.launch_id)
                .ok_or(err)?;

            let mut exe_command = ParsedCommand::parse(emulator_launch_config.command.clone())?;
            exe_command.env.extend(target_command.env.clone());
            exe_command.make_absolute(emulator_install_dir.into());

            target_command.make_absolute(PathBuf::from(install_dir.clone()));

            exe_command.args.iter_mut().for_each(|v| {
                *v = v.replace("{rom}", &target_command.command);
            });

            process_handler.create_launch_process(
                emulator_metadata,
                exe_command.reconstruct(),
                emulator_game_version,
                install_dir,
                &db_lock,
            )?
        } else {
            process_handler.create_launch_process(
                &meta,
                target_command.reconstruct(),
                game_version,
                install_dir,
                &db_lock,
            )?
        };

        let mut parsed_launch = ParsedCommand::parse(target_launch_string.clone())?;
        let executable_name = parsed_launch.command.clone();
        parsed_launch.make_absolute(install_dir.into());

        let format_args = DropFormatArgs::new(
            target_launch_string,
            install_dir,
            &executable_name,
            parsed_launch.command,
            None,
        );

        let target_launch_string = SimpleCurlyFormat
            .format(
                &game_version.user_configuration.launch_template,
                &format_args,
            )
            .map_err(|e| ProcessError::FormatError(e.to_string()))?
            .to_string();

        let target_launch_string = SimpleCurlyFormat
            .format(&target_launch_string, format_args)
            .map_err(|e| ProcessError::FormatError(e.to_string()))?
            .to_string();

        let launch_parameters = LaunchParameters(
            ParsedCommand::parse(target_launch_string)?,
            install_dir.into(),
        );

        let mut launch_parameters = launch_parameters;
        launch_parameters
            .0
            .make_command_absolute_if_local(&launch_parameters.1);

        launch_parameters.0.ensure_executable()?;

        info!(
            "launching (in {}): {:?}",
            launch_parameters.1.to_string_lossy(),
            launch_parameters.0
        );

        let mut command = {
            let mut command = Command::new(launch_parameters.0.command);
            command.args(launch_parameters.0.args);
            for parts in launch_parameters
                .0
                .env
                .into_iter()
                .map(|e| e.split("=").map(|v| v.to_string()).collect::<Vec<String>>())
            {
                if let Some(key) = parts.first()
                    && let Some(value) = parts.get(1)
                {
                    command.env(key, value);
                }
            }
            command
        };

        command
            .stderr(error_file)
            .stdout(log_file)
            .env_remove("RUST_LOG")
            .current_dir(launch_parameters.1);

        process_handler.modify_command(&mut command);

        let child = command.spawn()?;

        let launch_process_handle = Arc::new(SharedChild::new(child)?);

        db_lock
            .applications
            .transient_statuses
            .insert(meta.clone(), ApplicationTransientStatus::Running {});

        push_game_update(
            &self.app_handle,
            &meta.id,
            None,
            (None, Some(ApplicationTransientStatus::Running {})),
        );

        let wait_thread_handle = launch_process_handle.clone();
        let wait_thread_game_id = meta.clone();

        self.processes.insert(
            meta.id,
            RunningProcess {
                handle: wait_thread_handle,
                start: SystemTime::now(),
                manually_killed: false,
            },
        );
        spawn(move || {
            let result: Result<ExitStatus, std::io::Error> = launch_process_handle.wait();

            PROCESS_MANAGER
                .lock()
                .on_process_finish(wait_thread_game_id.id, result)
        });
        Ok(())
    }

    /// Hand a launcher-gated build to its storefront runner. A build the
    /// publisher declared as needing its store client cannot be cold-started
    /// by direct exec, so the play goes through the runner instead: the
    /// content runs in place under the compatibility layer, and the session is
    /// tracked like any other play so Stop reaches it and the wait thread
    /// reports playtime. Starting the store client itself is not part of this
    /// path yet - see the per-store handoff runners the roadmap still lists.
    fn hand_off_to_storefront(
        &mut self,
        db: &mut Database,
        meta: DownloadableMetadata,
        version_name: String,
        install_dir: String,
    ) -> Result<(), ProcessError> {
        db.applications
            .game_versions
            .get(&version_name)
            .ok_or(ProcessError::InvalidVersion)?;

        // The runner plays the content in place: natively on Windows, or
        // through umu + Proton against a storefront prefix on Linux, the way
        // the detected-copy play does.
        let install_path = PathBuf::from(&install_dir);
        let exe =
            importer::find_game_executable(&install_path).ok_or(ProcessError::NotInstalled)?;
        let exe_relative = Path::new(&exe);
        if exe_relative.is_absolute()
            || exe_relative
                .components()
                .any(|c| !matches!(c, Component::Normal(_)))
        {
            return Err(ProcessError::NotInstalled);
        }
        let exe_path = install_path.join(&exe);
        let mut command =
            storefront_play_command(&exe_path, &install_path, &meta.id, &version_name, db)?;

        let game_log_folder = &self.get_log_dir(meta.id.clone());
        create_dir_all(game_log_folder)?;
        let current_time = chrono::offset::Local::now();
        let log_file = OpenOptions::new()
            .write(true)
            .truncate(true)
            .read(true)
            .create(true)
            .open(game_log_folder.join(format!(
                "{}-{}.log",
                &meta.version,
                current_time.timestamp()
            )))?;
        let error_file = OpenOptions::new()
            .write(true)
            .truncate(true)
            .read(true)
            .create(true)
            .open(game_log_folder.join(format!(
                "{}-{}-error.log",
                &meta.version,
                current_time.timestamp()
            )))?;

        command
            .stderr(error_file)
            .stdout(log_file)
            .env_remove("RUST_LOG")
            .current_dir(install_dir);

        let child = command.spawn()?;
        let launch_process_handle = Arc::new(SharedChild::new(child)?);

        db.applications
            .transient_statuses
            .insert(meta.clone(), ApplicationTransientStatus::Running {});

        push_game_update(
            &self.app_handle,
            &meta.id,
            None,
            (None, Some(ApplicationTransientStatus::Running {})),
        );

        let wait_thread_handle = launch_process_handle.clone();
        let wait_thread_game_id = meta.clone();

        self.processes.insert(
            meta.id.clone(),
            RunningProcess {
                handle: wait_thread_handle,
                start: SystemTime::now(),
                manually_killed: false,
            },
        );
        spawn(move || {
            let result: Result<ExitStatus, std::io::Error> = launch_process_handle.wait();

            PROCESS_MANAGER
                .lock()
                .on_process_finish(wait_thread_game_id.id, result)
        });
        Ok(())
    }
}

/// The storefront play command for one launcher-gated build: the copy the
/// storefront owns, run in place - umu-run plus Proton on Linux, the
/// executable itself on every other platform - the same invocation the depot's
/// own launches use, minus the depot's launch formatting. Refusals here are
/// plain errors: the handoff reports them and starts nothing.
fn storefront_play_command(
    exe: &Path,
    folder: &Path,
    game_id: &str,
    version_id: &str,
    db: &Database,
) -> Result<Command, ProcessError> {
    #[cfg(target_os = "linux")]
    {
        use client::compat::{COMPAT_INFO, UMU_LAUNCHER_EXECUTABLE};
        let umu = UMU_LAUNCHER_EXECUTABLE
            .as_ref()
            .ok_or(ProcessError::RequiredDependency(
                game_id.to_string(),
                version_id.to_string(),
            ))?;
        if COMPAT_INFO.as_ref().is_none_or(|c| !c.umu_installed) {
            return Err(ProcessError::RequiredDependency(
                game_id.to_string(),
                version_id.to_string(),
            ));
        }
        let proton_path = db
            .applications
            .default_proton_path
            .clone()
            .ok_or(ProcessError::NoCompat)?;
        if !crate::compat::read_proton_path(PathBuf::from(&proton_path))
            .ok()
            .flatten()
            .is_some()
        {
            return Err(ProcessError::NoCompat);
        }
        let pfx = DATA_ROOT_DIR
            .join("pfx")
            .join("launcher")
            .join(slug_for(folder));
        create_dir_all(&pfx)?;
        let mut command = Command::new(umu);
        command
            .arg(exe)
            .current_dir(folder)
            .env("GAMEID", game_id)
            .env("WINEPREFIX", &pfx)
            .env("PROTONPATH", &proton_path);
        Ok(command)
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = (game_id, version_id);
        let mut command = Command::new(exe);
        command.current_dir(folder);
        Ok(command)
    }
}

/// The storefront prefix key for one install folder, the same slug the
/// detected-copy play uses to name the Proton prefix it runs.
fn slug_for(folder: &Path) -> String {
    let raw = folder.to_string_lossy();
    let slug = raw
        .chars()
        .map(|c| {
            if c.is_ascii_alphanumeric() {
                c.to_ascii_lowercase()
            } else {
                '-'
            }
        })
        .collect::<String>();
    let slug = slug.trim_matches('-').to_string();
    if slug.is_empty() {
        "game".to_string()
    } else {
        slug
    }
}

fn kill_process_tree(handle: &SharedChild) -> io::Result<()> {
    #[cfg(target_os = "windows")]
    {
        // handle.kill() only terminates the launched process (often a cmd or
        // powershell wrapper), orphaning the actual game. taskkill /T kills the
        // whole process tree.
        use std::os::windows::process::CommandExt;
        const CREATE_NO_WINDOW: u32 = 0x08000000;
        let pid = handle.id().to_string();
        let killed = Command::new("taskkill")
            .args(["/F", "/T", "/PID", pid.as_str()])
            .creation_flags(CREATE_NO_WINDOW)
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null())
            .status()
            .map(|status| status.success())
            .unwrap_or(false);
        if killed {
            return Ok(());
        }
    }
    handle.kill()
}

pub trait ProcessHandler: Send + 'static {
    fn create_launch_process(
        &self,
        meta: &DownloadableMetadata,
        launch_command: String,
        game_version: &GameVersion,
        current_dir: &str,
        database: &Database,
    ) -> Result<String, ProcessError>;

    fn valid_for_platform(&self, db: &Database, target: &Platform) -> bool;

    fn modify_command(&self, command: &mut Command);

    fn id(&self) -> &'static str;
    fn name(&self) -> &'static str;
    fn description(&self) -> &'static str;
}
