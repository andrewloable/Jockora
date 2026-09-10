import { Component, OnDestroy, inject, signal } from '@angular/core';
import { DecimalPipe } from '@angular/common';
import { AdminApi, ImportReport, serverSaid } from '../api/api';

/**
 * What the station knows about itself, and the two things worth changing
 * without a restart.
 */
// How often the overview re-reads itself.
//
// ENRICHMENT MOVES AT ABOUT A TRACK A MINUTE on a CPU-bound model, so a page
// that loads once and never refreshes shows a number that does not visibly
// change -- which reads as "enrichment is stuck" when it is working fine. That
// is exactly how it was first reported.
const REFRESH_MS = 10_000;

@Component({
  selector: 'app-overview',
  standalone: true,
  imports: [DecimalPipe],
  template: `
    <h2>Overview</h2>

    <!-- THE MODEL IS REFUSING, and until 2026-09-08 nothing on this page said
         so. A Cloudflare token stopped being accepted at 02:58; the station
         dropped nine consecutive breaks and enrichment died, and the dossier
         count below sat still while /now.json reported "llm": "ok".
         First thing on the page, and role=alert, because the operator is here
         reading a number that has quietly stopped moving. -->
    @if (modelTrouble(); as t) {
      <p role="alert" data-model-health>{{ t }}</p>
    }
    <!-- AND THE WORKER THAT GAVE UP. The dossier count on this page sat at
         3,923 of 7,595 for an hour under the word "running", because the flag
         being reported was the operator's own pause toggle. -->
    @if (enrichmentTrouble(); as t) {
      <p role="alert" data-enrichment-health>{{ t }}</p>
    }

    <section>
      <h3>Library</h3>
      @if (lib(); as l) {
        <p data-library-line>
          <strong>{{ l.tracks | number }}</strong> files scanned,
          <strong>{{ l.playable | number }}</strong> of them playable.
        </p>
      }

      @if (progress(); as p) {
        <div data-enrich-progress>
          <progress [value]="p.done" [max]="p.total"></progress>
          <p data-enrich-line>
            Dossiers <strong>{{ p.done | number }} of {{ p.total | number }}</strong> ({{ p.pct }}%)
            · {{ p.state }}
          </p>
          <p data-enrich-eta>{{ p.eta }}</p>
        </div>

        <!-- LOUDNESS IS THE OTHER SLOW PASS and it was a bare integer in a
               flat table. It is the reason one record arrives louder than the
               last, so how far along it is belongs beside the dossiers. -->
        <div data-enrich-progress>
          <progress [value]="loud().done" [max]="loud().total"></progress>
          <p data-loudness-line>
            Loudness <strong>{{ loud().done | number }} of {{ loud().total | number }}</strong> ({{
              loud().pct
            }}%)
          </p>
        </div>
      } @else {
        <p data-enrich-progress>Nothing scanned yet.</p>
      }

      <button type="button" data-pause (click)="setEnriching(false)">Pause enrichment</button>
      <!-- IT SAYS WHICH OF TWO THINGS IT WILL DO. "Resume" over a worker that
           gave up and "Resume" over one the operator paused are different
           promises, and before Jockora-e9a.52 the first of them set a boolean
           nothing was reading and reported success. -->
      <button type="button" data-resume (click)="setEnriching(true)">
        {{ enrichmentTrouble() ? 'Restart enrichment' : 'Resume' }}
      </button>

      @if (cost(); as c) {
        <p data-cost-line>
          {{ c.perTrack }} per track · {{ c.wall }} of model time so far · {{ c.tokens }} tokens.
        </p>
      }
      @if (failures() > 0) {
        <p data-failures>
          {{ failures() | number }} track{{ failures() === 1 ? '' : 's' }} could not be measured for
          loudness. They play at their own level.
        </p>
      }
    </section>

    <section>
      <h3>The DJ</h3>
      <p>
        <!-- THE SENTENCE IS THE LABEL. "A break every 4 tracks" says more than
             the word Cadence over a box would, and unlike a placeholder it does
             not vanish the moment there is a value. data-inline-field keeps the
             field inside the sentence rather than on a line of its own. -->
        <label data-inline-field>
          A break every
          <input
            data-cadence
            type="number"
            min="1"
            max="100"
            [value]="cadence()"
            (input)="editCadence(+$any($event.target).value)"
          />
          tracks.
        </label>
        <button type="button" data-set-cadence (click)="saveCadence()">Set</button>
      </p>
      <p>
        <!-- THE SENTENCE IS THE LABEL, like the cadence above it. -->
        <label data-inline-field>
          Overlap each break with
          <input
            data-overlap
            type="number"
            min="0"
            max="6"
            step="0.5"
            [value]="overlap()"
            (input)="editOverlap(+$any($event.target).value)"
          />
          seconds of music at each end.
        </label>
        <button type="button" data-set-overlap (click)="saveOverlap()">Set</button>
        <!-- WHAT IT ACTUALLY DOES, said where the number is set, and it is BOTH
             ENDS. This has been wrong twice: first it described only the
             lead-in, then it promised a cap by the measured outro that no
             longer exists. The operator asked for the GTA rule to be
             disregarded, so the number applies whatever the track is doing --
             which is worth saying out loud on the screen where it is typed,
             because it is the part that can sound like a fault. Jockora-ey4. -->
        <small data-overlap-note
          >The outgoing record plays under the start of each break and the incoming one under its
          end, and the music pauses in between. It applies to every break whatever the track is
          doing, so a song that sings to its last second gets talked over. Set it to 0 for no
          overlap and no pause.</small
        >
      </p>
      <p data-dj-line>
        {{ num('said_lines') | number }} lines spoken so far · {{ num('adverts') | number }} adverts
        in the pool.
      </p>
    </section>

    <section>
      <h3>What listeners said</h3>
      <div data-feedback>
        @if (feedback().length) {
          <ul>
            @for (f of feedback(); track f.at) {
              <li>
                {{ f.text }} <small data-inline>{{ f.jock }}</small>
              </li>
            }
          </ul>
        } @else {
          <p>No thumbs-down yet.</p>
        }
      </div>
    </section>

    <!-- EVERY FIGURE, ONE CLICK AWAY. The flat dump was unreadable as a front
         page and is still exactly what you want when something is wrong, so it
         is kept rather than replaced -- in a native disclosure, which costs no
         script and no state. -->
    @if (rows().length) {
      <details data-raw>
        <summary>Every figure</summary>
        <table data-overview>
          @for (row of rows(); track row.key) {
            <tr>
              <th>{{ row.key }}</th>
              <td data-label="value">{{ row.value }}</td>
            </tr>
          }
        </table>
      </details>
    }

    <!-- ENRICHMENT IS THE MOST EXPENSIVE THING THIS SERVER MAKES: one model
         pass per track, hours of wall clock, plus the loudness and ramp work on
         top. It lived in exactly one place, so a rebuilt box started from
         nothing. This is a download and an upload, and the REPORT is the half
         that matters -- an import that silently does nothing is the failure to
         design against. -->
    <section>
      <h3>Enrichment</h3>
      <p>
        <a data-export href="/admin/enrichment/export" download
          >Download this library's enrichment</a
        >
      </p>
      <!-- ITS OWN BLOCK. Inline, the two nodes touched with nothing between
           them and the page read "Download this library's enrichmentDossiers,
           your own tag edits and the audio analysis." -->
      <p data-export-note>
        <small
          >Dossiers, your own tag edits and the audio analysis. No accounts, no stations.</small
        >
      </p>
      <!-- THE LABEL IS THE CONTROL. A raw input type=file draws the browser's
           own "Choose File / No file chosen", which is the same defect as the
           vocabulary checkboxes: an unstyled native widget in a styled console.
           The input is still there and still does the work -- it is hidden
           behind the label, which is what a click and a keypress reach. -->
      <p>
        <label data-import-label>
          Merge a file from another install
          <input
            data-import
            type="file"
            accept=".gz,application/gzip"
            (change)="importEnrichment($event)"
          />
          <span data-import-button>Choose a file</span>
          <span data-import-name>{{ importName() || 'No file chosen yet' }}</span>
        </label>
      </p>
      <p data-import-note>
        <small>Fills holes only. Your own tag edits are never overwritten.</small>
      </p>
      @if (report(); as r) {
        <ul data-import-report>
          <li>{{ r.applied }} applied</li>
          <li>{{ r.had_dossier }} already had a dossier</li>
          <!-- The RECORD is not left alone: its dossier still came in. What was
               kept is the tag edit, which is the part somebody typed. -->
          <li>{{ r.had_override }} kept your own tag edits</li>
          <li>{{ r.unmatched }} unmatched, no file at that path here</li>
          <li>{{ r.wrong_track }} refused, a different recording at that path</li>
          <!-- A record can be PARTLY applied: a tag list nothing survived is
               refused while the dossier beside it lands. The refusal is what
               gets reported, because the file is what you can go and look at. -->
          <li>{{ r.rejected }} refused, something in the file could not be used</li>
          <li>{{ r.unreadable }} unreadable lines</li>
        </ul>
      }
    </section>

    <p data-said>{{ said() }}</p>
  `,
})
export class Overview implements OnDestroy {
  private readonly api = inject(AdminApi);

