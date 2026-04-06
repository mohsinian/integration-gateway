# Integration Gateway

A Go backend service that enriches foreclosure case records by fetching data from three unreliable external mock services. Built with resilience patterns (retry with backoff, circuit breakers, rate limiting) and PostgreSQL for durable state.

## Instructions to Run

### Prerequisites

- **Docker** (with Docker Compose V2)
- **Git**

No Go toolchain, no PostgreSQL, no local dependencies — everything runs inside Docker.

### Setup

```bash
# 1. Clone
git clone git@github.com:mohsinian/integration-gateway.git
cd integration-gateway

# 2. Configure environment
cp .env.example .env
# Edit .env and fill in POSTGRES_USER and POSTGRES_PASSWORD, or leave the defaults.

# 3. Build and start everything
./scripts/up.sh
```

The first run builds Docker images (about 30–60 seconds). Subsequent starts are instant.

The database (PostgreSQL 16) starts first with a health check. Once healthy, the backend automatically runs migrations and seeds the 6 test cases. No manual database setup needed.

Once running:

| Service | URL | Purpose |
|---------|-----|---------|
| Backend API | http://localhost:8080 | The integration gateway |
| Adminer (DB GUI) | http://localhost:8081 | Browse PostgreSQL data |
| Property Records | http://localhost:9001 | Mock external service |
| Court Records | http://localhost:9002 | Mock external service |
| SCRA Check | http://localhost:9003 | Mock external service |

### Stopping

```bash
docker compose down          # stop containers, keep data
docker compose down -v       # stop and delete database volume
```

## How to Trigger Enrichment and Observe Results

### API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/cases/{id}/enrich` | Trigger enrichment for one case |
| POST | `/api/enrich/bulk` | Trigger enrichment for multiple cases |
| GET | `/api/cases/{id}/enrichment` | Check enrichment status and data |
| GET | `/api/cases` | List all cases with status summary |
| GET | `/api/health` | Service health + circuit breaker states |

### Walkthrough

```bash
# Start the stack
./scripts/up.sh

# Enrich a single case (triggers async lookup)
curl -X POST http://localhost:8080/api/cases/case-001/enrich

# Wait a few seconds, then check status
curl http://localhost:8080/api/cases/case-001/enrichment | python3 -m json.tool

# Enrich all 6 cases at once
curl -X POST http://localhost:8080/api/enrich/bulk \
  -H "Content-Type: application/json" \
  -d '{"caseIds": ["case-001","case-002","case-003","case-004","case-005","case-006"]}'

# Check results for a specific case
curl http://localhost:8080/api/cases/case-002/enrichment | python3 -m json.tool

# List all cases and their status
curl http://localhost:8080/api/cases | python3 -m json.tool

# Check system health (database + circuit breakers)
curl http://localhost:8080/api/health | python3 -m json.tool
```

### The 6 Test Cases

| Case | Borrower | Sources | Edge Case |
|------|----------|---------|-----------|
| case-001 | Elena Martinez | Property + SCRA | No court case number (title-search) — court skipped |
| case-002 | David R. Thompson | All 3 | 3 liens including HOA lis pendens |
| case-003 | Thanh Nguyen | All 3 | Clean property, 1 lien |
| case-004 | Marcus Johnson | All 3 | SCRA returns activeDuty=true |
| case-005 | Sarah Williams | Property + SCRA | No court case number (title-search) |
| case-006 | Vikram Patel | All 3 | Property returns 404 (not in database) |

### Demo Script

Runs the full flow in one shot — health check, single enrich, bulk enrich, final status:

```bash
./scripts/demo.sh                # full demo
./scripts/demo.sh --skip-seed   # just check current status
```

### Stress Test

Repeatedly enriches all cases over multiple rounds to exercise the mock services' random failure rates (503s, malformed XML, rate limits, SCRA failures). Each round shows per-source results, circuit breaker states, and idempotency checks.

```bash
./scripts/stress.sh              # 5 rounds (default)
./scripts/stress.sh 10           # 10 rounds
./scripts/stress.sh --watch      # continuous until Ctrl+C
```

This is the fastest way to observe retries, partial recovery, and circuit breaker transitions in action.

### Tests

```bash
# Unit tests (all packages)
./scripts/test.sh

# Specific unit test suite
./scripts/test.sh client          # external service clients
./scripts/test.sh resilience      # retry, circuit breaker, rate limiter

# Integration tests (requires Docker stack running)
./scripts/up.sh                   # start services first
./scripts/test.sh integration     # run all integration tests
./scripts/test.sh integration -run Bulk   # specific test
```

### Inspecting Logs

```bash
./scripts/log-navigator.sh follow       # live tail all logs
./scripts/log-navigator.sh errors       # show errors only
./scripts/log-navigator.sh search case-001   # search across logs
./scripts/log-navigator.sh stats        # entry counts and file sizes
```

Log files are written to `./logs/` (mounted as a Docker volume):

| File | Contents |
|------|----------|
| `app.log` | Startup, shutdown, migrations, seeding |
| `server.log` | HTTP requests, responses, enrichment events |
| `error.log` | Failed migrations, external service failures |

### Database Management

The `migration.sh` script provides an interactive CLI for inspecting and re-running migrations:

```bash
./scripts/migration.sh status    # show applied / pending migrations
./scripts/migration.sh run-all   # apply any pending migrations
./scripts/migration.sh run       # pick and run a specific migration
```

You can also browse the database at http://localhost:8081 (Adminer) using the credentials from `.env`.

## Architecture

```
Client (curl / Postman)
   │
   ▼
┌──────────────────────────────────────────────┐
│  Backend Service  (Go + PostgreSQL)          │
│                                              │
│  REST API (5 endpoints)                      │
│  Lookup Orchestrator (concurrent sources)    │
│  Resilience (retry, circuit breaker, limiter)│
│  Structured Logging (app / server / error)   │
└────┬──────────────┬──────────────┬───────────┘
     │              │              │
     ▼              ▼              ▼
  :9001          :9002          :9003
Property       Court          SCRA
(JSON)         (XML)          (Async)
```

The service sits between the client and three unreliable external mock services. On startup it connects to PostgreSQL, runs numbered SQL migrations tracked in a `schema_version` table, and seeds 6 foreclosure cases.

When enrichment is triggered for a case, the orchestrator determines which sources are applicable (court records are skipped for pre-filing cases without a court case number) and fetches them concurrently using `errgroup`. Each source is protected by its own circuit breaker, and court records go through a global rate limiter (2 req/sec). Transient failures are retried with exponential backoff and jitter. SCRA uses a submit-then-poll pattern.

Results are stored per-source in PostgreSQL with full attempt history. Enrichment is idempotent — a `UNIQUE(case_id)` constraint ensures one lookup run per case. Re-triggering enrichment returns existing results or retries only failed sources.

### Helper Scripts

| Script | Description |
|--------|-------------|
| `./scripts/up.sh` | Build and start all Docker services |
| `./scripts/build.sh` | Build Docker images without starting |
| `./scripts/test.sh` | Run tests (unit or integration) |
| `./scripts/demo.sh` | End-to-end demo: enrich cases and print results |
| `./scripts/stress.sh` | Repeated enrichment to exercise retries and circuit breakers |
| `./scripts/migration.sh` | Inspect or re-run database migrations |
| `./scripts/log-navigator.sh` | Browse, tail, search, and filter logs |
