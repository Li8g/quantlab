#!/usr/bin/env bash
#
# run_testnet.sh — one-command bring-up of the QuantLab live-trading loop
# against Binance Spot testnet. Folds docs/ws-phase2-runbook.md steps 1–5
# into a single idempotent script: seeds the admin user / agent token /
# instance only when missing, starts SaaS + agent, and prints a status
# snapshot once the WS link is up.
#
# Usage:
#   scripts/run_testnet.sh              # bring up SaaS + agent, show status
#   scripts/run_testnet.sh --reseed-token   # rotate the agent token + rewrite config.agent.yaml
#   scripts/run_testnet.sh --stop       # stop the SaaS + agent started here
#
# Password: the admin login password is prompted for (read silently). Set
# ADMIN_PASSWORD in the environment to run non-interactively.
#
set -uo pipefail
cd "$(dirname "$0")/.."   # repo root

# ---------------------------------------------------------------- knobs
SAAS_CFG="config.yaml"
AGENT_CFG="config.agent.yaml"
ACCOUNT_ID="main"
STRATEGY_ID="sigmoid_v1"
PAIR="BTCUSDT"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@local}"
LOG_DIR="/tmp/quantlab-testnet"
SAAS_LOG="$LOG_DIR/saas.log"
AGENT_LOG="$LOG_DIR/agent.log"
SAAS_BIN="$LOG_DIR/ql_saas"
AGENT_BIN="$LOG_DIR/ql_agent"
READY_TIMEOUT=40   # seconds to wait for the WS handshake

mkdir -p "$LOG_DIR"

# ---------------------------------------------------------------- pretty
c_cyan=$'\033[1;36m'; c_grn=$'\033[1;32m'; c_yel=$'\033[1;33m'; c_red=$'\033[1;31m'; c_rst=$'\033[0m'
say()  { printf '%s▶ %s%s\n' "$c_cyan" "$*" "$c_rst"; }
ok()   { printf '%s✓ %s%s\n' "$c_grn" "$*" "$c_rst"; }
warn() { printf '%s! %s%s\n' "$c_yel" "$*" "$c_rst"; }
die()  { printf '%s✗ %s%s\n' "$c_red" "$*" "$c_rst" >&2; exit 1; }

