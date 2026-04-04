#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# log-navigator.sh — rich terminal-based log viewer for Integration Gateway
#
# Reads log files directly from the host-mounted ./logs directory.
# No need to docker exec into containers.
#
# Usage:
#   ./scripts/log-navigator.sh              # interactive menu
#   ./scripts/log-navigator.sh app          # view app.log
#   ./scripts/log-navigator.sh server       # view server.log
#   ./scripts/log-navigator.sh error        # view error.log
#   ./scripts/log-navigator.sh all          # merged view, sorted by time
#   ./scripts/log-navigator.sh follow [log] # tail -f (default: all)
#   ./scripts/log-navigator.sh search TERM  # grep across all logs
#   ./scripts/log-navigator.sh errors       # ERROR entries only
#   ./scripts/log-navigator.sh stats        # log statistics
#   ./scripts/log-navigator.sh clean        # truncate all log files
# ---------------------------------------------------------------------------
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
LOG_DIR="${PROJECT_ROOT}/logs"

# ── colours ──────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
DIM='\033[2m'
MAGENTA='\033[0;35m'
NC='\033[0m'

# ── helpers ──────────────────────────────────────────────────────────────

usage() {
    head -18 "$0" | tail -14 | sed 's/^# \?//'
    exit 0
}

die() { echo -e "${RED}error:${NC} $*" >&2; exit 1; }

ensure_log_dir() {
    mkdir -p "$LOG_DIR"
    # Touch files so they exist even if the service hasn't written yet
    touch "$LOG_DIR"/{app,server,error}.log 2>/dev/null || true
}

log_file() {
    local name="${1:-all}"
    case "$name" in
        app)    echo "$LOG_DIR/app.log" ;;
        server) echo "$LOG_DIR/server.log" ;;
        error)  echo "$LOG_DIR/error.log" ;;
        all)    echo "$LOG_DIR" ;;
        *)      die "unknown log: $name (use app|server|error|all)" ;;
    esac
}

# Colourise a single JSON log line based on level.
colourise() {
    while IFS= read -r line; do
        if echo "$line" | grep -q '"level":"ERROR"' 2>/dev/null; then
            echo -e "${RED}${line}${NC}"
        elif echo "$line" | grep -q '"level":"WARN"' 2>/dev/null; then
            echo -e "${YELLOW}${line}${NC}"
        elif echo "$line" | grep -q '"level":"INFO"' 2>/dev/null; then
            echo -e "${GREEN}${line}${NC}"
        elif echo "$line" | grep -q '"level":"DEBUG"' 2>/dev/null; then
            echo -e "${DIM}${line}${NC}"
        else
            echo "$line"
        fi
    done
}

# Also colourise slog text-format lines.
colourise_text() {
    while IFS= read -r line; do
        if echo "$line" | grep -qE 'level=ERROR'; then
            echo -e "${RED}${line}${NC}"
        elif echo "$line" | grep -qE 'level=WARN'; then
            echo -e "${YELLOW}${line}${NC}"
        elif echo "$line" | grep -qE 'level=INFO'; then
            echo -e "${GREEN}${line}${NC}"
        elif echo "$line" | grep -qE 'level=DEBUG'; then
            echo -e "${DIM}${line}${NC}"
        else
            echo "$line"
        fi
    done
}

# Pretty-print JSON log line into a readable single line.
pretty_line() {
    while IFS= read -r line; do
        # Attempt to parse as JSON; fall back to raw line
        if echo "$line" | python3 -c 'import sys,json; json.loads(sys.stdin.read())' 2>/dev/null; then
            local ts level msg comp rest
            ts=$(echo "$line" | python3 -c "import sys,json; d=json.loads(sys.stdin.read()); print(d.get('time',''))" 2>/dev/null)
            level=$(echo "$line" | python3 -c "import sys,json; d=json.loads(sys.stdin.read()); print(d.get('level',''))" 2>/dev/null)
            msg=$(echo "$line" | python3 -c "import sys,json; d=json.loads(sys.stdin.read()); print(d.get('msg',''))" 2>/dev/null)
            comp=$(echo "$line" | python3 -c "import sys,json; d=json.loads(sys.stdin.read()); print(d.get('component',''))" 2>/dev/null)
            rest=$(echo "$line" | python3 -c "
import sys, json
d = json.loads(sys.stdin.read())
for k in ('time','level','msg','component'):
    d.pop(k, None)
print(' '.join(f'{k}={v}' for k,v in d.items()))" 2>/dev/null)

            local colour="$GREEN"
            case "$level" in
                ERROR)  colour="$RED" ;;
                WARN)   colour="$YELLOW" ;;
                DEBUG)  colour="$DIM" ;;
            esac

            echo -e "${DIM}${ts}${NC} ${colour}${level}${NC} ${CYAN}[${comp}]${NC} ${BOLD}${msg}${NC} ${DIM}${rest}${NC}"
        else
            echo "$line"
        fi
    done
}

