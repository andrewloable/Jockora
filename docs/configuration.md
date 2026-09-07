<!--
Copyright (C) 2026 Andrew Loable
SPDX-License-Identifier: AGPL-3.0-only
-->

# Configuration

Every flag has a `JOCKORA_`-prefixed environment equivalent: `-library-path`
is `JOCKORA_LIBRARY_PATH`, `-break-every-n-tracks` is
`JOCKORA_BREAK_EVERY_N_TRACKS`. The flag wins when both are set.

`jockora <command> -h` prints the live list. This page explains what changing
each one does *audibly*, which `-h` cannot.

## Deploying this on a server

[deploying-with-an-agent.md](deploying-with-an-agent.md) is the step-by-step
install, written for a coding agent with shell access to the host but usable by
hand. It covers the decisions this page does not: sizing the language model
against the GPU that is actually present, what to measure before keeping a
tuning change, and how to tell a deployment that works from one that merely
starts.

## Building from source needs Node. Running a release does not.

**If you downloaded a release or pulled the image, skip this.** The browser app
is already built and embedded in the binary; there is nothing to install and no
Node on the machine that runs the station.

If you are building from a checkout, the web app is a separate build:

```
make web          # npm ci, then ng build into web/dist
go build ./cmd/jockora
```

`make web` is deliberately **not** a dependency of anything else, because the Go
build has to work on a machine with no Node at all. When `web/dist` is empty the
binary still compiles and still streams — it serves a short page saying the
assets were not built, instead of the console. That is the tell if you ever see
it in a browser.

`make gates` includes the npm licence gate, which skips itself when
`web/app/node_modules` is absent. CI installs Node, so there it always runs. See
`LICENCES-MANUAL.md`.

## Library and storage

| Flag | Default | What it changes |
|---|---|---|
| `-library-path` | — | Music root. **Read-only**; never written, retagged or transcoded. |
| `-db-path` | `jockora.db` | Dossiers, said-lines, scan state. Delete it and every track re-enriches. |
| `-subsonic-url` | — | Read the library from an OpenSubsonic server (Navidrome, Airsonic, Gonic) instead of a folder. Wins over `-library-path` when both are set. |
| `-subsonic-user` | — | OpenSubsonic username. |
| `-subsonic-password` | — | Prefer `JOCKORA_SUBSONIC_PASSWORD`; a password on a command line is visible in `ps`. |
| `-segment-dir` | `segments` | HLS segments. Transient; a tmpfs is ideal. |

### Reading from Navidrome instead of a folder

If you already run Navidrome, Airsonic or Gonic, point Jockora at it and skip
the folder scan entirely:

```sh
jockora serve -subsonic-url https://navidrome.example -subsonic-user you
```

Tracks are stored as stream URLs, so ffmpeg fetches audio over HTTP exactly as
it would read a file. **The connection is read-only by construction** — the
provider interface has no method that writes, and a test asserts every request
is a GET and that no rating, star, scrobble or playlist endpoint is ever called.

One consequence worth knowing: a long-lived auth token for that account is
written into the local database, because the stored URL has to carry its own
credentials. That is the same trust boundary as the password in your config.

## The DJ

| Flag | Default | What it changes |
|---|---|---|
| `-persona` | — | A `.toml` card, or a **directory** to pick from by what the library sounds like. |
| `-break-every-n-tracks` | `4` | **The knob most likely to be wrong.** See below. |
| `-station` | — | Which station to broadcast. |

### JockPacks

A JockPack **is** the persona file. There is no archive and no manifest: one
TOML, human-readable and diffable, installed by dropping it in the personas
directory. Every card written before the format existed is already a valid pack.

Beyond the persona card it may carry `[[advert]]` entries and a `[chain]` hint,
plus `author` and `licence`. Adverts are validated **at install**, not at
airtime — a pack that would name a real company on air fails to load, because
packs are this project's only community contribution surface and that is exactly
where such a name arrives from outside.

