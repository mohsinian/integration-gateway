#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BACKEND_DIR="$PROJECT_ROOT/backend"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

usage() {
    echo -e "${CYAN}Usage:${NC} $(basename "$0") [target] [options]"
    echo ""
    echo -e "${CYAN}Targets:${NC}"
    echo "  all          Run all unit test suites (default)"
    echo "  resilience   Retry, circuit breaker, rate limiter tests"
    echo "  client       Property, court, SCRA client tests"
    echo "  integration  Run integration tests via Docker (requires docker compose up)"
    echo ""
    echo -e "${CYAN}Options:${NC}"
    echo "  -v, --verbose    Verbose output"
    echo "  -r, --run NAME   Run only tests matching NAME (regex)"
    echo "  -h, --help       Show this help"
    echo ""
    echo -e "${CYAN}Examples:${NC}"
    echo "  $(basename "$0")                           # run all unit tests"
    echo "  $(basename "$0") resilience                # resilience layer only"
    echo "  $(basename "$0") client -v                 # client tests, verbose"
    echo "  $(basename "$0") integration               # integration tests via Docker"
    echo "  $(basename "$0") integration -run Bulk     # only Bulk* integration tests"
}

# Resolve which package(s) to test.
resolve_packages() {
    local target="$1"
    case "$target" in
        all)
            echo "./internal/resilience/" "./internal/client/"
            ;;
        resilience)
            echo "./internal/resilience/"
            ;;
        client)
            echo "./internal/client/"
            ;;
        integration)
            echo "integration"
            ;;
        *)
            echo -e "${RED}Unknown target: $target${NC}" >&2
            usage
            exit 1
            ;;
    esac
}

# ── Parse args ──────────────────────────────────────────────────────────

TARGET="all"
VERBOSE=""
RUN_FLAG=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help)
            usage
            exit 0
            ;;
        -v|--verbose)
            VERBOSE="-v"
            shift
            ;;
        -r|--run|-run)
            if [[ $# -lt 2 ]]; then
                echo -e "${RED}--run requires a pattern argument${NC}" >&2
                exit 1
            fi
            RUN_FLAG="-run $2"
            shift 2
            ;;
        resilience|client|integration|all)
            TARGET="$1"
            shift
            ;;
        *)
            echo -e "${RED}Unknown argument: $1${NC}" >&2
            usage
            exit 1
            ;;
    esac
done

# ── Run tests ───────────────────────────────────────────────────────────

cd "$BACKEND_DIR"

# ── Integration tests (via Docker) ─────────────────────────────────────────

if [[ "$TARGET" == "integration" ]]; then
    echo -e "${CYAN}── Running integration tests via Docker ──${NC}"
    echo ""

    # Verify Docker services are up.
    if ! docker compose -f "$PROJECT_ROOT/docker-compose.yml" ps --status running -q 2>/dev/null | grep -q .; then
        echo -e "${RED}Docker services are not running. Run scripts/up.sh first.${NC}" >&2
        exit 1
    fi

    docker compose -f "$PROJECT_ROOT/docker-compose.yml" \
        run --rm test \
        go test -tags=integration $VERBOSE $RUN_FLAG -timeout 150s ./internal/lookup/

    echo ""
    echo -e "${GREEN}Integration tests passed.${NC}"
    exit 0
fi

# ── Unit tests ─────────────────────────────────────────────────────────────

PACKAGES=($(resolve_packages "$TARGET"))

# Filter to packages that have unit test files (exclude integration-only).
EXISTING=()
for pkg in "${PACKAGES[@]}"; do
    for f in "$pkg"*_test.go; do
        if [[ -f "$f" ]] && ! grep -q '//go:build integration' "$f"; then
            EXISTING+=("$pkg")
            break
        fi
    done
done

if [[ ${#EXISTING[@]} -eq 0 ]]; then
    echo -e "${YELLOW}No test files found for target '$TARGET'${NC}"
    exit 0
fi

echo -e "${CYAN}── Running tests: $TARGET ──${NC}"
echo ""

FAIL=0
for pkg in "${EXISTING[@]}"; do
    echo -e "${CYAN}[ $pkg ]${NC}"
    if ! go test "$pkg" -count=1 $VERBOSE $RUN_FLAG; then
        FAIL=1
    fi
    echo ""
done

if [[ $FAIL -eq 0 ]]; then
    echo -e "${GREEN}All tests passed.${NC}"
else
    echo -e "${RED}Some tests failed.${NC}"
    exit 1
fi
