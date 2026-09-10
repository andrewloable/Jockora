<!--
Copyright (C) 2026 Andrew Loable
SPDX-License-Identifier: AGPL-3.0-only
-->

# Deploying Jockora to gsc-datacenter

The house deployment, written down after the 2026-09-08 deploy of v0.2 so the
next one does not rediscover the same six things. Everything here was measured
on the box, not recalled.

**The station is Docker.** One container, `jockora:v0.2`, built on the box.

---

## 1. Where things actually are

This matters first, because the obvious answer is wrong twice over.

| Thing | Where it really is |
|---|---|
| Compose file that runs the station | `/home/mandark/jockora-deploy/docker-compose.yml` |
| API key | `/home/mandark/jockora-deploy/.env` |
| Source checkout on the box | `/home/mandark/jockora-build/` |
| Database, models, config | `/home/mandark/jockora/` (owned by **uid 10001**) |
| Kokoro weights | `/home/mandark/tts-endurance/models` |
| Music, read-only | `/media/data1/music` |
| Port | `42017` → container `8080` |

**`deploy/gsc-datacenter.yml` in this repository is NOT what runs.** It is an
earlier draft and it disagrees with reality in three ways that will each cost
you an hour:

- it names `192.168.0.254:42009/jockora:latest`, but **the registry has no
  `jockora` repository at all** (`NAME_UNKNOWN`) — the image is built locally on
  the box and never pushed;
- it mounts the Kokoro weights from `/home/mandark/jockora/models`, but they
  live in `/home/mandark/tts-endurance/models`;
- it points the model at the host's `llama-server` on `172.20.0.1:8081`, which
  as of 2026-09-09 is **right again** — see §3.8. It was wrong for a day and a
  half, while the deployment ran Cloudflare Workers AI.

Read the compose file **on the box** before every deploy. Treat the one in the
repository as history until somebody reconciles them.

---

## 2. The procedure that worked

```sh
# 0. Deploy when nobody is listening. "now": null means no station is running.
curl -s http://192.168.0.254:42017/now.json | head -c 60

# 1. Keep a rollback. Costs nothing and is the only cheap way back.
ssh mandark@192.168.0.254 'docker tag jockora:v0.2 jockora:rollback-$(date +%Y%m%d)'

# 2. Sync the source. Exclude the agent material and anything huge.
rsync -az --delete \
  --exclude '.git/' --exclude '.beads/' --exclude '.claude/' \
  --exclude 'node_modules/' --exclude 'web/dist/' --exclude 'web/app/dist/' \
  --exclude '*.md' --exclude '.env' --exclude '.DS_Store' \
  ./ mandark@192.168.0.254:/home/mandark/jockora-build/

# 3. BUILD BEFORE STOPPING ANYTHING. The build takes minutes; the station
#    keeps playing through all of it. Downtime starts at step 4, not here.
ssh mandark@192.168.0.254 'cd /home/mandark/jockora-build && docker build -t jockora:v0.2 .'

# 4. Stop, back up, start. The backup is taken with NO WRITER ATTACHED, which
#    is the only way it is consistent -- see §3.2.
ssh mandark@192.168.0.254 '
  docker stop jockora
  mkdir -p /home/mandark/jockora-backups
  cp /home/mandark/jockora/jockora.db \
     /home/mandark/jockora-backups/jockora.db.pre-migration-<N>-$(date +%Y%m%d)
  cd /home/mandark/jockora-deploy && docker compose up -d'

# 5. Verify. §4.
```

The Dockerfile builds the Angular app and the Go binary itself, so no local
`npm` or `go` step is needed. It is self-contained.

---

## 3. The traps, and what to do instead

### 3.1 Back up before any migration, and know which one you are on

v0.2 carried **migrations 10, 11 and 12** — station brief and range columns, ad
brand/brief/delivery, and the `log_records` table. The console round that
followed carried **migration 13**, `ads.enabled`. Migrations are forward-only:
there is no down-migration in this codebase and there is not meant to be.

Name the backup after the migration you are about to cross, not after the date
alone, so the next person can tell what it is a backup *of*:

```
jockora.db.pre-migration-12-20260908
```

The backups on the box, newest last:

```
jockora.db.pre-migration-12-20260908    4964352
jockora.db.pre-migration-13-20260908    5619712
```

### 3.2 A backup taken while the container runs is not a backup

The database is in **WAL mode**. Copying `jockora.db` on its own while the
station is writing gives you a file that is missing everything since the last
checkpoint.

Stop the container first. SQLite checkpoints and removes the `-wal` on a clean
close, so after `docker stop` the single `.db` file is complete — you can see
this happen:

```
before stop:  jockora.db  jockora.db-wal (4.1 MB)  jockora.db-shm
after stop:   jockora.db                                        <- WAL folded in
```

If you must copy a live database, copy **`.db`, `-wal` and `-shm` together**.

### 3.3 The same WAL trap when READING the live database

This one cost the most time on 2026-09-08 and produced a false alarm.

Diagnosing the deploy, `jockora.db` was copied to `/tmp` and queried — without
the `-wal`. It reported `dossiers = 3923` twice, sixty seconds apart, and an
**empty `enrich_lock` table**. 3,923 is also the exact number enrichment had
stalled at during the 2026-09-08 02:58 model outage, so the reading looked like
a stalled worker and a confirmed regression.

It was neither. The snapshot was a stale checkpoint. Copying the WAL as well:

```
dossiers=3961  lock=[('30241a921258/1', ...)]  heartbeat_age=8s
dossiers=3973  lock=[('30241a921258/1', ...)]  heartbeat_age=2s
```

Enrichment was running the whole time at about eight tracks a minute.

**Always copy the WAL with the database, or do not trust the number.** A
coincidence that matches a number you already fear is the most convincing wrong
answer there is.

### 3.4 `sudo` on the box needs a password, and a non-interactive ssh cannot give it

`/home/mandark/jockora` is owned by **uid 10001** and `mandark` cannot write
there. `sudo cp` fails with *"a terminal is required to read the password"* —
and it fails **after** the container has already been stopped, so the station is
down while you work out what happened.

The files are world-readable, so read them as `mandark` and write the copy
somewhere `mandark` owns:

```sh
mkdir -p /home/mandark/jockora-backups
cp /home/mandark/jockora/jockora.db /home/mandark/jockora-backups/...
```

No `sudo` is needed anywhere in this procedure. If you reach for it, you have
taken a wrong turn.

### 3.5 Two API keys, and only one of them works

There are **two** places a Cloudflare key lives, and they drift:

| Where | Used when | State on 2026-09-08 |
|---|---|---|
| `settings.llm_config` in the database | whenever the console has been used | **works** (HTTP 200) |
| `JOCKORA_LLM_API_KEY` in `.env` | fallback, when nothing is stored | **dead** (HTTP 401) |

The stored one wins, and the startup log says which is in force:

```
"msg":"language model from the console","provider":"cloudflare","model":"@cf/..."
```

`from the console` means the database key. The `.env` key is a **latent trap**:
it is stale, and anything that clears `llm_config` — a fresh database, a reset —
silently falls back to a key that 401s, which presents as the DJ going quiet.

Test a key without printing it:

```sh
curl -s -o /dev/null -w '%{http_code}\n' -m 20 \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"messages":[{"role":"user","content":"say ok"}],"max_tokens":5}' \
  "https://api.cloudflare.com/client/v4/accounts/$ACCT/ai/run/@cf/meta/llama-3.3-70b-instruct-fp8-fast"
```

**Fix `.env` to match the console key, or empty it.** A wrong key is worse than
no key, because no key produces a clear "not configured" and a wrong one
produces an outage that looks like a code fault.

### 3.6 The box's Dockerfile can be older than the repository's

The box was carrying a Dockerfile without `--platform=$BUILDPLATFORM` on the
node and Go stages. It builds fine natively on the box (amd64 building for
amd64), so nothing complains — but it is not the file in the repository, and a
cross-build from an arm64 workstation with it segfaults the Go compiler under
QEMU.

