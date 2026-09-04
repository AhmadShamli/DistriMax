# Internal MaxMind MMDB Distribution Server

## 1. Goals

Build an internal-only server that:

- Downloads and distributes MaxMind GeoLite MMDB databases.
- Supports multiple MMDB products, initially:
  - GeoLite City
  - GeoLite Country
  - GeoLite ASN
- Tracks database versions and release metadata.
- Verifies SHA-256 checksums before publishing a file.
- Publishes updates atomically.
- Provides authenticated downloads using multiple API keys.
- Exposes health and status endpoints.
- Records and displays download statistics.
- Automatically removes expired database versions.
- Sends a webhook or notification when a product changes.
- Minimizes direct downloads from MaxMind by centralizing updates for internal consumers.
- Is implemented in Go.
- Provides a simple authenticated administration UI.
- Manages operational settings from the administration UI wherever safe and practical.

## 2. Scope And Constraints

### In scope

- Private HTTP API and file distribution.
- Go service with a simple server-rendered or embedded administration UI.
- One MaxMind account/license configuration for upstream downloads.
- Multiple local client API keys.
- Product- and version-aware artifact metadata.
- Automated scheduled synchronization.
- Immutable stored versions and a current-version pointer.
- Uses in-memory caching for frequently requested latest-product metadata to reduce SQLite reads.
- Operational health, status, metrics, and audit data.
- Configurable retention and cleanup policies.

### Out of scope for the first version

- Public user registration.
- Public distribution or anonymous downloads.
- Multi-tenant billing.
- Database lookup/query APIs.
- CSV database support unless explicitly added later.
- Replication across multiple regions.
- Arbitrary file uploads by clients.

The service must remain an internal distribution mechanism, not a public mirror. Network-level restrictions should complement, but not replace, API-key authentication.

## 3. Licensing And Compliance Requirements

MaxMind's current GeoLite terms should be reviewed before implementation and captured in project documentation.

The service should:

- Include MaxMind attribution in internal documentation and metadata responses where appropriate.
- Restrict use to the organization's permitted internal business purposes.
- Prevent access from unapproved external networks.
- Avoid exposing files or metadata through unauthenticated endpoints.
- Document that GeoLite data must not be used to identify a specific household, individual, or street address.
- Track the upstream release date and the local expiration/deletion deadline.
- Delete superseded GeoLite data within 30 days of a newer release, except for any retention explicitly permitted and justified by the applicable agreement.
- Notify operators when cleanup cannot complete, so expired data is not silently retained.
- Avoid using the repository as a long-term archive of historical MaxMind data.

The service should store upstream credentials only in protected runtime secrets or encrypted application settings, never in the repository. Credentials that cannot safely be managed in the UI must be provided through `.env` or an equivalent secret-injection mechanism.

## 4. Proposed Architecture

Use a small service with the following logical components:

1. **HTTP API**
   - Authenticated metadata and download endpoints.
   - Health and status endpoints.
   - API-key management endpoints for the administration UI and automation.
   - In-memory latest-product metadata cache for read-heavy manifest and current-version requests.

2. **Administration UI**
   - Simple authenticated internal UI for operational management.
   - Dashboard for product freshness, current versions, recent syncs, storage, and download activity.
   - Settings page for product, synchronization, retention, notification, access-control, and statistics settings.
   - API-key management, including creation, revocation, scopes, product restrictions, and last-use visibility.
   - Manual sync, retry, cleanup, and notification actions with confirmation and audit logging.

3. **Synchronization worker**
   - Runs on a schedule.
   - Checks configured products for newer releases.
   - Downloads only when a new version is available.
   - Verifies, validates, and publishes artifacts.

4. **Metadata database**
   - Stores products, versions, lifecycle state, API-key records, download events, sync runs, and notification attempts.
   - SQLite is the required metadata database for the initial implementation.
   - Enable WAL mode, foreign keys, busy timeouts, and a suitable synchronous setting.
   - Keep the SQLite database on persistent local storage and perform regular online backups.
   - Run the service as a single active writer instance; do not place the SQLite database on an object store or a network filesystem unless the filesystem's locking and durability guarantees have been explicitly validated.

5. **Artifact storage**
   - Local filesystem storage is the required and supported backend for the first implementation.
   - Store artifacts outside the SQLite database under immutable product/version paths.
   - Never overwrite an already published artifact.
   - Keep the SQLite database and artifact directory on the same host or on storage with tested backup and recovery behavior.

6. **Notification worker**
   - Sends webhook requests after a version is committed as current.
   - Retries failures with backoff.
   - Records delivery attempts and final failure state.