Adverts air in one break slot in four, with a 90-minute floor between them.
A pack with no adverts changes nothing.

**Break cadence is the setting you will actually want to change.** Spotify's AI
DJ draws its loudest complaints for talking too much. If you find yourself
wishing it would be quiet, raise this number *before* concluding the writing is
bad. Going from 4 to 8 halves how often you hear a voice and changes nothing
else.

## Language model

| Flag | Default | What it changes |
|---|---|---|
| `-llm-url` | `http://127.0.0.1:8081` | Endpoint. |
| `-llm-api` | `llamacpp` | `llamacpp`, `ollama`, or `openai` for any OpenAI-compatible host. |
| `-llm-api-key` | — | Prefer `JOCKORA_LLM_API_KEY`; a key on a command line is visible in `ps`. |
| `-llm-model` | — | A `.gguf`. Set this and Jockora starts and supervises `llama-server` itself. |
| `-llm-binary` | `llama-server` | Used only with `-llm-model`. |
| `-llm-context` | `8192` | Context size. Below ~4096 the enrichment prompt stops fitting. |
| `-llm-gpu-layers` | `99` | `0` forces CPU. See [hardware.md](hardware.md) — the GPU buys about 2.6×, not an order of magnitude. |

## Speech

| Flag | Default | What it changes |
|---|---|---|
| `-tts-python` | `python3.10` | Interpreter with `kokoro-onnx` installed. |
| `-tts-script` | `sidecar/kokoro_server.py` | Sidecar entry point. |
| `-tts-url` | `http://127.0.0.1:8090` | Attach to a sidecar you run instead. |

The voice comes from the persona card's `voice_id`, not from a flag.

## Tuning

Every default here is the value the project was measured with. **Changing
nothing changes nothing**, and a test asserts these defaults so they cannot
drift away from the gates that were run against them. All are applied once at
startup.

| Flag | Default | What it changes, audibly |
|---|---|---|
| `-music-lufs` | `-16` | Overall music loudness. Room and speaker dependent. |
| `-music-ducked-lufs` | `-28` | How far music drops under the DJ. Closer to `-16` and the voice fights the music; further and the music vanishes. |
| `-speech-lufs` | `-16` | Voice loudness. Matched to music on purpose. |
| `-true-peak-ceiling` | `-1` | dBTP ceiling. Raising it invites clipping on cheap speakers. |
| `-lookahead-seconds` | `150` | How far ahead a break is generated. Lower drops more breaks on a slow machine; higher makes them refer to what is coming from further away. |
| `-ad-every-n-breaks` | `4` | One break slot in N becomes an advert. **The most taste-sensitive number here.** |
| `-ad-interval-minutes` | `90` | Floor between adverts regardless of the ratio. |
| `-fact-confidence` | `0.6` | How sure a dossier must be before the DJ may assert from it. Lower gives a chattier DJ leaning on weaker facts; higher gives more personality-only talk. |

`-sample-rate` and `-channels` are the canonical bus. They are flags for
historical reasons and changing them is not supported.

## Audio and streaming

| Flag | Default | What it changes |
|---|---|---|
| `-sample-rate` | `48000` | Canonical bus rate. Changing it is not supported. |
| `-channels` | `2` | Canonical bus channels. |
| `-crossfade-seconds` | `2` | Track-boundary crossfade. |
| `-segment-seconds` | `4` | Segment duration. Lower is more responsive, more HTTP requests. |
| `-list-size` | `10` | Playlist window. Segment × list = how far a client can rewind. |
| `-listen-addr` | `127.0.0.1:8080` | HTTP bind address. |
| `-log-format` | inferred | `text`, `json`, or empty to infer — JSON when stdout is not a terminal. |
| `-allow-lan` | off | **Required for any non-loopback bind. There is no authentication.** |

## Coverage sampling

