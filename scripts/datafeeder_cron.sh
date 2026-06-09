#!/usr/bin/env bash
# datafeeder_cron.sh — incremental K-line top-up for live trading.
#
# Designed to run from cron (hourly recommended). Finds the last imported bar
# date via `datafeeder last-bar`, then imports from that date to today.
# ImportSymbol is idempotent, so re-importing the last day is safe.
#
# Environment variables (all optional):
#   CONFIG_PATH           path to config.yaml  (default: <repo>/config.yaml)
#   DATAFEEDER_SYMBOL     trading pair          (default: BTCUSDT)
#   DATAFEEDER_INTERVAL   kline interval        (default: 1m)
#   DATAFEEDER_BIN        path to datafeeder binary; if unset uses `go run`
#   DATAFEEDER_FALLBACK_DAYS  days to look back when last-bar fails (default: 30)
#
# Crontab example (runs every hour, logs to /var/log/datafeeder.log):
#   0 * * * * /path/to/scripts/datafeeder_cron.sh >> /var/log/datafeeder.log 2>&1
#
# Known limitations — see docs/mainnet-runbook.md §G:
#   - Only handles tail gaps; mid-range historical gaps require manual import.
#   - last-bar and import are two separate calls (non-atomic); a concurrent
#     manual import between the two is harmless but theoretically possible.
#   - If the DB is unreachable, falls back to DATAFEEDER_FALLBACK_DAYS (safe
#     but slower — imports a wider window than necessary).
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONFIG="${CONFIG_PATH:-$REPO_DIR/config.yaml}"
SYMBOL="${DATAFEEDER_SYMBOL:-BTCUSDT}"
INTERVAL="${DATAFEEDER_INTERVAL:-1m}"
FALLBACK_DAYS="${DATAFEEDER_FALLBACK_DAYS:-30}"

if [ -n "${DATAFEEDER_BIN:-}" ]; then
    DF="$DATAFEEDER_BIN"
else
    DF="go run $REPO_DIR/cmd/datafeeder"
fi

TODAY=$(date +%Y-%m-%d)

# Determine start date: last imported bar, or fallback if no data yet.
if LAST=$($DF --config "$CONFIG" last-bar --symbol "$SYMBOL" --interval "$INTERVAL" 2>/dev/null); then
    echo "[$(date -u +%FT%TZ)] last-bar=$LAST, importing $LAST → $TODAY"
else
    LAST=$(date -d "$FALLBACK_DAYS days ago" +%Y-%m-%d)
    echo "[$(date -u +%FT%TZ)] last-bar unavailable, falling back to $LAST → $TODAY"
fi

$DF --config "$CONFIG" import \
    --symbol "$SYMBOL" --interval "$INTERVAL" \
    --from "$LAST" --to "$TODAY"
