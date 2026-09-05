# Litebase

A self-hosted SQLite database service. Manage databases, browse and edit data,
run SQL, and turn any table or query into a REST API — from one binary you can
copy to a server and run.

- **Single binary.** The Go server embeds the compiled dashboard. No Node, no
  CGO, no external database.
- **Pure-Go SQLite.** Static builds and trivial cross-compilation to a VPS.
- **API Builder.** Generate CRUD endpoints from a table, or expose a SQL query
  with validated, safely bound parameters.
- **Safe backups.** Consistent snapshots via `VACUUM INTO`, optional
  AES-256-GCM encryption, retention, scheduling, and Google Drive.

---

## Quick start

### Run locally

```bash
make build
./litebase
```

Open <http://localhost:8090>. On first start Litebase creates an administrator
and prints a generated password once — copy it from the terminal.

To choose your own credentials instead:

```bash
LITEBASE_ADMIN_EMAIL=you@example.com LITEBASE_ADMIN_PASSWORD='a long password' ./litebase
```

### Run with Docker

```bash
cp .env.example .env
# set LITEBASE_ADMIN_PASSWORD and LITEBASE_BACKUP_ENCRYPTION_KEY
docker compose up -d
```

### Install on a Linux server

```bash
sudo ./install.sh
```

This creates a `litebase` system user, installs to `/usr/local/bin`, writes
`/etc/litebase/litebase.env` with generated secrets, and registers a hardened
systemd unit. It is safe to re-run.

---

## What it does

### Databases

Create, rename, delete and import SQLite databases. Import verifies the file is
a real, readable SQLite database before accepting it. Export downloads a
consistent snapshot rather than the live file, so it is safe to run while the
database is in use.

Every database opens in WAL mode with foreign keys enforced, and each one gets a
single serialized writer connection plus a pool of readers — the reliable way to
avoid `SQLITE_BUSY` under concurrent traffic.

### Tables and schema

Browse tables, views, indexes and triggers. Create and drop tables, add, rename
and drop columns, and manage indexes.

SQLite cannot alter a column in place, so editing one runs the documented table
rebuild: the rows are copied into a new table inside a transaction, indexes and
triggers are recreated, and `foreign_key_check` runs before the commit. If
anything fails the whole thing rolls back and your data is untouched.

### Rows

Browse, search, filter, sort and paginate. Create, edit and delete rows,
individually or in bulk. Import and export CSV and JSON.

Filters are structured (`column`, operator, value) and every value is sent as a
bound parameter. `LIKE` wildcards inside search terms are escaped, so searching
for `50%` matches the literal text.

### SQL editor

Run arbitrary SQL with results, errors, row counts and timings. Multiple
statements run in order and stop at the first failure.

Authorisation here is enforced by SQLite itself, not by parsing your SQL: a user
without `sql:write` runs on a connection opened `mode=ro`, so the engine refuses
any write — including ones reached through a trigger.

### API Builder

Two kinds of endpoint:

**Automatic CRUD** — pick a table and get `list`, `read`, `create`, `update` and
`delete` routes. Choose which operations to enable, restrict which columns are
readable or writable, and allow filtering and search.

**Custom query** — write a SQL statement with `?` placeholders and declare a
parameter for each one:

```
GET /api/products/search
```

```sql
SELECT * FROM products
WHERE category = ?
  AND price <= ?
ORDER BY created_at DESC
LIMIT ?;
```

| Parameter   | In    | Type    | Required |
|-------------|-------|---------|----------|
| `category`  | query | string  | yes      |
| `max_price` | query | number  | no       |
| `limit`     | query | integer | no       |

The placeholder count must match the declared parameters, which is checked when
you save. At request time each value is validated against its type and
constraints (min, max, length, pattern, enum) and then **bound** — user input
never becomes part of the statement text.

Endpoints can be enabled and disabled, rate limited individually, marked public,
and are documented automatically as an OpenAPI 3.1 spec generated from the live
definitions. The built-in explorer sends real requests and shows a matching
`curl` command.

### API keys

Issue keys with `read` and/or `write` scopes, grant them to all endpoints or
specific ones, set a per-key rate limit, and give them an expiry. A key is shown
once at creation; only its SHA-256 hash is stored.

### Backups

Snapshots use `VACUUM INTO`, which writes a transactionally consistent copy
while the database stays online — unlike copying the file, which can capture a
torn page or miss commits still in the WAL.