`-sample` (with `enrich`) takes a track count and measures synced-lyric coverage over N
sampled tracks and stops, instead of enriching. It answers "how many of my
tracks will the DJ actually know anything about", which decides how much of the
talking is personality-only.

```sh
jockora enrich -sample 200 -library-path /music
```

## Room-test splicing

`-break`, `-break-at` and `-break-every` splice one **pre-recorded WAV** on a
timer. That is the room-test-A harness, not the DJ. Leave them unset for a real
station.

## Track analysis

Alongside enrichment, a background pass measures each track's **loudness** and
**tempo**. Loudness needs a full decode and tempo needs real analysis, so
neither belongs in the scan — the scan reads tags and stops, which is what keeps
it minutes rather than hours.

Loudness is the audible half: without it every track plays at its native
mastering level, and the −16 LUFS contract is enforced for speech only. A
development library measured −13 to −23 LUFS across four tracks, which is a
10 dB swing between songs.

Tempo is optional and only smooths the running order. It comes from librosa in
the speech sidecar — no extra process, no cgo — and an implausible result is
discarded rather than stored, so a track may legitimately have no tempo. The
selector widens its matching window until something fits, so those tracks are
still played.

## The operator console

`GET /admin` is the operator console: a section for the overview, one for
sources, one for stations, one for the playlist of a station, one for jocks and
one for people. It is behind a guard — a listener who opens it is sent back to
the dial, and somebody with no session goes to the sign-in form.

The **overview** is what the station knows about itself: library and dossier
progress, loudness and tempo coverage, measured enrichment cost with a
projection of the time remaining, the advert pool, the break cadence, and the
listener thumbs-down feed. Two things are changeable there without a restart:
the break cadence, and pausing enrichment to hand the machine back for an
evening without stopping the station.

**Reads of `/now.json` are open; everything else needs a session.** `/now.json`
already exposes its class of information to anyone who can reach the stream, so
gating it would break the player and protect nothing. Everything under
`/admin/` requires the admin role, and the listener endpoints require a signed-in
listener.

Create the first operator account from the machine running the server:

```
jockora admin create -name andrew
```

The password is read from the terminal without echo — never from a flag or an
environment variable, either of which would leave it in shell history or in a
`ps` listing. It must be at least 8 characters; a long passphrase is better than
a short one with a digit stapled on. **Accounts are admin-created and there is
no self-registration**, which is why this command exists at all: a fresh install
has nobody who can sign in to create anybody. Every account after the first is
made in the console's **people** section, which is the only place in the product
that can create one.

Tuning a station is a **listener** control and is not in the console — it
changes what is playing now, which is what the dial is for.

## The dial

`GET /stations.json` returns the proposed dial: one entry per station with its
track count, its matched jock and the moods that actually occur in it, plus
`enriched` and `total` so a client can say how provisional the proposal is.

It is cached and recomputed every 5 minutes, never built per request — it reads
every dossier in the library, which is fine on a timer and absurd on every poll.
On a fresh library most tracks land in `unsorted`, and the dial genuinely does
fill in over the following days as enrichment classifies them: stations appear
once a tag reaches 8 tracks. A failed recompute keeps the previous dial rather
than emptying it.

**Tuning.** `POST /tune` with `{"tag":"rock"}` switches station. The song
playing is not interrupted — the change is heard when it ends. A station with no
playable tracks is refused with a 400 and you stay where you were.

**The roster.** `GET /jocks.json` lists all nine personas with their voices,
genres and moods, and marks who is on air. `POST /jock` with `{"id":"..."}`
puts a different one on. The persona, the said-lines index and the TTS voice
move together — a jock changed in only one of those speaks as one character in
another's voice, or inherits someone else's phrase history and gets its own
writing rejected as repetition. A break already being generated airs in the
previous voice; the change is heard from the next one.

**Feedback.** `POST /feedback` with `{"verdict":"down"}` records what you
thought of the break that just aired, into `break_feedback`. It stores the
break's TEXT rather than an id, because breaks are written, aired and gone —
what a later prompt-tuning pass needs is the sentence somebody disliked.

