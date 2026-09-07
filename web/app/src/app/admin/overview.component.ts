import { Component, inject, signal } from '@angular/core';
import { AdminApi } from '../api/api';

/**
 * What the station knows about itself, and the two things worth changing
 * without a restart.
 */
@Component({
  selector: 'app-overview',
  standalone: true,
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
        (input)="cadence.set(+$any($event.target).value)"
      />
      <button type="button" data-set-cadence (click)="saveCadence()">Set</button>
    </fieldset>

    <fieldset>
      <legend>Enrichment</legend>
      <button type="button" data-pause (click)="setEnriching(false)">Pause</button>
      <button type="button" data-resume (click)="setEnriching(true)">Resume</button>
    </fieldset>

    <p data-said>{{ said() }}</p>
  `,
})
export class Overview {
  private readonly api = inject(AdminApi);

  readonly rows = signal<{ key: string; value: string }[]>([]);
  readonly cadence = signal(4);
  readonly said = signal('');

  constructor() {
    this.load();
  }

  load(): void {
    this.api.overview().subscribe({
      next: (o) => this.rows.set(flatten(o)),
      error: () => this.said.set('Could not read the overview.'),
    });
  }

  saveCadence(): void {
    this.api.setCadence(this.cadence()).subscribe({
      next: () => this.said.set('Cadence set. It takes effect from the next break.'),
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
