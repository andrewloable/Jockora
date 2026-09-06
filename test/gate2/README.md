# GATE 2 — 30 minutes unattended, with faults

Thirty minutes of continuous streaming on the Linux target, with three faults
injected in the last third, graded automatically.

## Run it in a container

The harness needs ffmpeg AND bash, curl and python3. The runtime image carries
ffmpeg and deliberately carries nothing else, so the harness image adds the
shell tooling on top of it.

```sh
docker build -f test/gate2/Dockerfile -t jockora-gate2 \
  --build-arg JOCKORA_IMAGE=<your jockora image> test/gate2/

docker run -d --name gate2run \
  -v /path/to/music:/music:ro -v "$PWD/test/gate2":/gate2 -w /gate2 \
  jockora-gate2 ./gate2.sh /music/trackA.mp3 /music/trackB.mp3 30

docker logs -f gate2run
```

A `jockora` binary built for the target must sit beside the script.

## Why a container, and not just ffmpeg on the host

Two reasons, and the second is the important one.

1. The ffmpeg under test is then the one the product actually ships with,
   rather than whatever the host happens to have.

2. **The harness kills processes by name.** On the host, a containerised
   Jockora is visible in the same PID namespace under the same name, so a
   careless pattern would kill a live deployment. Running the gate in its own
   namespace makes that STRUCTURALLY impossible instead of relying on the
   pattern being written carefully. The script is still careful — it scopes
   every kill to its own absolute path and captured PID — but the namespace is
   what makes the mistake unavailable.

## What it injects, and when

Faults land in the last third, so the clean metrics have a long uncontaminated
run behind them first.

| t | fault | what it proves |
|---|---|---|
| 60% | 8s decoder stall (SIGUSR1) | the ring absorbs it: no underrun, no gap |
| 70% | stall longer than the ring (SIGUSR2) | silence-fill actually fires and recovers |
| 85% | ffmpeg killed | the supervisor restarts it inside 2s, one discontinuity |

The 8-second stall is what the gate specifies, and on a 10-second ring it is
ABSORBED — `underruns` stays at zero and silence-fill never runs. That is the
system working, but it does not demonstrate the safety net, which is why the
second, longer stall exists.

## What it grades

p99 inter-write gap ≤ 250ms · minimum ring occupancy ≥ 2s · segment deltas
4.00 ±0.25s · final drift ≤ 100ms · ffmpeg recovery < 2s · exactly one
discontinuity · media sequence strictly increasing and never reused.

Criteria (b) and (c) are graded over the CLEAN window and the fault recovery is
asserted separately, because the injected faults necessarily produce one long
segment delta and a dip in occupancy — reading them across the whole run would
make the gate contradict itself.