# ── commands ─────────────────────────────────────────────────────────────

cmd_view() {
    local target="${1:-all}"
    if [[ "$target" == "all" ]]; then
        cat "$LOG_DIR"/*.log | sort -t'"' -k4 | pretty_line
    else
        local f
        f=$(log_file "$target")
        pretty_line < "$f"
    fi
}

cmd_follow() {
    local target="${1:-all}"
    echo -e "${BOLD}Following logs (${target}) — Ctrl+C to stop${NC}"
    if [[ "$target" == "all" ]]; then
        # tail -F follows file creation + rotation
        tail -F "$LOG_DIR"/{app,server,error}.log 2>/dev/null | colourise_text
    else
        local f
        f=$(log_file "$target")
        tail -F "$f" | colourise_text
    fi
}

cmd_search() {
    local pattern="${1:?usage: log-navigator.sh search TERM}"
    echo -e "${BOLD}Searching all logs for: ${CYAN}${pattern}${NC}"
    echo -e "${DIM}─────────────────────────────────────────────${NC}"
    grep -n "$pattern" "$LOG_DIR"/*.log | colourise_text || echo -e "${DIM}(no matches)${NC}"
}

cmd_errors() {
    echo -e "${BOLD}${RED}Error entries across all logs${NC}"
    echo -e "${DIM}─────────────────────────────────────────────${NC}"
    grep '"level":"ERROR"' "$LOG_DIR"/*.log | pretty_line || echo -e "${DIM}(no errors found)${NC}"
}

cmd_stats() {
    echo -e "${BOLD}Log Statistics${NC}"
    echo -e "${DIM}─────────────────────────────────────────────${NC}"
    for f in "$LOG_DIR"/*.log; do
        [[ -f "$f" ]] || continue
        local name
        name=$(basename "$f")
        local lines errors infos warns size
        lines=$(wc -l < "$f")
        errors=$(grep -c '"level":"ERROR"' "$f" 2>/dev/null) || true
        infos=$(grep -c '"level":"INFO"' "$f" 2>/dev/null) || true
        warns=$(grep -c '"level":"WARN"' "$f" 2>/dev/null) || true
        errors=${errors:-0} infos=${infos:-0} warns=${warns:-0}
        size=$(du -h "$f" | cut -f1)
        printf "  %-12s %5s entries  " "$name" "$lines"
        echo -e "${GREEN}${infos} info${NC}  ${YELLOW}${warns} warn${NC}  ${RED}${errors} error${NC}  (${size})"
    done
    echo -e "${DIM}─────────────────────────────────────────────${NC}"
    echo -e "  ${DIM}Directory: ${LOG_DIR}${NC}"
}

cmd_clean() {
    echo -e "${YELLOW}Truncating all log files in ${LOG_DIR}${NC}"
    for f in "$LOG_DIR"/*.log; do
        [[ -f "$f" ]] || continue
        : > "$f"
        echo -e "  ${GREEN}cleared $(basename "$f")${NC}"
    done
}

cmd_menu() {
    echo -e "${BOLD}${CYAN}Integration Gateway — Log Navigator${NC}"
    echo -e "${DIM}─────────────────────────────────────────────${NC}"
    echo ""
    echo -e "  ${BOLD}Viewing${NC}"
    echo -e "    ${GREEN}app${NC}       View app.log       (startup, migrations, config)"
    echo -e "    ${GREEN}server${NC}    View server.log     (HTTP requests, responses)"
    echo -e "    ${GREEN}error${NC}     View error.log      (errors only)"
    echo -e "    ${GREEN}all${NC}       Merged view, sorted by timestamp"
    echo ""
    echo -e "  ${BOLD}Live${NC}"
    echo -e "    ${GREEN}follow${NC}    Tail -f all logs (or: follow app/server/error)"
    echo ""
    echo -e "  ${BOLD}Search & Filter${NC}"
    echo -e "    ${GREEN}search${NC} X  Grep for X across all logs"
    echo -e "    ${GREEN}errors${NC}    Show ERROR entries from all logs"
    echo ""
    echo -e "  ${BOLD}Operations${NC}"
    echo -e "    ${GREEN}stats${NC}     Log statistics (entry counts, file sizes)"
    echo -e "    ${GREEN}clean${NC}     Truncate all log files"
    echo ""
    echo -e "${DIM}Directory: ${LOG_DIR}${NC}"
}

# ── main ─────────────────────────────────────────────────────────────────

ensure_log_dir

case "${1:-menu}" in
    app|server|error|all)  cmd_view "$1" ;;
    follow)                cmd_follow "${2:-all}" ;;
    search)                cmd_search "${2:?usage: log-navigator.sh search TERM}" ;;
    errors)                cmd_errors ;;
    stats)                 cmd_stats ;;
    clean)                 cmd_clean ;;
    menu|--help|-h)        cmd_menu ;;
    *)                     die "unknown command: $1\nRun: $(basename "$0") --help" ;;
esac
