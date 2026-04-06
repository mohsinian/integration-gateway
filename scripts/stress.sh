#!/usr/bin/env bash
set -euo pipefail

# ──────────────────────────────────────────────
# Integration Gateway — Stress Test
#
# Repeatedly enriches cases to exercise retries,
# circuit breakers, partial recovery, and
# idempotency under the mock services' random
# failure rates.
#
# Each round resets enrichment state so the
# external services are hit fresh every time.
#
# Usage:
#   ./scripts/stress.sh              # default: 5 rounds
#   ./scripts/stress.sh 10           # 10 rounds
#   ./scripts/stress.sh --watch      # continuous mode (Ctrl+C to stop)
# ──────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BASE_URL="${BASE_URL:-http://localhost:8080}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
DIM='\033[2m'
NC='\033[0m'

CASES=("case-001" "case-002" "case-003" "case-004" "case-005" "case-006")
ROUNDS=5
WATCH=false

for arg in "$@"; do
    case "$arg" in
        --watch) WATCH=true ;;
        *[0-9])  ROUNDS=$arg ;;
    esac
done

die() { echo -e "${RED}error:${NC} $*" >&2; exit 1; }

curl -sf "$BASE_URL/api/health" >/dev/null 2>&1 || die "Backend not reachable at $BASE_URL. Run scripts/up.sh first."

# Find the postgres container
PG_CONTAINER="$(docker ps --filter "ancestor=postgres:16-alpine" --format "{{.Names}}" | head -1)" || true
[ -n "$PG_CONTAINER" ] || die "PostgreSQL container not found. Run scripts/up.sh first."

# Load DB credentials from .env
ENV_FILE="$PROJECT_ROOT/.env"
DB_USER="gateway"
DB_NAME="integration_gateway"
if [ -f "$ENV_FILE" ]; then
    DB_USER="$(grep POSTGRES_USER "$ENV_FILE" | cut -d= -f2 | tr -d ' ')"
    DB_NAME="$(grep POSTGRES_DB "$ENV_FILE" | cut -d= -f2 | tr -d ' ')"
fi
DB_USER="${DB_USER:-gateway}"
DB_NAME="${DB_NAME:-integration_gateway}"

echo ""
echo -e "${BOLD}${CYAN}━━━ Integration Gateway — Stress Test ━━━${NC}"
echo -e "${DIM}Rounds: $([ "$WATCH" = true ] && echo "continuous" || echo "$ROUNDS")${NC}"
echo ""

# ── Helpers ─────────────────────────────────────────────────────────────────

idempotency_ok=0
idempotency_fail=0

# Reset enrichment state: truncate lookup tables and restart
# the backend to reset in-memory circuit breaker state.
# Cases table stays intact — only enrichment results are cleared.
BACKEND_CONTAINER="$(docker ps --filter "name=backend" --format "{{.Names}}" | head -1)" || true

reset_enrichment() {
    docker exec "$PG_CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -c \
        "TRUNCATE lookup_sources, lookup_runs CASCADE;" >/dev/null 2>&1
    if [ -n "$BACKEND_CONTAINER" ]; then
        docker restart "$BACKEND_CONTAINER" >/dev/null 2>&1
        # Wait for backend to become healthy again
        for i in $(seq 1 15); do
            curl -sf "$BASE_URL/api/health" >/dev/null 2>&1 && break
            sleep 1
        done
    fi
}

check_idempotency() {
    local case_id="$1"
    local resp
    resp=$(curl -sf "$BASE_URL/api/cases/$case_id/enrichment" 2>/dev/null) || return
    local run_id
    run_id=$(echo "$resp" | python3 -c "import sys,json; print(json.loads(sys.stdin.read()).get('runId',''))" 2>/dev/null) || return

    # Re-trigger enrichment
    local re_resp
    re_resp=$(curl -s -X POST "$BASE_URL/api/cases/$case_id/enrich" 2>/dev/null) || return
    local re_run_id
    re_run_id=$(echo "$re_resp" | python3 -c "import sys,json; print(json.loads(sys.stdin.read()).get('runId',''))" 2>/dev/null) || return

    if [ "$run_id" = "$re_run_id" ]; then
        ((idempotency_ok++)) || true
    else
        ((idempotency_fail++)) || true
        echo -e "  ${RED}IDEMPOTENCY VIOLATION: $case_id run changed from $run_id to $re_run_id${NC}"
    fi
}