The rsync in §2 replaces it. Check the checksums match if you ever build by
hand:

```sh
md5sum Dockerfile                                    # local
ssh ... 'md5sum /home/mandark/jockora-build/Dockerfile'
```

### 3.7 The stored break cadence outranks the compose flag

The compose passes `-break-every-n-tracks 4`; the startup log said:

```
"msg":"break cadence restored","every_n_tracks":1
```

This is deliberate — a cadence chosen in the console is the setting, and a
restart must not undo it — but it means **the compose flag is only the default
for a station nobody has tuned**. Do not debug a cadence by reading the compose
file. Read the log line, or the `break_cadence` row in `settings`.

### 3.8 The stored provider outranks the compose file, exactly like the cadence

Same trap as §3.7 and it costs more, because it is silent. A provider chosen in
the operator console is written to `settings.llm_config`, and `buildLLM` reads
that row **before** it looks at any environment variable. Editing
`JOCKORA_LLM_URL` in the compose and restarting therefore changes nothing at all
while that row exists — the station comes up healthy, on the old provider, with
no warning anywhere.

Moving the box back to the local llama-server on 2026-09-09 needed both halves:

```sh
# the row wins, so it has to go. As uid 10001, with the container stopped.
docker run --rm -u 10001 -v /home/mandark/jockora:/config \
  --entrypoint python3 jockora:v0.2 -c "
import sqlite3
c = sqlite3.connect('/config/jockora.db')
c.execute('delete from settings where key=?', ('llm_config',)); c.commit()"
```

That `docker run` is also the general answer to §3.4: `/home/mandark/jockora` is
owned by uid 10001 and `mandark` cannot write there, but a throwaway container
on the same volume can, and the image already carries python3.

**Read the startup log to find out which one won.** The three outcomes are
distinguishable, and only by their log lines:

| Line at startup | What is in force |
|---|---|
| `language model from the console` | the database row |
| `using a hosted language model; TRACK METADATA AND LYRICS LEAVE THIS MACHINE` | compose, OpenAI-compatible route |
| *(nothing)* | compose, llama.cpp native route — silence is success here |
| `no language model yet; choose one in the operator console` | nothing works |

The silent case is the awkward one: a healthy local model logs nothing at all,
so the only positive proof is from the other side. `journalctl -u llama-server`
shows the request arriving **from the container's address**, not the host's:

```
srv log_server_r: done request: POST /completion 172.20.0.22 200
```

`docker inspect jockora --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}'`
confirms that address is the station. A request from `192.168.0.254` is the host
— which is you, testing — and proves nothing about Jockora.

### 3.9 The "local is too slow" measurement was taken under load

The compose file carried a note rejecting the local model: breaks generated in
**187s against the 148s** the lookahead gives them, arriving too late to air.
That was true when it was measured, and it was measured while enrichment was
running flat out against the same 12 threads.

Enrichment finished (7,595 of 7,595). Re-measured on 2026-09-09 with the CPU
free, against the same Qwen3.5-4B Q4\_K\_M on `172.20.0.1:8081`:

| | |
|---|---|
| Prompt, 1,331 tokens | 74.8 tok/s |
| Output, 160 tokens | 4.26 tok/s |
| **Total** | **55 s**, against a 148 s budget |

So the rejection had an expiry date on it and nobody had noticed. **Re-measure
before trusting a performance note that predates a finished enrichment run** —
and re-measure again if enrichment ever restarts, because the 187s comes back
with it.

---

## 4. Verifying a deploy

In order, cheapest first. Every one of these is a read.

```sh
# The container is the new image, and stays up.
docker ps --filter name=jockora --format '{{.Image}} {{.Status}}'
docker inspect jockora --format '{{.Image}}'

# Nothing went wrong. The LAN warning is expected and is the only WARN.
docker logs jockora 2>&1 | grep '"level":"ERROR"' | wc -l      # want 0
docker logs jockora 2>&1 | grep '"level":"WARN"' \
  | grep -v 'SERVING WITHOUT AUTH'                             # want empty

# Health.
curl -s http://127.0.0.1:42017/now.json | python3 -c 'import json,sys;print(json.load(sys.stdin)["health"])'
```

