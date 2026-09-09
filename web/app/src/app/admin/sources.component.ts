import { Component, OnDestroy, inject, signal } from '@angular/core';
import { Api, AdminApi, Source, serverSaid } from '../api/api';
import { TableView } from './table-view';
import { FormDialog } from './form-dialog';

/**
 * How often the console re-asks WHILE A SCAN IS RUNNING.
 *
 * The same four seconds now-playing settled on. This exists because
 * pollProgress used to call the endpoint exactly once: Rescan said "Progress
 * appears below", one snapshot appeared, and the number never moved again -- so
 * on a large library the operator could not tell a running scan from a finished
 * one. Jockora-e9a.64.
 */
export const SCAN_POLL_MS = 4000;

/**
 * Where the music comes from, and the button that goes and gets it.
 */
@Component({
  selector: 'app-sources',
  standalone: true,
  imports: [FormDialog],
  template: `
    <h2>Sources</h2>
    <!-- SEARCH AND SORT over the whole list, which is all in the browser.
         Jockora-9g7. -->
    <label data-table-search>
      Search
      <input
        data-search
        type="search"
        placeholder="kind, folder or server"
        [value]="view.query()"
        (input)="view.query.set($any($event.target).value)"
      />
    </label>
    <table data-sources>
      <!-- A HEADER ROW AT ALL. This table had none: five columns of bare cells,
           so nothing said which was the folder and which the username, and a
           screen reader had no column names to announce. Jockora-9g7. -->
      <thead>
        <tr>
          <th scope="col" [attr.aria-sort]="view.ariaSort('kind')">
            <button type="button" data-sort="kind" (click)="view.toggle('kind')">
              Kind <span data-sort-marker>{{ view.marker('kind') }}</span>
            </button>
          </th>
          <th scope="col" [attr.aria-sort]="view.ariaSort('locator')">
            <button type="button" data-sort="locator" (click)="view.toggle('locator')">
              Where <span data-sort-marker>{{ view.marker('locator') }}</span>
            </button>
          </th>
          <th scope="col" [attr.aria-sort]="view.ariaSort('username')">
            <button type="button" data-sort="username" (click)="view.toggle('username')">
              Username <span data-sort-marker>{{ view.marker('username') }}</span>
            </button>
          </th>
          <th scope="col" [attr.aria-sort]="view.ariaSort('enabled')">
            <button type="button" data-sort="enabled" (click)="view.toggle('enabled')">
              Status <span data-sort-marker>{{ view.marker('enabled') }}</span>
            </button>
          </th>
          <!-- NOT SORTABLE: a column of buttons has no order. -->
          <th scope="col">Actions</th>
        </tr>
      </thead>
      <tbody>
        @for (source of view.shown(); track source.id) {
          <tr>
            <td>{{ source.kind }}</td>
            <td>{{ source.locator }}</td>
            <td>{{ source.username }}</td>
            <td>{{ source.enabled ? 'on' : 'off' }}</td>
            <td>
              <button type="button" data-toggle (click)="toggle(source)">
                {{ source.enabled ? 'Disable' : 'Enable' }}
              </button>
              <button type="button" data-remove data-danger (click)="remove(source)">Remove</button>
            </td>
          </tr>
        } @empty {
          <!-- LOADING AND EMPTY ARE NOT THE SAME THING and both drew a table
             with no rows, so a slow first paint told a new operator their
             library was empty. Jockora-e9a.50. -->
          <tr>
            <td colspan="5">
              @if (!loaded()) {
                <span data-loading>Loading…</span>
              } @else if (!failed()) {
                <span data-empty
                  ><strong>Nothing scanned yet.</strong> Add a source below and Jockora reads it. It
                  is never written to.</span
                >
              }
            </td>
          </tr>
        }
      </tbody>
    </table>

    <button type="button" data-add-open (click)="adding.set(true)">Add a source</button>
    <app-form-dialog [title]="'Add a source'" [open]="adding()" (closed)="closeAdd()">
      @if (adding()) {
        <fieldset data-source-form>
          <legend>Add a source</legend>
          <label>
            Kind
            <select data-kind [value]="kind()" (change)="kind.set($any($event.target).value)">
              <option value="folder">folder</option>
              <option value="subsonic">subsonic</option>
            </select>
          </label>
          <label>
            Where it is
            <!-- THE PLACEHOLDER SURVIVES because it is an EXAMPLE rather than a
             repeat of the label -- which is the whole distinction this task
             turns on. -->
            <input
              data-locator
              placeholder="path or URL"
              [value]="locator()"
              (input)="locator.set($any($event.target).value)"
            />
          </label>
          @if (kind() === 'subsonic') {
            <label>
              Username
              <input
                data-username
                [value]="username()"
                (input)="username.set($any($event.target).value)"
              />
            </label>
            <label>
              Password
              <input
                data-password
                type="password"
                [value]="password()"
                (input)="password.set($any($event.target).value)"
              />
            </label>
          }
          <div data-form-actions>
            <button type="button" data-add (click)="add()">Add</button>
          </div>
        </fieldset>
      }
    </app-form-dialog>

    <button type="button" data-rescan (click)="rescan()">Rescan</button>
    @if (progress(); as p) {
      <p data-progress>
        {{ p.running ? 'Scanning' : 'Last scan' }}: found {{ p.found }}, skipped {{ p.skipped }},
        missing {{ p.missing }}
      </p>
    }
    <p data-said>{{ said() }}</p>
  `,
})
export class Sources implements OnDestroy {
  private readonly api = inject(AdminApi);
  private readonly listener = inject(Api);

