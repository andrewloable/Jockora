import { Component, inject, output, signal } from '@angular/core';
import { Api, DialStation } from '../api/api';

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
export class Dial {
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

  constructor() {
    this.load();
  }

  load(): void {
    this.api.dial().subscribe({
      next: (d) => {
        this.stations.set(d.stations);
        this.loaded.set(true);
      },
      // A dial that will not load is not a reason to hide the player: a
      // listener already tuned keeps hearing their station.
      error: () => {
        this.loaded.set(true);
        this.error.set('Could not load the dial.');
      },
    });
  }

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
