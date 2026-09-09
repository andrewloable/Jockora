import { Component, OnDestroy, inject, output, signal } from '@angular/core';
import { Api, DialStation } from '../api/api';

/**
 * How often the dial re-asks WHILE SOMEBODY IS LOOKING AT IT.
 *
 * The same four seconds now-playing settled on, and for the same reason. This
 * exists because the dial used to fetch once: a station that finished building
 * its playlist stayed a disabled "still filling" button for as long as the tab
 * was open, on the listener's whole UI. Jockora-e9a.64.
 */
export const DIAL_POLL_MS = 4000;

/**
 * The dial. THE LISTENER'S WHOLE UI: they pick a station, never a jock, and
 * never a track.
 */
@Component({
  selector: 'app-dial',
  standalone: true,
  template: `
    <div data-dial>
      @for (station of stations(); track station.id) {
        <button
          type="button"
          data-station
          [attr.aria-pressed]="station.id === current()"
          [attr.aria-disabled]="station.ready === false ? 'true' : null"
          [disabled]="station.ready === false"
          (click)="tune(station)"
        >
          <span data-station-name>{{ station.name }}</span>
          <small>
            @if (station.ready === false) {
              still filling · {{ station.tracks }} tracks so far
            } @else {
              {{ station.tracks }} tracks
            }
            @if (station.jock_name) {
              · {{ station.jock_name }}
            }
            @if (station.mood) {
              · {{ spaced(station.mood) }}
            }
            @if (station.listeners) {
              · {{ station.listeners }} listening
            }
          </small>
        </button>
      }
      @if (!loaded()) {
        <p data-loading>Finding the stations…</p>
      } @else if (!stations().length && !error()) {
        <!-- A listener cannot fix this and should not be sent to a console
             they cannot open, so it says who can. -->
        <p data-empty>No stations yet. An operator builds them in the console.</p>
      }
      @if (error()) {
        <p role="alert" data-error>{{ error() }}</p>
      }
    </div>
  `,
})
export class Dial implements OnDestroy {
  /**
   * The moods, with somewhere to break.
   *
   * The server stores and sends them comma-joined with no spaces, and in the
   * monospace face "melancholic,euphoric,calm,nocturnal,lonely,hypnotic" is one
   * unbreakable 51-character token: it drew 377px of text inside a 206px card
   * and scrolled the whole listener page sideways on a phone. Not truncated --
   * somebody choosing a station is exactly who needs to know what is on it.
   */
  spaced(list: string): string {
    return list
      .split(',')
      .map((v) => v.trim())
      .filter((v) => v)
      .join(', ');
  }

  private readonly api = inject(Api);

  readonly stations = signal<DialStation[]>([]);
  readonly current = signal<number | null>(null);
  readonly error = signal('');

  /**
   * True once the first answer has arrived, success OR failure.
   *
   * The dial IS the listener's UI, and "no stations yet" drew while the answer
   * was still in flight -- so a slow connection told a listener their operator
   * had built nothing. Jockora-e9a.50.
   */
  readonly loaded = signal(false);

  /** tuned carries the station and where to listen to it. */
  readonly tuned = output<{ station: DialStation; hls: string }>();

  private timer: ReturnType<typeof setInterval> | null = null;

  constructor() {
    document.addEventListener('visibilitychange', this.onVisibility);
    this.load();
  }

  load(): void {
    this.api.dial().subscribe({
      next: (d) => {
        this.stations.set(d.stations);
        this.loaded.set(true);
        this.pollWhileVisible();
      },
      // A dial that will not load is not a reason to hide the player: a
      // listener already tuned keeps hearing their station.
      error: () => {
        this.loaded.set(true);
        this.error.set('Could not load the dial.');
        // NOT RETRIED. A dial that will not load is a different problem from a
        // station that is still filling, and hammering a server that just
        // refused is not how to find out which.
        this.stop();
      },
    });
  }

  /**
   * Keep asking while the dial is being looked at.
   *
   * NOT followFilling any more, which is what this was called: it no longer
   * has anything to do with a station filling up, and a name that describes
   * the condition it used to test is worse than no name.
   *
   * THIS USED TO STOP AS SOON AS EVERY STATION WAS READY, and Jockora-e9a.64's
   * DO NOT said so in as many words: a dial of ready stations should make no
   * requests at all. That was written before the dial carried a listener
   * count. The count is the one number here that changes on its own -- the
   * server recomputes it from the presence tracker on every request -- and it
   * was the one number the poll had been taught to stop watching, so it froze
   * at whatever it was when the page loaded. Reported live. Jockora-1ge.
   *
   * THE REASON BEHIND THAT DO NOT IS KEPT AND ONLY ITS WORDING CHANGES. What
   * it was defending against is a four-second timer running for ever on a page
   * nobody is looking at, and the Page Visibility API answers exactly that
   * question. A backgrounded tab still makes no requests; a destroyed
   * component still leaves no timer; and a listener actually watching the dial
   * sees the count move.
   */
  private pollWhileVisible(): void {
    if (document.visibilityState === 'hidden') {
      this.stop();
      return;
    }
    this.timer ??= setInterval(() => this.load(), DIAL_POLL_MS);
  }

  private stop(): void {
    if (this.timer !== null) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }

  ngOnDestroy(): void {
    // A timer left running polls for ever on a page nobody is looking at.
    this.stop();
    document.removeEventListener('visibilitychange', this.onVisibility);
  }

  /**
   * Bound once, so removeEventListener can actually find it again. An inline
   * arrow here would register a different function every time and the listener
   * would outlive the component it belongs to.
   */
  private readonly onVisibility = (): void => {
    if (document.visibilityState === 'hidden') {
      this.stop();
      return;
    }
    // BACK IN FRONT OF THEM, and asked at once rather than in four seconds:
    // returning to a tab is exactly when the count on screen is most stale.
    this.load();
  };

  tune(station: DialStation): void {
    this.error.set('');
    this.api.tune(station.id).subscribe({
      next: (answer) => {
        this.current.set(station.id);
        this.tuned.emit({ station, hls: answer.hls });
      },
      error: () => this.error.set('That station is not available.'),
    });
  }
}
