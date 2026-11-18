use std::collections::HashSet;
use std::path::Path;
use std::sync::Mutex;

use rusqlite::{params, Connection};

/// Local install state. Tracks which game versions are installed and, per version,
/// which objects and files have been verified and written. The database holds no
/// credentials; the bearer token lives in the OS keyring.
///
/// The connection is behind a mutex so `Store` is `Sync` (rusqlite's `Connection` is
/// `Send` but not `Sync`). Guards are never held across an await.
pub struct Store {
    conn: Mutex<Connection>,
}

#[derive(Debug, thiserror::Error)]
pub enum StoreError {
    #[error("sqlite: {0}")]
    Sql(#[from] rusqlite::Error),
    #[error("store poisoned")]
    Poisoned,
}

impl StoreError {
    fn poisoned() -> StoreError {
        StoreError::Poisoned
    }
}

const SCHEMA: &str = "
CREATE TABLE IF NOT EXISTS installed_version (
    version_id  TEXT PRIMARY KEY,
    game_id     TEXT NOT NULL,
    digest      TEXT NOT NULL,
    dest        TEXT NOT NULL,
    completed_at TEXT
);
CREATE TABLE IF NOT EXISTS object_state (
    version_id  TEXT NOT NULL,
    sha256      TEXT NOT NULL,
    done        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (version_id, sha256)
);
CREATE TABLE IF NOT EXISTS file_state (
    version_id  TEXT NOT NULL,
    path        TEXT NOT NULL,
    sha256      TEXT NOT NULL,
    done        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (version_id, path)
);
CREATE TABLE IF NOT EXISTS save_cursor (
    game_id        TEXT NOT NULL,
    slot           INTEGER NOT NULL,
    base_seq       INTEGER NOT NULL DEFAULT 0,
    content_hash   TEXT NOT NULL DEFAULT '',
    updated_at     TEXT,
    PRIMARY KEY (game_id, slot)
);
";

impl Store {
    pub fn open(path: &Path) -> Result<Self, StoreError> {
        let conn = Connection::open(path)?;
        conn.execute_batch("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;")?;
        conn.execute_batch(SCHEMA)?;
        Ok(Self { conn: Mutex::new(conn) })
    }

    pub fn in_memory() -> Result<Self, StoreError> {
        let conn = Connection::open_in_memory()?;
        conn.execute_batch(SCHEMA)?;
        Ok(Self { conn: Mutex::new(conn) })
    }

    fn c(&self) -> Result<std::sync::MutexGuard<'_, Connection>, StoreError> {
        self.conn.lock().map_err(|_| StoreError::poisoned())
    }

    /// Register (or refresh) an installed version and reset its progress rows to the
    /// given object and file sets. Idempotent for a repeated (re)install of a version.
    pub fn prepare_version(
        &self,
        version_id: &str,
        game_id: &str,
        digest: &str,
        dest: &str,
        object_shas: &[String],
        files: &[(String, String)],
    ) -> Result<(), StoreError> {
        let guard = self.c()?;
        let tx = guard.unchecked_transaction()?;
        tx.execute(
            "INSERT INTO installed_version(version_id, game_id, digest, dest)
             VALUES (?1,?2,?3,?4)
             ON CONFLICT(version_id) DO UPDATE SET digest=excluded.digest, dest=excluded.dest",
            params![version_id, game_id, digest, dest],
        )?;
        tx.execute("DELETE FROM object_state WHERE version_id=?1", params![version_id])?;
        tx.execute("DELETE FROM file_state WHERE version_id=?1", params![version_id])?;
        for sha in object_shas {
            tx.execute(
                "INSERT INTO object_state(version_id, sha256, done) VALUES (?1,?2,0)",
                params![version_id, sha],
            )?;
        }
        for (path, sha) in files {
            tx.execute(
                "INSERT INTO file_state(version_id, path, sha256, done) VALUES (?1,?2,?3,0)",
                params![version_id, path, sha],
            )?;
        }
        tx.commit()?;
        Ok(())
    }