Then the schema, **with the WAL** (§3.3):

```sh
rm -f /tmp/v.db*; for f in "" -wal -shm; do
  [ -f /home/mandark/jockora/jockora.db$f ] && cp /home/mandark/jockora/jockora.db$f /tmp/v.db$f
done
python3 - <<'PY'
import sqlite3, time
c = sqlite3.connect('/tmp/v.db'); now = int(time.time())
print('schema_version', c.execute('select version from schema_version').fetchone()[0])
print('dossiers', c.execute('select count(*) from dossiers').fetchone()[0])
print('lock', [(o, now - h) for o, h in c.execute('select owner, heartbeat from enrich_lock')])
PY
rm -f /tmp/v.db*
```

A **fresh lock heartbeat (single-digit seconds) and a dossier count that moves
between two reads a minute apart** is the only proof enrichment is alive. The
`enriching: true` in the "on air" line is the operator's pause toggle, not
whether anything is happening — that distinction is what made the 02:58 outage
invisible for fifty minutes.

### A deploy verified with nobody listening is not verified

Everything in this section passes on a station that has never played a note.
`now.json` reports `"now": null`, no encoder runs, no break is ever scheduled,
and the whole audio path — decode, ring, crossfade, break placement — is
untouched by all of it.

The v0.2 deploy on 2026-09-08 passed every check above and then a listener heard
the DJ announce **"Now Green Day 21 Guns" over the closing seconds of Nine Inch
Nails, Hurt**. The bug was in break placement (`Jockora-8om`), it was not caused
by the deploy, and **nothing in this checklist could have found it** because
nothing here plays anything.

So: after the checks pass, **tune in and listen through two or three track
boundaries.** If you cannot, say the deploy is verified *as far as the schema
and the process*, and that the audio path is not.

The station log is the second-best thing, and it needs a listener too — these
lines only exist once something is playing:

```sh
docker logs jockora 2>&1 | grep -E 'break slot offered|break scheduled'
```

`now_s` of each boundary should equal the previous boundary's `insertion_at_s`.
When it does not, breaks are landing off the transition — that is precisely how
`Jockora-8om` was found, and `buffered_s` was added to that line so the missing
term is visible without a debugger.

### What the second 2026-09-08 deploy verified (migration 13)

The console round: eight UI defects, the durable-log startup fix, and
`ads.enabled`.

```
schema_version                                        13
ads.enabled                                           present    (migration 13)
adverts back-filled to enabled=1                      2 of 2, none left NULL or 0
log_records                                           1  <- see below
dossiers                                    5145 -> 5150 over 70s
enrich_lock heartbeat age                             13 s, then 40 s
ERROR lines                                           0
WARN lines other than the expected LAN one            none
restarts                                              0
image                                       sha256:db939163305f
console bundle served                       styles-XVNTMTJK.css, 18790 bytes,
                                            carrying the textarea, ember-accent,
                                            inline-field and card rules
app boots in a real browser                 title, app-root populated, login
                                            form with labelled fields, no JS errors
```

**`log_records` is 1, and that is the whole point.** After the FIRST v0.2 deploy
this table was empty, which is what `Jockora-69n.9` was filed for: `persistLogs`
started inside `App.Run`, so every WARN from `app.New` reached stderr and the
ring and never the table. The row now present is exactly the one that used to be
lost:

```
WARN | SERVING WITHOUT AUTHENTICATION ON A NON-LOOPBACK ADDRESS
```

That is the symptom from the issue, reproduced and closed on the box rather than
only in a test.

**The back-fill is the migration's real risk and it was checked directly.**
`enabled INTEGER NOT NULL DEFAULT 1` has to leave every advert that predates the
column on air; a nullable flag read as false would take a working rotation off
the air on upgrade. Both existing adverts read `1`, and the count of rows left
NULL or 0 is zero.

### The heartbeat-age criterion above is calibrated to a LOCAL model