7. **Cleanup worker**
   - Deletes expired artifacts and associated metadata according to the retention policy.
   - Runs independently so cleanup failures do not block downloads or publishing.

### Supported artifact storage

The storage layer should use a small backend interface so the HTTP API and workers do not depend on a specific storage implementation.

#### Required: local filesystem

The initial release supports a persistent local filesystem directory. It must provide:

- Atomic rename/move within the staging and artifact filesystem.
- Reliable file locking or application-level coordination.
- Sufficient capacity for the current version, temporary staging files, and the permitted rollback window.
- Access restricted to the service account.
- Backup and restore procedures that do not retain deleted GeoLite artifacts beyond the applicable retention requirement.

Suggested layout:

```text
/var/lib/distrimax/
  distrimax.sqlite3
  artifacts/<product>/<version>/<filename>
  staging/<sync-run-id>/...
```

#### Optional future backend: private S3-compatible object storage

The design may later support private S3-compatible storage, including AWS S3 or an internally hosted MinIO-compatible service. This is not required for the initial release. An object-storage backend must provide:

- Private bucket/container access.
- Immutable versioned object keys.
- Post-upload size and SHA-256 verification.
- Explicit cleanup of expired and orphaned objects.
- A database-backed current-version pointer; never overwrite a mutable `current` object as the publication mechanism.
- Credentials managed outside the repository.

Cloudflare R2 may be supported only if it is deployed as private internal storage and its retention, access, and deletion behavior can satisfy the GeoLite requirements. Direct public object URLs are not supported.

#### Not supported initially

- Storing MMDB files as SQLite BLOBs.
- NFS or other network filesystems without validated SQLite locking and atomic-rename behavior.
- Public buckets or unauthenticated object URLs.
- Client-provided storage locations.

## 5. Product Configuration

Products should be data-driven rather than hardcoded into separate workflows.

Each configured product should define:

- Stable internal product identifier, such as `geolite-city`.
- Upstream MaxMind edition ID.
- Local filename, such as `GeoLite2-City.mmdb`.
- Expected file format: MMDB.
- Upstream download method.
- Enabled/disabled state.
- Synchronization schedule.
- Retention policy.
- Optional validation rules.
- Notification topic or webhook behavior.

The initial product configuration should include:

| Internal ID | MaxMind product | Expected artifact |
|---|---|---|
| `geolite-city` | GeoLite2 City | `GeoLite2-City.mmdb` |
| `geolite-country` | GeoLite2 Country | `GeoLite2-Country.mmdb` |
| `geolite-asn` | GeoLite2 ASN | `GeoLite2-ASN.mmdb` |

The implementation should support adding additional MMDB products through configuration and database records without requiring a new download or API code path.

## 6. Go Implementation And Configuration

The service should be implemented in Go. Keep the initial deployment as one Go binary with separately runnable worker commands or scheduled internal jobs where that simplifies operations.

Recommended Go structure:

- HTTP server using the standard library or a small, well-supported router.
- HTML templates and static assets embedded into the binary with `embed`.
- SQLite driver with explicit connection and transaction configuration.
- Separate packages for API handlers, admin handlers, authentication, configuration, storage, synchronization, cleanup, notifications, and metrics.
- Context-aware upstream requests, file operations, database operations, and webhook delivery.
- Structured JSON logging.
- No runtime code generation or external frontend build requirement for the initial admin UI.

### Configuration ownership

Most operational configuration should be stored in SQLite and edited through the authenticated administration UI. The settings page should validate changes, show the effective value, record who changed it, and apply changes without requiring a process restart whenever possible.

Settings managed in the admin UI should include:

- Enabled MMDB products and product identifiers.
- MaxMind edition IDs and local filenames.
- Synchronization schedules and retry policy.
- Download and request rate limits.
- Artifact retention and cleanup deadlines.
- Maximum artifact and staging sizes.
- Webhook endpoints, event filters, retry policy, and signing configuration where secrets can be safely protected.
- Health/status freshness thresholds.
- Download-statistics retention and aggregation settings.
- Trusted proxy addresses or networks.
- Allowed client networks and API-key policy defaults.
- Notification and alert thresholds.
- Display name, attribution text, and other non-secret service metadata.

Values that must remain outside the admin UI and be provided through `.env` or an equivalent secret-injection mechanism should be limited to bootstrap and infrastructure concerns:

- `SQLITE_PATH`: path to the SQLite database.
- `ARTIFACT_ROOT`: path to the local artifact and staging directories.
- `HTTP_BIND_ADDRESS` and, if applicable, the externally configured service port.
- `PUBLIC_BASE_URL` only if it is required to construct absolute links and cannot be safely managed in the UI.
- Initial admin bootstrap secret or one-time admin setup token.
- Secret used to encrypt sensitive settings stored in SQLite, if encrypted settings are implemented.
- TLS certificate/key paths when TLS terminates in the Go process.
- Process-level log destination or required infrastructure log settings.
- A controlled development/test mode flag, if needed.