- Manual and scheduled backups
- Retention by count and by age — and never removes the last remaining backup
- Restore, with a safety snapshot taken first and the checksum verified before
  anything is overwritten
- Download, decrypted on the way out
- Local storage and Google Drive, behind a provider interface that S3 or R2 can
  be added to
- Optional AES-256-GCM encryption before upload, streamed in chunks so a backup
  larger than RAM is never buffered whole

---

## Security

| Area | Approach |
|---|---|
| Passwords | Argon2id (64 MiB, t=3, p=2), per-password salt, PHC-encoded |
| Sessions | Random 256-bit tokens, stored only as SHA-256; HttpOnly, SameSite=Lax |
| CSRF | Double-submit token on every cookie-authenticated write |
| SQL injection | Values always bound; identifiers validated against a strict allowlist and quoted in one place |
| Path traversal | Every user-derived path resolved through one checked join |
| Rate limiting | Separate token buckets for sign-in, dashboard and data API |
| Headers | CSP, `X-Frame-Options: DENY`, `nosniff`, Referrer-Policy, HSTS over TLS |
| Body limits | Separate caps for JSON bodies and file uploads |
| Errors | Internal failures logged, never returned; clients get a generic message and a request id |
| Secrets | No hardcoded credentials; cloud credentials encrypted at rest |
| Roles | `owner`, `admin`, `editor`, `viewer`, checked per route as permissions |

Enumeration is handled deliberately: an unknown email costs the same as a wrong
password, because the login path hashes against a dummy value either way.

### Before exposing it to the internet

1. Put a TLS-terminating reverse proxy in front.
2. Set `LITEBASE_SECURE_COOKIES=true`.
3. Set `LITEBASE_TRUST_PROXY=true` — **only** behind a proxy you control,
   since it makes the server trust `X-Forwarded-For`.
4. Set `LITEBASE_BACKUP_ENCRYPTION_KEY` and store it somewhere safe.
5. Sign in and change the generated password.

---

## Configuration

Configuration comes from a JSON file and environment variables, in that order of
precedence. Every setting has a working default. See
[`.env.example`](.env.example) for the full list.

```bash
LITEBASE_ADDR=0.0.0.0:8090
LITEBASE_DATA_DIR=/var/lib/litebase
LITEBASE_BACKUP_ENCRYPTION_KEY=...
LITEBASE_SECURE_COOKIES=true
```

All state lives under `LITEBASE_DATA_DIR`:

```
data/
├── litebase.db        # internal metadata (users, endpoints, backups)
├── databases/         # your SQLite databases
├── backups/           # local backup artefacts
└── tmp/               # staging for snapshots and uploads
```

The metadata database has its own versioned, forward-only migrations, applied
transactionally at startup.

---

## Development

```bash
make dev            # API server on :8090
make dev-frontend   # Vite dev server on :5173, proxying /api
```

```bash
make check          # go vet, go test, tsc --noEmit
make test-race      # tests under the race detector
```

### Layout

```
backend/
  cmd/litebase/         entry point, embedded dashboard
  internal/
    auth/               passwords, sessions, API keys, roles
    api/                API Builder: validation, routing, runtime, OpenAPI
    backup/             snapshots, encryption, schedules, retention
    config/             configuration loading
    database/           SQLite layer: pools, migrations, identifiers, values
    httpx/              JSON responses and the error envelope
    middleware/         logging, recovery, headers, limits, auth, CSRF
    query/              SQL editor: splitting, classification, execution
    rows/               row CRUD, filters, sorting
    scheduler/          periodic jobs
    server/             routes and handlers
    storage/            backup storage providers
    tables/             schema introspection and DDL
frontend/               React + TypeScript dashboard
```

Everything SQLite-specific stays behind `internal/database`. Identifier quoting
lives in exactly one file, which is what makes "no user input is ever
concatenated into SQL" an auditable claim rather than a hopeful one.

### Tests

The suite covers the parts where a bug is expensive: identifier validation and
path traversal, password hashing and session lifecycle, API key scoping, DDL
generation and the table rebuild, filter and injection handling, SQL
classification and read-only enforcement, parameter binding and validation, and
backup encryption, restore and retention.

```bash
make test
```

---

## Deployment

### Coolify (Hostinger VPS or any Docker host)

Litebase needs no configuration to deploy. It generates its own administrator
password and encryption key on first start, detects that it is behind a proxy,
and serves the dashboard and the API from one port.

