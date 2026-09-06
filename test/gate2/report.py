#!/usr/bin/env python3
# Copyright (C) 2026 Andrew Loable
# SPDX-License-Identifier: AGPL-3.0-only
"""Grade a GATE 2 run.

The four metrics are graded on the CLEAN portion of the run, before any fault is
injected. That is deliberate, and it resolves a real conflict in the gate as
written: criterion (b) demands minimum ring occupancy >= 2s while fault 1
deliberately drains the ring, and criterion (c) demands segment deltas hold
4.00 +/- 0.25s while fault 2 deliberately kills the encoder. Both cannot be true
of the same seconds. Grading the clean window and asserting RECOVERY separately
is the only reading under which every criterion can hold at once.
"""
import json, os, re, statistics, sys, glob

OUT   = os.environ["OUT"]
SEG   = os.environ["SEG"]
FIRST_FAULT = int(os.environ["STALL_SHORT_AT"])
KILL_AT     = int(os.environ["KILL_AT"])

def rows():
    out = []
    with open(f"{OUT}/metrics.jsonl") as f:
        for line in f:
            line = line.strip()
            if line:
                try: out.append(json.loads(line))
                except Exception: pass
    return out

def final_log():
    try:
        for line in reversed(open(f"{OUT}/run.log").read().splitlines()):
            if "off air" in line: return line
    except FileNotFoundError: pass
    return ""

def kv(line, key):
    """Read one field from the final log line, in either log format.

    Jockora INFERS its log format: text at a terminal, JSON when stderr is
    redirected -- which is exactly what happens here, since the harness pipes
    the run to a file. This parser only understood logfmt, so it read every
    field as "?" and crashed grading (d) on a run whose numbers were fine.
    """
    try:
        obj = json.loads(line)
        if key in obj:
            return str(obj[key])
    except (ValueError, TypeError):
        pass
    m = re.search(rf'{key}=([^\s]+)', line)
    return m.group(1) if m else "?"

samples = rows()
clean   = [r for r in samples if r.get("t", 0) < FIRST_FAULT]
last    = samples[-1] if samples else {}
fin     = final_log()

def met(r, k, d=0.0):
    return (r.get("metrics") or {}).get(k, d)

print("=" * 66)
print("GATE 2 RESULT")
print("=" * 66)
print(f"  samples: {len(samples)} total, {len(clean)} before the first fault\n")

verdicts = []
def grade(name, value, ok, detail):
    verdicts.append(ok)
    print(f"  [{'PASS' if ok else 'FAIL'}] {name}")
    print(f"         {detail}")

# (a) p99 inter-write gap <= 250ms, on the clean window.
if clean:
    p99 = max(met(r, "p99_inter_write_gap_ms") for r in clean)
    mx  = max(met(r, "max_inter_write_gap_ms") for r in clean)
    grade("(a) p99 inter-write gap <= 250ms", p99, p99 <= 250,
          f"p99 {p99:.1f}ms, max {mx:.1f}ms  (clean window)")
else:
    grade("(a) p99 inter-write gap", 0, False, "no clean samples")

# (b) minimum ring occupancy >= 2s, on the clean window.
if clean:
    lo = min(met(r, "min_ring_occupancy_s") for r in clean)
    grade("(b) min ring occupancy >= 2s", lo, lo >= 2.0,
          f"{lo:.3f}s (clean window; the injected stalls drive this to 0 ON PURPOSE)")
else:
    grade("(b) min ring occupancy", 0, False, "no clean samples")

# (c) segment mtime deltas, excluding a window around each fault.
segs = []
for p in glob.glob(f"{SEG}/seg*.ts"):
    m = re.match(r"seg(\d+)\.ts$", os.path.basename(p))
    if m: segs.append((int(m.group(1)), os.path.getmtime(p), p))
