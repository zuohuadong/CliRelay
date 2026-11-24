#!/usr/bin/env bash
set -euo pipefail

BASE_DIR="${BASE_DIR:-/opt/clirelay2}"
STACK_DIR="${CLIRELAY_RUNTIME_STACK_DIR:-${BASE_DIR}/runtime-data-stack}"
STACK_ENV="${CLIRELAY_RUNTIME_STACK_ENV_FILE:-${STACK_DIR}/.env}"
APP_ENV="${CLIRELAY_RUNTIME_ENV_FILE:-${BASE_DIR}/.env}"
COMPOSE_FILE="${CLIRELAY_RUNTIME_COMPOSE_FILE:-${STACK_DIR}/docker-compose.yml}"
PROJECT="${CLIRELAY_RUNTIME_COMPOSE_PROJECT:-clirelay-runtime}"
ACTION="${1:-up}"

POSTGRES_DB="${CLIRELAY_POSTGRES_DB:-cliproxy}"
POSTGRES_USER="${CLIRELAY_POSTGRES_USER:-cliproxy}"
POSTGRES_PORT="${CLIRELAY_POSTGRES_PORT:-55432}"
POSTGRES_DATA_PATH="${CLIRELAY_POSTGRES_DATA_PATH:-${STACK_DIR}/postgres-data}"

fail() {
	echo "$*" >&2
	exit 1
}

file_env_value() {
	[ -f "$2" ] || return 0
	awk -F= -v key="$1" '
		$1 == key {
			value = substr($0, length(key) + 2)
			gsub(/^[[:space:]"'\'']+|[[:space:]"'\'']+$/, "", value)
			print value
			exit
		}
	' "$2"
}

stack_env_value() {
	file_env_value "$1" "$STACK_ENV"
}

random_hex() {
	od -An -N24 -tx1 /dev/urandom | tr -d ' \n'
}

compose() {
	if docker compose version >/dev/null 2>&1; then
		docker compose --env-file "$STACK_ENV" -f "$COMPOSE_FILE" -p "$PROJECT" "$@" < /dev/null
	elif command -v docker-compose >/dev/null 2>&1; then
		docker-compose --env-file "$STACK_ENV" -f "$COMPOSE_FILE" -p "$PROJECT" "$@" < /dev/null
	else
		fail "docker compose is required"
	fi
}

write_compose_file() {
	mkdir -p "$STACK_DIR" "$POSTGRES_DATA_PATH"
	cat > "$COMPOSE_FILE" <<'EOF'
services:
  postgres:
    image: ${CLIRELAY_POSTGRES_IMAGE:-postgres:15-alpine}
    environment:
      POSTGRES_DB: ${CLIRELAY_POSTGRES_DB:-cliproxy}
      POSTGRES_USER: ${CLIRELAY_POSTGRES_USER:-cliproxy}
      POSTGRES_PASSWORD: ${CLIRELAY_POSTGRES_PASSWORD:?CLIRELAY_POSTGRES_PASSWORD is required}
    ports:
      - "127.0.0.1:${CLIRELAY_POSTGRES_PORT:-55432}:5432"
    volumes:
      - ${CLIRELAY_POSTGRES_DATA_PATH:?CLIRELAY_POSTGRES_DATA_PATH is required}:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $${POSTGRES_USER} -d $${POSTGRES_DB}"]
      interval: 5s
      timeout: 5s
      retries: 30
    restart: unless-stopped

EOF
}

