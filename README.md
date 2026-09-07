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

## The five sentences that make the rest obvious

- **Your music library is read-only input.** Jockora never writes to it, retags
  it or transcodes it. It layers on top of what you already have.
- **Enrichment runs one language-model pass per track and stores a dossier.**
  Lyrics are read and then discarded; the dossier is the only thing the DJ is
  allowed to assert facts from. No dossier means personality-only talk, which is
  a designed state rather than a broken one.
- **A station is a genre and mood filter over those dossiers**, materialised
  into a playlist you can pin and exclude tracks in. The admin owns the dial.
- **A jock is a persona card**: a voice, a speech style, a personality, and a
  list of things it will never say. One jock per station. Listeners pick
  stations, never jocks.
- **The server renders one shared HLS stream per station, only while somebody is
  listening.** Clients are dumb players. Breaks are optional; the music is not.

Everything below is a consequence of those five.

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

## Install on a host, without containers

The same program, assembled by hand. Every command below was run on a clean
directory before it was written down; the output quoted is what it actually
printed.

**1. Build it.** The browser app first: `web/dist` is build output and the
binary embeds it, so skipping this compiles, links, starts, and serves an
"assets not built" notice to every listener.

```sh
cd web/app && npm ci && npx ng build && cd ../..
CGO_ENABLED=0 go build -o jockora ./cmd/jockora
```

**2. Install ffmpeg, and check the two filters.** Operator-supplied and run out
of process, never linked — that is a licensing requirement, not a preference.
Both filters are used on every break:

```sh
ffmpeg -hide_banner -filters | grep -E ' (loudnorm|aresample) '
```

**3. Make the speech sidecar's virtualenv.** Python 3.10; kokoro-onnx pulls
onnxruntime with it.

```sh
python3.10 -m venv tts
./tts/bin/pip install kokoro-onnx
./tts/bin/python -c "import kokoro_onnx"
```

**4. Fetch the voice models** (337 MB, operator-supplied for the same reason
ffmpeg is):

```sh
mkdir -p models && cd models
curl -LO https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0/kokoro-v1.0.onnx
curl -LO https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0/voices-v1.0.bin
cd ..
```

**5. Start a language model.** Either `llama-server` from llama.cpp with a GGUF
on disk, or a hosted OpenAI-compatible endpoint — see **Hosted endpoints** in
[docs/configuration.md](docs/configuration.md). Note the port: Jockora looks at
**8081**, because it is already using 8080 itself.

```sh
llama-server -m /path/to/model.gguf --host 127.0.0.1 --port 8081
```

**6. Point Jockora at all of it.** Every flag has an environment variable:
`JOCKORA_` plus the flag name uppercased, dashes to underscores, so
`-library-path` is `JOCKORA_LIBRARY_PATH`.

```sh
export JOCKORA_LIBRARY_PATH=/path/to/music
export JOCKORA_DB_PATH=$PWD/jockora.db
export JOCKORA_PERSONA=personas/
export JOCKORA_TTS_PYTHON=$PWD/tts/bin/python
export JOCKORA_KOKORO_MODEL=$PWD/models/kokoro-v1.0.onnx
export JOCKORA_KOKORO_VOICES=$PWD/models/voices-v1.0.bin
```

**7. Check it before starting it.** `jockora doctor` names what is missing and
what to do about it, and `serve` refuses to start on a failed check:

```
$ ./jockora doctor
JOCKORA PREFLIGHT
  [ok  ] ffmpeg                 /opt/homebrew/bin/ffmpeg
  [ok  ] ffprobe                /opt/homebrew/bin/ffprobe
  [ok  ] ffmpeg filters         loudnorm, aresample, afade, volume present
  [ok  ] llama-server           http://127.0.0.1:8081/completion honoured a json_schema round-trip
  [ok  ] tts sidecar            can import kokoro_onnx, and the voice models are present
  [ok  ] library path           /path/to/music
  [ok  ] segment dir            segments writable
  [ok  ] database               jockora.db
  [ok  ] free disk              139.0 GiB free
```

**8. Make yourself an account, then start.** Nobody can sign in until you do,
and there is no self-registration:

```sh
./jockora admin create -name you     # asks for a password, twice, without echo
./jockora serve
```

`admin create` writes to `JOCKORA_DB_PATH`, so **export it rather than putting
it in front of a pipe** — a variable set on the left of a `|` belongs to the
left-hand command, and the account lands in a different database than the one
the server reads. The account is created, the message says so, and sign-in then
fails with 401. It cost half an hour to find; it takes one `export` to avoid.