  readonly sources = signal<Source[]>([]);

  /** The search box and the sortable headers. Jockora-9g7. */
  readonly view = new TableView<Source>(this.sources, [
    { key: 'kind', value: (s) => s.kind },
    { key: 'locator', value: (s) => s.locator },
    { key: 'username', value: (s) => s.username },
    { key: 'enabled', value: (s) => (s.enabled ? 'on' : 'off') },
  ]);
  /**
   * True once the first answer has arrived, success OR failure.
   *
   * Without it a table with no rows means two opposite things -- the request is
   * still in flight, or there is genuinely nothing -- and both drew the same
   * empty table. A new operator was told their library was empty and given
   * nothing to do about it. Jockora-e9a.50.
   */
  readonly loaded = signal(false);
  /**
   * True when the last read FAILED, as opposed to returning nothing.
   *
   * Three states, not two: a table with no rows can be in flight, genuinely
   * empty, or the wreckage of a request that did not come back. Without this
   * the third one wore the second one's words and told an operator whose
   * server was down that they had never scanned anything.
   */
  readonly failed = signal(false);
  readonly kind = signal('folder');
  readonly locator = signal('');
  readonly username = signal('');
  readonly password = signal('');
  /** Whether the Add a source dialog is open. */
  readonly adding = signal(false);
  readonly said = signal('');
  private timer: ReturnType<typeof setInterval> | null = null;
  readonly progress = signal<{
    running: boolean;
    found: number;
    skipped: number;
    missing: number;
  } | null>(null);

  constructor() {
    this.load();
  }

  load(): void {
    this.api.sources().subscribe({
      next: (s) => {
        this.sources.set(s);
        this.loaded.set(true);
        this.failed.set(false);
      },
      error: () => {
        this.loaded.set(true);
        this.failed.set(true);
        this.said.set('Could not read the sources.');
      },
    });
  }

  /**
   * Close the Add a source dialog, taking whatever was half-typed with it --
   * the password especially, which is a credential for one server.
   */
  closeAdd(): void {
    this.adding.set(false);
    this.locator.set('');
    this.username.set('');
    this.password.set('');
  }

  add(): void {
    this.said.set('');
    this.api
      .addSource({
        kind: this.kind(),
        locator: this.locator(),
        username: this.username(),
        password: this.password(),
      })
      .subscribe({
        next: () => {
          this.closeAdd();
          this.load();
        },
        // The SERVER'S OWN WORDS: it stats the folder and pings the server, and
        // its refusal says which one failed and why.
        error: (e: unknown) => this.said.set(serverSaid(e, 'Could not add that source.')),
      });
  }

  remove(source: Source): void {
    // ASKED, and it says what is NOT destroyed: the tracks stay, marked
    // missing, and keep their dossiers. Somebody who thinks they are deleting
    // a library needs to know they are not.
    if (!confirm('Remove this source? Its tracks are kept and marked missing.')) {
      return;
    }
    this.api.removeSource(source.id).subscribe({
      next: () => {
        // Said out loud, because the tracks are NOT deleted with it -- they
        // are marked missing and keep their dossiers.
        this.said.set('Removed. Its tracks are kept and marked missing.');
        this.load();
      },
      error: () => this.said.set('Could not remove that source.'),
    });
  }

  toggle(source: Source): void {
    this.api.setSourceEnabled(source.id, !source.enabled).subscribe({
      next: () => this.load(),
      error: () => this.said.set('Could not change that source.'),
    });
  }

  rescan(): void {
    this.said.set('');
    this.api.rescan().subscribe({
      next: () => {
        this.said.set('Scanning. Progress appears below.');
        this.pollProgress();
      },
      error: (e: { status?: number }) =>
        this.said.set(e.status === 409 ? 'A scan is already running.' : 'Could not start a scan.'),
    });
  }

  /** Progress rides on /now.json, which the page already polls elsewhere. */
  pollProgress(): void {
    this.listener.now(0).subscribe({
      next: (n) => {
        this.progress.set((n as { rescan?: never }).rescan ?? null);
        this.followScan();
      },
      // A dropped poll leaves the LAST answer on screen and stops asking. The
      // alternative is retrying against a server that just failed, which is
      // not how to find out whether the scan is still going.
      error: () => this.stopPolling(),
    });
  }

  /**
   * Keep asking only while a scan is actually running.
   *
   * ONLY WHILE. An idle scanner has nothing to report, so the page makes no
   * requests at all -- a four-second poll that never ends is a poll running on
   * every console tab for ever.
   */
  private followScan(): void {
    if (!this.progress()?.running) {
      this.stopPolling();
      return;
    }
    this.timer ??= setInterval(() => this.pollProgress(), SCAN_POLL_MS);
  }

  private stopPolling(): void {
    if (this.timer !== null) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }

  ngOnDestroy(): void {
    // A timer left running polls for ever on a page nobody is looking at.
    this.stopPolling();
  }
}