The `.env` file must not contain routine product settings, API-key definitions, retention values, schedules, or ordinary UI preferences. It must be excluded from version control and documented with a safe example file containing placeholders only.

Secrets entered through the UI, such as MaxMind credentials or webhook signing secrets, must be encrypted at rest using an application key supplied through `.env` or an external secret manager. The UI must never display stored secret values after initial entry.

Configuration changes must be audited with the administrator identity, source IP, timestamp, setting name, old-value summary, and new-value summary. Secret values must be redacted from audit records.

### First-deployment admin setup

The service must support an explicit first-run setup flow for the initial deployment and for any later state in which no admin user exists.

Startup and request behavior:

- Store human administrators in a SQLite `users` table; API keys do not count as users for setup purposes.
- Store a durable `setup_completed` state in the SQLite settings/install-state table. On startup, check both this state and the users table.
- If `setup_completed` is false and no user has ever been created, enter `setup_required` state and expose only health endpoints and the protected setup flow. Normal admin pages, API-key management, product management, downloads, and operational actions remain unavailable.
- Users must be soft-disabled rather than hard-deleted so the service can distinguish an initial empty database from an installation that has already completed setup. Disabled users still count as created users for setup-state purposes.
- The setup page must be served at a clearly defined route such as `/setup` and must display no product data, filesystem paths, secrets, or operational details.
- Require the bootstrap secret from `.env` or an equivalent secret-injection mechanism before showing or accepting the admin-creation form. Do not make an unprotected setup page available merely because the database is empty.
- Restrict setup requests to the configured internal network or trusted proxy path and record the source IP for every setup request, including rejected attempts.
- Apply rate limiting and generic failure responses to invalid bootstrap-secret attempts.

Creating the first admin:

1. The operator supplies the bootstrap secret and submits the initial admin username and password.
2. Validate the username and password using the same policy required for later admin accounts.
3. Hash the password with Argon2id or an equivalent password KDF; never store the plaintext password.
4. In one SQLite transaction, verify that `setup_completed` is false and no user exists, create the first admin, record the setup audit event, and mark `setup_completed` as true.
5. Use a database uniqueness constraint and transaction-level coordination so two simultaneous setup requests cannot create competing first users.
6. Invalidate or consume the bootstrap token after successful setup if it is configured as one-time; otherwise keep the `.env` secret as a recovery gate but never expose it through the UI.
7. Establish an authenticated admin session and redirect to the dashboard only after the transaction commits.

After the first admin is created:

- `/setup` must return a generic unavailable response and must not reveal whether users exist.
- The normal admin login/session flow becomes available.
- The first admin can create, disable, revoke, or rotate other admin users from the admin UI, subject to preventing accidental loss of the last usable admin without a recovery action.
- API keys remain separate credentials for client downloads and automation; they cannot access first-run setup.
- If all admin users are disabled or lost after initial setup, the service must not silently reopen first-run setup. Require an explicitly documented recovery action using the bootstrap secret or an operator-only recovery command, and audit and rate-limit that action.

The initial admin password must never be accepted through query parameters or written to logs. Setup forms require CSRF protection when using cookie sessions, and successful setup should invalidate any pre-existing setup sessions or CSRF tokens.

## 7. Simple Administration UI

The first UI should prioritize operational clarity over a broad management console. It should be available only on the private network and require a dedicated admin session established from an admin credential. The UI must provide the protected first-run `/setup` flow when no admin user exists, then switch to the normal login flow after the first admin is created.

Required pages:

- **Dashboard**: current version and age for each product, last successful and failed sync, storage usage, cleanup state, recent notifications, and recent download totals.
- **Products**: enable/disable products, view current and historical versions, inspect checksums, trigger a product sync, and download or verify a version.
- **Downloads**: filter download statistics by time range, product, version, API key, source IP, status, and route; show totals, bytes, failures, and unique source IPs.
- **API keys**: create keys, show the secret once, set scopes and product restrictions, revoke keys, expire keys, and view last-used time and source IP history.
- **Users**: create and disable admin users, rotate credentials, revoke sessions, and protect against accidentally removing the last usable administrator.
- **Settings**: edit all UI-managed operational configuration with validation and change history.
- **Operations**: inspect sync runs, retry failed syncs, run cleanup, inspect webhook deliveries, and run an integrity check.
- **Audit**: search administrative actions and request activity, including source IP addresses subject to the configured audit-retention policy.

