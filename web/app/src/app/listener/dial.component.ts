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
          (click)="tune(station)"
        >
          {{ station.name }} {{ station.tracks }}
          <small>
            @if (station.jock_name) {
              {{ station.jock_name }}
            }
            @if (station.mood) {
              · {{ station.mood }}
            }
            @if (station.listeners) {
              · {{ station.listeners }} listening
            }
          </small>
        </button>
      }
      @if (!stations().length) {
        <p data-empty>No stations yet. An operator builds them in the console.</p>
      }
      @if (error()) {
        <p role="alert" data-error>{{ error() }}</p>
      }
    </div>
  `,
})
export class Dial {
  private readonly api = inject(Api);

  readonly stations = signal<DialStation[]>([]);
  readonly current = signal<number | null>(null);
  readonly error = signal('');

  /** tuned carries the station and where to listen to it. */
  readonly tuned = output<{ station: DialStation; hls: string }>();

  constructor() {
    this.load();
  }

  load(): void {
    this.api.dial().subscribe({
      next: (d) => this.stations.set(d.stations),
      // A dial that will not load is not a reason to hide the player: a
      // listener already tuned keeps hearing their station.
      error: () => this.error.set('Could not load the dial.'),
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
