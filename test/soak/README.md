# Soak test

Between the 30-minute GATE 2 run and "live with it for a month" there was
nothing. That gap means a regression is discovered as a bad night rather than as
a red test run, and the things that live in it are exactly the things thirty
minutes cannot see.

## What only breaks at this timescale

- `said_lines` collision-query time as rows accumulate — it is a full table scan
  by design, so its cost grows with every break ever aired
- SQLite WAL growth with no checkpoint
- leaked decoder processes and file descriptors, one per track
- leaked goroutines, one per break
- cumulative resampler rounding across thousands of transitions and several
  encoder restarts
- TTS sidecar memory growth and respawn frequency

## Running it

It is behind a build tag, so `go test ./...` neither runs nor compiles it.

```sh
JOCKORA_SOAK_LIBRARY=/media/data1/music \
JOCKORA_SOAK_DURATION=24h \
go test -tags soak ./test/soak/ -run TestSoak -v -timeout 30h
```

Prove the harness works with a short run first:

```sh
JOCKORA_SOAK_LIBRARY=/media/data1/music \
JOCKORA_SOAK_DURATION=10m JOCKORA_SOAK_INTERVAL=1m \
go test -tags soak ./test/soak/ -run TestSoak -v -timeout 15m
```

## Running it on the Linux box: use a container

The target host has no ffmpeg -- only the container image does, which is the
whole point of ffmpeg being operator-supplied and out of process. So the soak
runs inside the shipping image, which already carries everything a Go test
binary needs:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -tags soak -c ./test/soak -o soak.test
scp soak.test <host>:~/soak/internal/tts/     # the test resolves ../../sidecar

docker run --rm -v /path/to/music:/music:ro -v "$HOME/soak":/w -w /w \
  -e JOCKORA_SOAK_LIBRARY=/music -e JOCKORA_SOAK_DURATION=24h \
  --entrypoint /w/soak.test <your jockora image> \
  -test.run TestSoak -test.v -test.timeout 30h
```

See `test/gate2/README.md` for why a container is the right answer here and not
merely a convenient one.

## Run it on the Linux box, not on the laptop

Not a portability limitation — a measurement one. Every number here is timing and
resource sensitive, and a laptop that sleeps, throttles and runs a browser
produces a trend that means nothing. The test warns when it detects macOS, and
**the open-descriptor check does not run there at all**: it needs
`/proc/self/fd`, and it FAILS rather than skipping quietly, because a metric that
reads zero and passes is the same shape as a gate that cannot fail.

## What it asserts, and why they are trends

| check | threshold |
|---|---|
| goroutines | last ≤ first + 10 |
| open descriptors | last ≤ first + 10 |
| said-lines collision p95 | < 50 ms |
| cumulative drift | < 500 ms |
| encoder restarts | < 1/hour |

The comparisons are first-sample-to-last, not against fixed ceilings, because a
leak shows as a rise that never comes back down and an absolute number cannot
tell those apart.

**A single encoder restart is not a failure.** ffmpeg dying once in a day is a
fact of life and the supervisor exists for it. A restart every hour is a trend.
The test reports the rate and only fails on the rate.

## Reading a failure

The p95 and the row count are printed together on every sample line for a
reason: if the collision query crosses 50 ms, the question is whether it crossed
because the table grew or because something else did, and one number cannot
answer that.
