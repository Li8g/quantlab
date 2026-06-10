#!/usr/bin/env bash
# optuna_export_cron.sh — periodic refresh of the analysis page snapshot (G2).
#
# Re-exports Postgres → optuna sqlite (the Python script swaps the file in
# atomically, so the live dashboard never reads a half-rebuilt db), then
# restarts the dashboard so it reopens the freshly-built file. Schedule from
# cron at 15–30 min; the analysis page is an optional diagnostic, so the brief
# restart blip is acceptable.
#
# Environment variables (all optional):
#   QUANTLAB_DIR   repo root            (default: derived from this script)
#   CONFIG_PATH    path to config.yaml  (default: <repo>/config.yaml)
#   OPTUNA_MODE    export mode          (default: traces)
#   RESTART_UNIT   systemd unit to restart after export (default: quantlab-optuna-dashboard)
#
# Crontab example (every 20 min, logs to /var/log/optuna_export.log):
#   */20 * * * * /path/to/scripts/optuna_export_cron.sh >> /var/log/optuna_export.log 2>&1
#
# Note: restarting the unit needs privilege. Either run this from a root crontab,
# grant the cron user passwordless sudo for `systemctl restart <unit>`, or run
# the dashboard as a `systemctl --user` unit. If systemctl/the unit is absent,
# the restart is skipped (the atomic swap already applied; new dashboard
# connections pick up fresh data once the process is restarted by other means).
set -euo pipefail

QUANTLAB_DIR="${QUANTLAB_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
CONFIG="${CONFIG_PATH:-$QUANTLAB_DIR/config.yaml}"
OPTUNA_MODE="${OPTUNA_MODE:-traces}"
RESTART_UNIT="${RESTART_UNIT:-quantlab-optuna-dashboard}"
TOY_DIR="$QUANTLAB_DIR/research/optuna_toy"

ts() { date -u +%FT%TZ; }

echo "[$(ts)] optuna export start (mode=$OPTUNA_MODE)"
"$TOY_DIR/.venv/bin/python" "$TOY_DIR/quantlab_to_optuna.py" \
    --config "$CONFIG" \
    --mode "$OPTUNA_MODE" \
    --output "$TOY_DIR/quantlab_phase1.db"

# Reopen the fresh file. Skip gracefully if systemd or the unit isn't present.
if command -v systemctl >/dev/null 2>&1 \
    && systemctl list-unit-files "${RESTART_UNIT}.service" >/dev/null 2>&1; then
    systemctl restart "$RESTART_UNIT" && echo "[$(ts)] restarted $RESTART_UNIT"
else
    echo "[$(ts)] $RESTART_UNIT not managed by systemd here; skipped restart"
fi
echo "[$(ts)] optuna export done"
