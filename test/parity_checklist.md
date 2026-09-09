<!--
Copyright (C) 2026 Andrew Loable
SPDX-License-Identifier: AGPL-3.0-only
-->

# Parity checklist: the hand-written pages

One row per `fetch(` call and per `id="` element in `web/index.html` and
`web/admin.html`, taken from
`grep -nE 'fetch\(|id="' web/index.html web/admin.html` before either was
deleted — plus the guarantees `web/web_test.go` made about those pages, which
the grep does not see and which would otherwise have been deleted with them.

`test/parity_test.go` parses this table. Every row must name a spec file that
exists under `web/app/src` and a spec name that appears in it, or be marked
`DROPPED` with a reason. **A forgotten behaviour fails CI rather than a
memory** — which is the whole reason the checklist is a table and not prose.

Four rows are `DROPPED`. All are deliberate and all are argued in the reason
column: none is an oversight, and none is a behaviour a person can still
reach. (This paragraph said *two* while the table held three, which is exactly
the drift the row cap in `parity_test.go` exists to catch — count the rows, do
not trust the sentence.)

| page | line | behaviour | spec file | spec name |
|---|---|---|---|---|
| index.html | 51 | `#player` — play, pause and volume (the browser's own controls were REPLACED, not lost: the native widget gave a live stream a scrub bar and a running duration, which the no-seek invariant forbids) | listener/player.component.spec.ts | player transport toggles play and pause |
| index.html | 51 | `#player` — no skip and no seek, ever | listener/player.component.spec.ts | has no skip, next or seek control |
| index.html | 55 | `#status` — what the page says before anybody has tuned | listener/listener.spec.ts | shows the player, the dial and what is on air |
| index.html | 61 | `#dial` — the station list a person picks from | listener/dial.component.spec.ts | renders what a person picks a station by |
| index.html | 65 | `#jocks` — a jock picker | DROPPED | Listeners pick stations, never jocks. A station IS a genre plus one jock, chosen by the admin; the dial is the listener's whole UI. The API behind this control (Jocks, SetJock) was removed in row 13, so the control had nothing left to call. |
| index.html | 70 | `#nowplaying` — the track on air | listener/now-playing.component.spec.ts | renders the track, and does not print what the DJ said |
| index.html | 71 | `#transcript` — what the DJ just said | DROPPED | Removed on the developer's instruction, 2026-09-09: radio is heard, and printing the break under the player turns something the DJ says into something the listener reads along with. The break itself is unchanged and still airs. What the paragraph displayed is still fetched, still emitted, and still reaches the thumbs-down one component sideways, which has nothing to rate without it -- so no behaviour was lost with the element, only its display. Jockora-do2. |
<!-- Row 76 MOVED PAGE rather than being dropped, Jockora-e9a.47. The dossier
     count was operator telemetry rendered to LISTENERS: "Enriched 3649 of 7595
     (48%)" in monospace under the DJ transcript, a library-wide figure nobody
     listening can act on and the only debug-looking text on the screen. The
     behaviour is intact and better -- the admin Overview has it with a
     progress bar, an ETA and a cost line, where somebody who can act on it is
     already looking -- so this row points at the spec that covers it there. -->
| index.html | 76 | `#enrichment` — dossier progress, hidden when there is none | admin/overview.component.spec.ts | shows how far along it is, and how long is left |
| index.html | 82 | `#feedback` — hidden until a break has aired | listener/feedback.component.spec.ts | offers nothing until a break has aired |
| index.html | 82 | `#thumbsdown` — the only verdict there is | listener/feedback.component.spec.ts | has no thumbs-up |
| index.html | 83 | `#feedback-said` — what came of it | listener/feedback.component.spec.ts | says so when it could not be recorded |
| index.html | 175 | `fetch('/now.json?station=')` — polled while playing | listener/now-playing.component.spec.ts | polls on a timer |
| index.html | 175 | `fetch('/now.json')` — a failed poll keeps the last answer | listener/now-playing.component.spec.ts | keeps the last answer when a poll fails |
| index.html | 221 | `fetch('/tune')` — tuning by station id | listener/dial.component.spec.ts | tunes by station id and says where to listen |
| index.html | 243 | `fetch('/stations.json')` — reading the dial | listener/dial.component.spec.ts | renders what a person picks a station by |
| index.html | 243 | `fetch('/stations.json')` — an empty dial explains itself | listener/dial.component.spec.ts | explains an empty dial rather than showing nothing |
| index.html | 279 | `fetch('/feedback')` — attributed to the station it was about | listener/feedback.component.spec.ts | sends an attributed thumbs-down and says it landed |
| admin.html | 35 | `#body` — the overview tables | admin/overview.component.spec.ts | renders the overview as rows |
| admin.html | 38 | `#writewarn` — a banner saying writes were closed | DROPPED | It said writes would stay shut until sign-in landed. Row 13a landed sign-in, so the sentence is now false. The console signs in like anything else and a 403 is the guard's answer, which is proven in admin/admin.guard.spec.ts. |
| admin.html | 46 | `#cadence` — the break-cadence input | admin/overview.component.spec.ts | sets the cadence and says when it takes effect |
| admin.html | 47 | `#setcadence` — applying it without a restart | admin/overview.component.spec.ts | sets the cadence and says when it takes effect |
| admin.html | 52 | `#pause` — handing the machine back for an evening | admin/overview.component.spec.ts | pauses and resumes enrichment |
| admin.html | 53 | `#resume` — taking it back | admin/overview.component.spec.ts | pauses and resumes enrichment |
| admin.html | 56 | `#said` — what came of a write | admin/overview.component.spec.ts | says so when the overview or a toggle fails |
| admin.html | 108 | `fetch('/admin/overview.json')` | admin/overview.component.spec.ts | renders the overview as rows |
| admin.html | 118 | `fetch(path)` — the write helper, POSTing cadence | admin/overview.component.spec.ts | repeats the server’s reason for refusing a cadence |
| admin.html | 118 | `fetch(path)` — the write helper, POSTing enriching | admin/overview.component.spec.ts | pauses and resumes enrichment |
| index.html | web_test | native HLS is used where hls.js cannot run, so iOS Safari still plays | listener/player.component.spec.ts | uses native HLS only where hls.js cannot run |
| index.html | web_test | hls.js is the FIRST choice, because canPlayType lies — the old page checked native first and every non-Safari browser got silence | listener/player.component.spec.ts | uses hls.js on a browser that only CLAIMS it can play HLS |
| index.html | web_test | no autoplay, and tuning alone never starts sound; browsers block autoplay and the block looks like a broken stream (the player now HAS a play button, so this is asserted on behaviour rather than on the absence of the string) | listener/player.component.spec.ts | never starts playing by itself |
| index.html | web_test | no CDN; this has to work on a machine with no route to the internet | listener/player.component.spec.ts | loads nothing from a CDN |
| index.html | web_test | hls.js is vendored rather than fetched | DROPPED | The hand-vendored `web/vendor/hls.light.min.js` is gone with the pages that loaded it. The Angular app takes hls.js from npm and Angular bundles it into the app, so it is still served from this server and never from a CDN — which is the guarantee that mattered — and `scripts/check-licences-node.sh` now covers its licence, which the vendored copy needed `LICENCES-MANUAL.md` to record by hand. |