  /** What the last import did. Null until one has been run. */
  readonly report = signal<ImportReport | null>(null);

  /** The file the operator picked, because the native widget that used to say
   *  so is hidden behind the label now. */
  readonly importName = signal('');

  readonly rows = signal<{ key: string; value: string }[]>([]);
  // The raw answer as well as the flattened rows: the table wants every field,
  // the progress readout wants three of them by name.
  readonly overview = signal<Record<string, unknown>>({});
  readonly cadence = signal(4);
  readonly said = signal('');

  // True between the operator typing a cadence and the server accepting it.
  //
  // The poll below re-reads the overview every ten seconds, so without this it
  // would wipe a half-typed number back to the stored one mid-keystroke. Not a
  // signal: nothing renders it, and a signal written inside load() would be one
  // more thing to reason about in a change-detection cycle.
  private editingCadence = false;

  private timer: ReturnType<typeof setInterval> | null = null;

  constructor() {
    this.load();
    this.timer = setInterval(() => this.load(), REFRESH_MS);
  }

  /**
   * Enrichment, as something a person can read.
   *
   * The numbers were already on the page -- library.enriched and library.tracks
   * are two rows in the table above -- but a job that takes DAYS and moves at a
   * track a minute needs a shape, not a pair of integers buried in a readout.
   * Reported as "does not show any progress" while it was in fact progressing.
   */
  /** The library block, or null when the server has not said. */
  lib(): { tracks: number; playable: number } | null {
    const l = this.overview()['library'] as Record<string, number> | undefined;
    if (!l || !l['tracks']) {
      return null;
    }
    // No default on tracks: the guard above has already refused a library
    // without one, so a fallback here would be a branch nothing can reach.
    return { tracks: l['tracks'], playable: l['playable'] ?? 0 };
  }

