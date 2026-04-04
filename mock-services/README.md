# Mock External Services

Simulates three external APIs for the Backend Engineer take-home assignment.

## Running

```bash
go run main.go
```

Starts three services:

| Service | Port | Description |
|---|---|---|
| Property Records | 9001 | REST/JSON. Intermittent 503s, occasional slow responses. |
| Court Records | 9002 | JSON request → XML response. Rate-limited. Occasional malformed XML. |
| SCRA Check | 9003 | Async: submit search (202) → poll for results (200 when ready). |

## Environment Variables

All optional. Defaults match the evaluation settings.

| Variable | Default | Description |
|---|---|---|
| `PROPERTY_FAIL_RATE` | `0.25` | Probability of 503 response (0.0–1.0) |
| `PROPERTY_SLOW_RATE` | `0.10` | Probability of 8-second delay on success (0.0–1.0) |
| `COURT_RATE_LIMIT` | `2` | Maximum requests per second before 429 |
| `COURT_CORRUPT_RATE` | `0.03` | Probability of truncated/malformed XML (0.0–1.0) |
| `SCRA_FAIL_RATE` | `0.10` | Probability of permanent search failure (0.0–1.0) |
| `SCRA_MIN_DELAY` | `2` | Minimum seconds before SCRA result is ready |
| `SCRA_MAX_DELAY` | `5` | Maximum seconds before SCRA result is ready |

### Disabling failures for development

```bash
PROPERTY_FAIL_RATE=0 PROPERTY_SLOW_RATE=0 COURT_CORRUPT_RATE=0 SCRA_FAIL_RATE=0 go run main.go
```

This runs all services with 100% success rate — useful for initial development. Switch back to defaults before final testing.
