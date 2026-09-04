# DistriMax

**High-Performance Internal MaxMind MMDB Distribution Server in Pure Go**

[![Go Version](https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go)](https://golang.org)
[![Zero CGO](https://img.shields.io/badge/CGO-Zero%20(Disabled)-success)](#zero-cgo-architecture)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Docker Ready](https://img.shields.io/badge/Docker-Multi--stage%20Rootless-2496ED?logo=docker)](Dockerfile)

DistriMax is a secure, single-tenant distribution server that centralizes upstream MaxMind GeoLite MMDB database synchronization (City, Country, ASN), strictly enforces GeoLite compliance retention policies, and serves authenticated downloads to internal microservices, data pipelines, and distributed nodes.

---

## Key Highlights

- **Zero CGO Architecture**: 100% pure Go compiled with `CGO_ENABLED=0` using `modernc.org/sqlite`. Produces a static, self-contained, rootless container or single binary.
- **Dual-Driver Storage**: Supports both **Local Persistent Filesystem** (default) and **S3-Compatible Object Storage** (AWS S3, MinIO, Cloudflare R2). Drivers can be configured and hot-swapped directly from the Admin UI without restarting.
- **Client Authentication**: Flexible client consumption via `Authorization: Bearer <key>`, `X-API-Key: <key>`, or query parameter `?api_key=<key>`. Sensitive query parameters and tokens are automatically redacted from telemetry logs.
- **Strict MaxMind Compliance**: Automated background retention worker purges superseded MMDB files older than 30 days to guarantee compliance with MaxMind GeoLite terms.
- **High-Throughput Streaming**: HTTP `Accept-Ranges` (resumable downloads), `ETag`, `If-None-Match`, `If-Modified-Since` (304 Not Modified), and a 50-connection concurrency semaphore.
- **Self-Contained Embedded Admin UI**: Server-rendered HTML/CSS embedded directly via `embed.FS`. Zero external CDN or Node.js/npm dependencies, responsive dark/light styling, and server-rendered SVG sparklines.
- **Automated Schedulers & Webhooks**: Daily cron sync at 04:00 UTC, HMAC-SHA256 signed event dispatching with exponential backoff, and online non-blocking SQLite `VACUUM INTO` backup snapshots with optional S3 rotation.
- **Enterprise Defense-in-Depth**: Argon2id password and key hashing, AES-256-GCM encryption at rest for database secrets, CSRF synchronizer tokens, trusted reverse-proxy CIDR resolution, and an emergency break-glass `/recover` route.

---

## System Architecture

```
┌────────────────────────────────────────────────────────┐
│                   MaxMind Upstream                     │
│               download.maxmind.com                     │
└──────────────────────────┬─────────────────────────────┘
                           │ Daily Sync / Manual Trigger
                           ▼
┌────────────────────────────────────────────────────────┐
│                 DistriMax Server (Go)                  │
│                                                        │
│  ┌─────────────────────────┐  ┌──────────────────────┐ │
│  │   Upstream Sync Engine  │  │  Background Workers  │ │
│  │  - Streaming tar.gz     │  │  - 30d Compliance    │ │
│  │  - SHA-256 computation  │  │  - 90d Audit Purge   │ │
│  │  - MMDB header check    │  │  - VACUUM INTO backup│ │
│  └────────────┬────────────┘  └──────────┬───────────┘ │
│               │                          │             │
│               ▼                          ▼             │
│  ┌──────────────────────────────────────────────────┐  │
│  │       StorageManager (Filesystem / S3)           │  │
│  │  - Local: /var/lib/distrimax/artifacts           │  │
│  │  - S3: MinIO / AWS S3 / Cloudflare R2            │  │
│  └──────────────────────────┬───────────────────────┘  │
│                             │                          │
│                             ▼                          │
│  ┌──────────────────────────────────────────────────┐  │
│  │        SQLite WAL Database (Zero CGO)            │  │
│  │  - Products, Versions, API Keys, Audit Logs      │  │
│  │  - AES-256-GCM encrypted credentials at rest     │  │
│  └──────────────────────────┬───────────────────────┘  │
│                             │                          │
│  ┌──────────────────────────┴───────────────────────┐  │
│  │                   HTTP Ingress                   │  │
│  │  - Concurrency Limiter (Max 50 active streams)   │  │
│  │  - In-Memory Manifest Cache (sync.RWMutex)       │  │
│  │  - API Key & Session Auth Middleware             │  │
│  └─────────────┬──────────────────────────┬─────────┘  │
└────────────────┼──────────────────────────┼────────────┘
                 │                          │
                 │ Authenticated Download   │ Browser Session
                 ▼                          ▼
   ┌───────────────────────────┐ ┌───────────────────────┐
   │   Internal Microservices  │ │ Engineering Operators │
   │   (ETag, Range 206, 304)  │ │      (Admin UI)       │
   └───────────────────────────┘ └───────────────────────┘
```

---

## Quick Start with Docker Compose

### 1. Clone and Configure

```bash
git clone https://github.com/AhmadShamli/DistriMax.git
cd DistriMax

# Copy environment template
cp .env.example .env
```

Edit `.env` and set secure secrets:
```env
DISTRIMAX_BOOTSTRAP_SECRET=your-strong-bootstrap-secret-phrase
DISTRIMAX_RECOVERY_SECRET=your-strong-recovery-secret-phrase
SETTINGS_ENCRYPTION_KEY=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef # 64-character hex (32 bytes)
```

### 2. Start the Service

```bash
docker compose up -d
```

### 3. Complete Initial Setup

1. Open `http://localhost:8080/setup` in your web browser.
2. Enter your `DISTRIMAX_BOOTSTRAP_SECRET` from `.env`.
3. Create your initial administrator username and password.
4. Select your **Storage Backend Driver** (defaults to **Local Persistent Filesystem**; S3 can also be selected and configured here).
5. Click **Initialize DistriMax**. The `/setup` wizard is permanently locked after this step.

---

## Manual Installation from Source

### Prerequisites

- Go 1.22+ installed
- Linux / macOS / BSD host

### Build & Run

```bash
# Compile pure static binary
CGO_ENABLED=0 go build -ldflags="-s -w" -o distrimax ./cmd/distrimax

# Run DistriMax
./distrimax
```

---

## Configuration Reference

Configuration is managed via environment variables and the `.env` file:

| Variable | Default | Description |
| :--- | :--- | :--- |
| `HTTP_BIND_ADDRESS` | `:8080` | TCP network address and port for the HTTP server. |
| `SQLITE_PATH` | `/var/lib/distrimax/distrimax.sqlite3` | Filepath for the SQLite database. |
| `ARTIFACT_ROOT` | `/var/lib/distrimax/artifacts` | Directory for published MMDB files. |
| `STAGING_ROOT` | `/var/lib/distrimax/staging` | Temporary staging area for unpacking tar.gz archives. |
| `BACKUP_ROOT` | `/var/lib/distrimax/backups` | Target directory for online SQLite backup snapshots. |
| `DISTRIMAX_BOOTSTRAP_SECRET` | *(Required)* | Secret token required to access the first-run `/setup` wizard. |
| `DISTRIMAX_RECOVERY_SECRET` | *(Required)* | Secret token required to access `/recover` break-glass admin recovery. |
| `SETTINGS_ENCRYPTION_KEY` | *(Required)* | 32-byte hexadecimal AES-256 key used to encrypt secrets at rest in SQLite. |
| `TRUSTED_PROXIES` | `127.0.0.1/32` | Comma-separated list of proxy CIDRs trusted to pass `X-Forwarded-For`. |

---

## Consumer API Reference

All consumer endpoints require an active API key generated in the Admin UI.

### Supported Authentication Headers

- **Bearer Token**: `Authorization: Bearer <api_key>`
- **Custom Header**: `X-API-Key: <api_key>`
- **Query Parameter**: `GET /v1/products/geolite-city/download?api_key=<api_key>`

### 1. Get Product Manifest
Returns current version metadata, hash, file size, and release date.

```bash
curl -s -H "Authorization: Bearer dm_live_yourkeyhere" \
  http://localhost:8080/v1/products/geolite-city/manifest
```

**Response (200 OK)**:
```json
{
  "product": "geolite-city",
  "edition_id": "GeoLite2-City",
  "version": "2026-09-04",
  "released_at": "2026-09-04T00:00:00Z",
  "sha256": "8f4803980...0a92",
  "size_bytes": 74523912,
  "filename": "GeoLite2-City.mmdb",
  "download_url": "/v1/products/geolite-city/download"
}
```

### 2. Download MMDB Database
Streams the active MMDB database file. Supports conditional downloads (`ETag`, `If-Modified-Since`) and HTTP Range resumes (`Accept-Ranges`).

```bash
# Direct download with curl
curl -O -J -H "X-API-Key: dm_live_yourkeyhere" \
  http://localhost:8080/v1/products/geolite-city/download

# Conditional request (returns 304 Not Modified if already up to date)
curl -i -H "Authorization: Bearer dm_live_yourkeyhere" \
  -H 'If-None-Match: "8f4803980...0a92"' \
  http://localhost:8080/v1/products/geolite-city/download

# Resumable Range request (bytes 0-1048575)
curl -i -H "Authorization: Bearer dm_live_yourkeyhere" \
  -H "Range: bytes=0-1048575" \
  http://localhost:8080/v1/products/geolite-city/download
```

### 3. Health & Readiness Probes

- `GET /health/liveness`: Returns `200 OK` (`{"status": "alive"}`) when the HTTP process is responsive.
- `GET /health/readiness`: Returns `200 OK` when SQLite is operational and active databases are within staleness thresholds (≤ 8 days).
- `GET /status`: Detailed JSON metrics including product freshness, active versions, and database WAL status.

---

## Webhook Notifications

When a new MMDB version is downloaded, validated, and atomically published, DistriMax dispatches an HTTP POST webhook with HMAC-SHA256 signature verification.

### Webhook Payload
```json
{
  "event": "product.published",
  "event_id": "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d",
  "occurred_at": "2026-09-04T04:05:12Z",
  "product": "geolite-city",
  "version": "2026-09-04",
  "released_at": "2026-09-04T00:00:00Z",
  "sha256": "8f4803980...0a92",
  "size_bytes": 74523912,
  "download_path": "/v1/products/geolite-city/download"
}
```

### Signature Verification
The request includes the header:
```
DistriMax-Signature: t=1757000000,v1=9a8b7c6d...
```
Where `v1` is `hex(hmac_sha256(secret, t + "." + payload))`.

---

## Administration & Operations

### Admin UI Screens
Navigate to `/admin` to access:
- **Dashboard**: Real-time product freshness status, SVG download volume sparklines, and storage utilization.
- **Products**: One-click manual synchronization, integrity verification, and atomic rollback to retained versions.
- **Downloads & Stats**: 24-hour throughput analytics and sanitized client request logs.
- **API Keys**: Create scoped keys with rate limits, CIDR restrictions, and instant token generation.
- **Operations**: Trigger manual syncs, run compliance cleanups, and view webhook delivery audit logs.
- **Audit Logs**: Filterable request history with bookmarkable pagination.
- **Users**: Admin user management protected by a last-admin guard.
- **Settings**: Upstream MaxMind credentials, storage backend configuration (Filesystem vs S3), sync schedules, and live connection test diagnostics.

### Break-Glass Emergency Recovery
If administrative credentials are lost or locked:
```bash
curl -X POST http://localhost:8080/recover \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "recovery_secret=your-strong-recovery-secret-phrase" \
  -d "username=admin" \
  -d "new_password=YourNewSecurePassword123!"
```

---

## Testing

DistriMax includes an automated test suite with valid MMDB mock fixtures, SQLite in-memory integration, and concurrency checks:

```bash
# Run all unit and integration tests
go test -v ./...

# Run with race condition detector
go test -race ./...
```

---

## License

DistriMax is open-source software licensed under the [MIT License](LICENSE).
