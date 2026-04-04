#!/usr/bin/env bash
set -euo pipefail

# ──────────────────────────────────────────────
# Integration Gateway — Migration CLI
# ──────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
MIGRATIONS_DIR="$PROJECT_DIR/backend/migrations"
ENV_FILE="$PROJECT_DIR/.env"

# ── Load .env ──────────────────────────────────
load_env() {
    if [[ -f "$ENV_FILE" ]]; then
        set -a
        source "$ENV_FILE"
        set +a
    fi
}

# ── Build psql connection string ───────────────
db_url() {
    local host="${DB_HOST:-localhost}"
    local port="${DB_PORT:-5432}"
    local user="${POSTGRES_USER:-gateway}"
    local pass="${POSTGRES_PASSWORD:-gateway}"
    local name="${POSTGRES_DB:-integration_gateway}"
    echo "postgresql://${user}:${pass}@${host}:${port}/${name}"
}

# ── Run SQL via psql (docker exec or local) ────
run_sql() {
    local sql="$1"
    # Try docker exec first (if postgres container is running)
    local container
    container="$(docker ps --filter "ancestor=postgres:16-alpine" --format "{{.Names}}" | head -1)" || true
    if [[ -n "$container" ]]; then
        docker exec "$container" psql -U "${POSTGRES_USER:-gateway}" -d "${POSTGRES_DB:-integration_gateway}" -c "$sql"
    elif command -v psql &>/dev/null; then
        psql "$(db_url)" -c "$sql"
    else
        echo "Error: No psql available. Start the docker stack first."
        exit 1
    fi
}

# ── Run SQL file via psql ──────────────────────
run_sql_file() {
    local filepath="$1"
    local filename
    filename="$(basename "$filepath")"
    local container
    container="$(docker ps --filter "ancestor=postgres:16-alpine" --format "{{.Names}}" | head -1)" || true

    if [[ -n "$container" ]]; then
        docker exec -i "$container" psql -U "${POSTGRES_USER:-gateway}" -d "${POSTGRES_DB:-integration_gateway}" < "$filepath"
    elif command -v psql &>/dev/null; then
        psql "$(db_url)" -f "$filepath"
    else
        echo "Error: No psql available. Start the docker stack first."
        exit 1
    fi
}

# ── Ensure schema_version table exists ─────────
ensure_tracking() {
    run_sql "
        CREATE TABLE IF NOT EXISTS schema_version (
            id            SERIAL PRIMARY KEY,
            filename      TEXT NOT NULL UNIQUE,
            status        TEXT NOT NULL,
            error_message TEXT,
            applied_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
        );
    " > /dev/null 2>&1
}