UI requirements:

- Use server-rendered HTML with embedded assets for the initial release.
- Provide pagination and bounded date ranges for request and download views.
- Require explicit confirmation for destructive actions, revocation, cleanup, and manual publication operations.
- Show clear success, warning, and failure states without exposing stack traces or secrets.
- Enforce the same authorization and product-scope rules in UI handlers as in the API.
- Record every administrative action, including failed actions, request IP, actor, and outcome.
- Avoid exposing full API keys, MaxMind credentials, webhook secrets, or filesystem paths.
- Include CSRF protection for cookie-authenticated form submissions, or use non-cookie bearer authentication for every mutating request.

## 8. Version And Metadata Model

Each published database version should contain:

- Product ID.
- Internal version ID.
- Upstream release/build date.
- Upstream filename.
- Local publication timestamp.
- File size.
- SHA-256 checksum.
- Optional upstream checksum and checksum source.
- Artifact storage key/path.
- Lifecycle state:
  - `staged`
  - `published`
  - `superseded`
  - `expired`
  - `deleted`
  - `failed`
- Whether it is the current version.
- Sync run that produced it.
- Cleanup deadline.
- Validation result.
- Notification state.

Version identity should be based on stable upstream metadata and content checksum, not only on the date. If the upstream release date is reused or an off-schedule release occurs, a different checksum must still produce a distinct version record.

Recommended uniqueness constraints:

- One product plus upstream release/build identifier.
- One product plus SHA-256 checksum.
- Only one current version per product.

The API should expose both a stable `current` route and immutable version routes.

### Latest-product metadata cache

Latest-product metadata is read frequently by internal clients and should be served from an in-memory cache rather than querying SQLite for every request. The cache should be process-local and treated as an optimization only; SQLite remains the source of truth.

Cache the complete response data needed for the common latest/current endpoints, including:

- Product identifier.
- Current version identifier.
- Upstream release date.
- Publication timestamp.
- File size.
- SHA-256 checksum and HTTP `ETag`.
- Last-Modified value.
- Download filename and route metadata.
- Cleanup deadline and freshness state where shown by the endpoint.

Cache requirements:

- Use one entry per product, keyed by the stable internal product ID.
- Optionally cache the combined `/v1/manifest` response with a separate generation number.
- Use a bounded TTL as a safety net, with a default in the low-minute range configurable in the admin Settings page.
- Prefer explicit invalidation over waiting for TTL expiry.
- On a cache miss, load from SQLite and populate the cache.
- Prevent a concurrent miss from causing a query stampede; use per-product single-flight or equivalent locking so one request performs the load while others wait for the same result.
- Do not cache request-specific authorization decisions, API-key records, source IPs, or download statistics.
- Do not cache file contents; artifact streaming remains a filesystem operation.
- Do not serve stale metadata after a known successful publication or deletion event.

Cache invalidation must occur after the SQLite transaction that changes the source-of-truth state commits successfully:

- Invalidate the affected product after a new version becomes current.
- Invalidate the affected product after rollback or manual current-version changes.
- Invalidate the affected product after version deletion or expiry if the endpoint can observe that state.
- Invalidate the combined manifest after any product entry changes.
- Invalidate settings-dependent metadata after relevant settings changes, such as freshness thresholds or display metadata.

When invalidation cannot be performed synchronously, increment a cache generation and force the next read to reload from SQLite. On process restart, start with an empty cache and lazy-load entries. Cache metrics should include hits, misses, loads, invalidations, load failures, and entries currently resident.

The cache must have tests for publication invalidation, concurrent misses, TTL expiry, process restart, SQLite read failure, and ensuring that a newly published checksum is immediately returned by the current and manifest endpoints.

Example routes:

```text
GET /v1/products
GET /v1/products/{product}
GET /v1/products/{product}/current
GET /v1/products/{product}/versions
GET /v1/products/{product}/versions/{version}
GET /v1/products/{product}/versions/{version}/download
GET /v1/manifest
```

The manifest should provide enough metadata for clients to determine whether they need to download a file:

```json
{
  "product": "geolite-city",
  "version": "2026-09-04",
  "released_at": "2026-09-04T00:00:00Z",
  "sha256": "...",
  "size_bytes": 123456789,
  "download_url": "/v1/products/geolite-city/current/download"
}
```

## 9. Upstream Synchronization Flow

The sync process should be safe to run repeatedly and concurrently.

### Discovery

1. Load enabled product configurations.
2. Query MaxMind using the supported update mechanism.
3. Follow HTTPS redirects, including redirects to MaxMind's R2-backed download host.
4. Obtain release metadata using a lightweight check where supported.
5. Compare upstream release information against the current local version.
6. Skip a full download when no update is available.

