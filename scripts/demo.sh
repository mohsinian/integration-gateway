#!/usr/bin/env bash
set -euo pipefail

# ──────────────────────────────────────────────
# Integration Gateway — Demo Script
#
# Runs the full enrichment flow against a live
# stack and prints results. Assumes the stack
# is already running (scripts/up.sh).
#
# Usage:
#   ./scripts/demo.sh
#   ./scripts/demo.sh --skip-seed   # skip bulk enrich, just check status
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

SKIP_SEED=false
for arg in "$@"; do
    case "$arg" in
        --skip-seed) SKIP_SEED=true ;;
    esac
done

die() { echo -e "${RED}error:${NC} $*" >&2; exit 1; }

# Check the stack is up
health=$(curl -sf "$BASE_URL/api/health" 2>/dev/null) || die "Backend not reachable at $BASE_URL. Run scripts/up.sh first."

echo ""
echo -e "${BOLD}${CYAN}━━━ Integration Gateway — Demo ━━━${NC}"
echo -e "${DIM}Base URL: $BASE_URL${NC}"
echo ""

# ── Health ──────────────────────────────────────────────────────────────────

echo -e "${BOLD}1. Health check${NC}"
echo -e "${DIM}─────────────────────────────────────────────${NC}"
echo "$health" | python3 -m json.tool 2>/dev/null || echo "$health"
echo ""

# ── List cases (before enrichment) ─────────────────────────────────────────

echo -e "${BOLD}2. Cases before enrichment${NC}"
echo -e "${DIM}─────────────────────────────────────────────${NC}"
cases=$(curl -sf "$BASE_URL/api/cases")
echo "$cases" | python3 -c "
import sys, json
cases = json.loads(sys.stdin.read())
for c in cases:
    status = c.get('enrichmentStatus', 'none')
    name = c.get('borrower', {}).get('lastName', '?')
    print(f\"  {c['id']}  {name:<12}  stage={c.get('currentStage','?'):<15}  enrichment={status}\")
" 2>/dev/null || echo "$cases"
echo ""

# ── Enrich single case ─────────────────────────────────────────────────────

if [ "$SKIP_SEED" = false ]; then
    echo -e "${BOLD}3. Enrich case-001 (single case)${NC}"
    echo -e "${DIM}─────────────────────────────────────────────${NC}"
    result=$(curl -s -w "\n%{http_code}" -X POST "$BASE_URL/api/cases/case-001/enrich")
    code=$(echo "$result" | tail -1)
    body=$(echo "$result" | sed '$d')
    echo -e "  HTTP ${code}"
    echo "$body" | python3 -m json.tool 2>/dev/null || echo "$body"
    echo ""

    echo -e "${DIM}  Waiting 8 seconds for async enrichment...${NC}"
    sleep 8

    echo -e "${BOLD}4. Check case-001 enrichment result${NC}"
    echo -e "${DIM}─────────────────────────────────────────────${NC}"
    enrichment=$(curl -sf "$BASE_URL/api/cases/case-001/enrichment")
    echo "$enrichment" | python3 -m json.tool 2>/dev/null || echo "$enrichment"
    echo ""

    # ── Bulk enrich all cases ───────────────────────────────────────────────

    echo -e "${BOLD}5. Bulk enrich all 6 cases${NC}"
    echo -e "${DIM}─────────────────────────────────────────────${NC}"
    bulk=$(curl -s -w "\n%{http_code}" -X POST "$BASE_URL/api/enrich/bulk" \
        -H "Content-Type: application/json" \
        -d '{"caseIds":["case-001","case-002","case-003","case-004","case-005","case-006"]}')
    code=$(echo "$bulk" | tail -1)
    body=$(echo "$bulk" | sed '$d')
    echo -e "  HTTP ${code}"
    echo "$body" | python3 -m json.tool 2>/dev/null || echo "$body"
    echo ""

    echo -e "${DIM}  Waiting 12 seconds for async enrichment...${NC}"
    sleep 12
fi

# ── Final status for all cases ─────────────────────────────────────────────

echo -e "${BOLD}$([ "$SKIP_SEED" = false ] && echo "6" || echo "3"). Final enrichment status (all cases)${NC}"
echo -e "${DIM}─────────────────────────────────────────────${NC}"
for id in case-001 case-002 case-003 case-004 case-005 case-006; do
    resp=$(curl -sf "$BASE_URL/api/cases/$id/enrichment" 2>/dev/null) || continue
    python3 -c "
import sys, json
d = json.loads('''$resp''')
overall = d.get('status', '?')
sources = d.get('sources', {})
parts = []
for name, info in sources.items():
    s = info.get('status', '?')
    attempts = info.get('attempts', 0)
    label = f'{s}' + (f' ({attempts} attempts)' if attempts > 1 else '')
    if s == 'failed':
        err = info.get('error', '')
        label += f' [{err[:40]}]' if err else ''
    elif s == 'not_applicable':
        label += f' ({info.get(\"reason\", \"\")[:30]})'
    parts.append(f'{name}={label}')
print(f'  $id  overall={overall}')
for p in parts:
    print(f'    {p}')
" 2>/dev/null || echo "  $id  (could not parse)"
done
echo ""

# ── Final health ────────────────────────────────────────────────────────────

echo -e "${BOLD}$([ "$SKIP_SEED" = false ] && echo "7" || echo "4"). Final health check${NC}"
echo -e "${DIM}─────────────────────────────────────────────${NC}"
curl -sf "$BASE_URL/api/health" | python3 -m json.tool 2>/dev/null
echo ""

echo -e "${GREEN}${BOLD}Done.${NC}"
