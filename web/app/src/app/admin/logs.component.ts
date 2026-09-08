import { Component, OnDestroy, computed, inject, signal } from '@angular/core';
import { AdminApi, LogRecord } from '../api/api';

/**
 * What the server is doing.
 *
 * LOGS WENT TO STDERR AND NOWHERE ELSE, so finding out why a station went quiet
 * meant ssh and then docker logs. Three live defects on this deployment were
 * found exactly that way and could not have been found any other way -- which
 * is the argument for this screen and also the proof that the information
 * already existed and was merely out of reach.
 */
@Component({
  selector: 'app-logs',
  standalone: true,
  template: `
    <h2>Logs</h2>

    <fieldset data-log-controls>
      <legend>What you see, and what the server keeps</legend>

      <!-- TWO DIFFERENT THINGS, and the labels have to say so. An operator who
           sets the display filter to debug and sees nothing new concludes the
           feature is broken; the server was never recording debug in the first
           place. One says SHOW, one says RECORD. -->
      <label>
        Show
        <select data-log-show (change)="showing.set($any($event.target).value)">
          @for (l of levels; track l.value) {
            <option [value]="l.value" [selected]="l.value === showing()">{{ l.label }}</option>
          }
        </select>
        <small>Filters the list below. Nothing is thrown away.</small>
      </label>

      <label>
        Record
        <select data-log-record (change)="setRecorded($any($event.target).value)">
          @for (l of levels; track l.value) {
            <option [value]="l.value" [selected]="l.value === recorded()">{{ l.label }}</option>
          }
        </select>
        <small>Changes what the server writes down, immediately and without a restart.</small>
      </label>

      @if (recorded() === 'DEBUG') {
        <!-- A SENTENCE NEXT TO THE CONTROL, not a dialog. Debug is not free and
             the honest place to say so is where the operator just clicked. -->
        <small data-log-debug-warning>
          Debug on a busy station fills the buffer in minutes, so you can look back over less, not
          more. Turn it off when you are done.
        </small>
      }

      <button type="button" data-log-pause (click)="togglePause()">
        {{ paused() ? 'Resume' : 'Pause' }}
      </button>
      <button type="button" data-log-copy (click)="copy()">Copy</button>
      <button type="button" data-log-clear data-danger (click)="clear()">Clear</button>
    </fieldset>

    <p data-log-status>
      <!-- EventSource reconnects on its own, and a quiet station and a dead
           connection look identical without this. -->
      <span data-log-connection>{{ connection() }}</span>
      @if (paused() && pending().length) {
        <!-- COUNTED, never silently dropped: the list held still on purpose and
             the operator decides when to catch up. -->
        <button type="button" data-log-pending (click)="togglePause()">
          {{ pending().length }} new — show them
        </button>
      }
    </p>

    @if (dropped()) {
      <!-- A gap in a log that presents itself as complete is worse than a gap
           that admits it. -->
      <p role="alert" data-log-dropped>
        {{ dropped() }} records were lost because a reader fell behind. What is here is not
        everything.
      </p>
    }

    <table data-logs>
      <thead>
        <tr>
          <th scope="col">Time</th>
          <th scope="col">Level</th>
          <th scope="col">What happened</th>
        </tr>
      </thead>
      <tbody>
        @for (r of visible(); track $index) {
          <tr data-log-row [attr.data-level]="r.level">
            <td data-label="Time">{{ clock(r.time) }}</td>
            <td data-label="Level">{{ r.level }}</td>
            <td data-label="What happened">
              {{ r.message }}
              @if (details(r); as d) {
                <!-- AVAILABLE, NOT SHOUTING. Most records carry several
                     attributes and a wall of key-value pairs is unreadable at a
                     glance; the level and the message are what the eye needs
                     first. -->
                <small data-log-attrs>{{ d }}</small>
              }
            </td>
          </tr>
        } @empty {
          <tr>
            <td colspan="3">
              @if (!loaded()) {
                <span data-loading>Loading…</span>
              } @else if (!failed()) {
                <span data-empty
                  ><strong>Nothing logged yet.</strong> A quiet station writes little; set Record to
                  debug if you are reproducing something.</span
                >
              }
            </td>
          </tr>
        }
      </tbody>
    </table>

    <p data-said>{{ said() }}</p>
  `,
})
export class Logs implements OnDestroy {
  private readonly api = inject(AdminApi);

  readonly levels = [
    { value: 'DEBUG', label: 'everything' },
    { value: 'INFO', label: 'info and above' },
    { value: 'WARN', label: 'warnings and errors' },
    { value: 'ERROR', label: 'errors only' },
  ];

  readonly records = signal<LogRecord[]>([]);
  /** Held back while paused, newest first. */
  readonly pending = signal<LogRecord[]>([]);
  // WARNINGS AND ERRORS, both of them, before anybody chooses.
  // Reported by the operator: at info the list is more than twenty consecutive
  // "scanning elapsed=Ns files=N" lines and the one WARN that matters --
  // SERVING WITHOUT AUTHENTICATION ON A NON-LOOPBACK ADDRESS -- is one row in
  // forty of it. app.DefaultLogLevel is the server's half of the same default.
  readonly showing = signal('WARN');
  readonly recorded = signal('WARN');
  readonly dropped = signal(0);
  readonly paused = signal(false);
  readonly connection = signal('connecting…');
  readonly said = signal('');
  readonly loaded = signal(false);
  readonly failed = signal(false);