MaxMind recommends `geoipupdate` for binary MMDB updates. The plan should evaluate either:

- Running the official `geoipupdate` client in a controlled worker/container, or
- Implementing direct authenticated downloads with the required redirect and response handling.

The first implementation should prefer the official update client unless product-level metadata and checksum handling require direct downloads.

### Staging

1. Create a unique staging directory or object key.
2. Download the compressed upstream artifact.
3. Enforce HTTPS and certificate validation.
4. Apply connection, request, and total download timeouts.
5. Enforce maximum file and compressed-payload sizes.
6. Do not expose staged files through the HTTP server.
7. Extract the expected MMDB file into staging.
8. Reject unexpected paths or archive traversal entries.
9. Compute SHA-256 while reading the extracted file.
10. Compare against the upstream-provided checksum when available.
11. Record the upstream headers and release metadata.
12. Validate that the file is a readable MMDB database.

### Idempotency

- If the checksum already exists for the product, mark the sync as a no-op.
- If a version with the same upstream release date but a different checksum appears, preserve both records and flag the event for operator review.
- Failed or partial staging data must not become visible to clients.
- Retries must not create duplicate published versions.

## 10. SHA-256 And Integrity Verification

Checksum verification should happen at multiple points:

- During download, calculate the checksum of the final extracted MMDB.
- Verify against MaxMind's published SHA-256 artifact when available.
- Store the calculated checksum in metadata.
- Verify the stored artifact before changing the current pointer.
- Optionally verify on a scheduled integrity scan.
- Return the checksum through metadata and an HTTP `ETag`.
- Support conditional requests with `ETag` and `Last-Modified`.

A checksum mismatch must:

- Fail the sync.
- Leave the current version unchanged.
- Delete the staged artifact.
- Record the failure and diagnostic details without logging credentials.
- Trigger an operator alert after the configured retry threshold.

Checksum verification is an integrity control, not a replacement for TLS, authentication, or source validation.

## 11. Atomic Publication

The current version must never point to a partially downloaded or unverified file.

For the required local-filesystem backend:

1. Download to a unique temporary directory.
2. Extract and validate the MMDB file.
3. Compute and verify the checksum.
4. Move the complete artifact into its immutable version directory.
5. Commit the version metadata transaction.
6. Update the product's current-version pointer in the same database transaction.
7. Expose the new version only after the commit succeeds.

For the future object-storage backend:

- Upload to an immutable version key.
- Verify size and checksum after upload.
- Commit metadata only after successful verification.
- Update the current pointer through a transactional database record rather than overwriting the object.
- Never use a mutable object as the source of truth for a published version.

Downloads should resolve the current pointer once at request start. Existing downloads may finish against the old version while new downloads use the new version.

If publication fails after artifact storage succeeds, the artifact remains unreferenced and is cleaned by a garbage-collection job after a safety delay.

## 12. Access Control

API keys should be independently manageable even though the service is single-user organizationally.

Each key should have:

- Public identifier or prefix.
- One-way hash of the secret.
- Display name.
- Created timestamp.
- Last-used timestamp.
- Revoked timestamp, if applicable.
- Optional expiration timestamp.
- Allowed products.
- Allowed operations:
  - `manifest:read`
  - `metadata:read`
  - `download`
  - `admin:sync`
  - `admin:keys`
  - `admin:cleanup`
- Optional source-network restrictions.
- Optional rate limit.
- Optional download quota.

API key behavior:

- Accept `Authorization: Bearer <key>` as the primary method.
- Avoid putting keys in query parameters.
- Show the full secret only once at creation time.
- Hash keys using a password/secret hashing algorithm such as Argon2id or an equivalent suitable KDF.
- Support immediate revocation.
- Return generic authentication failures without revealing whether a key exists.
- Do not log raw keys or authorization headers.
- Use separate operator/admin keys from consumer download keys.
- Allow product-scoped keys for least privilege.

Network controls should include:

- Private DNS or private load balancer where available.
- Firewall or security-group allowlisting.
- Optional mTLS for especially sensitive consumers.
- TLS termination with internal certificates.
- No direct public object-storage URLs; all client downloads go through the authenticated internal API.

## 13. Request Logging And Download Statistics

Because this service is for internal distribution, collect the source IP address for **every HTTP request**, including health checks, metadata requests, manifest requests, downloads, failed requests, and requests that do not authenticate successfully. Request-IP collection is an explicit audit and access-control requirement, not an optional abuse-detection enhancement.

For each request, capture:

- Request ID.
- Source IP address as observed by the trusted ingress or application server.
- Forwarded client IP information only from explicitly trusted reverse proxies; never trust arbitrary client-supplied forwarding headers.
- Product.
- Version served.
- Request timestamp.
- API-key ID, not the raw key.
- HTTP status.
- Bytes transferred when available.
- Duration.
- Conditional request result, such as `200` or `304`.
- Request method and path or route template.
- User-agent.
- Authentication result and failure reason category.
- Referrer only if operationally useful.

Store the full source IP address for the defined audit-retention period. Do not hash, truncate, anonymize, or discard it during that period. After the retention period, delete or irreversibly anonymize IP addresses according to the organization's security and privacy policy. The retention policy must be documented, configurable, and applied consistently to database records, structured logs, backups, exports, and monitoring systems.

Request-IP data must be protected as sensitive operational data:

- Restrict access to authorized operators and security/audit personnel.
- Do not expose request IPs through ordinary product metadata, status responses, or client-facing download APIs.
- Do not log authorization headers, API-key secrets, or other credentials alongside request data.
- Encrypt request logs and audit records at rest and in transit where supported.
- Use a trusted proxy configuration and record the proxy chain when forwarded headers are accepted.
- Alert on requests from unexpected networks, repeated authentication failures, and unusual key/IP combinations.
- Define how IPv4 and IPv6 addresses are normalized and stored.

Recommended derived metrics:

- Downloads by product and version.
- Downloads by API key and source IP.
- Successful versus failed downloads.
- Bytes served.
- `304 Not Modified` responses.
- Unique source IPs by time window.
- Authentication failures by source IP and API key.
- Last-seen source IP and source-IP history for each API key.
- Active API keys.
- Last download per key.
- Download rate by client.
- Current-version adoption.
- Versions still being requested after supersession.
- Sync success/failure counts.
- Cleanup success/failure counts.
- Webhook success/failure counts.

Use structured logs and a metrics endpoint compatible with the organization's monitoring system. Request-audit records must be written for every request; download-specific aggregates may be sampled or rolled up if volume grows, but the initial single-user deployment can store one detailed record per request.

The admin UI must provide download-statistics views backed by SQLite queries and bounded time ranges. At minimum, operators must be able to see total requests, successful downloads, failed downloads, bytes served, downloads by product/version, downloads by API key, source-IP activity, unique source IP counts, and recent authentication failures. Detailed request records must support pagination and filtering without loading the entire audit table into memory.

## 14. Health And Status Endpoints

Separate liveness from readiness.

### Liveness

```text
GET /health/live
```

Returns success if the process is running and able to serve basic requests. It should not depend on MaxMind, the database, or object storage.

### Readiness

```text
GET /health/ready
```

Checks:

- Database connectivity.
- Artifact storage availability.
- Required configuration and secrets.
- Ability to resolve/read the current artifact metadata.
- Worker state if the deployment requires background workers.

### Status

```text
GET /v1/status
```

Authenticated or restricted to internal monitoring. Include:

- Service version.
- Current time.
- Last successful sync per product.
- Last attempted sync per product.
- Current version per product.
- Release age.
- Cleanup status.
- Last checksum verification.
- Last notification result.
- Upstream availability state.
- Whether any product is stale or missing.

Do not expose upstream credentials, internal filesystem paths, raw error traces, or sensitive configuration in status responses.

## 15. Webhook And Notification Design

A notification should be emitted only after a new version has been fully verified and committed as current.

Webhook payload:

```json
{
  "event": "database.updated",
  "event_id": "uuid",
  "occurred_at": "2026-09-04T12:00:00Z",
  "product": "geolite-city",
  "version": "2026-09-04",
  "released_at": "2026-09-04T00:00:00Z",
  "sha256": "...",
  "size_bytes": 123456789,
  "download_path": "/v1/products/geolite-city/current/download"
}
```

Security and reliability:

- Configure one or more private webhook destinations.
- Sign payloads with HMAC-SHA256.
- Include a timestamp and event ID to support replay protection.
- Use short connection and response timeouts.
- Retry transient failures with exponential backoff and jitter.
- Persist delivery attempts.
- Avoid retrying permanent authentication or malformed-request failures indefinitely.
- Provide an operator-visible failed-delivery state.
- Make consumers idempotent using `event_id`, product, version, and checksum.
- Optionally support notification channels such as email, Slack, or an internal queue later.

Notifications should not block the artifact publication transaction or client downloads.

## 16. Automatic Cleanup

Cleanup must reflect the GeoLite 30-day destruction requirement while preserving operational safety.

Recommended policy:

- Keep the current version.
- Keep the immediately previous version only when it remains within the permitted window and rollback is explicitly needed.
- Delete superseded versions no later than 30 days after the newer release.
- Use a configurable safety margin, such as cleanup at 25 to 28 days, with hard enforcement at 30 days.
- Remove database metadata and artifact storage together, or mark metadata deleted only after storage deletion succeeds.
- Preserve minimal audit records without retaining the database contents.
- Do not delete a version currently being streamed.
- Use a lease/reference count or delayed deletion strategy for in-flight downloads.
- Run cleanup daily.
- Alert on artifacts past their cleanup deadline.
- Run a garbage collector for unreferenced staging and failed artifacts.

Cleanup should be based on upstream release dates, not merely local publication timestamps, to ensure compliance during outages or delayed synchronization.

## 17. Failure Handling

Define behavior for:

- MaxMind unavailable.
- Authentication failure upstream.
- Rate limiting or download quota exhaustion.
- Redirect or R2 host connectivity failure.
- Invalid archive.
- Missing expected MMDB file.
- Checksum mismatch.
- MMDB parser validation failure.
- Storage full.
- Database transaction failure.
- Webhook failure.
- Cleanup failure.
- Clock skew affecting release and retention calculations.

General rules:

- Preserve the last known good current version.
- Never replace a valid current version with a failed or unverified artifact.
- Retry transient errors with bounded exponential backoff.
- Use a circuit breaker or cooldown when upstream repeatedly fails.
- Record structured failure details and sync run IDs.
- Alert only after meaningful thresholds to avoid notification storms.
- Make all worker jobs resumable and idempotent.

## 18. API And HTTP Behavior

Download endpoints should support:

- `GET` and `HEAD`.
- `Content-Type: application/octet-stream`.
- `Content-Disposition` with the canonical MMDB filename.
- `Content-Length`.
- `ETag` based on the SHA-256 checksum.
- `Last-Modified` based on the database release date or publication metadata.
- `Cache-Control` appropriate for private clients.
- `Range` requests only if storage and statistics handling can support them correctly.
- `304 Not Modified` for matching conditional requests.

Error responses should be consistent and avoid leaking implementation details:

```json
{
  "error": {
    "code": "version_not_found",
    "message": "The requested database version was not found."
  }
}
```

Use appropriate status codes:

- `401` for missing or invalid credentials.
- `403` for valid credentials without required scope.
- `404` for unknown product/version, where revealing existence is acceptable.
- `409` for conflicting publication operations.
- `429` for rate limits.
- `503` when a product has no available current version or storage is unavailable.

## 19. Deployment And Operations

Initial deployment should use a single private instance with separate worker processes or scheduled jobs. SQLite requires a single active writer and should not be used as a shared database across multiple application instances.

Required operational components:

- Application container or managed service.
- Persistent local artifact volume.
- SQLite metadata database on persistent local storage.
- Embedded Go admin UI served by the same Go process.
- Secret manager integration.
- TLS certificate management.
- Scheduled sync job.
- Scheduled cleanup job.
- Metrics and structured logging.
- Backup of metadata and configuration.
- Artifact integrity verification procedure.
- Recovery runbook.

Backups should be designed carefully:

- Back up metadata and configuration.
- Use SQLite's online backup mechanism or an equivalent consistent snapshot procedure; do not copy the database file during arbitrary writes.
- Do not create indefinite backups of expired GeoLite artifacts.
- Apply the same retention and destruction requirements to backup copies.
- Document how to restore metadata without restoring deleted database files.
- Test recovery from a storage failure and a database failure.

## 20. Testing Strategy

### Unit tests

- Product configuration parsing.
- Version identity and deduplication.
- SHA-256 calculation.
- Checksum mismatch handling.
- MMDB validation.
- Retention deadline calculation.
- API-key hashing and scope checks.
- Webhook signing.
- Retry and backoff behavior.
- Conditional request handling.
- Latest-product metadata cache hit/miss, TTL, invalidation, and concurrent-miss behavior.

### Integration tests

- Sync against a local fake MaxMind server.
- Redirect handling.
- Compressed artifact extraction.
- Atomic publication under concurrent reads.
- Database transaction rollback.
- Filesystem storage failure.
- SQLite lock contention, corruption, backup, and restore failure.
- API-key creation, revocation, and authorization.
- Download event recording.
- Cleanup with active downloads.
- Webhook retries and idempotency.
- Cache invalidation after publication, rollback, deletion, and relevant settings changes.

### End-to-end tests

- Publish a fake product version.
- Download it using a scoped API key.
- Publish a second version.
- Verify the current route changes atomically.
- Verify current and manifest metadata are served from cache after the initial SQLite load.
- Verify a new publication invalidates the cache and immediately exposes the new checksum and version.
- Verify the old version remains available only within policy.
- Verify cleanup removes the old artifact at the deadline.
- Verify update notification contains the published checksum.
- Verify health and status responses reflect the worker state.