1. **Push this repository** to GitHub or GitLab.
2. In Coolify: **+ New → Resource → Public/Private Repository**, select the repo.
3. Set **Build Pack** to **`Docker Compose`**, with the compose file at
   `docker-compose.yml`. This matters: the Compose build pack creates the data
   volume for you, whereas the plain Dockerfile pack does not.
4. Under **Domains**, click **Generate Domain**.
5. **Deploy.**
6. Open **Logs** and copy the generated password from the boxed banner.
7. Visit the domain, sign in, and change the password in the sidebar.

That is the whole procedure. There is nothing to set under *Environment
Variables* and nothing to add under *Storages*.

#### Why no configuration is needed

| Concern | How it is handled |
|---|---|
| HTTPS | Coolify's proxy terminates TLS and issues the certificate. Litebase marks its cookies `Secure` automatically once requests arrive over HTTPS |
| URL | Coolify assigns the domain and routes it to port 8090, which the compose file declares |
| Admin account | A strong password is generated on first start and printed once to the deploy log |
| Encryption key | Generated on first start and stored on the data volume, so backup encryption works immediately and keeps working across redeploys |
| Persistence | The compose file declares the `/data` volume, which Coolify creates |
| Client addresses | Litebase trusts forwarding headers when the connecting peer is on a private network, which is always true behind a proxy, so rate limiting sees real client addresses |

#### Optional overrides

Set any of these under *Environment Variables* only if you want them:

| Variable | Effect |
|---|---|
| `LITEBASE_ADMIN_EMAIL` / `LITEBASE_ADMIN_PASSWORD` | Choose the first account instead of having one generated. Only read while no user exists |
| `LITEBASE_BACKUP_ENCRYPTION_KEY` | Manage the key yourself rather than letting the instance generate one |
| `LITEBASE_MAX_UPLOAD_BYTES` | Raise the import limit. Coolify's proxy has its own body limit, so raise that too |

#### Things worth knowing

- **Keep the volume.** Deleting it destroys your databases *and* the generated
  encryption key, which makes existing encrypted backups unrecoverable. Take a
  copy of `/data/.instance_key` if you rely on encrypted backups.
- **Redeploys are safe.** Data lives on the volume, not in the image, and the
  metadata database migrates itself forward on startup.
- **Lost the password?** Open a terminal on the container and run
  `litebase --reset-password admin@litebase.local`.
- **Auto-generated domains.** Coolify's generated domain is an `sslip.io`
  address. Let's Encrypt rate-limits that domain, so if certificate issuance
  fails, point a domain of your own at the server and use it instead.

### Reverse proxy (without Coolify)

```nginx
server {
    listen 443 ssl http2;
    server_name db.example.com;

    ssl_certificate     /etc/letsencrypt/live/db.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/db.example.com/privkey.pem;

    # Database and backup uploads need a generous body limit.
    client_max_body_size 512M;

    location / {
        proxy_pass http://127.0.0.1:8090;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Large exports and restores can take a while.
        proxy_read_timeout 300s;
    }
}
```

### Health checks

- `GET /api/health` — liveness; returns version and uptime
- `GET /api/ready` — readiness; verifies the metadata database is reachable

Both are unauthenticated and disclose nothing else.

### Cross-compiling

```bash
make release   # static binaries for linux/amd64 and linux/arm64 in dist/
```

### Recovering access

```bash
litebase --reset-password admin@example.com
```

Prints a new password and signs out that account everywhere.

---

## API usage

```bash
# List rows
curl -H 'Authorization: Bearer lbk_...' \
  'https://db.example.com/api/products?limit=20&sort=-price'

# Filter
curl -H 'Authorization: Bearer lbk_...' \
  'https://db.example.com/api/products?category=tools&price__lte=100'

# Create
curl -X POST -H 'Authorization: Bearer lbk_...' \
  -H 'Content-Type: application/json' \
  -d '{"name":"Widget","price":9.99}' \
  https://db.example.com/api/products
```

Filter operators: `eq`, `neq`, `gt`, `gte`, `lt`, `lte`, `like`, `contains`,
`starts_with`, `ends_with`, `in`, `not_in`, `between`, `is_null`, `is_not_null`
— used as `column__operator=value`.

Errors share one shape:

```json
{
  "error": {
    "code": "validation_failed",
    "message": "one or more parameters are invalid",
    "fields": { "limit": "must be at most 100" }
  },
  "request_id": "h6gny7muaixxvrj4ygepzhfj"
}
```

The `request_id` also appears in the server log, so a report can be traced
without exposing anything internal to the caller.

---

## License

MIT
