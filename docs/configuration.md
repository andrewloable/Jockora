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
| `-break-overlap` | `3` | Seconds of music that play under each end of a break. See below. |
| `-station` | — | Which station to broadcast. |

### The music under a break

`-break-overlap` is how many seconds of music play underneath each end of a
break. Three by default, and the console has the same control on the Overview
page — the stored value outranks this flag, because a setting an operator chose
must survive a restart.

A break is heard like this:

```
outgoing record ─────────────╮
                             ╰── 3s under the DJ
DJ                     ┌─────────────────────────────────┐
                       │  3s  │   in the clear    │  3s  │
                       └─────────────────────────────────┘
                                                  ╭────────────────
incoming record ──────────────────────────────────╯
```

The outgoing record ends three seconds into the break, the DJ talks in the
clear, and the incoming record comes up under the last three seconds. **The
music pauses in the middle**, for however long is left of the break — about nine
seconds of a fifteen second one. That silence is the point rather than a fault:
it is what leaves the DJ in the clear.

**It is not bounded by the music.** The overlap applies to every break at both
ends whatever the track is doing, so a record that sings to its final second
gets talked over for three of them, and so does one that starts singing
immediately.

That is deliberate. It used to be capped by the outgoing track's measured
instrumental outro and the incoming track's measured intro — the rule that a
break never lands over a vocal — but only about a third of a real library
carries those measurements, so the cap refused the overlap on most boundaries
and the setting looked broken. The caps were removed on instruction rather than
by accident.

It also applies whichever side of the transition the DJ's words are about.
Breaks introducing the incoming record used to get no overlap at all, and since
that is the placement the station prefers, the setting almost never did
anything.

**The setting is the only bound left.** If breaks start clipping vocal endings,
lower it or set it to `0`.

Set it to `0` for the old behaviour — every break starting exactly at the
transition, with no gap and no music underneath it.

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

### Hosted endpoints

`-llm-api openai` posts to `{-llm-url}/chat/completions` with an
`Authorization: Bearer` header and asks for structured output as
`response_format: {"type": "json_schema", "json_schema": {"name", "strict",
"schema"}}`. Any host that accepts that shape works with no code change; set
three environment variables and nothing else.

| Host | `JOCKORA_LLM_URL` | `JOCKORA_LLM_MODEL` |
|---|---|---|
| OpenRouter | `https://openrouter.ai/api/v1` | `nvidia/nemotron-nano-9b-v2:free` |
| Cloudflare AI Gateway → OpenRouter | `https://gateway.ai.cloudflare.com/v1/<account>/<gateway>/openrouter/v1` | as above |
| Cloudflare Workers AI | `https://api.cloudflare.com/client/v4/accounts/<account>/ai/v1` | `@cf/meta/llama-3.1-8b-instruct` |

**Cloudflare AI Gateway is the one to reach for**, because its
provider-specific route is a transparent proxy: the URL above is the OpenRouter
URL with a prefix, the key stays *your OpenRouter key* in the ordinary
`Authorization` header, and the body is forwarded untouched — so schema
enforcement is whatever OpenRouter gives you, not something the gateway
weakens. What it adds is per-request logging, caching, rate limiting and
fallback across a library-sized enrichment run of several thousand calls, which
is otherwise invisible.

Use that route rather than the gateway's own `/compat/chat/completions`
endpoint. That one authenticates with a `cf-aig-authorization` header, which
Jockora does not send.

**Workers AI works, and costs the schema.** Its free daily allocation is real
and it needs no third-party account, but two things bite:

- Its JSON mode is **best-effort**, not enforced at the sampler. It returns an
  error when the model will not comply. Jockora's anti-hallucination design
  leans on the schema enum making an ungrounded fact *unrepresentable*;
  best-effort demotes that to post-validation, which rejects rather than
  prevents.
- It expects `json_schema` to be the schema itself. Jockora sends OpenAI's
  wrapper — `{"name", "strict", "schema"}` — so the schema is likely ignored
  rather than applied.

Both failures are silent from the outside: the model still answers every
request. Run `jockora doctor` after changing any of this. It does a live
`json_schema` round trip rather than a reachability ping, precisely because a
host that ignores the schema looks healthy right up until a break asserts a
fact no dossier supports.

**Free tiers are rate limited**, and enrichment is one request per track. A
7,000-track library will exceed a daily allowance; the queue is serial and
resumable, so this costs days rather than correctness.

**Track metadata and lyrics leave the machine** on any hosted endpoint. The
read-then-discard guarantee still holds locally — nothing is stored — but the
text is sent to a third party on the way.

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
| `-allow-lan` | off | **Required for any non-loopback bind.** Accounts guard the stream and the APIs; there is no TLS. |
| `-session-key` | generated | Secret that signs session cookies, 32 bytes or more. See below. |

### The session key

`-session-key`, or `JOCKORA_SESSION_KEY`, is the secret every sign-in cookie is
signed with. **Leave it empty and Jockora generates one on first run and keeps
it in the database**, which is the right answer for a single host: sessions then
survive restarts with nothing for you to store.