# ---------------------------------------------------------------- config parse
yaml_top()  { grep -E "^$1:" "$2" | head -1 | sed -E "s/^$1:[[:space:]]*//; s/[[:space:]]*#.*$//; s/^\"//; s/\"$//"; }
yaml_under(){ # yaml_under <block> <key> <file>: value of <key> indented under <block>:
  awk -v b="$1" -v k="$2" '
    $0 ~ "^"b":" {f=1; next}
    f && /^[^[:space:]]/ {f=0}
    f && $0 ~ "^[[:space:]]+"k":" {sub("^[[:space:]]+"k":[[:space:]]*",""); sub(/[[:space:]]*#.*$/,""); gsub(/"/,""); print; exit}
  ' "$3"
}

HTTP_PORT="$(yaml_under server http_listen "$SAAS_CFG" | grep -oE '[0-9]+' | tail -1)"; HTTP_PORT="${HTTP_PORT:-8080}"
WS_PORT="$(yaml_under server ws_listen "$SAAS_CFG"   | grep -oE '[0-9]+' | tail -1)"; WS_PORT="${WS_PORT:-8081}"
API="http://localhost:${HTTP_PORT}"

DB_HOST="$(yaml_under database host "$SAAS_CFG")"; [ "$DB_HOST" = localhost ] && DB_HOST=127.0.0.1
DB_PORT="$(yaml_under database port "$SAAS_CFG")"; DB_PORT="${DB_PORT:-5432}"
DB_USER="$(yaml_under database user "$SAAS_CFG")"
DB_PASS="$(yaml_under database password "$SAAS_CFG")"
DB_NAME="$(yaml_under database database "$SAAS_CFG")"

db() { PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -tA -c "$1" 2>/dev/null; }

# ---------------------------------------------------------------- --stop
if [ "${1:-}" = "--stop" ]; then
  say "stopping agent + saas"
  pkill -TERM -f "$AGENT_BIN" 2>/dev/null && ok "agent stopped" || warn "no agent running"
  pkill -TERM -f "$SAAS_BIN"  2>/dev/null && ok "saas stopped"  || warn "no saas running"
  exit 0
fi

# --clear-freeze: wipe a stale auto-freeze so a retry starts clean. The
# /live frozen banner reads the latest instance.kill AuditLog (subject
# account:<id>); drift rows are just noise. The agent-side latch clears on
# the next agent restart (this script restarts it anyway). v1 has no resume
# message, so this manual clear IS the unfreeze path.
if [ "${1:-}" = "--clear-freeze" ]; then
  say "clearing frozen state for account=$ACCOUNT_ID"
  db "select 1" >/dev/null || die "cannot reach Postgres"
  K="$(db "delete from audit_logs where action='instance.kill' and subject='account:$ACCOUNT_ID'; select 'ok'")"
  D="$(db "delete from reconciliation_discrepancies where account_id='$ACCOUNT_ID'; select 'ok'")"
  [ "$K" = ok ] && ok "cleared instance.kill audit rows (frozen banner)" || warn "audit clear: $K"
  [ "$D" = ok ] && ok "cleared drift discrepancies"                      || warn "discrepancy clear: $D"
  warn "now restart the agent (re-run this script) to drop its in-process freeze latch"
  exit 0
fi

RESEED_TOKEN=0
[ "${1:-}" = "--reseed-token" ] && RESEED_TOKEN=1

# ---------------------------------------------------------------- preflight
say "preflight"
for t in go jq curl psql ss; do command -v "$t" >/dev/null || die "missing tool: $t"; done
[ -f "$SAAS_CFG" ]  || die "missing $SAAS_CFG"
[ -f "$AGENT_CFG" ] || die "missing $AGENT_CFG (fill testnet api_key/secret first)"
db "select 1" >/dev/null || die "cannot reach Postgres at $DB_HOST:$DB_PORT/$DB_NAME (check $SAAS_CFG database:)"
ok "tools + Postgres reachable"

# Testnet reachability — warned, not fatal: SaaS still comes up, but the
# agent handshake needs exchange.Positions(), so L1 cannot complete while down.
TN_BASE="$(yaml_under exchange base_url "$AGENT_CFG")"
TN_CODE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 8 "${TN_BASE%/}/api/v3/ping" || echo 000)"
if [ "$TN_CODE" = 200 ]; then ok "Binance testnet reachable ($TN_BASE)"; else warn "Binance testnet ${TN_BASE} returned HTTP $TN_CODE — L1 will not complete until it recovers"; fi

# ---------------------------------------------------------------- build
say "building binaries"
go build -o "$SAAS_BIN"  ./cmd/saas  || die "saas build failed"
go build -o "$AGENT_BIN" ./cmd/agent || die "agent build failed"
ok "built ql_saas + ql_agent"

# ---------------------------------------------------------------- SaaS
if ss -ltn 2>/dev/null | grep -qE ":${WS_PORT}\b"; then
  warn "something already listening on :$WS_PORT — reusing it"
else
  say "starting SaaS"
  nohup "$SAAS_BIN" --config "$SAAS_CFG" >"$SAAS_LOG" 2>&1 &
  for _ in $(seq 1 40); do
    grep -qE "ws listening on|listening on :${WS_PORT}" "$SAAS_LOG" && break
    grep -qiE "fatal|panic|bind: address already in use" "$SAAS_LOG" && { tail -5 "$SAAS_LOG"; die "SaaS failed to start"; }
    sleep 0.5
  done
  ss -ltn 2>/dev/null | grep -qE ":${WS_PORT}\b" || { tail -8 "$SAAS_LOG"; die "SaaS not listening on :$WS_PORT"; }
  ok "SaaS up — http :$HTTP_PORT / ws :$WS_PORT (log: $SAAS_LOG)"
fi

# ---------------------------------------------------------------- admin login
if [ -z "${ADMIN_PASSWORD:-}" ]; then
  read -rsp "admin password for ${ADMIN_EMAIL}: " ADMIN_PASSWORD; echo
fi
[ -n "$ADMIN_PASSWORD" ] || die "empty admin password"

login() {
  curl -s -X POST "$API/api/v1/auth/login" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg e "$ADMIN_EMAIL" --arg p "$ADMIN_PASSWORD" '{email:$e,password:$p,role:"admin"}')" \
    | jq -r '.token // empty'
}
say "logging in as $ADMIN_EMAIL"
JWT="$(login)"
if [ -z "$JWT" ]; then
  warn "login failed — seeding admin user $ADMIN_EMAIL (one-time)"
  "$SAAS_BIN" --config "$SAAS_CFG" --seed-user-email "$ADMIN_EMAIL" --seed-user-password "$ADMIN_PASSWORD" \
    >>"$SAAS_LOG" 2>&1 || warn "seed-user returned non-zero (may already exist)"
  JWT="$(login)"