  /**
   * The model's reason for refusing, or nothing at all when it is working.
   *
   * "not configured" is NOT trouble: a library with no model is a working
   * shuffle, and reporting it as an outage would make a fresh install look
   * broken to the person setting it up.
   */
  modelTrouble(): string {
    const m = this.overview()['model'] as { health?: string } | undefined;
    const h = m?.health ?? '';
    const mark = 'degraded: ';
    return h.startsWith(mark) ? `The language model is refusing: ${h.slice(mark.length)}` : '';
  }

  /**
   * Why enrichment stopped, or nothing.
   *
   * The server sends this ONLY when the worker gave up, never when the operator
   * paused it -- somebody who paused it for the evening knows where it went,
   * and telling them it stopped is the console shouting about a thing they did
   * on purpose.
   */
  enrichmentTrouble(): string {
    return (this.overview()['enrichment_stopped'] as string) ?? '';
  }

  /** How much of the playable library has a loudness measurement. */
  loud(): { done: number; total: number; pct: number } {
    const l = (this.overview()['library'] ?? {}) as Record<string, number>;
    const total = l['playable'] ?? 0;
    const done = l['loudness_measured'] ?? 0;
    return { done, total, pct: total ? Math.round((done / total) * 1000) / 10 : 0 };
  }

  /**
   * What enrichment has cost, in units a person thinks in.
   *
   * The server reports 17.468946255848465 seconds a track and 25015.531038375
   * seconds of wall time. Both are correct and neither is readable; seventeen
   * significant figures in a console is noise pretending to be precision.
   */
  cost(): { perTrack: string; wall: string; tokens: string } | null {
    const c = this.overview()['enrichment_cost'] as Record<string, number> | undefined;
    if (!c || !c['tracks_measured']) {
      return null;
    }
    return {
      perTrack: `${Math.round(c['seconds_per_track'] ?? 0)}s`,
      wall: duration(c['wall_seconds'] ?? 0),
      tokens: compact(c['tokens'] ?? 0),
    };
  }

