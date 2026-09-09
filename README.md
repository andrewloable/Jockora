<!--
Copyright (C) 2026 Andrew Loable
SPDX-License-Identifier: AGPL-3.0-only
-->

# Jockora

**A radio station for the music you already own.** Point it at your library and
it does not build a playlist — it builds a station: AI DJs with fixed
personalities who introduce your records, backsell the one that just played, and
tell you what is coming next.

It layers on top of a library you already have. It never writes to it, never
retags it, never transcodes it.

> **Status: v0.2, running.** The station runs unattended, writes and speaks its
> own breaks, and recovers from a killed encoder. What is measured and what is
> still unknown are both listed in [Where this actually is](#where-this-actually-is)
> — including the one thing that matters most and is hardest to score.

---

## What it is, in five sentences

Everything else follows from these.

- **Your music library is read-only input.** Jockora layers on top of what you
  already have and never modifies it.
- **One language-model pass per track produces a dossier, cached forever.**
  Lyrics are read and then discarded; the dossier is the only thing a DJ may
  assert facts from. No dossier means personality-only talk — a designed state,
  not a broken one.
- **A station is a genre and mood filter over those dossiers**, materialised
  into a playlist you can pin and exclude tracks in. The admin owns the dial.
- **A jock is a persona card**: a voice, a speech style, a personality, and a
  list of things it will never say. One jock per station; listeners pick
  stations, never jocks.
- **The server renders one shared HLS stream per station, only while somebody is
  listening.** Clients are dumb players. Breaks are optional; the music is not —
  if a break runs late it is dropped and the stream never stalls.

### What it is not

Not a player, not a library manager, not a recommender. There is **no skip and
no seek** — changing station is the escape hatch, and that is what lets a DJ
safely say what is coming up without anything invalidating it.

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
        HLS — one shared stream per station, while someone is listening
```

**One Go binary** serves the Angular app, runs the audio path and holds the
station brain. Beside it sit three things it does not own: **ffmpeg** and
**ffprobe**, a **speech sidecar** (Python, Kokoro), and a **language model** —
llama.cpp locally or a hosted endpoint. State is one SQLite file.

The copyleft tools run **out of process and are supplied by you**. ffmpeg is
GPL; Jockora is AGPL-3.0-only with a commercial exception. Shelling out to a
tool you installed is mere aggregation, which is how both licences hold at once.
Nothing GPL is ever linked into the binary.

## Running it

**Deployment lives in its own documents, not here.**

| | |
|---|---|
| Install, by compose or by hand | [docs/install.md](docs/install.md) |
| Every setting, and which ones matter | [docs/configuration.md](docs/configuration.md) |
| Do I need a GPU? | [docs/hardware.md](docs/hardware.md) |
| Checks that catch a deploy which looks fine and is not | [docs/deploying-with-an-agent.md](docs/deploying-with-an-agent.md) |

The short version: Docker, a music folder, and a GGUF model. The station is on
air as soon as the first scan finishes — minutes, not hours. Enrichment then
runs for a long time in the background and is resumable, so the DJ knows more
about your library each evening for the first few days.

## Security

**There are accounts, and there is no TLS.** Both matter.

Sign-in is a bcrypt-hashed password for a signed, HttpOnly session cookie
lasting 30 days, rate-limited to 10 failed attempts a minute. Every account is
admin-created — there is no self-registration anywhere, by design. Signing out
revokes the session server-side rather than only clearing the cookie.

**The stream itself requires a session**: `/hls/` answers `401` without one,
because a listener *is* their session — presence is what keeps a station on air.
Two things stay public on purpose: `/now.json`, and the app shell and sign-in
page.

Jockora refuses to bind a non-loopback address unless you pass `--allow-lan`,
and warns on every start when you do.

**Do not port-forward this to the internet.** It speaks plain HTTP, so the
cookie's `Secure` flag never sets and every password crosses the wire in clear.
None of this has been through a security review. If it must leave your network,
put it behind a reverse proxy that terminates TLS.

## Where this actually is

Measured, not asserted:

- Runs unattended and recovers — an 8/8 fault-injection gate covering decoder
  stalls, a killed ffmpeg, and playlist discontinuity.
- Writes, speaks and airs real breaks end to end.
- 100% groundedness across every break measured; 86% pass the checkable rubric.
- The console's own gates — sources, stations, jocks, adverts, accounts,
  logs — have been walked end to end and signed off.

Still not known:

- **Whether it is good to listen to, over weeks.** Taste is deliberately not
  self-scored: whoever wrote the persona cannot hear it fresh. Single sessions
  have been heard and approved; a month has not.
- The real repetition rate over a month rather than a few hundred breaks.
- Whether the break cadence is right. It remains the single most likely thing to
  be misconfigured.

## Licence

AGPL-3.0-only, with a commercial exception available — see
[LICENSING.md](LICENSING.md). ffmpeg is operator-supplied and run out of
process, never linked or vendored — see [LICENCES-MANUAL.md](LICENCES-MANUAL.md).

Community contribution runs through jock cards, voice profiles and clients
rather than patches; selling commercial exceptions requires a single copyright
holder.
