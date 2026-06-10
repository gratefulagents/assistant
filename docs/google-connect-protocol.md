# Google Connect broker protocol

The assistant connects a Google account through a **Connect broker**: a separate
service that owns a verified Google OAuth web app, holds Google refresh tokens
centrally, and mints short-lived access tokens for paired assistants. The broker
is **not** part of this open-source assistant; this document specifies the
HTTP/JSON contract so anyone can run a compatible broker.

The assistant only ever speaks this network protocol to the broker — it never
links against broker code. Any server that implements these endpoints works.

## Concepts

- **Pairing credential.** The assistant generates a random `assistant_id` and a
  random `secret`. It sends only the SHA-256 hex of the secret at pairing time
  (`secret_hash`) and presents the raw secret later to claim the grant and mint
  tokens. The broker stores the secret only as a hash and compares it in
  constant time.
- **Device code vs OAuth state.** The `device_code` returned by `/device/start`
  identifies the CLI↔broker pairing attempt. It is distinct from the OAuth
  `state` the broker uses in the browser consent leg.
- **Scopes.** The assistant requests Google scopes; the broker SHOULD enforce an
  allowlist and append `openid email` so it can record the account email.
- **Providers.** The protocol supports multiple identity providers. The optional
  `provider` field on `/device/start` selects one (`"google"` or
  `"microsoft"`); omitted or empty means `"google"` for backward compatibility.
  Grants are keyed by `(assistant_id, provider)`; the assistant uses a distinct
  pairing credential file per provider (`google-auth.json`,
  `microsoft-auth.json`). For Microsoft the broker appends
  `openid email offline_access` and the minted tokens are Microsoft Graph
  access tokens.

All request and response bodies are JSON unless noted.

## Endpoints

### `POST /device/start`

Register a pairing attempt.

Request:

```json
{
  "provider": "google",
  "scopes": ["https://www.googleapis.com/auth/gmail.readonly"],
  "assistant_id": "client-generated-id",
  "secret_hash": "sha256-hex-of-secret"
}
```

`provider` is optional; omitted means `"google"`. A broker that does not
support the requested provider returns `{ "error": "provider_not_supported" }`
with a non-2xx status.

Response:

```json
{
  "device_code": "broker-generated",
  "user_code": "ABCD-EFGH",
  "verification_uri": "https://connect.example.com/device",
  "verification_uri_complete": "https://connect.example.com/device?code=ABCD-EFGH",
  "interval": 5,
  "expires_in": 900
}
```

On error (e.g. a disallowed scope) the broker returns `{ "error": "..." }` with
a non-2xx status.

### `GET /device?code=USER_CODE`

Browser entry point. The broker looks up the pending attempt by `user_code` and
redirects to Google's consent screen with `state=device_code`,
`access_type=offline`, and `prompt=consent`.

### `GET /oauth/callback?code=...&state=...`

Google redirects here after consent. The broker exchanges the authorization
`code` (using its client secret) for tokens, fetches the account email, and
stores the grant keyed by the `device_code`/`assistant_id` from `state`.

### `POST /device/token`

Poll for the result of a pairing attempt, then claim it once authorized.

Request:

```json
{ "device_code": "...", "secret": "raw-secret" }
```

Response while pending:

```json
{ "status": "pending" }
```

Response once authorized (claim-once):

```json
{
  "status": "authorized",
  "assistant_id": "...",
  "scopes": ["https://www.googleapis.com/auth/gmail.readonly"],
  "email": "user@example.com"
}
```

The broker MAY return `{ "error": "slow_down" }` to ask the client to back off.

### `POST /token`

Mint a fresh short-lived Google access token for a paired assistant.

Request:

```json
{ "provider": "google", "assistant_id": "...", "secret": "raw-secret" }
```

`provider` is optional; omitted means `"google"`. It selects which provider's
grant to mint from when one assistant has connected multiple providers.

Response:

```json
{
  "access_token": "ya29....",
  "expires_in": 3600,
  "scopes": "https://www.googleapis.com/auth/gmail.readonly",
  "email": "user@example.com"
}
```

If the underlying Google refresh token is no longer valid the broker returns
`{ "error": "invalid_grant" }`; the assistant treats this as "reconnect
required" and stops using the credential.

### `POST /revoke`

Revoke and delete a grant. `provider` is optional and defaults to `"google"`.

Request:

```json
{ "provider": "google", "assistant_id": "...", "secret": "raw-secret" }
```

### `GET /healthz`

Returns `200` with `{ "status": "ok" }`.

## Stored client credential

After a successful pairing the assistant writes `google-auth.json` (or
`microsoft-auth.json` for `provider: microsoft`) containing
only the pairing credential (never a Google refresh token):

```json
{
  "broker_url": "https://connect.example.com",
  "assistant_id": "...",
  "secret": "raw-secret",
  "scopes": ["https://www.googleapis.com/auth/gmail.readonly"],
  "email": "user@example.com"
}
```

The assistant caches the most recently minted access token (and its expiry) in
the same file and refreshes it via `POST /token` when it expires.

## Broker responsibilities (summary)

A conforming broker SHOULD:

- Own a single verified Google OAuth web app; never expose its client secret.
- Enforce a scope allowlist and append `openid email` for account identity.
- Store pairing secrets only as hashes, compared in constant time.
- Optionally encrypt Google refresh tokens at rest.
- Treat itself as a high-value token custodian: restrict filesystem access and
  prefer least-privilege scopes.