  failures(): number {
    return Number(this.overview()['analysis_failures'] ?? 0);
  }

  /** One top-level number, defaulting to zero rather than rendering undefined. */
  num(key: string): number {
    return Number(this.overview()[key] ?? 0);
  }

  feedback(): { verdict: string; jock: string; text: string; at: number }[] {
    return (this.overview()['feedback'] ?? []) as {
      verdict: string;
      jock: string;
      text: string;
      at: number;
    }[];
  }

  progress(): { done: number; total: number; pct: number; state: string; eta: string } | null {
    const lib = (this.overview()['library'] ?? {}) as Record<string, number>;
    // THE PLAYABLE LIBRARY, which is the only thing enrichment ever walks --
    // the same denominator loud() below has always used. Dividing by every row
    // in the tracks table meant one unreadable file held the bar under 100 per
    // cent for ever: the deployment reported "7,595 of 7,696 (98.7%)" with
    // every playable track already enriched. Jockora-0gm.
    // Falling back to tracks for a server too old to report playable: a
    // slightly wrong denominator beats a blank panel.
    const total = lib['playable'] || lib['tracks'] || 0;
    if (!total) {
      return null;
    }
    const done = lib['enriched'] ?? 0;
    const cost = (this.overview()['enrichment_cost'] ?? {}) as Record<string, number>;
    const perTrack = cost['seconds_per_track'] ?? 0;
    const running = this.overview()['enriching'] === true;
    const left = total - done;

    return {
      done,
      total,
      pct: Math.round((done / total) * 1000) / 10,
      // COMPLETE OUTRANKS THE TOGGLE. "paused" is the operator's own switch, so
      // it reads as "somebody stopped this" -- and an operator who sees it
      // beside a full bar goes looking for something to restart. A pass with no
      // work left is finished, whichever way the switch is set.
      state: left <= 0 ? 'complete' : running ? 'running' : 'paused',
      eta: this.eta(left, perTrack, running),
    };
  }

  /**
   * How long the rest will take, in units a person thinks in.
   *
   * SAID PLAINLY BECAUSE IT IS LONG. On a CPU-bound model this is tens of
   * hours for a real library, and an operator who does not know that reads a
   * slow-moving number as a stuck one.
   */
  private eta(left: number, perTrack: number, running: boolean): string {
    if (left <= 0) {
      return 'Every track has a dossier.';
    }
    if (!perTrack || !running) {
      return `${left.toLocaleString()} to go.`;
    }
    const hours = (left * perTrack) / 3600;
    const when =
      hours < 1
        ? `${Math.max(1, Math.round(hours * 60))} minutes`
        : hours < 48
          ? `${Math.round(hours)} hours`
          : `${Math.round(hours / 24)} days`;
    return `${left.toLocaleString()} to go, about ${when} left at ${Math.round(perTrack)}s per track.`;
  }

  load(): void {
    this.api.overview().subscribe({
      next: (o) => {
        this.overview.set(o as Record<string, unknown>);
        this.rows.set(flatten(o));

        // THE SERVER OWNS THIS NUMBER. It was a hardcoded 4 that never once
        // read the answer, so an operator who set 1 -- and whose 1 the server
        // stored, applied and logged -- reloaded the page and saw 4, which is
        // indistinguishable from the setting having been lost. It was reported
        // as exactly that, twice, the second time after the storage half was
        // already fixed.
        const n = (o as Record<string, unknown>)['cadence'];
        if (typeof n === 'number' && !this.editingCadence) {
          this.cadence.set(n);
        }
        // The same rule for the same reason: the server owns it, and the
        // operator's own typing wins until the server has taken it.
        const v = (o as Record<string, unknown>)['break_overlap_s'];
        if (typeof v === 'number' && !this.editingOverlap) {
          this.overlap.set(v);
        }
      },
      error: () => this.said.set('Could not read the overview.'),
    });
  }

  /** How far before a transition the DJ starts, in seconds. */
  readonly overlap = signal(3);
  // True between the operator typing an overlap and the server accepting it.
  private editingOverlap = false;