segs.sort()
if len(segs) > 3:
    t0 = segs[0][1]
    deltas, excluded = [], 0
    for i in range(1, len(segs)):
        rel = segs[i][1] - t0
        # Skip a generous window around each injected fault and the final
        # segment, which SIGINT truncates.
        near_fault = any(abs(rel - f) < 12 for f in (FIRST_FAULT, KILL_AT))
        if near_fault or i == len(segs) - 1:
            excluded += 1; continue
        deltas.append(segs[i][1] - segs[i-1][1])
    if deltas:
        bad = [d for d in deltas if abs(d - 4.0) > 0.25]
        grade("(c) segment deltas 4.00 +/- 0.25s", 0, not bad,
              f"{len(segs)} segments, {len(deltas)} graded, {excluded} excluded near faults; "
              f"min {min(deltas):.3f} max {max(deltas):.3f} mean {statistics.mean(deltas):.3f} "
              f"stdev {statistics.pstdev(deltas):.4f}; {len(bad)} outside tolerance")
    else:
        grade("(c) segment deltas", 0, False, "no gradeable deltas")
else:
    grade("(c) segment deltas", 0, False, f"only {len(segs)} segments on disk")

# (d) final drift <= 100ms, from the shutdown line.
drift = kv(fin, "final_drift")
try:
    if drift.endswith("ms"):
        val = float(drift[:-2])
    elif drift.endswith("s"):
        val = float(drift[:-1]) * 1000
    else:
        # A bare number is NANOSECONDS: that is how a time.Duration serialises
        # into JSON, and the log format is JSON whenever stderr is redirected --
        # which it always is here. Comparing 2000000 against a 100 threshold
        # failed a run whose drift was two milliseconds.
        val = float(drift) / 1e6
    grade("(d) final drift <= 100ms", val, abs(val) <= 100, f"{val:.1f}ms")
except Exception:
    grade("(d) final drift <= 100ms", 0, False, f"could not parse: {drift!r}")

print()
# Fault 1: silence-fill must be PROVEN to fire, not proven never to be needed.
und = int(kv(fin, "underruns") or 0) if fin else 0
grade("fault 1: decoder stall recovers, silence-fill fires", und, und > 0,
      f"underruns={und} (the 8s stall is absorbed by the 10s ring by design; "
      f"the longer SIGUSR2 stall is what must drive this above zero)")

restarts = kv(fin, "encoder_restarts")
grade("fault 2: encoder restarted", restarts, restarts not in ("?", "0"),
      f"encoder_restarts={restarts}")

# Exactly one discontinuity, and a media sequence that never goes backwards.
# Read the SNAPSHOT taken just after the kill, not the final playlist.
#
# The playlist is a sliding ten-segment window. The kill lands at 85% of the
# run, so by the end the discontinuity tag has scrolled out of it -- the final
# playlist reported zero on a run where the tag was written correctly. The
# snapshot is the only place it can still be seen.
snapshot = f"{OUT}/playlist-after-kill.m3u8"
try:
    pl = open(snapshot).read()
    disc = pl.count("#EXT-X-DISCONTINUITY")
    grade("fault 2: exactly one EXT-X-DISCONTINUITY", disc, disc == 1, f"count={disc} (snapshot taken just after the kill)")
except FileNotFoundError:
    grade("fault 2: discontinuity count", 0, False,
          "no post-kill playlist snapshot; the final playlist cannot answer this, "
          "its window has moved on")

nums = [n for n, _, _ in segs]
grade("fault 2: segment numbers strictly increasing, none reused",
      0, nums == sorted(set(nums)) and len(nums) == len(set(nums)),
      f"{len(nums)} segments, {len(set(nums))} distinct, range {min(nums) if nums else '-'}..{max(nums) if nums else '-'}")

print()
print(f"  stream stayed up: {'yes' if 'STREAM DIED' not in open(f'{OUT}/events.log').read() else 'NO'}")
print(f"  shutdown line: {fin.split('msg=')[-1] if fin else '(missing)'}")
print()
print("=" * 66)
print(f"  {'GATE 2 PASSED' if all(verdicts) else 'GATE 2 FAILED'}   "
      f"({sum(verdicts)}/{len(verdicts)} checks)")
print("=" * 66)
sys.exit(0 if all(verdicts) else 1)