§4 asks for a lock heartbeat in single-digit seconds. That number comes from the
local llama-server era, when enrichment ran about eight tracks a minute. The
hosted era stretched it: one Cloudflare dossier took roughly thirty-five
seconds, so the heartbeat was written once per track and its age swept from zero
to about that. **The box is back on the local model as of 2026-09-09** (§3.8),
so the original number applies again — but the rule below is the one to keep,
because it survived both.

Measured here: 13 s, then 40 s, while dossiers moved 5145 → 5150. That is
healthy. **Read the heartbeat against the dossier count, not against a fixed
number** — a stalled worker is one where the count does not move, whatever the
heartbeat says.

### What the 2026-09-08 v0.2 deploy verified

```
schema_version                 12
stations: brief, year_min, year_max, tempo_min, tempo_max,
          duration_min_s, duration_max_s              all present   (migration 10)
ads:      brand, brief, delivery, created_at          all present   (migration 11)
log_records                                           present       (migration 12)
stations carrying a back-filled brief                 1 of 1
pack adverts imported into the shared pool            2
dossiers                                    3961 -> 3973 in 90s
enrich_lock heartbeat age                             2-8 s
ERROR lines                                           0
```

---

## 5. Known gaps, filed rather than fixed here

- ~~**Startup warnings are never persisted.**~~ **FIXED** in the migration-13
  deploy, `Jockora-69n.9`. `Sink.Persist` now drains what the ring already holds
  at or above `PersistLevel` before it starts taking new records, under the same
  lock the fan-out uses so nothing is written twice. Proven on the box:
  `log_records` holds the non-loopback warning after a clean deploy, where it
  was empty before.

- **`deploy/gsc-datacenter.yml` disagrees with the box** in three ways (§1).
  Either reconcile it or mark it clearly as not-the-deployment.

- **The `.env` key is dead** (§3.5). Harmless while `llm_config` is populated,
  an outage the moment it is not.

- **`Jockora-8om`, fixed and redeployed the same day but not yet heard.** The
  break insertion point ignored audio already decoded and not yet played, so
  every break after the first landed up to `station.RingSeconds` (ten seconds)
  early — inside the tail of the track it was meant to follow. Fixed by counting
  `ring.Occupancy()`; proven by test, **not yet proven by ear**. Listen through a
  few boundaries next time the station is up.

### What the 2026-09-09 deploy verified (migration 14)

The dialog round: six console forms moved into a shared platform `<dialog>`,
server-side session revocation, dial and rescan polling, and the seven review
fixes that followed. Rollback image `jockora:rollback-20260909`, backup
`jockora.db.pre-migration-14-20260909` (6692864 bytes).

Verified, all reads:

- `jockora:v0.2` up, **0 ERROR lines, 0 unexpected WARN lines** — the
  `SERVING WITHOUT AUTH` LAN warning did not even appear this time.
- `health` all three `ok`.
- **`schema_version` 14** and the `revoked_sessions` table present, so signing
  out now ends the session server-side rather than only clearing the cookie.
- **`bpm` is populated for all 7595 playable tracks.** It was blank before: the
  analyser only selected tracks with a NULL loudness, so a library measured
  before BPM was wired could never fill. That is `Jockora-ffh` landing, and it
  is what makes the tempo filter mean anything on this box.
- **Enrichment reads `7595 of 7595, 100%, running`.** It reported
  `7595 of 7696 (98.7%) paused` before; the 101 that never resolved are no
  longer counted in the total.
- The shipped console **is** this build: `main-JPUGOL7D.js` and the lazy
  `chunk-GLTZLOF7.js` at 81241 bytes match the local `ng build` byte for byte,
  and the chunk carries `data-form-dialog`, `data-dialog-close`,
  `data-add-open`, `Discard your changes` and `Discard my tags`.

**The audio path is NOT verified.** Nobody was listening, `now` was null, so no
encoder ran and no break was placed — exactly the gap §4 warns about. This
deploy is verified as far as the schema, the process and the served assets, and
no further. `Jockora-8om` is still unheard.

### What the second 2026-09-09 deploy verified (no migration)