print_cb_state() {
    curl -sf "$BASE_URL/api/health" 2>/dev/null | python3 -c "
import sys, json
d = json.loads(sys.stdin.read())
cbs = d.get('circuitBreakers', {})
for cb_name, info in cbs.items():
    state = info.get('state', '?')
    fails = info.get('failures', 0)
    if state == 'open':
        colour = '\033[0;31m'
    elif state == 'half-open':
        colour = '\033[1;33m'
    else:
        colour = '\033[0;32m'
    print(f'  {cb_name}: {colour}{state}\033[0m (failures={fails})')
" 2>/dev/null || true
}

print_case_result() {
    local case_id="$1"
    local resp
    resp=$(curl -sf "$BASE_URL/api/cases/$case_id/enrichment" 2>/dev/null) || return
    echo "$resp" | python3 -c "
import sys, json
d = json.loads(sys.stdin.read())
overall = d.get('status', '?')
sources = d.get('sources', {})
parts = []
for src_name, info in sources.items():
    s = info.get('status', '?')
    a = info.get('attempts', 0)
    if s == 'failed':
        colour = '\033[0;31m'
    elif s == 'success':
        colour = '\033[0;32m'
    else:
        colour = '\033[0m'
    parts.append(f'{colour}{src_name}:{s}({a})\033[0m')
print(f'    $case_id  {overall}  {\"  \".join(parts)}')
" 2>/dev/null || true
}

# ── Main loop ───────────────────────────────────────────────────────────────

round=0
while true; do
    round=$((round + 1))

    if [ "$WATCH" = false ] && [ "$round" -gt "$ROUNDS" ]; then
        break
    fi

    echo -e "${BOLD}${CYAN}── Round $round ──${NC}"

    # Reset enrichment state for a fresh run
    reset_enrichment
    echo -e "${DIM}  Cleared enrichment state${NC}"

    # Trigger bulk enrichment
    curl -sf -X POST "$BASE_URL/api/enrich/bulk" \
        -H "Content-Type: application/json" \
        -d "{\"caseIds\":$(printf '%s\n' "${CASES[@]}" | python3 -c 'import sys,json; print(json.dumps([l.strip() for l in sys.stdin]))' 2>/dev/null || echo '[]')}" >/dev/null 2>&1 || true

    # Poll until all cases reach a terminal state (complete, partial, or failed)
    echo -e "${DIM}  Waiting for enrichment to settle...${NC}"
    max_wait=30
    waited=0
    while [ "$waited" -lt "$max_wait" ]; do
        all_done=true
        for case_id in "${CASES[@]}"; do
            status=$(curl -sf "$BASE_URL/api/cases/$case_id/enrichment" 2>/dev/null \
                | python3 -c "import sys,json; print(json.loads(sys.stdin.read()).get('status','pending'))" 2>/dev/null || echo "pending")
            if [ "$status" = "pending" ]; then
                all_done=false
                break
            fi
        done
        if [ "$all_done" = true ]; then
            echo -e "${DIM}  All cases settled after ${waited}s${NC}"
            break
        fi
        sleep 2
        waited=$((waited + 2))
    done
    if [ "$waited" -ge "$max_wait" ]; then
        echo -e "${YELLOW}  Timeout (${max_wait}s) — some cases still pending${NC}"
    fi

    # Check results
    echo -e "${BOLD}  Results:${NC}"
    for case_id in "${CASES[@]}"; do
        print_case_result "$case_id"
    done

    # Circuit breaker states
    echo -e "${BOLD}  Circuit breakers:${NC}"
    print_cb_state

    # Idempotency spot check (random case)
    rand_case="${CASES[$((RANDOM % ${#CASES[@]}))]}"
    check_idempotency "$rand_case"

    echo ""
done

# ── Summary ─────────────────────────────────────────────────────────────────

echo -e "${BOLD}${CYAN}━━━ Summary ━━━${NC}"
echo ""
echo -e "  Rounds completed: ${BOLD}$([ "$WATCH" = true ] && echo "$round (watch mode)" || echo "$ROUNDS")${NC}"
echo -e "  Idempotency checks: ${GREEN}$idempotency_ok passed${NC}${idempotency_fail:+, ${RED}$idempotency_fail FAILED${NC}}"
echo ""

echo -e "${BOLD}Final circuit breaker states:${NC}"
print_cb_state
echo ""

echo -e "${GREEN}${BOLD}Stress test complete.${NC}"
