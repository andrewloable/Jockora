#!/usr/bin/env bash
# Copyright (C) 2026 Andrew Loable
# SPDX-License-Identifier: AGPL-3.0-only
# GATE 2 — 30-minute unattended run with fault injection.
#
# RUN THIS ON THE LINUX BOX, not the Mac. Every metric here is wall-clock
# sensitive and a loaded laptop makes them flap; that is the gate's own DO NOT.
#
#   ./gate2.sh <trackA> <trackB> [minutes]      default 30
#
# Needs: ffmpeg, ffprobe, curl, python3, and the jockora binary beside this file.
set -uo pipefail
cd "$(dirname "$0")"

TRACK_A="${1:?usage: ./gate2.sh <trackA> <trackB> [minutes]}"
TRACK_B="${2:?usage: ./gate2.sh <trackA> <trackB> [minutes]}"
MINUTES="${3:-30}"
PORT=8121
SEG="$PWD/seg"
OUT="$PWD/results"
mkdir -p "$OUT"

# Faults land in the last third, so the clean metrics have a long uncontaminated
# run behind them before anything is injected.
STALL_SHORT_AT=$(( MINUTES * 60 * 60 / 100 ))   # 60% in: the 8s stall GATE 2 specifies
STALL_LONG_AT=$(( MINUTES * 60 * 70 / 100 ))    # 70% in: a stall longer than the ring
KILL_FFMPEG_AT=$(( MINUTES * 60 * 85 / 100 ))   # 85% in: kill ffmpeg

# Kill ONLY this harness's own binary, by absolute path.
#
# 'pkill -x jockora' is wrong and dangerous here: a containerised Jockora is
# visible in the host PID namespace under the same name, so that pattern would
# kill a running deployment. Scope it to $PWD/jockora and to the captured PID.
cleanup() {
  kill -9 "${JOCK:-0}" 2>/dev/null
  pkill -9 -f "^$PWD/jockora " 2>/dev/null
}
trap cleanup EXIT INT TERM

cleanup; sleep 1

# Refuse to run if the port is taken, rather than fighting whatever holds it.
if ss -tlnH "sport = :$PORT" 2>/dev/null | grep -q .; then
  echo "port $PORT is already in use; choose another or stop what is on it"
  exit 1
fi
rm -rf "$SEG" && mkdir -p "$SEG"

echo "GATE 2  —  ${MINUTES} minutes, $(uname -s) $(uname -m)"
echo "  faults:  8s stall at t=${STALL_SHORT_AT}s   long stall at t=${STALL_LONG_AT}s   ffmpeg kill at t=${KILL_FFMPEG_AT}s"
echo

JOCKORA_SEGMENT_DIR="$SEG" JOCKORA_LISTEN_ADDR=127.0.0.1:$PORT \
  ./jockora serve "$TRACK_A" "$TRACK_B" > "$OUT/run.log" 2>&1 &
JOCK=$!

for _ in $(seq 1 60); do
  curl -sf -o /dev/null "http://127.0.0.1:$PORT/hls/stream.m3u8" 2>/dev/null && break
  sleep 0.5
done
if ! kill -0 "$JOCK" 2>/dev/null; then
  echo "FAILED TO START:"; sed 's/^/  /' "$OUT/run.log"; exit 1
fi
echo "on air as pid $JOCK"

: > "$OUT/metrics.jsonl"
: > "$OUT/events.log"
note() { echo "$(date +%s) $*" | tee -a "$OUT/events.log"; }

TOTAL=$(( MINUTES * 60 ))
FF_BEFORE=""
for (( t=0; t<TOTAL; t++ )); do
  if (( t % 10 == 0 )); then
    curl -sf --max-time 3 "http://127.0.0.1:$PORT/now.json" 2>/dev/null \
      | python3 -c "import json,sys,time;d=json.load(sys.stdin);d['t']=$t;print(json.dumps(d))" \
      >> "$OUT/metrics.jsonl" 2>/dev/null || true
  fi
  case $t in
    "$STALL_SHORT_AT")
      note "FAULT 1a: 8s decoder stall (SIGUSR1) — the ring should ABSORB this"
      kill -USR1 "$JOCK" ;;
    "$STALL_LONG_AT")
      note "FAULT 1b: 15s decoder stall (SIGUSR2) — longer than the ring, silence-fill MUST fire"
      kill -USR2 "$JOCK" ;;
    "$KILL_FFMPEG_AT")
      # Match only the encoder writing into THIS run's segment directory, so a
      # containerised Jockora's ffmpeg is never the one killed.
      FF_BEFORE=$(pgrep -f "hls_segment_filename $SEG" | head -1)
      [ -z "$FF_BEFORE" ] && FF_BEFORE=$(pgrep -af 'hls_segment_filename' | grep -F "$SEG" | awk '{print $1}' | head -1)
      note "FAULT 2: killing ffmpeg pid $FF_BEFORE"
      T0=$(date +%s.%N)
      kill -9 "$FF_BEFORE" 2>/dev/null
      for _ in $(seq 1 100); do
        NEW=$(pgrep -af 'hls_segment_filename' | grep -F "$SEG" | awk '{print $1}' | head -1)
        [ -n "$NEW" ] && [ "$NEW" != "$FF_BEFORE" ] && break
        sleep 0.1
      done
      note "FAULT 2: ffmpeg restarted as $NEW after $(python3 -c "print(f'{$(date +%s.%N)-$T0:.2f}')")s (budget 2.00s)"
      # Capture the playlist across the window where the tag exists at all.
      #
      # It is a sliding ten-segment window, so the tag scrolls out within a
      # minute and the FINAL playlist cannot answer whether it was written. But
      # an immediate snapshot is too EARLY -- the replacement ffmpeg has not
      # published a playlist yet, so it catches the dead process's last one.
      # Measured: the tag appears about six seconds after the kill. So poll,
      # and keep the first snapshot that actually contains it.
      for _ in $(seq 1 25); do
        curl -sf "http://127.0.0.1:$PORT/hls/stream.m3u8" -o "$OUT/playlist-after-kill.m3u8" 2>/dev/null
        grep -q "EXT-X-DISCONTINUITY" "$OUT/playlist-after-kill.m3u8" 2>/dev/null && break
        sleep 1
      done ;;
  esac
  if ! kill -0 "$JOCK" 2>/dev/null; then note "STREAM DIED at t=${t}s"; break; fi
  sleep 1
done

note "run complete, shutting down for the final metrics line"
kill -INT "$JOCK" 2>/dev/null
wait "$JOCK" 2>/dev/null

STALL_SHORT_AT=$STALL_SHORT_AT STALL_LONG_AT=$STALL_LONG_AT KILL_AT=$KILL_FFMPEG_AT \
SEG="$SEG" OUT="$OUT" python3 report.py | tee "$OUT/GATE2-RESULT.txt"
