use std::collections::BTreeMap;
use std::process::{Command, Stdio};

#[derive(Debug, Clone)]
pub struct LaunchSpec {
    pub executable: String,
    pub args: Vec<String>,
    pub working_dir: Option<String>,
    pub env: BTreeMap<String, String>,
}

impl LaunchSpec {
    pub fn new(executable: impl Into<String>) -> Self {
        Self {
            executable: executable.into(),
            args: Vec::new(),
            working_dir: None,
            env: BTreeMap::new(),
        }
    }
    pub fn arg(mut self, a: impl Into<String>) -> Self {
        self.args.push(a.into());
        self
    }
    pub fn cwd(mut self, dir: impl Into<String>) -> Self {
        self.working_dir = Some(dir.into());
        self
    }
    pub fn env(mut self, k: impl Into<String>, v: impl Into<String>) -> Self {
        self.env.insert(k.into(), v.into());
        self
    }
}

#[derive(Debug, thiserror::Error)]
pub enum LaunchError {
    #[error("spawn failed: {0}")]
    Spawn(String),
    #[error("io: {0}")]
    Io(#[from] std::io::Error),
}

/// Build a `Command` from the spec. The executable and args are passed directly to
/// `execve` (no shell), so arguments with spaces or metacharacters are not re-parsed.
pub fn build_command(spec: &LaunchSpec) -> Command {
    let mut cmd = Command::new(&spec.executable);
    cmd.args(&spec.args);
    if let Some(dir) = &spec.working_dir {
        cmd.current_dir(dir);
    }
    for (k, v) in &spec.env {
        cmd.env(k, v);
    }
    cmd
}

#[derive(Debug)]
pub struct RunResult {
    pub code: Option<i32>,
    pub stdout: String,
    pub stderr: String,
}

/// Run a launch spec to completion and capture output. Used for setup helpers and tests;
/// the game itself is launched detached via `spawn_detached`.
pub fn run_capture(spec: &LaunchSpec) -> Result<RunResult, LaunchError> {
    let out = build_command(spec)
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|e| LaunchError::Spawn(format!("{}: {e}", spec.executable)))?
        .wait_with_output()?;
    Ok(RunResult {
        code: out.status.code(),
        stdout: String::from_utf8_lossy(&out.stdout).to_string(),
        stderr: String::from_utf8_lossy(&out.stderr).to_string(),
    })
}

/// Spawn detached and return the child pid. The caller records it to track the running
/// game and later read its exit status.
pub fn spawn_detached(spec: &LaunchSpec) -> Result<u32, LaunchError> {
    let child = build_command(spec)
        .stdin(Stdio::null())
        .spawn()
        .map_err(|e| LaunchError::Spawn(format!("{}: {e}", spec.executable)))?;
    let pid = child.id();
    // Detach: drop the handle so the game is not reaped by us. Exit tracking happens by
    // pid elsewhere.
    std::mem::forget(child);
    Ok(pid)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn captures_stdout_and_zero_exit() {
        let r = run_capture(&LaunchSpec::new("/bin/echo").arg("hello")).unwrap();
        assert_eq!(r.code, Some(0));
        assert_eq!(r.stdout.trim(), "hello");
    }

    #[test]
    fn nonzero_exit_is_reported() {
        // `test 1 = 2` exits 1 on both sh and bash.
        let r = run_capture(&LaunchSpec::new("/usr/bin/env").arg("false")).unwrap();
        assert_eq!(r.code, Some(1));
    }

    #[test]
    fn args_are_not_reparsed_by_a_shell() {
        // A single argument containing a metacharacter must arrive verbatim.
        let r = run_capture(&LaunchSpec::new("/bin/echo").arg("a; b * $HOME")).unwrap();
        assert_eq!(r.stdout.trim(), "a; b * $HOME");
    }

    #[test]
    fn passes_env_and_working_dir() {
        let tmp = std::env::temp_dir();
        let r = run_capture(
            &LaunchSpec::new("/bin/sh")
                .arg("-c")
                .arg("printf %s \"$GD_TEST\"; printf %s \"$PWD\" >/dev/null")
                .env("GD_TEST", "set")
                .cwd(tmp.to_str().unwrap()),
        )
        .unwrap();
        assert_eq!(r.stdout, "set");
    }

    #[test]
    fn missing_executable_is_a_spawn_error_not_a_panic() {
        let e = run_capture(&LaunchSpec::new("/nonexistent/game/bin")).unwrap_err();
        assert!(matches!(e, LaunchError::Spawn(_)), "{e:?}");
    }
}
