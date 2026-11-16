use async_trait::async_trait;
use serde::{Deserialize, Serialize};

#[derive(Debug, thiserror::Error)]
pub enum ApiError {
    /// A structured error returned by the server: `{"error":{"code","message"}}`.
    #[error("api {status} {code}: {message}")]
    Api {
        status: u16,
        code: String,
        message: String,
    },
    /// A 409 from the save push endpoint, carrying the server's current head.
    #[error("conflict: head={head_version_id} seq={head_seq}")]
    Conflict {
        head_seq: i32,
        head_version_id: String,
    },
    #[error("transport: {0}")]
    Transport(String),
    #[error("decode: {0}")]
    Decode(String),
}

#[derive(Debug, Deserialize)]
struct ServerErrorBody {
    error: ServerError,
}

#[derive(Debug, Deserialize)]
struct ServerError {
    #[serde(default)]
    code: String,
    #[serde(default)]
    message: String,
}

/// Turn a non-2xx response into a typed error. Falls back to `Transport` when the body
/// is not the expected JSON envelope. Pure, so it is unit-testable without a server.
pub fn parse_api_error(status: u16, body: &[u8]) -> ApiError {
    if let Ok(se) = serde_json::from_slice::<ServerErrorBody>(body) {
        return ApiError::Api {
            status,
            code: if se.error.code.is_empty() {
                status.to_string()
            } else {
                se.error.code
            },
            message: se.error.message,
        };
    }
    let text = String::from_utf8_lossy(body).trim().to_string();
    ApiError::Api {
        status,
        code: "http".into(),
        message: if text.is_empty() {
            status.to_string()
        } else {
            text
        },
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PairingInitiate {
    pub code: String,
    pub expires_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PairingClaim {
    pub client_id: String,
    pub token: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Me {
    pub id: String,
    pub username: String,
    #[serde(default)]
    pub display_name: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct LibraryItem {
    pub id: String,
    pub name: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default)]
    pub cover_url: Option<String>,
    #[serde(default)]
    pub installed_version_id: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ReleaseInfo {
    pub digest: String,
    pub total_size: i64,
    pub manifest_url: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SaveSlot {
    pub id: String,
    pub game_id: String,
    pub slot_index: i32,
    pub label: String,
    #[serde(default)]
    pub head_version_id: Option<String>,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SaveVersion {
    pub id: String,
    pub seq: i32,
    pub kind: String,
    pub content_hash: String,
    pub archive_sha256: String,
    pub size: i64,
    pub file_count: i32,
    pub pinned: bool,
    #[serde(default)]
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SavePath {
    pub id: String,
    pub platform: String,
    pub kind: String,
    pub template: String,
    pub recursive: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SaveManifest {
    pub game_id: String,
    #[serde(default)]
    pub paths: Vec<SavePath>,
}

/// Outcome of a push: either a newly created version, or a no-op because the
/// client's content already equals the server head.
#[derive(Debug, Clone)]
pub enum PushOutcome {
    Version(SaveVersion),
    Noop { head_version_id: String },
}

/// The subset of the client API the desktop needs. Object and manifest bytes are
/// returned raw; the caller parses them with `depot::Manifest`. An object source
/// (`depot` download engine) is built on top of this.
#[async_trait]
pub trait ApiClient: Send + Sync {
    async fn initiate_pairing(&self, name: &str, platform: &str) -> Result<PairingInitiate, ApiError>;
    async fn claim_pairing(&self, code: &str) -> Result<PairingClaim, ApiError>;
    async fn me(&self) -> Result<Me, ApiError>;
    async fn library(&self) -> Result<Vec<LibraryItem>, ApiError>;
    async fn release(&self, version_id: &str) -> Result<ReleaseInfo, ApiError>;
    async fn get_manifest(&self, digest: &str) -> Result<Vec<u8>, ApiError>;
    async fn get_object(&self, sha256: &str) -> Result<Vec<u8>, ApiError>;
    async fn post_playtime(&self, game_id: &str, seconds: u64) -> Result<(), ApiError>;

    // Cloud saves.
    async fn list_saves(&self, game_id: Option<&str>) -> Result<Vec<SaveSlot>, ApiError>;
    async fn save_manifest(&self, game_id: &str) -> Result<SaveManifest, ApiError>;
    async fn push_save(
        &self,
        game_id: &str,
        slot: i32,
        content_hash: &str,
        base_seq: i32,
        file_count: u32,
        archive: &[u8],
    ) -> Result<PushOutcome, ApiError>;
    async fn download_save(&self, game_id: &str, slot: i32, seq: Option<i32>) -> Result<Vec<u8>, ApiError>;
    async fn list_save_versions(&self, game_id: &str, slot: i32) -> Result<Vec<SaveVersion>, ApiError>;
    async fn restore_save(&self, game_id: &str, slot: i32, version_id: &str) -> Result<SaveVersion, ApiError>;
    async fn pin_save(&self, game_id: &str, slot: i32, version_id: &str, pinned: bool) -> Result<(), ApiError>;
    async fn delete_save_version(&self, game_id: &str, slot: i32, version_id: &str) -> Result<(), ApiError>;
}

/// reqwest-backed implementation.
#[derive(Clone)]
pub struct HttpApiClient {
    http: reqwest::Client,
    base_url: String,
    token: Option<String>,
}

impl HttpApiClient {
    pub fn new(base_url: impl Into<String>, token: Option<String>) -> Result<Self, ApiError> {
        let http = reqwest::Client::builder()
            .user_agent("gamedepot-desktop")
            .build()
            .map_err(|e| ApiError::Transport(e.to_string()))?;
        Ok(Self {
            http,
            base_url: base_url.into().trim_end_matches('/').to_string(),
            token,
        })
    }

    pub fn with_token(&self, token: Option<String>) -> Self {
        Self {
            http: self.http.clone(),
            base_url: self.base_url.clone(),
            token,
        }
    }

    fn url(&self, path: &str) -> String {
        format!("{}{}", self.base_url, path)
    }

    fn apply_auth(&self, rb: reqwest::RequestBuilder) -> reqwest::RequestBuilder {
        match &self.token {
            Some(t) => rb.bearer_auth(t),
            None => rb,
        }
    }

    async fn send_json<T: serde::de::DeserializeOwned>(
        &self,
        rb: reqwest::RequestBuilder,
    ) -> Result<T, ApiError> {
        let resp = rb.send().await.map_err(|e| ApiError::Transport(e.to_string()))?;
        let status = resp.status();
        if !status.is_success() {
            let body = resp.bytes().await.map_err(|e| ApiError::Transport(e.to_string()))?;
            return Err(parse_api_error(status.as_u16(), &body));
        }
        let bytes = resp.bytes().await.map_err(|e| ApiError::Transport(e.to_string()))?;
        serde_json::from_slice(&bytes).map_err(|e| ApiError::Decode(e.to_string()))
    }
}

#[async_trait]
impl ApiClient for HttpApiClient {
    async fn initiate_pairing(&self, name: &str, platform: &str) -> Result<PairingInitiate, ApiError> {
        let rb = self
            .http
            .post(self.url("/api/v1/pairing/initiate"))
            .json(&serde_json::json!({ "name": name, "platform": platform }));
        self.send_json(rb).await
    }

    async fn claim_pairing(&self, code: &str) -> Result<PairingClaim, ApiError> {
        let rb = self
            .http
            .post(self.url("/api/v1/pairing/claim"))
            .json(&serde_json::json!({ "code": code }));
        self.send_json(rb).await
    }

    async fn me(&self) -> Result<Me, ApiError> {
        let rb = self.apply_auth(self.http.get(self.url("/api/v1/me")));
        self.send_json(rb).await
    }

    async fn library(&self) -> Result<Vec<LibraryItem>, ApiError> {
        let rb = self.apply_auth(self.http.get(self.url("/api/v1/library")));
        self.send_json(rb).await
    }

    async fn release(&self, version_id: &str) -> Result<ReleaseInfo, ApiError> {
        let rb = self.apply_auth(self.http.get(self.url(&format!(
            "/api/v1/releases/{version_id}"
        ))));
        self.send_json(rb).await
    }

    async fn get_manifest(&self, digest: &str) -> Result<Vec<u8>, ApiError> {
        let rb = self.apply_auth(
            self.http
                .get(self.url(&format!("/api/v1/depot/manifests/{digest}"))),
        );
        let resp = rb.send().await.map_err(|e| ApiError::Transport(e.to_string()))?;
        let status = resp.status();
        let bytes = resp.bytes().await.map_err(|e| ApiError::Transport(e.to_string()))?;
        if !status.is_success() {
            return Err(parse_api_error(status.as_u16(), &bytes));
        }
        Ok(bytes.to_vec())
    }

    async fn get_object(&self, sha256: &str) -> Result<Vec<u8>, ApiError> {
        let rb = self.apply_auth(
            self.http
                .get(self.url(&format!("/api/v1/depot/objects/{sha256}"))),
        );
        let resp = rb.send().await.map_err(|e| ApiError::Transport(e.to_string()))?;
        let status = resp.status();
        let bytes = resp.bytes().await.map_err(|e| ApiError::Transport(e.to_string()))?;
        if !status.is_success() {
            return Err(parse_api_error(status.as_u16(), &bytes));
        }
        Ok(bytes.to_vec())
    }

    async fn post_playtime(&self, game_id: &str, seconds: u64) -> Result<(), ApiError> {
        let rb = self.apply_auth(
            self.http
                .post(self.url("/api/v1/playtime"))
                .json(&serde_json::json!({ "gameId": game_id, "seconds": seconds })),
        );
        let resp = rb.send().await.map_err(|e| ApiError::Transport(e.to_string()))?;
        let status = resp.status();
        if !status.is_success() {
            let body = resp.bytes().await.map_err(|e| ApiError::Transport(e.to_string()))?;
            return Err(parse_api_error(status.as_u16(), &body));
        }
        Ok(())
    }

    // ---- cloud saves ----

    async fn list_saves(&self, game_id: Option<&str>) -> Result<Vec<SaveSlot>, ApiError> {
        let path = match game_id {
            Some(g) => format!("/api/v1/saves?game={g}"),
            None => "/api/v1/saves".to_string(),
        };
        let rb = self.apply_auth(self.http.get(self.url(&path)));
        self.send_json(rb).await
    }

    async fn save_manifest(&self, game_id: &str) -> Result<SaveManifest, ApiError> {
        let rb = self.apply_auth(self.http.get(self.url(&format!("/api/v1/saves/manifests/{game_id}"))));
        self.send_json(rb).await
    }

    async fn push_save(&self, game_id: &str, slot: i32, content_hash: &str, base_seq: i32, file_count: u32, archive: &[u8]) -> Result<PushOutcome, ApiError> {
        let rb = self.apply_auth(
            self.http
                .post(self.url(&format!("/api/v1/saves/slot/{game_id}/{slot}/push")))
                .header("X-Content-Hash", content_hash)
                .header("X-Base-Seq", base_seq.to_string())
                .header("X-File-Count", file_count.to_string())
                .body(archive.to_vec()),
        );
        let resp = rb.send().await.map_err(|e| ApiError::Transport(e.to_string()))?;
        let status = resp.status();
        let bytes = resp.bytes().await.map_err(|e| ApiError::Transport(e.to_string()))?;
        if status.as_u16() == 409 {
            // {"error":{...},"headSeq":N,"headVersionId":"..."}
            #[derive(Deserialize)]
            struct ConflictBody { #[serde(default)] head_seq: i32, #[serde(default, rename="headVersionId")] head_version_id: String }
            let cb: ConflictBody = serde_json::from_slice(&bytes).unwrap_or(ConflictBody { head_seq: 0, head_version_id: String::new() });
            return Err(ApiError::Conflict { head_seq: cb.head_seq, head_version_id: cb.head_version_id });
        }
        if !status.is_success() {
            return Err(parse_api_error(status.as_u16(), &bytes));
        }
        // noop body has "noop": true
        #[derive(Deserialize)]
        struct NoopBody { #[serde(default)] noop: bool, #[serde(default, rename="headVersionId")] head_version_id: String }
        let noop: NoopBody = serde_json::from_slice(&bytes).unwrap_or(NoopBody { noop: false, head_version_id: String::new() });
        if noop.noop {
            return Ok(PushOutcome::Noop { head_version_id: noop.head_version_id });
        }
        let v: SaveVersion = serde_json::from_slice(&bytes).map_err(|e| ApiError::Decode(e.to_string()))?;
        Ok(PushOutcome::Version(v))
    }

    async fn download_save(&self, game_id: &str, slot: i32, seq: Option<i32>) -> Result<Vec<u8>, ApiError> {
        let path = match seq {
            Some(s) => format!("/api/v1/saves/slot/{game_id}/{slot}/download?seq={s}"),
            None => format!("/api/v1/saves/slot/{game_id}/{slot}/download"),
        };
        let rb = self.apply_auth(self.http.get(self.url(&path)));
        let resp = rb.send().await.map_err(|e| ApiError::Transport(e.to_string()))?;
        let status = resp.status();
        let bytes = resp.bytes().await.map_err(|e| ApiError::Transport(e.to_string()))?;
        if !status.is_success() {
            return Err(parse_api_error(status.as_u16(), &bytes));
        }
        Ok(bytes.to_vec())
    }

    async fn list_save_versions(&self, game_id: &str, slot: i32) -> Result<Vec<SaveVersion>, ApiError> {
        let rb = self.apply_auth(self.http.get(self.url(&format!("/api/v1/saves/slot/{game_id}/{slot}/versions"))));
        self.send_json(rb).await
    }

    async fn restore_save(&self, game_id: &str, slot: i32, version_id: &str) -> Result<SaveVersion, ApiError> {
        let rb = self.apply_auth(self.http.post(self.url(&format!("/api/v1/saves/slot/{game_id}/{slot}/versions/{version_id}/restore"))));
        self.send_json(rb).await
    }

    async fn pin_save(&self, game_id: &str, slot: i32, version_id: &str, pinned: bool) -> Result<(), ApiError> {
        let rb = self.apply_auth(self.http.post(self.url(&format!("/api/v1/saves/slot/{game_id}/{slot}/versions/{version_id}/pin?{pinned}", pinned = if pinned { "pinned=true" } else { "pinned=false" }))));
        let resp = rb.send().await.map_err(|e| ApiError::Transport(e.to_string()))?;
        let status = resp.status();
        if !status.is_success() {
            let body = resp.bytes().await.map_err(|e| ApiError::Transport(e.to_string()))?;
            return Err(parse_api_error(status.as_u16(), &body));
        }
        Ok(())
    }

    async fn delete_save_version(&self, game_id: &str, slot: i32, version_id: &str) -> Result<(), ApiError> {
        let rb = self.apply_auth(self.http.delete(self.url(&format!("/api/v1/saves/slot/{game_id}/{slot}/versions/{version_id}"))));
        let resp = rb.send().await.map_err(|e| ApiError::Transport(e.to_string()))?;
        let status = resp.status();
        if !status.is_success() {
            let body = resp.bytes().await.map_err(|e| ApiError::Transport(e.to_string()))?;
            return Err(parse_api_error(status.as_u16(), &body));
        }
        Ok(())
    }
}



#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_structured_error() {
        let body = br#"{"error":{"code":"not_found","message":"version not found"}}"#;
        match parse_api_error(404, body) {
            ApiError::Api { status, code, message } => {
                assert_eq!(status, 404);
                assert_eq!(code, "not_found");
                assert_eq!(message, "version not found");
            }
            other => panic!("expected Api, got {other:?}"),
        }
    }

    #[test]
    fn falls_back_on_non_json_error() {
        let e = parse_api_error(502, b"<html>bad gateway</html>");
        match e {
            ApiError::Api { status, code, .. } => {
                assert_eq!(status, 502);
                assert_eq!(code, "http");
            }
            other => panic!("expected Api, got {other:?}"),
        }
    }
}

#[cfg(test)]
mod save_dto_tests {
    use super::*;

    #[test]
    fn deserializes_dtos() {
        let claim: PairingClaim =
            serde_json::from_slice(br#"{"clientId":"c1","token":"t1"}"#).unwrap();
        assert_eq!(claim.client_id, "c1");
        let item: LibraryItem =
            serde_json::from_slice(br#"{"id":"g1","name":"Game"}"#).unwrap();
        assert_eq!(item.cover_url, None);
        assert_eq!(item.installed_version_id, None);
    }

    #[test]
    fn parses_save_dtos() {
        let v: SaveVersion = serde_json::from_slice(br#"{"id":"x","seq":2,"kind":"save","contentHash":"h","archiveSha256":"a","size":12,"fileCount":1,"pinned":false}"#).unwrap();
        assert_eq!(v.seq, 2);
        let s: SaveSlot = serde_json::from_slice(br#"{"id":"x","gameId":"g","slotIndex":0,"label":"l","headVersionId":null}"#).unwrap();
        assert!(s.head_version_id.is_none());
    }
}
