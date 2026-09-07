import { Component, inject, output, signal } from '@angular/core';
import { AdminApi, Diff, Jock, Station } from '../api/api';

/**
 * The dial, from the operator's side.
 */
@Component({
  selector: 'app-stations',
  standalone: true,
  template: `
    <h2>Stations</h2>
    <table data-stations>
      <thead>
        <tr>
          <th>Name</th>
          <th>Genre &middot; mood</th>
          <th>Tracks</th>
          <th>Warning</th>
          <th>Jock</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
      @for (station of stations(); track station.id) {
        <tr>
          @if (editing() === station.id) {
            <td>
              <input
                data-edit-name
                [value]="editName()"
                (input)="editName.set($any($event.target).value)"
              />
            </td>
            <td>
              <!-- CHECKBOXES, not a multiple select. A multiple select needs
                   ctrl-click to pick two things that are not next to each
                   other, which is a keyboard trick people do not know and
                   cannot see. A list of checkboxes shows every option and its
                   state at once, and the checked boxes ARE the display -- so
                   nothing restates them underneath. -->
              <fieldset data-edit-genre>
                <legend>Genre</legend>
                @for (g of vocab().genres; track g) {
                  <label>
                    <input
                      type="checkbox"
                      [value]="g"
                      [checked]="editGenres().includes(g)"
                      (change)="editGenres.set(pick(vocab().genres, editGenres(), g, $event))"
                    />{{ g }}
                  </label>
                }
                <small>{{ editGenres().length ? '' : 'none ticked · any genre' }}</small>
              </fieldset>
              <fieldset data-edit-mood>
                <legend>Mood</legend>
                @for (m of vocab().moods; track m) {
                  <label>
                    <input
                      type="checkbox"
                      [value]="m"
                      [checked]="editMoods().includes(m)"
                      (change)="editMoods.set(pick(vocab().moods, editMoods(), m, $event))"
                    />{{ m }}
                  </label>
                }
                <small>{{ editMoods().length ? '' : 'none ticked · any mood' }}</small>
              </fieldset>
            </td>
          } @else {
            <td>{{ station.name }}</td>
            <td>{{ describe(station) }}</td>
          }
          <td>{{ station.tracks }}</td>
          <td>
            @if (station.warning) {
              <span data-warning>{{ station.warning }}</span>
            }
          </td>
          <td>
            @if (editing() === station.id) {
              <select data-jock (change)="editJock.set($any($event.target).value)">
                <option value="" [selected]="!editJock()">— no jock —</option>
                @for (jock of jocks(); track jock.id) {
                  <option [value]="jock.id" [selected]="jock.id === editJock()">
                    {{ jock.name }}
                  </option>
                }
              </select>
            } @else {
              {{ jockName(station) }}
            }
          </td>
          <td>
            @if (editing() === station.id) {
              <button type="button" data-save (click)="save(station)">Save</button>
              <button type="button" data-cancel (click)="cancel()">Cancel</button>
            } @else {
              <button type="button" data-edit (click)="startEdit(station)">Edit</button>
              <button type="button" data-toggle (click)="toggle(station)">
                {{ station.enabled ? 'Disable' : 'Enable' }}
              </button>
              <button type="button" data-remove (click)="remove(station)">Delete</button>
              <button type="button" data-playlist (click)="playlist.emit(station)">Playlist</button>
            }
          </td>
        </tr>
      }
      </tbody>
    </table>

    <fieldset>
      <legend>Add a station</legend>
      <input
        data-name
        placeholder="name"
        [value]="name()"
        (input)="name.set($any($event.target).value)"
      />
      <!-- [selected] per option, never [value] on the select: these options
           come from the vocabulary, which arrives over HTTP, and a select whose
           value is bound before its options exist silently falls back to the
           first one. -->
      <fieldset data-genre>
        <legend>Genre</legend>
        @for (g of vocab().genres; track g) {
          <label>
            <input
              type="checkbox"
              [value]="g"
              [checked]="genres().includes(g)"
              (change)="genres.set(pick(vocab().genres, genres(), g, $event))"
            />{{ g }}
          </label>
        }
        <small>{{ genres().length ? '' : 'none ticked · any genre' }}</small>
      </fieldset>
      <fieldset data-mood>
        <legend>Mood</legend>
        @for (m of vocab().moods; track m) {
          <label>
            <input
              type="checkbox"
              [value]="m"
              [checked]="moods().includes(m)"
              (change)="moods.set(pick(vocab().moods, moods(), m, $event))"
            />{{ m }}
          </label>
        }
        <small>{{ moods().length ? '' : 'none ticked · any mood' }}</small>
      </fieldset>
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
  readonly genres = signal<string[]>([]);
  readonly moods = signal<string[]>([]);
  readonly said = signal('');

  /** playlist asks the console to open one station's playlist. */
  readonly playlist = output<Station>();

  // WHICH ROW IS OPEN, by station id rather than a boolean, so opening a second
  // row closes the first: two half-edited rows and one Save button between them
  // is a way to write the wrong station's name.
  readonly editing = signal<number | null>(null);
  readonly editName = signal('');
  readonly editGenres = signal<string[]>([]);
  readonly editMoods = signal<string[]>([]);
  readonly editJock = signal('');

  constructor() {
    this.load();
    // The VOCABULARY comes from the server, so the console offers choices
    // rather than a text box a typo can defeat -- a station whose genre is a
    // typo is one that will never fill, with nothing on screen to say why.
    this.api.vocab().subscribe({
      // NOTHING PRESELECTED. An empty genre now means every genre, which is a
      // sensible station to make and used to be impossible to ask for.
      next: (v) => this.vocab.set(v),
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

  /**
   * The selection with one value ticked or unticked, IN VOCABULARY ORDER.
   *
   * Built by filtering the vocabulary rather than by appending, so the stored
   * value depends on what is chosen and not on the order it was clicked in --
   * the same station saved twice is then the same string, and a diff between
   * two of them means something.
   */
  pick(all: string[], current: string[], value: string, e: Event): string[] {
    const on = (e.target as HTMLInputElement).checked;
    return all.filter((v) => (v === value ? on : current.includes(v)));
  }

  /** What this station will play, said plainly. */
  hint(genres: string[], moods: string[]): string {
    const g = genres.length ? genres.join(', ') : 'any genre';
    const m = moods.length ? moods.join(', ') : 'any mood';
    return `${g} · ${m}`;
  }

  /** The same sentence for a row that is not being edited. */
  describe(st: Station): string {
    return this.hint(st.genres ?? [], st.moods ?? []);
  }

  startEdit(st: Station): void {
    this.said.set('');
    this.editing.set(st.id);
    this.editName.set(st.name);
    this.editGenres.set([...(st.genres ?? [])]);
    this.editMoods.set([...(st.moods ?? [])]);
    this.editJock.set(st.jock_id ?? '');
  }

  /** The jock's NAME, for a row that is not being edited. */
  jockName(st: Station): string {
    return this.jocks().find((j) => j.id === st.jock_id)?.name ?? '— no jock —';
  }

  cancel(): void {
    this.editing.set(null);
  }

  /**
   * Save one station's name, genre and mood.
   *
   * A REFUSAL LEAVES THE ROW OPEN. The server rejects a duplicate name and an
   * out-of-vocabulary genre, and closing the row on refusal would throw away
   * what the operator typed along with the chance to correct it.
   */
  save(st: Station): void {
    this.said.set('');
    this.api
      .updateStation(st.id, {
        name: this.editName(),
        genres: this.editGenres(),
        moods: this.editMoods(),
      })
      .subscribe({
        next: (d) => this.saveJock(st, d),
        error: (e: { error?: { error?: string } }) =>
          this.said.set(String(e.error?.error ?? 'Could not save that station.')),
      });
  }

  /**
   * The jock, after the station itself.
   *
   * Only when it CHANGED. The jock does not affect which tracks the station
   * selects, so re-assigning the same one is a write that buys nothing and can
   * still fail. The station update is unconditional by contrast, because the
   * server regenerates the playlist on it -- which is the point of an edit.
   */
  private saveJock(st: Station, d: Diff): void {
    const want = this.editJock();
    if (want === (st.jock_id ?? '')) {
      this.done(d, '');
      return;
    }
    // WHO, AND WHEN IT TAKES EFFECT. Kept from when the jock saved on change:
    // silence made it look like nothing had happened, reported as "there is no
    // way to save a selected jockey". There is a Save button now, so the
    // sentence is no longer load-bearing -- but naming the jock and saying that
    // a station already on air changes at its next break still is.
    const jock = this.jocks().find((j) => j.id === want);
    const said = jock
      ? `${st.name} is ${jock.name}'s now. A station already on air changes at its next break. `
      : `${st.name} has no jock and will play music only. `;

    this.api.assignJock(st.id, want || null).subscribe({
      next: () => this.done(d, said),
      // The station SAVED and the jock did not, which is a different sentence
      // from "could not save that station" and needs to be, or the operator
      // retypes a change that already went through.
      error: () => {
        this.editing.set(null);
        this.load();
        this.said.set('Station saved, but the jock could not be assigned.');
      },
    });
  }

  private done(d: Diff, prefix: string): void {
    // The DIFF, because the playlist is materialised from the filter: changing
    // a genre silently restates every track the station plays, and a count is
    // the only thing that makes that visible.
    this.said.set(
      `${prefix}Saved. Playlist regenerated: ${d.added} added, ${d.removed} removed, ${d.kept} kept.`,
    );
    this.editing.set(null);
    this.load();
  }

  add(): void {
    this.said.set('');
    this.api
      .addStation({ name: this.name(), genres: this.genres(), moods: this.moods() })
      .subscribe({
        next: (made) => {
          // The TRACK COUNT, immediately: the operator sees a number rather
          // than a promise, and finds out at once if the filter selects
          // nothing.
          this.said.set(`Added with ${made.tracks} tracks.` + this.moodHint(made.tracks));
          this.name.set('');
          this.load();
        },
        error: (e: { error?: { error?: string } }) =>
          this.said.set(String(e.error?.error ?? 'Could not add that station.')),
      });
  }

  // A MOOD IS THE USUAL REASON A STATION IS EMPTY, and nothing on screen used
  // to say so. Moods come from dossiers, the dossier prompt is told to leave
  // mood empty when nothing fits, and a library the model was unsure about
  // therefore yields a genre+mood station with no tracks -- while the same
  // genre on its own is full. An operator watching "0 of 10 needed" has no way
  // to know the mood did that, and the next thing they try is usually a
  // different genre, which does not help either.
  private moodReason(mood: string): string {
    return (
      ` This station also filters on the mood "${mood}", which only tracks whose` +
      ` dossier carries that mood can match. Clearing the mood widens it to the genre alone.`
    );
  }

  // Said at CREATION too, while the operator is still looking at the form,
  // rather than only when they later fail to switch it on.
  private moodHint(tracks: number): string {
    return tracks === 0 && this.moods().length ? this.moodReason(this.moods().join(', ')) : '';
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
          this.said.set(
            `Too few tracks: ${e.error.tracks} of ${e.error.minimum} needed.` +
              (station.mood ? this.moodReason(station.mood) : ''),
          );
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

}