  editOverlap(seconds: number): void {
    this.editingOverlap = true;
    this.overlap.set(seconds);
  }

  saveOverlap(): void {
    this.api.setBreakOverlap(this.overlap()).subscribe({
      next: () => {
        this.editingOverlap = false;
        this.said.set('Overlap set. It takes effect from the next break.');
      },
      error: (e: unknown) => this.said.set(serverSaid(e, 'Could not set that.')),
    });
  }

  /** The operator's number wins over the poll until the server has taken it. */
  editCadence(n: number): void {
    this.editingCadence = true;
    this.cadence.set(n);
  }

  saveCadence(): void {
    this.api.setCadence(this.cadence()).subscribe({
      next: () => {
        // Accepted, so the server's answer is now theirs: let the poll drive
        // the field again. A REFUSAL deliberately does not do this -- they
        // need to see the number they asked for beside the reason it failed.
        this.editingCadence = false;
        this.said.set('Cadence set. It takes effect from the next break.');
      },
      // The SERVER'S OWN WORDS: it refuses a cadence outside its range and
      // says why, and repeating that is more use than "failed".
      error: (e: unknown) => this.said.set(serverSaid(e, 'Could not set that.')),
    });
  }

  setEnriching(on: boolean): void {
    this.api.setEnriching(on).subscribe({
      // THE SERVER'S OWN WORDS. A restart that brings a dead worker back and
      // one that finds nothing to do look identical from here, and which one
      // happened is the entire reason the operator pressed it.
      next: (r) => {
        // r ?? : an older server answers 204 with no body at all, and reading
        // .said off null threw inside the subscribe -- which left the operator
        // pressing a button that neither worked nor said anything. Same defect
        // shape as the missing health field on the model page.
        this.said.set(
          r?.said ?? (on ? 'Enrichment running.' : 'Enrichment paused. The stream is unaffected.'),
        );
        // Re-read: the state this page is reporting has just changed, and the
        // give-up line has to clear without waiting for the ten-second poll.
        this.load();
      },
      error: () => this.said.set('Could not change that.'),
    });
  }

  // The console mounts one section at a time, so a timer left running would
  // poll for a page nobody is looking at.
  /**
   * Merge an uploaded enrichment file.
   *
   * The report replaces the last one rather than accumulating: an operator
   * reads the answer to the import they just ran, and a list of previous ones
   * is a log, which is not what this screen is.
   */
  importEnrichment(event: Event): void {
    const input = event.target as HTMLInputElement;
    const file = input.files?.[0];
    if (!file) {
      return;
    }
    this.said.set('');
    this.report.set(null);
    // The hidden input cannot say what it holds, so the label does.
    this.importName.set(file.name);
    this.api.importEnrichment(file).subscribe({
      next: (r) => this.report.set(r),
      // NAMED, not "something went wrong": the two things that go wrong here
      // are a file this program did not write and one from a newer version,
      // and both are the operator's to fix.
      error: () => this.said.set('That file could not be read as an enrichment export.'),
    });
  }

  ngOnDestroy(): void {
    if (this.timer !== null) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }
}

/** flatten turns the overview's nested JSON into rows a table can render. */
export function flatten(o: unknown, prefix = ''): { key: string; value: string }[] {
  const out: { key: string; value: string }[] = [];
  for (const [k, v] of Object.entries(o as Record<string, unknown>)) {
    const key = prefix ? `${prefix}.${k}` : k;
    if (v !== null && typeof v === 'object' && !Array.isArray(v)) {
      out.push(...flatten(v, key));
      continue;
    }
    out.push({ key, value: String(v) });
  }
  return out;
}

/** duration renders seconds as hours and minutes: 25015 -> "6h 57m". */
export function duration(seconds: number): string {
  const total = Math.round(seconds / 60);
  const h = Math.floor(total / 60);
  const m = total % 60;
  return h ? `${h}h ${m}m` : `${m}m`;
}

/** compact shortens a big count: 200933 -> "201k". */
export function compact(n: number): string {
  if (n >= 1_000_000) {
    return `${Math.round(n / 100_000) / 10}M`;
  }
  return n >= 1000 ? `${Math.round(n / 1000)}k` : String(n);
}