fi
[ -n "$JWT" ] || die "login still failing — wrong password, or seed it: $SAAS_BIN --config $SAAS_CFG --seed-user-email $ADMIN_EMAIL --seed-user-password '<pw>'"
ok "admin JWT acquired"

# ---------------------------------------------------------------- agent token
TOK="$(yaml_top saas_token "$AGENT_CFG")"
HAS_DB_TOKEN="$(db "select count(*) from agent_tokens where account_id='$ACCOUNT_ID' and revoked_at is null")"
if [ "$RESEED_TOKEN" = 1 ] || [[ "$TOK" != agt_* ]] || [ "${HAS_DB_TOKEN:-0}" = 0 ]; then
  say "seeding agent token for account=$ACCOUNT_ID"
  NEW_TOK="$("$SAAS_BIN" --config "$SAAS_CFG" --seed-agent-token "$ACCOUNT_ID" 2>>"$SAAS_LOG" | grep -oE 'agt_[A-Za-z0-9_]+' | head -1)"
  [ -n "$NEW_TOK" ] || die "failed to seed agent token"
  # rewrite the saas_token line in config.agent.yaml in place
  cp "$AGENT_CFG" "$AGENT_CFG.bak"
  awk -v t="$NEW_TOK" '/^saas_token:/{print "saas_token: \"" t "\""; next} {print}' "$AGENT_CFG.bak" >"$AGENT_CFG"
  ok "agent token rotated + written into $AGENT_CFG (backup: $AGENT_CFG.bak)"
else
  ok "agent token present in $AGENT_CFG and live in DB — kept"
fi

# ---------------------------------------------------------------- instance
INST="$(db "select instance_id from strategy_instances where account_id='$ACCOUNT_ID' and status <> 'retired' order by created_at desc limit 1")"
if [ -z "$INST" ]; then
  say "creating StrategyInstance ($STRATEGY_ID/$PAIR/$ACCOUNT_ID)"
  INST="$(curl -s -X POST "$API/api/v1/instances" -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg s "$STRATEGY_ID" --arg p "$PAIR" --arg a "$ACCOUNT_ID" '{strategy_id:$s,pair:$p,account_id:$a}')" \
    | jq -r '.instance_id // empty')"
  [ -n "$INST" ] || die "instance creation failed (re-login may be needed: admin JWT TTL is 10min)"
  ok "instance created: $INST"
else
  ok "instance exists: $INST"
fi

# ---------------------------------------------------------------- agent
if pgrep -f "$AGENT_BIN --config" >/dev/null; then
  warn "agent already running — reusing it"
