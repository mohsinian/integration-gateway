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
    echo "  all         Run all test suites (default)"
    echo "  resilience  Retry, circuit breaker, rate limiter tests"
    echo "  client      Property, court, SCRA client tests"
    echo "  store       Database / store tests"
    echo "  lookup      Orchestrator tests"
    echo "  api         HTTP handler tests"
    echo ""
    echo -e "${CYAN}Options:${NC}"
    echo "  -v, --verbose    Verbose output"
    echo "  -r, --run NAME   Run only tests matching NAME (regex)"
    echo "  -h, --help       Show this help"
    echo ""
    echo -e "${CYAN}Examples:${NC}"
    echo "  $(basename "$0")                    # run all tests"
    echo "  $(basename "$0") resilience         # resilience layer only"
    echo "  $(basename "$0") client -v          # client tests, verbose"
    echo "  $(basename "$0") all -run TestDo_   # only TestDo_* across all packages"
}

# Resolve which package(s) to test.
resolve_packages() {
    local target="$1"
    case "$target" in
        all)
            echo "./internal/resilience/" "./internal/client/" "./internal/store/" "./internal/lookup/" "./internal/api/"
            ;;
        resilience)
            echo "./internal/resilience/"
            ;;
        client)
            echo "./internal/client/"
            ;;
        store)
            echo "./internal/store/"
            ;;
        lookup)
            echo "./internal/lookup/"
            ;;
        api)
            echo "./internal/api/"
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
        resilience|client|store|lookup|api|all)
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

PACKAGES=($(resolve_packages "$TARGET"))

# Filter to packages that actually exist.
EXISTING=()
for pkg in "${PACKAGES[@]}"; do
    if ls "$pkg"*_test.go &>/dev/null; then
        EXISTING+=("$pkg")
    fi
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