Open **http://127.0.0.1:8080**, sign in, and pick a station.

## What it will read

A watched folder is scanned for six containers:

`.mp3` &nbsp; `.flac` &nbsp; `.m4a` (AAC and ALAC) &nbsp; `.ogg` &nbsp; `.opus` &nbsp; `.wav`

Anything else is passed over in silence rather than reported as broken, so a
folder of `.wma`, `.aiff`, `.aac` or `.mp4` files looks empty. That list is a
scanner filter, not a decoder limit — playback is ffmpeg, which reads far more
than six formats.

An **OpenSubsonic source has no such filter**: whatever the server lists is
ingested and streamed through it, so a file Jockora skips on disk plays fine
through Navidrome.

A file with the right extension but no audio stream in it — a video somebody
dropped in the folder — is marked unplayable during the scan and never
selected. Deliberately at scan time: a scan is allowed to take an hour, a
stream is not allowed to find out mid-transition.

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

## Security

**There are accounts, and there is no TLS.** Both matter.

Sign-in is a bcrypt-hashed password for a signed, HttpOnly session cookie
lasting 30 days, rate-limited to 10 failed attempts a minute. Every account is
created by an admin — there is no self-registration anywhere, by design. The
signing key comes from `JOCKORA_SESSION_KEY`, or is generated on first run and
kept in the database.

**The stream itself requires a session.** `/hls/` answers `401 sign in to
listen` without one, because a listener *is* their session: presence is what
keeps a station on air, so an anonymous one would let anyone hold a station up.

Two things stay public on purpose:

- **`/now.json`** — what is playing, the last thing the DJ said, and enrichment
  progress. Anyone who can reach the port can read it.
- **The app shell and the sign-in page.** The APIs behind them are guarded; the
  HTML is not worth hiding.

Jockora refuses to bind a non-loopback address unless you pass `--allow-lan`
(`JOCKORA_ALLOW_LAN=1`), and logs a warning on every start when you do. The
supplied `docker-compose.yml` publishes on the LAN — a deliberate choice you
should make consciously rather than inherit.

**Do not port-forward this to the internet.** It speaks plain HTTP, so the
session cookie's `Secure` flag never sets and every password crosses the wire in
clear. None of this has been through a security review. If it has to leave your
network, put it behind a reverse proxy that terminates TLS.

## How it works

```
Music library  (local folder or OpenSubsonic — READ-ONLY, never written)
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

## How it is built

The diagram above is the audio. This is the program: **one Go binary**, two
processes beside it that it does not own, and one file of state.

```
                    ┌──────────────── one Go binary: cmd/jockora ────────────────┐
   browser  ───────►│  server + web       the Angular app, embedded              │
   / at 8080        │                     / listener dial   /admin console       │
                    │                                                            │
                    │  AUDIO PATH         decode → mix → sched → encode          │
                    │                     per-track ffmpeg, one paced mix bus,   │
                    │                     one long-lived encoder per station     │
                    │                                                            │
                    │  STATION BRAIN      library → enrich → station → dj        │
                    │                     scan, dossiers, playlists, breaks      │
                    │                                                            │
                    │  PLUMBING           store auth config clock obs doctor tts │
                    └───┬──────────────┬──────────────┬────────────────┬─────────┘
                        │              │              │                │
                        ▼              ▼              ▼                ▼
                   ┌─────────┐   ┌──────────┐   ┌───────────┐   ┌─────────────┐
                   │ SQLite  │   │ ffmpeg   │   │ speech    │   │ language    │
                   │ one file│   │ ffprobe  │   │ sidecar   │   │ model server│
                   └─────────┘   └──────────┘   └───────────┘   └─────────────┘
                    tracks,        OPERATOR-        shipped,       OPERATOR-
                    dossiers,      SUPPLIED         supervised     SUPPLIED
                    stations,      copyleft,        by the         llama.cpp or
                    playlists,     out of           binary         a hosted
                    jocks,         process,         (Python,       endpoint
                    accounts,      never linked     Kokoro)
                    said lines
```

Two things about that shape are decisions rather than accidents:

- **Everything is rendered server-side and leaves as HLS**, which is what keeps
  every client dumb. A browser, a phone, a car head unit and a Chromecast all
  play the same stream, and none of them contains any mixing logic to get wrong.
- **The copyleft tools run out of process and are supplied by you.** ffmpeg is
  GPL; Jockora is AGPL-3.0-only with a commercial exception available. Shelling
  out to a tool the operator installed is mere aggregation, and it is the reason
  both licences can hold at once. Nothing GPL is ever linked into the binary.

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
