# Approach

## Key Architectural Decisions

### Service Structure

The service is organized as a layered Go application with four boundaries: HTTP handlers (`internal/api`), an orchestration layer (`internal/lookup`), external service clients (`internal/client`), and a PostgreSQL data layer (`internal/store`). Each layer communicates through typed Go structs — clients return domain types, not raw HTTP responses; the store reads and writes model structs; handlers serialize to JSON only at the edge.

**Why this shape:** The critical complexity in this problem is not "call three APIs" — it's handling partial failures, concurrent state, and idempotency correctly. Isolating the orchestrator as its own package keeps that logic in one place, testable independently of HTTP or database concerns.

### Schema Design

Four tables, each in its own numbered migration file, tracked by a `schema_version` table:

- **`cases`** — seed data from `cases.json`, flat columns matching the JSON structure
- **`lookup_runs`** — one row per case, `UNIQUE(case_id)` constraint enforces idempotency at the database level
- **`lookup_sources`** — one row per (run, source) pair with `JSONB data` column, tracking attempts, status, error messages, and the SCRA search ID

The `UNIQUE(case_id)` constraint is the backbone of idempotency. No application-level locking can guarantee that two concurrent `POST /enrich` requests won't create duplicate runs — the database constraint does. The `lookup_sources` table makes partial enrichment a first-class concept: each source has independent lifecycle (pending → success/failed/not_applicable), and re-triggering enrichment only resets rows in `failed` or `pending` state.

### Concurrency Model

Within a single case, sources are fetched concurrently using `errgroup`. Each goroutine manages its own database writes — there is no shared mutable state between goroutines. After all goroutines complete, the orchestrator re-reads source rows from the database and computes the overall status (complete / partial / failed) from the authoritative persisted state.

For bulk enrichment (`POST /api/enrich/bulk`), a 3-worker pool processes cases concurrently. All workers share the same court records rate limiter, ensuring the global 2 req/sec constraint holds across concurrent cases.

**Why not channels for coordination:** The `errgroup` pattern is simpler here because each goroutine is independent (no fan-in/fan-out of data). The database is the coordination point — each goroutine writes its result, and the final status is computed by reading what was written.

## Resilience Strategy

The three resilience primitives layer on top of each other:

**Retry with backoff** is the innermost layer. Every external call goes through `resilience.Do()`, which wraps the call in exponential backoff (1s → 2s → 4s → 8s → 16s, capped at 30s) with ±500ms jitter. Errors are classified as permanent (404 from Property Records, SCRA search failure) or transient (503, timeout, malformed XML, 429). Permanent errors short-circuit immediately — no wasted retries.

**Circuit breakers** sit outside retry. Each external service gets its own breaker (closed → open after 5 consecutive failures → half-open after 30s cooldown). When a breaker is open, the source is marked failed immediately without hitting the external service. This prevents burning retry budget on a service that's confirmed down.

**Rate limiter** gates court records specifically. It's a token-bucket implementation (2 tokens/sec) shared globally across all goroutines. A goroutine calls `Wait()` before making a court records request; if the bucket is empty, it blocks until a token arrives. This runs before the retry loop for each court records attempt.

**What happens when everything goes wrong simultaneously:** Property Records is returning 503s — retries with backoff handle this until either a request succeeds or 5 attempts exhaust. Court Records is rate-limiting — the rate limiter spaces requests, and 429 responses are retried after the `Retry-After` header. SCRA searches are failing — submit is retried up to 3 times; if all fail, the source is marked failed. If all three services stay down long enough, all three circuit breakers open, and subsequent enrichment attempts return immediately with "circuit open" failures. The service stays up and responsive — it just can't enrich until services recover. When a service comes back, the half-open probe lets one request through, and success closes the circuit.

### Error Classification

Errors are wrapped with semantic types at the client layer:

- `PermanentError` (404, SCRA search failure) — wrapped so retry stops immediately
- Transient errors (503, timeout, malformed XML, 429) — returned unwrapped, retried with backoff

The `IsPermanent()` check in the retry loop uses `errors.As` to unwrap, so classification works correctly even when errors are further wrapped with context.

### SCRA Polling

The submit-then-poll pattern is split into two phases: submit (retried up to 3 times), then poll (1-second intervals, 30-second total timeout). The poll loop uses a `time.Ticker`, not a tight loop. SCRA search failures (status `"error"`) are permanent — the poll stops and marks the source failed. Transport errors during polling are transient — the loop continues until the timeout.

## Planning and Development Process

I worked from a detailed `PLAN.md` that laid out the full architecture before writing any code — database schema, client interfaces, resilience contracts, orchestrator flow, and handler signatures. This let me build in a bottom-up order where each layer could be tested in isolation: migrations → models → clients → resilience → orchestrator → handlers. Each step produced something testable, which caught integration issues early.

I also used Claude Code's memory system to persist progress across sessions. After each implementation phase, I saved what was built and what remained, so subsequent sessions could pick up exactly where the previous one left off without re-reading the entire codebase. This was especially useful for a multi-session project where context windows reset between conversations.

## What I Would Change With More Time

- **Structured context propagation** — correlation IDs passed through `context.Context` and included in all log entries, so an operator can trace a single enrichment request across all external calls and goroutines.
- **Prometheus metrics** — request latency histograms per external service, error rate counters, circuit breaker state gauges, and enrichment completion counters. The health endpoint provides a snapshot, but time-series metrics are what you need for alerting.
- **Webhook callbacks** — optional `callbackUrl` parameter on the enrich endpoint that receives a POST when enrichment completes, eliminating the need for clients to poll.
- **Database connection resilience** — the current `pgxpool` handles reconnects, but I'd add explicit health checks and retry logic around store operations for the case where PostgreSQL itself becomes temporarily unavailable.
- **Source-level retry configuration** — currently all sources use the same retry config. Property Records (with 8s slow responses) could benefit from a longer initial delay, while SCRA polling might want shorter intervals.

## Nice-to-Haves Chosen

1. **Concurrent Enrichment** — sources within a case run in parallel via `errgroup`. This was a natural fit because the sources are independent; the only coordination point is the final status computation, which reads from the database after all goroutines finish.

2. **Bulk Enrichment** — `POST /api/enrich/bulk` with a 3-worker pool. Demonstrates controlled concurrency across cases while respecting the global court records rate limit. Returns 202 immediately with per-case status.

3. **Containerized Deployment** — `docker-compose.yml` with PostgreSQL, mock services, the backend, and Adminer. A single `./scripts/up.sh` builds and starts everything. Integration tests run inside Docker against the live stack.