The afternoon's twelve fixes: the advert writer, the station derive schema and
sampling temperature, the dial's listener count, the DJ transcript, and the
error-shape work across eight console components. Rollback image
`jockora:rollback-20260909b`.

**NO MIGRATION IS CROSSED.** `CurrentSchemaVersion` is still 14 and the box was
already on 14, so the backup is named for the schema it is a copy *of* rather
than for a migration it precedes: `jockora.db.schema-14-20260909b`. Naming it
`pre-migration-15` would have been a lie about what comes next.

Verified, all reads:

- `jockora:v0.2` up, **0 ERROR lines, 0 unexpected WARN lines**.
- `health` all three `ok`; enrichment `7595 of 7595, 100%, running`.
- **The served bundle is this build and the morning's is gone.**
  `main-FMEMVWH3.js` matches the local `ng build` hash exactly, the lazy admin
  chunk `chunk-KE2RV5MU.js` serves 81188 bytes against a local 81.19 kB, and
  the morning's `main-JPUGOL7D.js` now **404s** — which is the check that
  actually distinguishes a deploy from a restart.
- **The afternoon's changes are observable in the bundle**: `data-transcript`
  appears **0** times in main (the DJ transcript is gone), `visibilitychange`
  appears (the dial polls while visible), and the admin chunk still carries
  `data-form-dialog`.

**The audio path is NOT verified.** `now` was null again, so no encoder ran and
no break was placed. Verified as far as the schema, the process and the served
assets, and no further.

**And the advert and derive fixes are not verified at all.** Both are Go-side
and neither is reachable without an admin session, so nothing here exercised
them. `Jockora-91o` is the open question they turn on: the derive was tuned
against the 4B test model and this box runs llama-3.3-70b.

### What the 2026-09-09 v0.2.0 release deploy verified

**The first deploy of a PUBLISHED image rather than one built on the box.**
`docker pull docker.io/andrewloable/jockora:v0.2.0`, then retagged onto the
local name `jockora:v0.2` that the compose file already expects -- so the
released artifact ships without editing the compose file. The box reported the
same digest the release published, `sha256:f3438f5d...`, which is the check that
makes "the thing that was tested is the thing that is running" a fact rather
than a hope.

Rollback image `jockora:rollback-20260909c`. **No migration crossed**: schema is
still 14 both sides, so the backup is `jockora.db.schema-14-20260909c`.

Verified, all reads:

- `jockora:v0.2` up; `docker exec jockora jockora version` says **0.2.0**.
- **0 ERROR lines, 0 unexpected WARN lines**; health all three `ok`;
  enrichment `7595 of 7595, running`.
- The served console is this build: a new `main-VU65YIWX.js`, both new icons
  answer 200, `index.html` links `icon.svg` and **no longer links
  `favicon.ico`**, `visibilitychange` is present (the dial's listener count
  polls again) and `data-transcript` is **gone** (the DJ's words are no longer
  printed).
- The lazy admin chunk `chunk-ZYHU3SM2.js` carries `data-sort` and
  `data-form-dialog` -- the searchable, sortable tables and the modal forms.

**A NOTE ON WHICH VERSION STRING YOU GET.** The image says `0.2.0` and the
release tarballs say `v0.2.0`. The release job stamps
`-X main.Version=${GITHUB_REF_NAME}` when it builds the tarball binaries, but
`Dockerfile` builds with `-ldflags="-s -w"` and no `-X` at all, so the image
carries the value compiled into `cmd/jockora/main.go`. Same release, two
strings. Worth knowing before you read one into a bug report.

**The audio path is NOT verified.** `now` was null again, so no encoder ran and
no break was placed. In particular the break lead-in shipped here -- the DJ now
starting up to three seconds before a transition -- has never been heard.

### What the 2026-09-09 provider deploy verified (no migration)

Two changes in one window: the playlist's server-side sort (`Jockora-22s`), and
the move off Cloudflare back to the local llama-server.

