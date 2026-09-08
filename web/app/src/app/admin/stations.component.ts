import { Component, inject, output, signal } from '@angular/core';
import { AdminApi, Derived, Diff, Jock, Station, StationBody } from '../api/api';

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
              <td data-label="Name">
                <input
                  data-edit-name
                  [value]="editName()"
                  (input)="editName.set($any($event.target).value)"
                />
                <!-- The SAME description this station was made from, so an
                     operator can rewrite the sentence rather than reverse
                     engineer the boxes. Describing again replaces what is
                     ticked; they can adjust it or leave without saving. -->
                <textarea
                  data-edit-brief
                  rows="2"
                  [attr.maxlength]="maxBrief"
                  [value]="editBrief()"
                  (input)="editBrief.set($any($event.target).value)"
                ></textarea>
                <button
                  type="button"
                  data-edit-describe
                  [disabled]="editDescribing() || !editBrief().trim()"
                  (click)="describeEdit()"
                >
                  {{ editDescribing() ? 'Describing…' : 'Describe it' }}
                </button>
                <label>
                  From year
                  <input
                    data-edit-year-min
                    type="number"
                    [value]="editYearMin() || ''"
                    (input)="editYearMin.set(+$any($event.target).value)"
                  />
                </label>
                <label>
                  To year
                  <input
                    data-edit-year-max
                    type="number"
                    [value]="editYearMax() || ''"
                    (input)="editYearMax.set(+$any($event.target).value)"
                  />
                </label>
              </td>
              <td data-label="Genre · mood">
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
              <td data-label="Name">{{ station.name }}</td>
              <td data-label="Genre · mood">{{ describe(station) }}</td>
            }
            <td data-label="Tracks">{{ station.tracks }}</td>
            <td data-label="Warning">
              @if (station.warning) {
                <span data-warning>{{ station.warning }}</span>
              }
            </td>
            <td data-label="Jock">
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
            <!-- LABELLED like the rest: below 48rem the row becomes a card, and
               Edit, Disable, Delete and Playlist were all drawn past the right
               edge of a phone with nothing to scroll. -->
            <td data-label="Actions">
              @if (editing() === station.id) {
                <button type="button" data-save (click)="save(station)">Save</button>
                <button type="button" data-cancel (click)="cancel()">Cancel</button>
              } @else {
                <button type="button" data-edit (click)="startEdit(station)">Edit</button>
                <button type="button" data-toggle (click)="toggle(station)">
                  {{ station.enabled ? 'Disable' : 'Enable' }}
                </button>
                <button type="button" data-remove data-danger (click)="remove(station)">
                  Delete
                </button>
                <button type="button" data-playlist (click)="playlist.emit(station)">
                  Playlist
                </button>
              }
            </td>
          </tr>
        } @empty {
          <!-- LOADING AND EMPTY ARE NOT THE SAME THING and both drew a table
               with no rows, so a slow first paint told a new operator their
               library was empty. Jockora-e9a.50. -->
          <tr>
            <td colspan="7">
              @if (!loaded()) {
                <span data-loading>Loading…</span>
              } @else if (!failed()) {
                <span data-empty
                  ><strong>No stations yet.</strong> Add a station below. A station is a genre, an
                  optional mood and a jock.</span
                >
              }
            </td>
          </tr>
        }
      </tbody>
    </table>

    <fieldset>
      <legend>Add a station</legend>

      <!-- A STATION IS DESCRIBED, NOT TICKED. Two closed vocabularies in a
           checkbox grid is a form that makes the operator do the model's job;
           the boxes are still here, they just stop being where you start. -->
      <label data-brief-label>
        What is this station?
        <textarea
          data-brief
          rows="2"
          [attr.maxlength]="maxBrief"
          placeholder="Late-night driving music, mostly 80s, nothing cheerful."
          [value]="brief()"
          (input)="brief.set($any($event.target).value)"
        ></textarea>
      </label>
      <!-- DISABLED WHILE BLANK AND WHILE WORKING, and it says which. A live
           model call takes seconds, and an unlabelled button that does nothing
           reads as broken. -->
      <button
        type="button"
        data-describe
        [disabled]="describing() || !brief().trim()"
        (click)="describeStation()"
      >
        {{ describing() ? 'Describing…' : 'Describe it' }}
      </button>
      @if (derived(); as d) {
        <small data-derived
          >{{ d.tracks }} tracks match. Change anything below before saving.</small
        >
        @if (d.warning) {
          <!-- THE VERDICT ON THE NUMBER, at the one moment the operator can
               still change the brief. Without it they read "7 tracks", save,
               and discover the station cannot be enabled at all. Advice, not a
               refusal: twelve deep cuts is a real station. -->
          <small role="alert" data-derived-warning>{{ d.warning }}</small>
        }
      }
      @if (describing()) {
        <!-- WHY IT IS SLOW. Enrichment is serial and pauses only on the
             operator's own toggle, so a derive fired mid-enrichment queues
             behind a dossier pass on the same model. Without this they watch a
             spinner on a feature that worked yesterday. -->
        <small data-describing-note
          >Asking the model. If this is slow, enrichment may be running — you can pause it on the
          Overview.</small
        >
      }

      <input
        data-name
        placeholder="name"
        [value]="name()"
        (input)="name.set($any($event.target).value)"
      />
      <label>
        From year
        <input
          data-year-min
          type="number"
          [value]="yearMin() || ''"
          (input)="yearMin.set(+$any($event.target).value)"
        />
      </label>
      <label>
        To year
        <input
          data-year-max
          type="number"
          [value]="yearMax() || ''"
          (input)="yearMax.set(+$any($event.target).value)"
        />
      </label>
      <!-- [selected] per option, never [value] on the select: these options
           come from the vocabulary, which arrives over HTTP, and a select whose
           value is bound before its options exist silently falls back to the
           first one. -->
      <!-- ONE details element, closed by default. This is the path a library
           with no model configured must use, so a 503 from the derive opens it
           rather than leaving the operator at a dead button. -->
      <details data-manual [open]="manualOpen()">
        <summary>set genres and moods by hand</summary>
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
      </details>
      <button type="button" data-add (click)="add()">Add</button>
    </fieldset>

    <p data-said>{{ said() }}</p>
  `,
})
export class Stations {
  private readonly api = inject(AdminApi);

  readonly stations = signal<Station[]>([]);
  /**
   * True once the first answer has arrived, success OR failure.
   *
   * Without it a table with no rows means two opposite things -- the request is
   * still in flight, or there is genuinely nothing -- and both drew the same
   * empty table. Jockora-e9a.50.
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
  readonly jocks = signal<Jock[]>([]);
  readonly vocab = signal<{ genres: string[]; moods: string[] }>({ genres: [], moods: [] });
  readonly name = signal('');
  readonly genres = signal<string[]>([]);
  readonly moods = signal<string[]>([]);

  /**
   * The server's own cap, mirrored so the box stops an operator BEFORE the
   * round trip rather than after it.
   */
  readonly maxBrief = 2000;

  /** What the operator wrote, and what the model made of it. */
  readonly brief = signal('');
  readonly yearMin = signal(0);
  readonly yearMax = signal(0);
  readonly derived = signal<Derived | null>(null);
  readonly describing = signal(false);
  /**
   * Whether the by-hand fieldsets are open.
   *
   * Opened by a 503, because a library with no model configured has to be able
   * to make a station and the alternative is an operator staring at a button
   * that will never work.
   */
  readonly manualOpen = signal(false);

  /** The same four, for the row editor. */
  readonly editBrief = signal('');
  readonly editYearMin = signal(0);
  readonly editYearMax = signal(0);
  readonly editDescribing = signal(false);
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
      next: (s) => {
        this.stations.set(s);
        this.loaded.set(true);
        this.failed.set(false);
      },
      error: () => {
        this.loaded.set(true);
        this.failed.set(true);
        this.said.set('Could not read the stations.');
      },
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
    // Every station has a brief after migration 10's back-fill, so this is
    // never empty in practice -- and re-describing replaces the ticked boxes.
    this.editBrief.set(st.brief ?? '');
    this.editYearMin.set(st.year_min ?? 0);
    this.editYearMax.set(st.year_max ?? 0);
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
      .updateStation(
        st.id,
        this.stationBody(
          this.editName(),
          this.editGenres(),
          this.editMoods(),
          this.editBrief(),
          this.editYearMin(),
          this.editYearMax(),
        ),
      )
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

  /**
   * Turn the description into a suggestion. NOTHING IS SAVED.
   *
   * The result lands in the same controls the operator would have filled in by
   * hand, already filled in -- so what they save is what is on screen, edits
   * included, and the tags the model chose stay visible. An operator who cannot
   * see the filter cannot fix it, and this feature's failure mode is a
   * plausible-looking wrong answer.
   */
  describeStation(): void {
    this.said.set('');
    this.describing.set(true);
    this.api.deriveStation(this.brief()).subscribe({
      next: (d) => {
        this.describing.set(false);
        this.derived.set(d);
        this.name.set(d.name);
        this.genres.set(d.genres ?? []);
        this.moods.set(d.moods ?? []);
        this.yearMin.set(d.year_min ?? 0);
        this.yearMax.set(d.year_max ?? 0);
      },
      error: (e: { status?: number; error?: { error?: string } }) => {
        this.describing.set(false);
        this.said.set(String(e.error?.error ?? 'Could not describe that station.'));
        // 503 IS "NO MODEL", and the server's sentence names both ways out.
        // Opening the manual path is the second of them, done rather than
        // described. A 502 is the model failing -- the operator was not wrong
        // and their tags are left exactly as they were.
        if (e.status === 503) {
          this.manualOpen.set(true);
        }
      },
    });
  }

  /** The same, for a station being edited. */
  describeEdit(): void {
    this.said.set('');
    this.editDescribing.set(true);
    this.api.deriveStation(this.editBrief()).subscribe({
      next: (d) => {
        this.editDescribing.set(false);
        this.editName.set(d.name);
        this.editGenres.set(d.genres ?? []);
        this.editMoods.set(d.moods ?? []);
        this.editYearMin.set(d.year_min ?? 0);
        this.editYearMax.set(d.year_max ?? 0);
      },
      error: (e: { error?: { error?: string } }) => {
        this.editDescribing.set(false);
        this.said.set(String(e.error?.error ?? 'Could not describe that station.'));
      },
    });
  }

  /**
   * The request body, with unbounded years ABSENT rather than zero.
   *
   * A zero is a year the server would have to guess the meaning of, and the
   * schema on the other side already treats absent as unbounded.
   */
  private stationBody(
    name: string,
    genres: string[],
    moods: string[],
    brief: string,
    yearMin: number,
    yearMax: number,
  ): StationBody {
    const body: StationBody = { name, genres, moods };
    if (brief.trim()) {
      body.brief = brief.trim();
    }
    if (yearMin) {
      body.year_min = yearMin;
    }
    if (yearMax) {
      body.year_max = yearMax;
    }
    return body;
  }

  add(): void {
    this.said.set('');
    this.api
      .addStation(
        this.stationBody(
          this.name(),
          this.genres(),
          this.moods(),
          this.brief(),
          this.yearMin(),
          this.yearMax(),
        ),
      )
      .subscribe({
        next: (made) => {
          // The TRACK COUNT, immediately: the operator sees a number rather
          // than a promise, and finds out at once if the filter selects
          // nothing.
          this.said.set(`Added with ${made.tracks} tracks.` + this.moodHint(made.tracks));
          this.name.set('');
          this.brief.set('');
          this.yearMin.set(0);
          this.yearMax.set(0);
          this.derived.set(null);
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