Set it yourself when the database is not the only copy of your state — restoring
an older backup would otherwise roll the key back and sign everybody out, and
two Jockoras sharing one library cannot verify each other's cookies without it.
Prefer the environment variable: a flag is visible in the process list to every
user on the machine.

Changing it signs out every session immediately, which is also the blunt way to
do that on purpose.

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
listener — unless public listening is on.

### Letting the DJ describe songs nothing was looked up about

The overview has a second switch with real consequences: **"Let the DJ describe
songs the library could not look up."** Off by default.

Enrichment has exactly two external sources. LRCLIB supplies lyrics, and it has
synced words for about 41% of a real library. MusicBrainz supplies four
primitives about the artist — country, group or person, founding year, and a
disambiguation note — and nothing else. There is **no web search anywhere in
this program.** So for the other half of a library the DJ has nothing to say
about the song itself and falls back to personality.

With this on, the model fills `subject_summary` and `themes` from its own
knowledge for exactly those tracks. **That is a deliberate weakening of the rule
the whole DJ design rests on** — that the DJ may only assert what a named source
supplied — and it is opt-in for that reason.

It is bounded as tightly as it can be:

- **Two fields only.** Artist facts still need a real MusicBrainz hit, the
  release still comes from the file's own tags, and `notable_line` stays empty
  because a lyric quoted from memory is a misquote.
- **Lyrics always win.** Recall applies only where LRCLIB found nothing.
- **The model must commit.** The schema gains a required `recognised` boolean,
  so declining is something the sampler can express. A `false` blanks both
  fields. Telling a small model to "leave it empty if you do not know" does not
  work; making the refusal representable does.
- **It is labelled.** Every recalled dossier carries `model` in its `sources`,
  beside `musicbrainz` and `lrclib`, so what grounded a break stays traceable.

**Measured on gemma-4-E4B-it-Q6_K**, 12 tracks: 7 of 8 real songs described,
every description accurate, including a Filipino OPM track; 0 of 4 invented
titles described. The one refusal was a genuinely obscure OPM record, which is
the conservative direction. That is one model and a small sample — re-run
`TestLiveRecall` in `internal/enrich/` against your own model before trusting it.

Obscure libraries benefit least: a model refuses what it does not know, which is
correct and also means an OPM- or bootleg-heavy library will see fewer tracks
gain meaning than a mainstream one.

#### It does nothing until you clear the old dossiers

A dossier is written once and kept for the life of the library, and the
enrichment queue only ever visits tracks that have **no dossier row at all**. So
turning this on changes nothing about a library that is already enriched.

The overview says how many stored dossiers say nothing about what the track is
about **and were written before you changed this setting**, and offers **Clear
and re-enrich**. That deletes only those rows — a dossier built from real lyrics
is never touched — and wakes the enrichment worker, which parks itself once
there is no work left. Those tracks leave the dial until their replacement
lands, which is why it is a button and not something that happens on its own.

**The cutoff is what stops it looping.** Recall does not rescue every track: a
model refuses what it does not know, so re-enrichment writes a fresh empty
dossier for each track it still cannot describe. Without a cutoff the console
would offer to clear those too, you would spend a night of model time
reproducing them exactly, and it would offer again. Only rows written under the
older setting are ever offered, so the button empties itself and stays empty.

Turning the setting off and on again re-stamps the cutoff, which offers every
meaningless dossier written before that moment for another try. That is the
escape hatch if you change models. It never re-offers a dossier that says
something: those are only ever written once.

### Letting anyone listen

The overview has one switch that changes **who can reach the product**: *Anyone
can listen without signing in*. Off by default, and off is what an install that
has never been asked stays.

With it on, the dial (`/stations.json`), tuning (`POST /tune`) and the stream
(`/hls/…`) answer callers with no account at all. **The console is not affected
and cannot be.** The switch only ever opens the listener role, so every
`/admin/` route keeps asking for an operator account whatever it is set to —
which is what makes leaving it on a decision about the dial rather than a
decision about the server.

Two things a guest does not get:

- **A thumbs-down.** A verdict is recorded against an account, and an unowned
  one in the feed the console reports as what listeners said would be worse
  than none. The control is absent for a guest rather than present and failing.
- **A thirty-day session.** A guest carries a `jockora_guest` cookie that lasts
  a day and does nothing but tell one browser from another, so presence can
  start and stop the station they are on. It is not a credential and it is
  deliberately not the session cookie.

The setting outlives a restart, in both directions. It is stored in the
database rather than in a flag, for the same reason the cadence is: a door that
silently reopened on every restart is one nobody can rely on having shut.

**It is a door onto whatever network can reach this server**, which on the usual
plain-HTTP LAN install is the whole house and anything else on that subnet.
Combined with `-allow-lan` it is the whole local network. Leave it off unless
that is what you want.

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