Two buckets never get a jock: `unsorted` (no dossier yet) and `other` (enriched,
but fitting no genre in the closed vocabulary). Neither describes a sound, so
there is no character to match a persona against.

---

# Troubleshooting

**Run `jockora doctor` first.** It checks ffmpeg, the model endpoint, the speech
sidecar, the library, the segment directory, the database and free disk, and it
names the fix rather than only the problem.

Most failures here look identical from the outside — the music keeps playing and
the DJ says nothing. That is deliberate: **breaks are optional, music is not.**
So each entry below names the log line or `/now.json` field that tells them
apart.

### Reading from Navidrome instead of a folder

If you already run Navidrome, Airsonic or Gonic, point Jockora at it and skip
the folder scan entirely:

```sh
jockora serve -subsonic-url https://navidrome.example -subsonic-user you
```

Tracks are stored as stream URLs, so ffmpeg fetches audio over HTTP exactly as
it would read a file. **The connection is read-only by construction** — the
provider interface has no method that writes, and a test asserts every request
is a GET and that no rating, star, scrobble or playlist endpoint is ever called.

One consequence worth knowing: a long-lived auth token for that account is
written into the local database, because the stored URL has to carry its own
credentials. That is the same trust boundary as the password in your config.

## The DJ never talks

The single most common report, and it has four distinct causes.

1. **No persona.** Log: `no DJ: no persona configured`. Set `-persona`.
2. **The model is unreachable.** `/now.json` → `health.llm`. Not `"ok"` means
   every break fails before it is written.
3. **Speech is failing.** `/now.json` → `health.tts`. A wrong `voice_id` fails
   *every* synthesis with a 503 while the station plays on perfectly — this once
   cost 20 breaks out of 20 with nothing appearing wrong.
4. **Breaks are being written and dropped.** `/now.json` →
   `metrics.break_drop_rate`. Above ~0.10 the writer is repeating itself or
   overrunning; check `last_break` to see what did air.

Also check `enrichment.done` / `.total`. Early on, most tracks have no dossier,
so the DJ has little to say — that is expected, not broken.

## The stream stalls after a few minutes

- `/now.json` → `metrics.underruns` climbing means the mixer is not keeping the
  ring fed; `metrics.ring_occupancy_s` shows how close to dry it ran.
- `metrics.encoder_restarts` above zero means ffmpeg died and was respawned.
  One is a recovery working; a rising count is a real fault.
- Free disk. Segments are swept by the server, not by ffmpeg, so a stuck sweeper
  fills the segment directory.

## Nothing plays at all

`jockora doctor`. Then the log: `no playable tracks under <path>` means the scan
found nothing — the mount is empty or the wrong directory. `library scanned`
with `found: 0` says the same thing.

## Speech is clipped, or too quiet under the music

The contract is fixed: music −16 LUFS, speech −16 LUFS, ducked to −28 LUFS
(−12 dB), true-peak ceiling −1 dBTP. If speech sounds buried, the music's
measured loudness is likely missing, so it was never normalised — check
`loudness_lufs` in `tracks`.

## Breaks land on top of the vocal

The DJ aims at a track's instrumental ramp or outro. Where those are unmeasured
it falls back to the between-track gap. Check `ramp_s`, `outro_s` and
`ramp_confidence` in `tracks`; if they are null for most of the library, most
breaks are landing between tracks rather than woven in.

## It says the same thing about the same artist

Known and measured. Artist facts come from MusicBrainz, which supplies about one
usable sentence per artist, so that sentence recurs whenever the artist does.
Prompt wording, fact cooldowns and withholding facts have each been measured
against this and none helped; richer per-track dossiers are the actual lever.

## First run seems to hang

It is scanning. ~8 minutes for 7,700 tracks over a network mount; later scans of
an unchanged library take about 48 seconds. Progress is logged every 500 files.
