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
}

/// reqwest-backed implementation.
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
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_structured_error() {
        let body = br#"{"error":{"code":"not_found","message":"version not found"}}"#;
        match parse_api_error(404, body) {
            ApiError::Api {
                status,
                code,
                message,
            } => {
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
}
