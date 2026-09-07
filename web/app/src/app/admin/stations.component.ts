import { Component, inject, output, signal } from '@angular/core';
import { AdminApi, Jock, Station } from '../api/api';

/**
 * The dial, from the operator's side.
 */
@Component({
  selector: 'app-stations',
  standalone: true,
  template: `
    <h2>Stations</h2>
    <table data-stations>
      @for (station of stations(); track station.id) {
        <tr>
          <td>{{ station.name }}</td>
          <td>{{ station.genre }}{{ station.mood ? ' · ' + station.mood : '' }}</td>
          <td>{{ station.tracks }}</td>
          <td>
            @if (station.warning) {
              <span data-warning>{{ station.warning }}</span>
            }
          </td>
          <td>
            <select
              data-jock
              [value]="station.jock_id ?? ''"
              (change)="assign(station, $any($event.target).value)"
            >
              <option value="">— no jock —</option>
              @for (jock of jocks(); track jock.id) {
                <option [value]="jock.id">{{ jock.name }}</option>
              }
            </select>
          </td>
          <td>
            <button type="button" data-toggle (click)="toggle(station)">
              {{ station.enabled ? 'Disable' : 'Enable' }}
            </button>
            <button type="button" data-remove (click)="remove(station)">Delete</button>
            <button type="button" data-edit (click)="edit.emit(station)">Playlist</button>
          </td>
        </tr>
      }
    </table>

    <fieldset>
      <legend>Add a station</legend>
      <input
        data-name
        placeholder="name"
        [value]="name()"
        (input)="name.set($any($event.target).value)"
      />
      <select data-genre [value]="genre()" (change)="genre.set($any($event.target).value)">
        @for (g of vocab().genres; track g) {
          <option [value]="g">{{ g }}</option>
        }
      </select>
      <select data-mood [value]="mood()" (change)="mood.set($any($event.target).value)">
        <option value="">any mood</option>
        @for (m of vocab().moods; track m) {
          <option [value]="m">{{ m }}</option>
        }
      </select>
      <button type="button" data-add (click)="add()">Add</button>
    </fieldset>

    <p data-said>{{ said() }}</p>
  `,
})
export class Stations {
  private readonly api = inject(AdminApi);

  readonly stations = signal<Station[]>([]);
  readonly jocks = signal<Jock[]>([]);
  readonly vocab = signal<{ genres: string[]; moods: string[] }>({ genres: [], moods: [] });
  readonly name = signal('');
  readonly genre = signal('');
  readonly mood = signal('');
  readonly said = signal('');

  /** edit asks the console to open one station's playlist. */
  readonly edit = output<Station>();

  constructor() {
    this.load();
    // The VOCABULARY comes from the server, so the console offers choices
    // rather than a text box a typo can defeat -- a station whose genre is a
    // typo is one that will never fill, with nothing on screen to say why.
    this.api.vocab().subscribe({
      next: (v) => {
        this.vocab.set(v);
        this.genre.set(v.genres[0] ?? '');
      },
      error: () => this.said.set('Could not read the vocabulary.'),
    });
    this.api.jocks().subscribe({ next: (j) => this.jocks.set(j), error: () => undefined });
  }

  load(): void {
    this.api.stations().subscribe({
      next: (s) => this.stations.set(s),
      error: () => this.said.set('Could not read the stations.'),
    });
  }

  add(): void {
    this.said.set('');
    this.api
      .addStation({ name: this.name(), genre: this.genre(), mood: this.mood() })
      .subscribe({
        next: (made) => {
          // The TRACK COUNT, immediately: the operator sees a number rather
          // than a promise, and finds out at once if the filter selects
          // nothing.
          this.said.set(`Added with ${made.tracks} tracks.`);
          this.name.set('');
          this.load();
        },
        error: (e: { error?: { error?: string } }) =>
          this.said.set(String(e.error?.error ?? 'Could not add that station.')),
      });
  }

  toggle(station: Station): void {
    this.api.setStationEnabled(station.id, !station.enabled).subscribe({
      next: (answer) => {
        this.said.set(answer?.warning ? answer.warning : '');
        this.load();
      },
      error: (e: { status?: number; error?: { tracks?: number; minimum?: number } }) => {
        // The NUMBERS, because "too few tracks" leaves an operator guessing
        // how many more they need.
        if (e.status === 409 && e.error?.tracks !== undefined) {
          this.said.set(`Too few tracks: ${e.error.tracks} of ${e.error.minimum} needed.`);
          return;
        }
        this.said.set('Could not change that station.');
      },
    });
  }

  remove(station: Station): void {
    if (!confirm(`Delete ${station.name}? Its playlist goes with it.`)) {
      return;
    }
    this.api.removeStation(station.id).subscribe({
      next: () => this.load(),
      error: () => this.said.set('Could not delete that station.'),
    });
  }

  assign(station: Station, jockId: string): void {
    this.api.assignJock(station.id, jockId === '' ? null : jockId).subscribe({
      next: () => this.load(),
      error: () => this.said.set('Could not put that jock on air.'),
    });
  }
}
