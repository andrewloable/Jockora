<!--
Copyright (C) 2026 Andrew Loable
SPDX-License-Identifier: AGPL-3.0-only
-->

# Jockora

A self-hosted music streaming server where **AI DJs with persistent personalities**
turn your music library into a living radio station.

It layers on top of a library you already have. It never writes to it, never
retags it, never transcodes it. Point it at a folder, and a jock chosen to suit
what your library actually sounds like starts introducing your records.

> **Status: pre-1.0, and honest about it.** The station runs unattended, writes
> and speaks its own breaks, and recovers from a killed encoder. But **nobody
> outside this project has listened to it yet** — the room test with an
> unbriefed listener has not been run. Whether it is *enjoyable* is genuinely
> unknown. See [Where this actually is](#where-this-actually-is).

---

## Quickstart

You need Docker, a music folder, and a GGUF language model. Everything else is
in the compose file.

**1. Configure.** Compose reads `.env` automatically, so you set the paths once:

```sh
cp .env.example .env
$EDITOR .env          # MUSIC, CONFIG, MODELS, MODEL
```

**2. Fetch the speech models** into `<CONFIG>/models` (337 MB; operator-supplied
and deliberately not vendored, like ffmpeg):

```sh
mkdir -p config/models && cd config/models
curl -LO https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0/kokoro-v1.0.onnx
curl -LO https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0/voices-v1.0.bin
cd ../..
```

**3. Give the config directory to uid 10001.** The container runs unprivileged
and cannot chown a bind mount from inside. This is the most common first-run
failure:

```sh
sudo chown -R 10001 ./config
```

**4. Start it:**

```sh
docker compose up -d
```

**5. Make yourself an account.** Nobody can sign in until you do, and there is
no self-registration:

```sh
docker compose exec jockora jockora admin create -name you
```

The password is read from the terminal without echo — not from a flag or an
environment variable, either of which would leave it in shell history and in a
`ps` listing.

Open **http://localhost:8080**, sign in, and pick a station: the dial is the
whole listener UI. **http://localhost:8080/admin** is the operator console —
sources, stations, playlists, jocks and accounts — and only an admin account can
open it.

`jockora doctor` runs at startup and refuses to serve on a failed check, so a
broken setup names the failing check rather than going quiet. `docker compose
logs -f jockora` to watch it.

Compose starts `llama-server` alongside Jockora and waits for it to report
healthy first. Already run your own model server? Delete the `llm` service and
point `JOCKORA_LLM_URL` at it.

### The settings that matter

| Variable | What it does |
|---|---|
| `MUSIC` | Your library, mounted **read-only** |
| `CONFIG` | Database and voice models. Must be owned by uid 10001 |
| `MODELS` / `MODEL` | Directory holding your `.gguf`, and its filename |

Inside the container everything is a `JOCKORA_`-prefixed environment variable
with a matching flag. Every one is documented in
[docs/configuration.md](docs/configuration.md), which also carries a
troubleshooting guide organised around the failure you will actually see —
usually "the music plays and the DJ never says anything", which has four
different causes and four different fixes.

Handing the install to a coding agent instead?
[docs/deploying-with-an-agent.md](docs/deploying-with-an-agent.md) is written to
be executed rather than read: how to size the language model against the GPU you
actually have (and when a GPU is worth nothing at all), what to verify before
believing a deployment worked, and the ten failures that look like something
else.

## What to expect on a first run, honestly

- **Scanning is fast.** ~8 minutes for 7,700 tracks over a network mount, and it
  is incremental afterwards — a restart re-scans the same library in ~48 s.
- **The station goes on air as soon as the scan finishes.** Minutes, not hours.
- **Enrichment then runs for a long time in the background.** One LLM pass per
  track, cached forever. Roughly 19 hours for 5,000 tracks on a fast CPU, ~7 with
  a GPU. See [docs/hardware.md](docs/hardware.md) for measured numbers.
- **A track with no dossier yet is not broken.** The DJ talks from personality
  and asserts nothing — that is a designed state, not a degraded one. The
  practical effect is that the DJ knows more about your library each evening for
  the first few days.
- Enrichment is resumable. Stop the server, start it next week, it continues.

## Security: there is no authentication

**None. At all.** Anyone who can reach the port can listen to your stream and
read `/now.json`.

Jockora refuses to bind a non-loopback address unless you pass `--allow-lan`
(`JOCKORA_ALLOW_LAN=1`), and it logs a warning on every start when you do. The
supplied `docker-compose.yml` publishes on the LAN, which is a deliberate choice
you should make consciously rather than inherit.

**Do not port-forward this to the internet.** Do not point a tunnel at it. Put it
behind something that authenticates, or keep it on your own network.

## How it works

```
Music library  (local folder — READ-ONLY, never written)
      │
      ├──► Enrichment worker ──► Track Dossier  (SQLite)
      │      one LLM pass per track, cached forever, resumable
      │      lyrics are read then DISCARDED — only the dossier is stored
      ▼
Station engine ──► broadcast buffer: breaks pre-rendered to disk
      │
      └──► DJ Director: persona + dossiers ──► LLM ──► script ──► TTS
                                                              │
                          ffmpeg mix (crossfade, duck) ◄──────┘
                                     │
                                     ▼
                                    HLS
```

Two rules shape everything else:

- **The DJ may only assert facts present in a track's dossier.** Ungrounded fact
  ids are made *unrepresentable at the sampler*, not rejected afterwards. Across
  every break measured in development, no asserted fact ever failed to resolve.
- **Breaks are optional; music is not.** They are pre-generated into a lookahead
  buffer. If generation fails or runs late the break is dropped and the stream
  continues. It never stalls waiting for a DJ.

There is also no skip button. Changing station is the escape hatch — which is
what lets a break safely refer forward to what is coming up.

## Where this actually is

Measured, not asserted:

- Runs unattended and recovers — an 8/8 fault-injection gate covering decoder
  stalls, a killed ffmpeg, and playlist discontinuity.
- Writes, speaks and airs real breaks end to end, in the container.
- 100% groundedness across every break measured.
- 86% of breaks pass the checkable rubric once the length ladder runs.

Not yet known:

- **Whether it is any good to listen to.** Taste is deliberately not
  self-scored: the person who wrote the persona cannot hear it fresh. The test
  for that needs an unbriefed listener and has not been run.
- The real repetition rate over a month rather than a few hundred breaks.
- Whether the break cadence is right. It is the single most likely thing to be
  misconfigured.

## Licence

AGPL-3.0-only, with a commercial exception available. See
[LICENSING.md](LICENSING.md).

ffmpeg is an operator-supplied dependency run **out of process** — installed in
the image, never linked and never vendored. See
[LICENCES-MANUAL.md](LICENCES-MANUAL.md).

Community contribution runs through jock cards, voice profiles and clients
rather than patches; selling commercial exceptions requires a single copyright
holder.
