import { Component, OnDestroy, inject, signal } from '@angular/core';
import { DecimalPipe } from '@angular/common';
import { AdminApi } from '../api/api';

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
    @if (rows().length) {
      <table data-overview>
        @for (row of rows(); track row.key) {
          <tr>
            <th>{{ row.key }}</th>
            <td>{{ row.value }}</td>
          </tr>
        }
      </table>
    }

    <fieldset>
      <legend>Break cadence</legend>
      <input
        data-cadence
        type="number"
        min="1"
        max="100"
        [value]="cadence()"
        (input)="editCadence(+$any($event.target).value)"
      />
      <button type="button" data-set-cadence (click)="saveCadence()">Set</button>
    </fieldset>

    <fieldset>
      <legend>Enrichment</legend>
      @if (progress(); as p) {
        <div data-enrich-progress>
          <progress [value]="p.done" [max]="p.total"></progress>
          <p data-enrich-line>
            <strong>{{ p.done | number }} of {{ p.total | number }}</strong> tracks
            ({{ p.pct }}%) · {{ p.state }}
          </p>
          <p data-enrich-eta>{{ p.eta }}</p>
        </div>
      } @else {
        <p data-enrich-progress>Nothing scanned yet.</p>
      }
      <button type="button" data-pause (click)="setEnriching(false)">Pause</button>
      <button type="button" data-resume (click)="setEnriching(true)">Resume</button>
    </fieldset>

    <p data-said>{{ said() }}</p>
  `,
})
export class Overview implements OnDestroy {
  private readonly api = inject(AdminApi);

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
  progress(): { done: number; total: number; pct: number; state: string; eta: string } | null {
    const lib = (this.overview()['library'] ?? {}) as Record<string, number>;
    const total = lib['tracks'] ?? 0;
    if (!total) {
      return null;
    }
    const done = lib['enriched'] ?? 0;
    const cost = (this.overview()['enrichment_cost'] ?? {}) as Record<string, number>;
    const perTrack = cost['seconds_per_track'] ?? 0;
    const running = this.overview()['enriching'] === true;

    return {
      done,
      total,
      pct: Math.round((done / total) * 1000) / 10,
      state: running ? 'running' : 'paused',
      eta: this.eta(total - done, perTrack, running),
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
      },
      error: () => this.said.set('Could not read the overview.'),
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
      error: (e: { error?: string }) => this.said.set(String(e.error ?? 'Could not set that.')),
    });
  }

  setEnriching(on: boolean): void {
    this.api.setEnriching(on).subscribe({
      next: () =>
        this.said.set(on ? 'Enrichment running.' : 'Enrichment paused. The stream is unaffected.'),
      error: () => this.said.set('Could not change that.'),
    });
  }

  // The console mounts one section at a time, so a timer left running would
  // poll for a page nobody is looking at.
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