# ── List available migration files ─────────────
list_files() {
    if [[ ! -d "$MIGRATIONS_DIR" ]]; then
        echo "Error: migrations directory not found at $MIGRATIONS_DIR"
        exit 1
    fi
    ls -1 "$MIGRATIONS_DIR"/*.sql 2>/dev/null | sort
}

# ── Menu helpers ───────────────────────────────
BOLD='\033[1m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
RED='\033[0;31m'
CYAN='\033[0;36m'
RESET='\033[0m'

print_header() {
    echo ""
    echo -e "${BOLD}━━━ Integration Gateway — Migration CLI ━━━${RESET}"
    echo ""
}

print_status() {
    ensure_tracking
    echo -e "${BOLD}Migration Status:${RESET}"
    echo ""
    run_sql "SELECT filename, status, error_message, applied_at FROM schema_version ORDER BY id;" 2>/dev/null
    echo ""
    echo -e "${CYAN}Available migration files:${RESET}"
    for f in $(list_files); do
        echo "  $(basename "$f")"
    done
    echo ""
}

# ── Run all pending migrations ─────────────────
run_all() {
    ensure_tracking
    local applied=0
    local skipped=0

    for f in $(list_files); do
        local fn
        fn="$(basename "$f")"
        # Check if already applied
        local count
        count="$(run_sql "SELECT COUNT(*) FROM schema_version WHERE filename='$fn' AND status='success';" 2>/dev/null | tail -3 | head -1 | tr -d ' ')" || true
        if [[ "$count" == "1" ]]; then
            echo -e "  ${YELLOW}SKIP${RESET} $fn (already applied)"
            ((skipped++)) || true
            continue
        fi

        echo -ne "  ${CYAN}RUN${RESET}  $fn ... "
        local output
        if output="$(run_sql_file "$f" 2>&1)"; then
            run_sql "INSERT INTO schema_version (filename, status) VALUES ('$fn', 'success') ON CONFLICT (filename) DO UPDATE SET status='success', error_message=NULL, applied_at=NOW();" > /dev/null 2>&1
            echo -e "${GREEN}OK${RESET}"
            ((applied++)) || true
        else
            local errMsg
            errMsg="$(echo "$output" | head -3 | tr "'" "''" | tr '\n' ' ')"
            run_sql "INSERT INTO schema_version (filename, status, error_message) VALUES ('$fn', 'failed', '\${errMsg}') ON CONFLICT (filename) DO UPDATE SET status='failed', error_message='\${errMsg}', applied_at=NOW();" > /dev/null 2>&1 || true
            echo -e "${RED}FAILED${RESET}"
            echo "  Error: $output"
            echo ""
            echo -e "${RED}Halting. Fix the issue and re-run.${RESET}"
            return 1
        fi
    done

    echo ""
    if [[ $applied -eq 0 && $skipped -gt 0 ]]; then
        echo -e "${GREEN}All migrations already applied. Nothing to do.${RESET}"
    elif [[ $applied -gt 0 ]]; then
        echo -e "${GREEN}Applied $applied migration(s), skipped $skipped.${RESET}"
    else
        echo -e "${YELLOW}No migration files found.${RESET}"
    fi
}

# ── Run a specific migration ───────────────────
run_specific() {
    ensure_tracking
    local files=()
    while IFS= read -r f; do
        files+=("$(basename "$f")")
    done < <(list_files)

    if [[ ${#files[@]} -eq 0 ]]; then
        echo "No migration files found."
        return
    fi

    echo -e "${BOLD}Select a migration to run:${RESET}"
    echo ""
    for i in "${!files[@]}"; do
        local fn="${files[$i]}"
        local count
        count="$(run_sql "SELECT COUNT(*) FROM schema_version WHERE filename='$fn' AND status='success';" 2>/dev/null | tail -3 | head -1 | tr -d ' ')" || true
        if [[ "$count" == "1" ]]; then
            echo -e "  $((i+1)). ${GREEN}${fn}${RESET} (already applied)"
        else
            echo -e "  $((i+1)). ${fn}"
        fi
    done
    echo ""
    echo -n "Enter number (or q to cancel): "
    read -r choice

    if [[ "$choice" == "q" || "$choice" == "Q" ]]; then
        echo "Cancelled."
        return
    fi

    if ! [[ "$choice" =~ ^[0-9]+$ ]] || [[ "$choice" -lt 1 ]] || [[ "$choice" -gt ${#files[@]} ]]; then
        echo "Invalid selection."
        return
    fi

    local fn="${files[$((choice-1))]}"
    local filepath="$MIGRATIONS_DIR/$fn"

    echo -ne "Running $fn ... "
    local output
    if output="$(run_sql_file "$filepath" 2>&1)"; then
        run_sql "INSERT INTO schema_version (filename, status) VALUES ('$fn', 'success') ON CONFLICT (filename) DO UPDATE SET status='success', error_message=NULL, applied_at=NOW();" > /dev/null 2>&1
        echo -e "${GREEN}OK${RESET}"
    else
        local errMsg
        errMsg="$(echo "$output" | head -3 | tr "'" "''" | tr '\n' ' ')"
        run_sql "INSERT INTO schema_version (filename, status, error_message) VALUES ('$fn', 'failed', '\${errMsg}') ON CONFLICT (filename) DO UPDATE SET status='failed', error_message='\${errMsg}', applied_at=NOW();" > /dev/null 2>&1 || true
        echo -e "${RED}FAILED${RESET}"
        echo "  Error: $output"
    fi
}

# ── Main menu ──────────────────────────────────
main() {
    load_env

    # If called with an argument, run that action directly
    if [[ $# -gt 0 ]]; then
        case "$1" in
            status)   print_status ;;
            run-all)  run_all ;;
            run)      run_specific ;;
            *)
                echo "Usage: $0 [status|run-all|run]"
                echo ""
                echo "  status   — Show migration status"
                echo "  run-all  — Run all pending migrations"
                echo "  run      — Select and run a specific migration"
                exit 1
                ;;
        esac
        exit 0
    fi

    # Interactive mode
    while true; do
        print_header
        echo "  1) Show migration status"
        echo "  2) Run all pending migrations"
        echo "  3) Run a specific migration"
        echo "  4) Exit"
        echo ""
        echo -n "Choose [1-4]: "
        read -r choice

        case "$choice" in
            1) print_status ;;
            2) run_all ;;
            3) run_specific ;;
            4) echo "Bye."; exit 0 ;;
            *) echo "Invalid choice." ;;
        esac
        echo ""
        echo -n "Press Enter to continue..."
        read -r
    done
}

main "$@"