```
image                        sha256:225adf3aca2e   built on the box
version                      jockora 0.2.0
schema                       14 -> 14, no migration crossed
backup                       jockora.db.schema-14-20260909d
rollback image               jockora:rollback-20260909d
ERROR lines                  0
WARN other than the LAN one  none
health                       llm ok, tts ok, ffmpeg ok
enrichment                   7595 of 7595, running
```

The provider, proved from both ends:

```
settings.llm_config                     deleted (was cloudflare)
compose                                 one LLM variable, JOCKORA_LLM_URL
docker compose config                   JOCKORA_LLM_URL: http://172.20.0.1:8081
startup log                             silent -- the llama.cpp success case
llama-server journal, 6s after boot     POST /completion 172.20.0.22 200
container address                       172.20.0.22
```

The playlist sort, proved in the binary and in the served bundle rather than by
clicking:

```
binary        "t.artist COLLATE NOCASE"    1 hit
binary        "coalesce(t.bpm, 0) = 0"     1 hit
main-D6DTLDHJ.js                           references 2 lazy chunks
chunk-TWBHOZSV.js                          data-playlist, aria-sort, data-sort,
                                           sort-marker, Tempo
```

**The audio path is STILL not verified, and the backlog of unheard changes is
now three deep.** `now` was null again at deploy time, so no encoder ran and no
break was placed. Unheard, in the order they shipped: the break lead-in (the DJ
starting up to three seconds before a transition), and now every break coming
from a 4B local model instead of a 70B hosted one. The second is the one to
listen for -- **groundedness and the said-lines rule were measured against
Cloudflare's model, not this one**, and a smaller model is exactly where a
schema-constrained prompt starts producing valid JSON that reads badly.

Tune in through three or four track boundaries before calling this good.

### What the 2026-09-10 DJ-content deploy verified (no migration)

The break writer round: meaning-first prompt material, a recitation check, and a
second generation for a rejected break.

```
image                        sha256:a085050654de   built on the box
version                      jockora 0.2.0
schema                       14 -> 14, no migration crossed
backup                       jockora.db.schema-14-20260910
rollback image               jockora:rollback-20260910
ERROR lines                  0
WARN other than the LAN one  none
health                       llm ok, tts ok, ffmpeg ok
enrichment                   7595 of 7595
provider                     still local llama.cpp -- POST /completion from
                             172.20.0.22, the container's own address
```

The changes, proved in the shipped binary rather than by clicking:

```
recited_dossier                     1 hit   the new drop reason
TALK ABOUT WHAT THE SONG IS ABOUT   1 hit   the pointer at the meaning
themes [                            1 hit   the field that was never shown
```

**THE AUDIO PATH IS STILL UNVERIFIED AND THE BACKLOG IS NOW FOUR DEEP.** `now`
was null again, so no encoder ran and no break was placed. Unheard, in shipping
order: the break lead-in, the music gap around a break, every break coming from
a 4B local model rather than a 70B hosted one, and now this round's prompt.

**What to listen for, and it is not subtle.** Measured with
`internal/dj/liveprobe_test.go` against the deployment's model at the real
sampler settings, seven generations gave one good break, two fillers, two loops,
one that returned ellipses and one unparseable. The checks catch the loops and
the recitations, and `MaxAttempts` is now two so a bad roll costs a retry rather
than the break -- but at roughly one usable generation in four, expect
boundaries with no break at all. `Jockora-izw` has the numbers.

The good one read: *"This is Midnight, 2031. I'm driving out of a city I'm not
coming back to, still talking to someone who isn't there."* That is the dossier's
subject summary, in the DJ's own words, which is the whole point of the round.

## 6. Rolling back

```sh
ssh mandark@192.168.0.254 '
  docker stop jockora
  cp /home/mandark/jockora-backups/jockora.db.pre-migration-12-<date> \
     /tmp/restore.db          # then move it into place as uid 10001
  docker tag jockora:rollback-<date> jockora:v0.2
  cd /home/mandark/jockora-deploy && docker compose up -d'
```

**The database must go back too.** Migrations are forward-only, so an older
binary against a migrated database fails on a schema version it does not know.
Restoring the image alone is not a rollback.