else
  say "starting agent"
  nohup "$AGENT_BIN" --config "$AGENT_CFG" >"$AGENT_LOG" 2>&1 &
  ok "agent launched (log: $AGENT_LOG)"
fi

# ---------------------------------------------------------------- wait for L1
say "waiting up to ${READY_TIMEOUT}s for WS handshake (agent_session_ready)…"
STATE=pending
for _ in $(seq 1 $((READY_TIMEOUT*2))); do
  if grep -q "agent_session_ready" "$AGENT_LOG" 2>/dev/null && grep -q "ws_agent_ready" "$SAAS_LOG" 2>/dev/null; then STATE=ready; break; fi
  if grep -qiE "auth_fail|invalid_token|account_mismatch" "$AGENT_LOG" 2>/dev/null; then STATE=auth_fail; break; fi
  sleep 0.5
done
[ "$STATE" = pending ] && grep -q "502 Bad Gateway" "$AGENT_LOG" 2>/dev/null && STATE=exchange_down

# ---------------------------------------------------------------- status
echo
printf '%s================= STATUS =================%s\n' "$c_cyan" "$c_rst"
case "$STATE" in
  ready)         ok  "L1 ACHIEVED — agent connected, WS link live" ;;
  auth_fail)     die "auth_fail — token mismatch. Re-run with --reseed-token to rotate it." ;;
  exchange_down) warn "agent connected to SaaS path but Binance testnet is DOWN (502) — handshake can't finish until it recovers. Agent is retrying with backoff." ;;
  pending)       warn "no agent_session_ready within ${READY_TIMEOUT}s — see log tails below" ;;
esac

FUNDED="$(db "select coalesce(to_timestamp(funded_at_ms/1000)::text,'NULL (not yet funded)') from strategy_instances where instance_id='$INST'")"
printf 'instance     : %s  (account=%s)\n' "$INST" "$ACCOUNT_ID"
printf 'funded_at    : %s\n' "$FUNDED"

LIVE="$(curl -s "$API/api/v1/instances/$INST/live" -H "Authorization: Bearer $JWT")"
if echo "$LIVE" | jq -e . >/dev/null 2>&1; then
  echo "$LIVE" | jq -r '
    "status       : \(.instance.status)  champion=\(.instance.active_champion_id // "none")",
    "connection   : \(if .connection.connected then "connected" else "not connected" end)",
    "portfolio    : " + (if .portfolio then "USDT=\(.portfolio.usdt)  FloatBTC=\(.portfolio.float_btc)  equity=\(.portfolio.equity // "n/a")" else "none (no tick / not funded yet)" end),
    "kill_status  : " + (if .kill_status then "FROZEN actor=\(.kill_status.actor) reason=\(.kill_status.reason) trigger=\(.kill_status.trigger)" else "active (not frozen)" end),
    "discrepancies: \(.recent_discrepancies | length)   agent_errors: \(.recent_errors | length)"
  '
else
  warn "could not pull /live (JWT may have expired — 10min TTL)"
fi

if echo "$LIVE" | jq -e '.kill_status' >/dev/null 2>&1; then
  warn "instance is FROZEN (auto kill_switch, likely from a prior session). L1 + funding still run, but trading (L2) is blocked. Clear it for a clean retry:  $0 --clear-freeze"
fi

echo
printf '%s--- agent log (last 4) ---%s\n' "$c_cyan" "$c_rst"; tail -4 "$AGENT_LOG" 2>/dev/null
printf '%s--- saas  log (ws/funded, last 4) ---%s\n' "$c_cyan" "$c_rst"; grep -E "ws_agent|funded|delta_report|reconcile|kill" "$SAAS_LOG" 2>/dev/null | tail -4
echo
printf 'logs: %s , %s\n' "$AGENT_LOG" "$SAAS_LOG"
printf 'stop: %s --stop\n' "$0"