  /** What the list shows: a client-side view, never a refetch. */
  readonly visible = computed(() => {
    const floor = rank(this.showing());
    return this.records().filter((r) => rank(r.level) >= floor);
  });

  private source: { close(): void; readyState: number } | null = null;
  /**
   * True once the operator has left this section.
   *
   * ONE SECTION IS MOUNTED AT A TIME, so switching away destroys this
   * component -- and on a slow box the first read is still in flight when that
   * happens. Its response then opened an EventSource that ngOnDestroy had
   * already run for: a subscriber the server keeps for ever, against a cap of
   * eight, dropping every record it will never read.
   */
  private destroyed = false;

  constructor() {
    this.api.logs(200).subscribe({
      next: (r) => {
        if (this.destroyed) {
          return;
        }
        this.records.set(r.records ?? []);
        this.dropped.set(r.dropped ?? 0);
        this.recorded.set(r.level ?? 'WARN');
        this.loaded.set(true);
        this.failed.set(false);
        this.stream();
      },
      error: () => {
        if (this.destroyed) {
          return;
        }
        this.loaded.set(true);
        this.failed.set(true);
        this.said.set('Could not read the log.');
      },
    });
  }

  /** Open the live feed. */
  private stream(): void {
    const es = new EventSource('/admin/logs/stream');
    this.source = es;
    es.onopen = () => this.connection.set('live');
    es.onmessage = (e: MessageEvent) => this.arrived(JSON.parse(e.data) as LogRecord);
    es.onerror = () => {
      // readyState says which of the two this is, and they are very different
      // things to an operator staring at a still list.
      this.connection.set(es.readyState === 2 ? 'disconnected' : 'reconnecting…');
    };
  }

  private arrived(rec: LogRecord): void {
    this.connection.set('live');
    if (this.paused()) {
      this.pending.update((p) => [rec, ...p]);
      return;
    }
    this.records.update((r) => [rec, ...r]);
  }

  togglePause(): void {
    if (this.paused()) {
      this.records.update((r) => [...this.pending(), ...r]);
      this.pending.set([]);
    }
    this.paused.update((p) => !p);
  }

  /** Change what the SERVER writes down. */
  setRecorded(level: string): void {
    this.api.setLogLevel(level).subscribe({
      next: () => {
        this.recorded.set(level);
        this.said.set(`Recording ${level.toLowerCase()} and above.`);
      },
      error: (e: { error?: { error?: string } }) =>
        this.said.set(e.error?.error ?? 'Could not change what is recorded.'),
    });
  }

  clear(): void {
    // DESTRUCTIVE, so it asks -- and names BOTH halves, because "clear" that
    // silently deletes the stored warnings as well is a surprise nobody wants
    // to discover at three in the morning.
    if (!confirm('Clear the log? This empties the live view and the stored warnings and errors.')) {
      return;
    }
    this.api.clearLogs().subscribe({
      next: () => {
        this.pending.set([]);
        this.said.set('Log cleared.');
        this.api.logs(200).subscribe({
          next: (r) => {
            this.records.set(r.records ?? []);
            this.dropped.set(r.dropped ?? 0);
          },
          error: () => this.said.set('Cleared, but the log could not be re-read.'),
        });
      },
      error: (e: { error?: { error?: string } }) =>
        this.said.set(e.error?.error ?? 'Could not clear the log.'),
    });
  }

  copy(): void {
    // THE VISIBLE ONES. The next thing that happens after an operator finds an
    // error is that they paste it to somebody, and a line they had filtered out
    // would confuse both of them.
    const text = this.visible()
      .map(
        (r) => `${r.time} ${r.level} ${r.message}${this.details(r) ? ' ' + this.details(r) : ''}`,
      )
      .join('\n');
    navigator.clipboard.writeText(text).then(
      () => this.said.set(`${this.visible().length} records copied.`),
      () => this.said.set('Could not reach the clipboard.'),
    );
  }

  /** The time a person reads, not the ISO string a machine wrote. */
  clock(at: string): string {
    const d = new Date(at);
    return isNaN(d.getTime()) ? at : d.toTimeString().slice(0, 8);
  }

  /** The attributes, on one line, or nothing. */
  details(r: LogRecord): string {
    const attrs = r.attrs ?? {};
    const keys = Object.keys(attrs);
    return keys.length ? keys.map((k) => `${k}=${attrs[k]}`).join(' ') : '';
  }

  ngOnDestroy(): void {
    // A leaked EventSource is a subscriber the server keeps and drops into,
    // counting every record as lost. The flag closes the other door: a read
    // still in flight must not open one AFTER this has run.
    this.destroyed = true;
    this.source?.close();
    this.source = null;
  }
}

/** Order for the display filter. Unknown levels sort with info. */
function rank(level: string): number {
  return { DEBUG: 0, INFO: 1, WARN: 2, ERROR: 3 }[level] ?? 1;
}