### Security tests

- No unauthenticated artifact access.
- No cross-product access with a scoped key.
- Revoked keys fail immediately.
- Setup cannot be accessed without the bootstrap secret.
- Setup becomes unavailable after the first admin is created.
- Concurrent setup requests cannot create more than one first admin.
- Removing all admins correctly returns the service to protected setup-required state.
- Raw secrets absent from logs.
- Path traversal rejected during extraction.
- Oversized downloads rejected.
- Malformed archives rejected.
- Webhook signature validation works.
- Rate limits and network restrictions behave as configured.

## 21. Observability And Alerts

Create alerts for:

- No successful sync within the expected product cadence.
- Current version older than the allowed freshness threshold.
- Checksum mismatch.
- MMDB validation failure.
- Storage nearing capacity.
- Cleanup deadline approaching.
- Expired artifact still present.
- Repeated upstream authentication failures.
- Webhook delivery failures.
- Unexpected download spikes.
- Downloads using deprecated versions.
- Readiness failures.
- Metadata database backup failures.
- Excessive latest-metadata cache misses or cache-load failures.
- Cache entries remaining stale after a successful publication.
- Unexpected source-IP activity or API-key use from an unapproved network.
- Excessive authentication failures from a source IP or API key.

Recommended dashboards:

- Product freshness.
- Sync history.
- Current versions and checksums.
- Download volume by product/version/key.
- Storage usage.
- Cleanup status.
- Notification delivery.
- API errors and latency.

## 22. Suggested Implementation Sequence

1. Define the SQLite schema for users, sessions, products, versions, API keys, sync runs, request audits, download events, settings, cleanup, notifications, and admin audit records.
2. Establish the Go module, service structure, `.env` bootstrap configuration, and secret-handling approach.
3. Implement settings storage, validation, migrations, and audit logging.
4. Implement first-run setup detection, bootstrap-secret protection, atomic first-admin creation, login, sessions, and recovery behavior.
5. Implement artifact storage abstraction with local-filesystem support.
6. Implement checksum calculation and MMDB validation.
7. Implement the upstream sync worker with staging and idempotency.
8. Implement transactional publication and current-version pointers.
9. Implement the process-local latest-product metadata cache with bounded TTL, single-flight loading, metrics, and explicit invalidation.
10. Implement authenticated metadata and download endpoints.
11. Implement API-key creation, revocation, scopes, and product restrictions.
12. Add mandatory request-IP audit logging and download-statistics queries.
13. Add the simple embedded Go admin UI, starting with setup, login, dashboard, products, downloads, users, API keys, settings, operations, and audit pages.
14. Add health, readiness, status, metrics, and structured logging.
15. Add webhook delivery with signing, retry, and persistence.
16. Add cleanup and unreferenced-artifact garbage collection.
17. Add integration, concurrency, security, UI, cache, and end-to-end tests, including concurrent first-admin setup attempts.
18. Add deployment manifests, `.env.example`, SQLite online backups, monitoring, and runbooks.
19. Perform a compliance review of retention, access, attribution, and destruction behavior.
20. Run a controlled rollout with one product and one consumer before enabling all products.

## 23. Decisions To Confirm Before Implementation

1. Whether to add private S3-compatible object storage after the filesystem-backed release.
2. SQLite backup destination and recovery-point objective.
3. Deployment target and internal network boundary.
4. Bootstrap-secret delivery method and whether the service should require an internal network allowlist in addition to the secret.
5. Whether `geoipupdate` can be installed in the worker environment.
6. Required client authentication format and whether mTLS is also needed.
7. Exact list of initial MMDB products.
8. Whether consumers need historical version downloads or only the current version.
9. Webhook destination and notification channel.
10. Required request-audit and download-statistics retention policy.
11. Maximum acceptable database staleness per product.
12. Whether the organization's legal/compliance owner requires a stricter cleanup policy than the 30-day maximum.

## References

- [MaxMind GeoLite databases and web services](https://dev.maxmind.com/geoip/geolite2-free-geolocation-data/)
- [MaxMind database updating guidance](https://dev.maxmind.com/geoip/updating-databases/)
- [MaxMind GeoLite End User License Agreement](https://www.maxmind.com/en/geolite2/eula)
- [MaxMind download and update guidance](https://support.maxmind.com/knowledge-base/articles/download-and-update-maxmind-databases)
- [MaxMind GeoIP Update](https://github.com/maxmind/geoipupdate)
