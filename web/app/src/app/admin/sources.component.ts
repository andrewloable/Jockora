import { Component, inject, signal } from '@angular/core';
import { Api, AdminApi, Source } from '../api/api';

/**
 * Where the music comes from, and the button that goes and gets it.
 */
@Component({
  selector: 'app-sources',
  standalone: true,
  template: `
    <h2>Sources</h2>
    <table data-sources>
      @for (source of sources(); track source.id) {
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
    </table>

    <fieldset>
      <legend>Add a source</legend>
      <select data-kind [value]="kind()" (change)="kind.set($any($event.target).value)">
        <option value="folder">folder</option>
        <option value="subsonic">subsonic</option>
      </select>
      <input
        data-locator
        placeholder="path or URL"
        [value]="locator()"
        (input)="locator.set($any($event.target).value)"
      />
      @if (kind() === 'subsonic') {
        <input
          data-username
          placeholder="username"
          [value]="username()"
          (input)="username.set($any($event.target).value)"
        />
        <input
          data-password
          type="password"
          placeholder="password"
          [value]="password()"
          (input)="password.set($any($event.target).value)"
        />
      }
      <button type="button" data-add (click)="add()">Add</button>
    </fieldset>

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
export class Sources {
  private readonly api = inject(AdminApi);
  private readonly listener = inject(Api);

  readonly sources = signal<Source[]>([]);
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
  readonly said = signal('');
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
          this.locator.set('');
          this.password.set('');
          this.load();
        },
        // The SERVER'S OWN WORDS: it stats the folder and pings the server, and
        // its refusal says which one failed and why.
        error: (e: { error?: { error?: string } }) =>
          this.said.set(String(e.error?.error ?? 'Could not add that source.')),
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
      next: (n) => this.progress.set((n as { rescan?: never }).rescan ?? null),
      error: () => undefined,
    });
  }
}