write_stack_env_file() {
	postgres_password="${CLIRELAY_POSTGRES_PASSWORD:-$(stack_env_value CLIRELAY_POSTGRES_PASSWORD)}"
	postgres_password="${postgres_password:-$(random_hex)}"
	postgres_dsn="postgres://${POSTGRES_USER}:${postgres_password}@127.0.0.1:${POSTGRES_PORT}/${POSTGRES_DB}?sslmode=disable"

	mkdir -p "$(dirname "$STACK_ENV")"
	tmp="$(mktemp "${STACK_ENV}.tmp.XXXXXX")"
	if [ -f "$STACK_ENV" ]; then
		awk '
			/^# BEGIN CLIRELAY RUNTIME DATA STACK$/ {skip=1; next}
			/^# END CLIRELAY RUNTIME DATA STACK$/ {skip=0; next}
			!skip {print}
		' "$STACK_ENV" > "$tmp"
	fi
	{
		printf '%s\n' "# BEGIN CLIRELAY RUNTIME DATA STACK"
		printf 'CLIRELAY_POSTGRES_DB=%s\n' "$POSTGRES_DB"
		printf 'CLIRELAY_POSTGRES_USER=%s\n' "$POSTGRES_USER"
		printf 'CLIRELAY_POSTGRES_PASSWORD=%s\n' "$postgres_password"
		printf 'CLIRELAY_POSTGRES_PORT=%s\n' "$POSTGRES_PORT"
		printf 'CLIRELAY_POSTGRES_DATA_PATH=%s\n' "$POSTGRES_DATA_PATH"
		printf 'CLIRELAY_POSTGRES_DSN=%s\n' "$postgres_dsn"
		printf '%s\n' "# END CLIRELAY RUNTIME DATA STACK"
	} >> "$tmp"
	chmod 600 "$tmp"
	mv "$tmp" "$STACK_ENV"
}

activate_app_env() {
	[ -f "$STACK_ENV" ] || fail "runtime stack env not found: $STACK_ENV"
	mkdir -p "$(dirname "$APP_ENV")"
	tmp="$(mktemp "${APP_ENV}.tmp.XXXXXX")"
	if [ -f "$APP_ENV" ]; then
		awk '
			/^# BEGIN CLIRELAY RUNTIME DATA STACK$/ {skip=1; next}
			/^# END CLIRELAY RUNTIME DATA STACK$/ {skip=0; next}
			!skip {print}
		' "$APP_ENV" > "$tmp"
	fi
	sed -n '/^# BEGIN CLIRELAY RUNTIME DATA STACK$/,/^# END CLIRELAY RUNTIME DATA STACK$/p' "$STACK_ENV" >> "$tmp"
	chmod 600 "$tmp"
	mv "$tmp" "$APP_ENV"
	echo "activated runtime data stack env: $APP_ENV"
}

wait_healthy() {
	service="$1"
	for _ in $(seq 1 90); do
		cid="$(compose ps -q "$service" 2>/dev/null || true)"
		if [ -n "$cid" ]; then
			status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$cid" 2>/dev/null || true)"
			[ "$status" = "healthy" ] && return 0
		fi
		sleep 1
	done
	compose ps >&2 || true
	fail "$service did not become healthy"
}

reset_stack() {
	[ "${CLIRELAY_RUNTIME_CONFIRM_RESET:-}" = "YES_DELETE_CLIRELAY_RUNTIME_DATA" ] || \
		fail "set CLIRELAY_RUNTIME_CONFIRM_RESET=YES_DELETE_CLIRELAY_RUNTIME_DATA to reset"
	compose down --remove-orphans || true
	rm -rf "$POSTGRES_DATA_PATH"
	echo "reset complete"
}

case "$ACTION" in
	up)
		write_stack_env_file
		write_compose_file
		compose up -d
		wait_healthy postgres
		compose exec -T -e PGPASSWORD="$(stack_env_value CLIRELAY_POSTGRES_PASSWORD)" postgres \
			psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -q -c 'SELECT 1' >/dev/null
		echo "runtime data stack is ready"
		echo "stack env file: $STACK_ENV"
		echo "app env file: $APP_ENV (not modified; run '$0 activate-env' to switch CliRelay)"
		echo "compose file: $COMPOSE_FILE"
		echo "postgres dsn: postgres://${POSTGRES_USER}:***@127.0.0.1:${POSTGRES_PORT}/${POSTGRES_DB}?sslmode=disable"
		;;
	activate-env)
		activate_app_env
		;;
	down)
		compose down --remove-orphans
		;;
	reset)
		reset_stack
		;;
	*)
		fail "usage: $0 [up|activate-env|down|reset]"
		;;
esac