    pub fn mark_object_done(&self, version_id: &str, sha: &str) -> Result<(), StoreError> {
        self.c()?.execute(
            "INSERT INTO object_state(version_id, sha256, done) VALUES (?1,?2,1)
             ON CONFLICT(version_id, sha256) DO UPDATE SET done=1",
            params![version_id, sha],
        )?;
        Ok(())
    }

    pub fn mark_file_done(&self, version_id: &str, path: &str) -> Result<(), StoreError> {
        self.c()?.execute(
            "UPDATE file_state SET done=1 WHERE version_id=?1 AND path=?2",
            params![version_id, path],
        )?;
        Ok(())
    }

    /// Objects that are still pending for a version (done = 0).
    pub fn pending_objects(&self, version_id: &str) -> Result<Vec<String>, StoreError> {
        let guard = self.c()?;
        let mut stmt = guard.prepare(
            "SELECT sha256 FROM object_state WHERE version_id=?1 AND done=0 ORDER BY sha256",
        )?;
        let rows = stmt.query_map(params![version_id], |r| r.get::<_, String>(0))?;
        let mut out = Vec::new();
        for r in rows {
            out.push(r?);
        }
        Ok(out)
    }

    /// Objects marked done for this version. Used for cross-version reuse: an object
    /// completed for a prior version need not be fetched again.
    pub fn done_set(&self, version_id: &str) -> Result<HashSet<String>, StoreError> {
        let guard = self.c()?;
        let mut stmt = guard
            .prepare("SELECT sha256 FROM object_state WHERE version_id=?1 AND done=1")?;
        let rows = stmt.query_map(params![version_id], |r| r.get::<_, String>(0))?;
        let mut out = HashSet::new();
        for r in rows {
            out.insert(r?);
        }
        Ok(out)
    }

    /// Copy completion flags for shared objects from one version to another, so an
    /// update does not re-download objects a prior version already verified.
    pub fn inherit_object_state(
        &self,
        from_version: &str,
        to_version: &str,
        shas: &[String],
    ) -> Result<(), StoreError> {
        let guard = self.c()?;
        let tx = guard.unchecked_transaction()?;
        for sha in shas {
            tx.execute(
                "UPDATE object_state SET done=1
                 WHERE version_id=?2 AND sha256=?1
                   AND EXISTS (SELECT 1 FROM object_state
                               WHERE version_id=?3 AND sha256=?1 AND done=1)",
                params![sha, to_version, from_version],
            )?;
        }
        tx.commit()?;
        Ok(())
    }

    pub fn mark_complete(&self, version_id: &str) -> Result<(), StoreError> {
        self.c()?.execute(
            "UPDATE installed_version SET completed_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE version_id=?1",
            params![version_id],
        )?;
        Ok(())
    }

    pub fn is_complete(&self, version_id: &str) -> Result<bool, StoreError> {
        let guard = self.c()?;
        let mut stmt = guard
            .prepare("SELECT completed_at FROM installed_version WHERE version_id=?1")?;
        let mut rows = stmt.query(params![version_id])?;
        match rows.next()? {
            None => Ok(false),
            Some(r) => {
                let completed: Option<String> = r.get(0)?;
                Ok(completed.is_some())
            }
        }
    }

    pub fn installed_version_for_game(&self, game_id: &str) -> Result<Option<String>, StoreError> {
        let guard = self.c()?;
        let mut stmt = guard.prepare(
            "SELECT version_id FROM installed_version WHERE game_id=?1 AND completed_at IS NOT NULL ORDER BY completed_at DESC LIMIT 1",
        )?;
        let mut rows = stmt.query(params![game_id])?;
        match rows.next()? {
            Some(r) => Ok(Some(r.get(0)?)),
            None => Ok(None),
        }
    }

    pub fn uninstall(&self, version_id: &str) -> Result<(), StoreError> {
        let guard = self.c()?;
        guard.execute("DELETE FROM object_state WHERE version_id=?1", params![version_id])?;
        guard.execute("DELETE FROM file_state WHERE version_id=?1", params![version_id])?;
        guard.execute("DELETE FROM installed_version WHERE version_id=?1", params![version_id])?;
        Ok(())
    }

    /// Last synced server sequence and content hash for a (game, slot) pair.
    /// `(0, "")` means "never synced".
    pub fn save_cursor(&self, game_id: &str, slot: i32) -> Result<(i32, String), StoreError> {
        let guard = self.c()?;
        let mut stmt = guard.prepare(
            "SELECT base_seq, content_hash FROM save_cursor WHERE game_id=?1 AND slot=?2",
        )?;
        let mut rows = stmt.query(params![game_id, slot])?;
        match rows.next()? {
            Some(r) => Ok((r.get(0)?, r.get(1)?)),
            None => Ok((0, String::new())),
        }
    }

    pub fn set_save_cursor(
        &self,
        game_id: &str,
        slot: i32,
        base_seq: i32,
        content_hash: &str,
    ) -> Result<(), StoreError> {
        self.c()?.execute(
            "INSERT INTO save_cursor (game_id, slot, base_seq, content_hash, updated_at)
             VALUES (?1, ?2, ?3, ?4, strftime('%Y-%m-%dT%H:%M:%SZ','now'))
             ON CONFLICT(game_id, slot) DO UPDATE SET base_seq=?3, content_hash=?4, updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')",
            params![game_id, slot, base_seq, content_hash],
        )?;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tracks_pending_and_complete() {
        let s = Store::in_memory().unwrap();
        let objs: Vec<String> = ["a", "b", "c"].iter().map(|x| x.to_string()).collect();
        let files = vec![("f1".to_string(), "a".to_string()), ("f2".to_string(), "b".to_string())];
        s.prepare_version("v1", "g1", "dig", "/dest", &objs, &files).unwrap();

        assert_eq!(s.pending_objects("v1").unwrap().len(), 3);
        assert!(!s.is_complete("v1").unwrap());

        s.mark_object_done("v1", "a").unwrap();
        let pending = s.pending_objects("v1").unwrap();
        assert_eq!(pending, vec!["b".to_string(), "c".to_string()]);

        s.mark_file_done("v1", "f1").unwrap();
        s.mark_complete("v1").unwrap();
        assert!(s.is_complete("v1").unwrap());
        assert_eq!(s.installed_version_for_game("g1").unwrap().as_deref(), Some("v1"));
    }

    #[test]
    fn reuse_inherits_done_objects_across_versions() {
        let s = Store::in_memory().unwrap();
        let v1_objs = vec!["shared".to_string(), "old".to_string()];
        s.prepare_version("v1", "g1", "d1", "/dest", &v1_objs, &[]).unwrap();
        s.mark_object_done("v1", "shared").unwrap();

        // v2 shares one object with v1.
        let v2_objs = vec!["shared".to_string(), "new".to_string()];
        s.prepare_version("v2", "g1", "d2", "/dest", &v2_objs, &[]).unwrap();
        s.inherit_object_state("v1", "v2", &v2_objs).unwrap();

        // Only "new" should remain to download for v2.
        assert_eq!(s.pending_objects("v2").unwrap(), vec!["new".to_string()]);
    }

    #[test]
    fn prepare_is_idempotent_and_uninstall_clears() {
        let s = Store::in_memory().unwrap();
        let objs = vec!["a".to_string()];
        s.prepare_version("v1", "g1", "d1", "/dest", &objs, &[]).unwrap();
        s.mark_object_done("v1", "a").unwrap();
        // Re-prepare resets progress.
        s.prepare_version("v1", "g1", "d1", "/dest", &objs, &[]).unwrap();
        assert_eq!(s.pending_objects("v1").unwrap(), vec!["a".to_string()]);

        s.uninstall("v1").unwrap();
        assert!(s.pending_objects("v1").unwrap().is_empty());
        assert_eq!(s.installed_version_for_game("g1").unwrap(), None);
    }
}
