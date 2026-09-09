import { Component, computed, inject, output, signal } from '@angular/core';
import { AdminApi, Derived, Diff, Jock, Station, StationBody, serverSaid } from '../api/api';
import { FormDialog } from './form-dialog';
import { TableView } from './table-view';

/**
 * The dial, from the operator's side.
 */
@Component({
  selector: 'app-stations',
  standalone: true,
  imports: [FormDialog],
  template: `
    <h2>Stations</h2>
    <!-- SEARCH AND SORT. This table holds every station, so both tell the truth
         about all of them. Jockora-9g7. -->
    <label data-table-search>
      Search
      <input
        data-search
        type="search"
        placeholder="name, genre, mood or jock"
        [value]="view.query()"
        (input)="view.query.set($any($event.target).value)"
      />
    </label>
    <table data-stations>
      <thead>
        <tr>
          <th [attr.aria-sort]="view.ariaSort('name')">
            <button type="button" data-sort="name" (click)="view.toggle('name')">
              Name <span data-sort-marker>{{ view.marker('name') }}</span>
            </button>
          </th>
          <th [attr.aria-sort]="view.ariaSort('tags')">
            <button type="button" data-sort="tags" (click)="view.toggle('tags')">
              Genre &middot; mood <span data-sort-marker>{{ view.marker('tags') }}</span>
            </button>
          </th>
          <th [attr.aria-sort]="view.ariaSort('tracks')">
            <button type="button" data-sort="tracks" (click)="view.toggle('tracks')">
              Tracks <span data-sort-marker>{{ view.marker('tracks') }}</span>
            </button>
          </th>
          <!-- THE ONE NUMBER THAT SAYS WHETHER A STATION IS DOING ANYTHING. A
               station streams only while it has a listener, so this is also
               the answer to "is it running". Jockora-cr7. -->
          <th [attr.aria-sort]="view.ariaSort('listeners')">
            <button type="button" data-sort="listeners" (click)="view.toggle('listeners')">
              Listeners <span data-sort-marker>{{ view.marker('listeners') }}</span>
            </button>
          </th>
          <th [attr.aria-sort]="view.ariaSort('jock')">
            <button type="button" data-sort="jock" (click)="view.toggle('jock')">
              Jock <span data-sort-marker>{{ view.marker('jock') }}</span>
            </button>
          </th>
          <!-- NOT SORTABLE: a column of buttons has no order. -->
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        @for (station of view.shown(); track station.id) {
          <tr>
            <td data-label="Name">
              {{ station.name }}
              <!-- A MARKER, NOT A COLUMN. On the dial is the ordinary state, so
                   a column would say nothing on most rows and cost width on all
                   of them -- the reason Jockora-e9a.53 took one out of this
                   very table. Off the dial is the exceptional state and the
                   only one worth drawing. Jockora-yv7. -->
              @if (!station.enabled) {
                <small data-off-air>off the dial</small>
              }
            </td>
            <td data-label="Genre · mood">{{ describe(station) }}</td>
            <!-- THE WARNING SITS ON THE NUMBER IT IS DERIVED FROM. It used to
                 have a column of its own, which was empty in every screenshot
                 across three review rounds: it holds one of two strings, both a
                 pure function of this very count, and a permanent column for a
                 rare derived string costs width on every row for ever. -->
            <td data-label="Tracks">
              {{ station.tracks }}
              @if (station.warning) {
                <small data-warning>{{ station.warning }}</small>
              }
            </td>
            <td data-label="Listeners" data-listeners>{{ station.listeners }}</td>
            <td data-label="Jock">{{ jockName(station) }}</td>
            <!-- LABELLED like the rest: below 48rem the row becomes a card, and
               Edit, Disable, Delete and Playlist were all drawn past the right
               edge of a phone with nothing to scroll. -->
            <td data-label="Actions">
              <button type="button" data-edit (click)="startEdit(station)">Edit</button>
              <button type="button" data-toggle (click)="toggle(station)">
                {{ station.enabled ? 'Disable' : 'Enable' }}
              </button>
              <button type="button" data-remove data-danger (click)="remove(station)">
                Delete
              </button>
              <button type="button" data-playlist (click)="playlist.emit(station)">Playlist</button>
            </td>
          </tr>
        } @empty {
          <!-- LOADING AND EMPTY ARE NOT THE SAME THING and both drew a table
               with no rows, so a slow first paint told a new operator their
               library was empty. Jockora-e9a.50. -->
          <tr>
            <td colspan="6">
              @if (!loaded()) {
                <span data-loading>Loading…</span>
              } @else if (stations().length) {
                <!-- A SEARCH THAT MATCHED NOTHING IS NOT AN EMPTY DIAL, and
                     telling an operator to add their first station when they
                     have twelve and mistyped a name is the loading-versus-empty
                     mistake wearing a third hat. Jockora-9g7. -->
                <span data-no-match
                  >No station matches “{{ view.query() }}”.
                  <button type="button" data-clear-search (click)="view.query.set('')">
                    Clear the search
                  </button></span
                >
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

    <!-- THE EDIT FORM IS A FORM, not a table row.
         It used to expand the row IN PLACE, so it inherited the table's column
         grid: the name box and all six range fields were crammed into the Name
         column at x109-413, the pickers sat in Genre-mood, and Save and Cancel
         were stranded in Actions at y610 and y644 -- vertically adrift of every
         field they applied to, with 200px of dead column between them. -->
    <app-form-dialog
      [title]="editingStation() ? 'Edit ' + editingStation()!.name : ''"
      [open]="editing() !== null"
      (closed)="cancel()"
    >
      @if (editingStation(); as station) {
        <fieldset data-station-edit>
          <legend>Edit {{ station.name }}</legend>

          <label>
            Name
            <input
              data-edit-name
              [value]="editName()"
              (input)="editName.set($any($event.target).value)"
            />
          </label>
          <!-- WHO PRESENTS IT belongs with what it is called, not after the year,
             tempo and duration filters where it read as a seventh filter.
             Jockora-e9a.61. -->
          <label>
            Jock
            <select data-jock (change)="editJock.set($any($event.target).value)">
              <option value="" [selected]="!editJock()">— no jock —</option>
              @for (jock of jocks(); track jock.id) {
                <option [value]="jock.id" [selected]="jock.id === editJock()">
                  {{ jock.name }}
                </option>
              }
            </select>
          </label>

          <!-- The SAME description this station was made from, so an operator can
             rewrite the sentence rather than reverse engineer the boxes.
             Describing again replaces what is ticked; they can adjust it or
             leave without saving. -->
          <!-- THE DESCRIPTION AND THE BUTTON THAT READS IT are one thing, and
             grouping them is what stops the button drifting into the numeric
             row below: measured at x209 y335, level with From year, acting on a
             field at y200. Jockora-e9a.61. -->
          <div data-brief-group>
            <label data-brief-label>
              What is this station?
              <textarea
                data-edit-brief
                rows="2"
                [attr.maxlength]="maxBrief"
                [value]="editBrief()"
                (input)="editBrief.set($any($event.target).value)"
              ></textarea>
            </label>
            <!-- NO SECOND NAME FOR IT. This button used to sit in a
               [data-field] box whose heading read "Derive it" above a button
               reading "Describe it" -- two imperatives, different verbs, for
               one action, and the operator asked which was which. That box
               existed only to give a bare button the same top offset as the
               labelled controls it once sat beside on the numeric row; it now
               sits beside a textarea in a group that aligns on the bottom
               edge, so the box earned nothing and cost a name. -->
            <button
              type="button"
              data-edit-describe
              [disabled]="editDescribing() || !editBrief().trim()"
              (click)="describeEdit()"
            >
              {{ editDescribing() ? 'Describing…' : 'Describe it' }}
            </button>
          </div>

          <!-- ONE VALUE, TWO BOXES. Grouped so a row wrap can never get between them:
           measured before, Shortest sat at x951 on one row and Longest at x113
           on the next. Jockora-e9a.61. -->
          <div data-range>
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
          </div>
          <!-- ONE LABEL PER BOX. A single label over a PAIR of stacked inputs
             said nothing about which box was which -- and the tempo one read
             "Fastest and slowest" above the MIN box, so it named them
             backwards. The same four fields, in the same units, as the add
             form: an operator who opens an edit and finds them blank has lost
             them, and saving would widen the station with nothing on screen to
             say so. -->
          <!-- The same pairing, for the same reason. -->
          <div data-range>
            <label>
              Slowest (BPM)
              <input
                data-edit-tempo-min
                type="number"
                placeholder="any"
                [value]="editTempoMin() || ''"
                (input)="editTempoMin.set(+$any($event.target).value)"
              />
            </label>
            <label>
              Fastest (BPM)
              <input
                data-edit-tempo-max
                type="number"
                placeholder="any"
                [value]="editTempoMax() || ''"
                (input)="editTempoMax.set(+$any($event.target).value)"
              />
            </label>
          </div>
          <!-- And again. -->
          <div data-range>
            <label>
              Shortest (minutes)
              <input
                data-edit-length-min
                type="number"
                step="0.5"
                placeholder="any"
                [value]="editLengthMin() || ''"
                (input)="editLengthMin.set(+$any($event.target).value)"
              />
            </label>
            <label>
              Longest (minutes)
              <input
                data-edit-length-max
                type="number"
                step="0.5"
                placeholder="any"
                [value]="editLengthMax() || ''"
                (input)="editLengthMax.set(+$any($event.target).value)"
              />
            </label>
          </div>

          <!-- CHECKBOXES, not a multiple select. A multiple select needs
             ctrl-click to pick two things that are not next to each other,
             which is a keyboard trick people do not know and cannot see. A list
             of checkboxes shows every option and its state at once, and the
             checked boxes ARE the display. -->
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

          <!-- AT THE END OF THE FORM, with the fields they apply to. -->
          <div data-form-actions>
            <button type="button" data-save (click)="save(station)">Save</button>
            <button type="button" data-cancel (click)="cancel()">Cancel</button>
          </div>
        </fieldset>
      }
    </app-form-dialog>

    <!-- HIDDEN WHILE AN EDIT IS OPEN. Both were on screen at once, so two name
         boxes and two Describe it buttons faced the operator together. -->
    <button type="button" data-add-open (click)="adding.set(true)">Add a station</button>
    <app-form-dialog [title]="'Add a station'" [open]="adding()" (closed)="closeAdd()">
      @if (adding()) {
        <fieldset data-station-add>
          <legend>Add a station</legend>

          <!-- A STATION IS DESCRIBED, NOT TICKED. Two closed vocabularies in a
           checkbox grid is a form that makes the operator do the model's job;
           the boxes are still here, they just stop being where you start. -->
          <!-- The description and the button that reads it are one thing; see the
           edit form above. Jockora-e9a.61. -->
          <div data-brief-group>
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
            <!-- ONE NAME, and it is on the button. See the edit form above.
             DISABLED WHILE BLANK AND WHILE WORKING, and it says which: a live
             model call takes seconds, and a button that does nothing reads as
             broken. -->
            <button
              type="button"
              data-describe
              [disabled]="describing() || !brief().trim()"
              (click)="describeStation()"
            >
              {{ describing() ? 'Describing…' : 'Describe it' }}
            </button>
          </div>
          <!-- WHAT CAME BACK, AS ONE BLOCK ON ITS OWN ROW.
           These were three loose flex items in a wrapping row, so they packed
           in beside whatever had space: the count landed to the LEFT of the
           Name box and read as its caption, and the low-track warning ran on
           the same line as the count. Reported with a screenshot. They are one
           thing -- the model's answer -- and they now say so. -->
          @if (derived() || describing()) {
            <p data-derived-status>
              @if (derived(); as d) {
                <span data-derived
                  >{{ d.tracks }} tracks match. Change anything below before saving.</span
                >
                @if (d.warning) {
                  <!-- THE VERDICT ON THE NUMBER, at the one moment the operator can
                   still change the brief. Without it they read "7 tracks", save,
                   and discover the station cannot be enabled at all. Advice, not
                   a refusal: twelve deep cuts is a real station. -->
                  <span role="alert" data-derived-warning>{{ d.warning }}</span>
                }
              }
              @if (describing()) {
                <!-- WHY IT IS SLOW. Enrichment is serial and pauses only on the
                 operator's own toggle, so a derive fired mid-enrichment queues
                 behind a dossier pass on the same model. Without this they watch
                 a spinner on a feature that worked yesterday. -->
                <span data-describing-note
                  >Asking the model. If this is slow, enrichment may be running — you can pause it
                  on the Overview.</span
                >
              }
            </p>
          }

          <!-- THE NAME IS WHAT THE STATION IS CALLED, so it gets a row rather
             than whatever gap the wrap leaves. It was drawn floating in the
             middle of the form with the track count to its left. -->
          <label data-name-field>
            Name
            <input
              data-name
              placeholder="NIGHT ROCK"
              [value]="name()"
              (input)="name.set($any($event.target).value)"
            />
          </label>
          <!-- One value, two boxes; see the edit form. Jockora-e9a.61. -->
          <div data-range>
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
          </div>
          <!-- THE UNIT IS IN THE LABEL, not left to be guessed. Tempo is BPM
           because that is what tracks.bpm holds; length is MINUTES because
           nobody describes a song as 240 seconds, and the conversion to
           seconds happens in exactly one place on the way out.

           EMPTY MEANS UNBOUNDED and must stay empty: an input bound to a zero
           renders "0", which an operator reads as a bound they did not set. -->
          <!-- The same pairing. -->
          <div data-range>
            <label>
              Slowest (BPM)
              <input
                data-tempo-min
                type="number"
                placeholder="any"
                [value]="tempoMin() || ''"
                (input)="tempoMin.set(+$any($event.target).value)"
              />
            </label>
            <label>
              Fastest (BPM)
              <input
                data-tempo-max
                type="number"
                placeholder="any"
                [value]="tempoMax() || ''"
                (input)="tempoMax.set(+$any($event.target).value)"
              />
            </label>
          </div>
          <!-- And again. -->
          <div data-range>
            <label>
              Shortest (minutes)
              <input
                data-length-min
                type="number"
                step="0.5"
                placeholder="any"
                [value]="lengthMin() || ''"
                (input)="lengthMin.set(+$any($event.target).value)"
              />
            </label>
            <label>
              Longest (minutes)
              <input
                data-length-max
                type="number"
                step="0.5"
                placeholder="any"
                [value]="lengthMax() || ''"
                (input)="lengthMax.set(+$any($event.target).value)"
              />
            </label>
          </div>
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
          <div data-form-actions>
            <button type="button" data-add (click)="add()">Add</button>
          </div>
        </fieldset>
      }
    </app-form-dialog>

    <p data-said>{{ said() }}</p>
  `,
})
export class Stations {
  private readonly api = inject(AdminApi);

  readonly stations = signal<Station[]>([]);

  /**
   * The search box and the sortable headers.
   *
   * Every station is in the browser already, so this filters and orders the
   * whole dial rather than a page of it -- which is exactly why the playlist
   * does not get one. Jockora-9g7.
   */
  readonly view = new TableView<Station>(this.stations, [
    { key: 'name', value: (s) => s.name },
    { key: 'tags', value: (s) => this.describe(s) },
    { key: 'tracks', value: (s) => s.tracks },
    { key: 'listeners', value: (s) => s.listeners },
    { key: 'jock', value: (s) => this.jockName(s) },
  ]);
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
  // BPM as typed. Zero is unbounded and renders as an empty box.
  readonly tempoMin = signal(0);
  readonly tempoMax = signal(0);
  // MINUTES, because nobody describes a song as 240 seconds. Converted on the
  // way out; the wire is always seconds.
  readonly lengthMin = signal(0);
  readonly lengthMax = signal(0);
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
  readonly editTempoMin = signal(0);
  readonly editTempoMax = signal(0);
  readonly editLengthMin = signal(0);
  readonly editLengthMax = signal(0);
  readonly editDescribing = signal(false);
  /**
   * Which derivation either form is waiting for.
   *
   * THE ANSWER HAS TO PROVE IT STILL APPLIES. Describing is a model call, and
   * this form's own copy warns it can queue behind enrichment -- so the window
   * is wide, and both forms are now dialogs the operator can shut while one is
   * running. Start a description for a new station, close the dialog, open a
   * station's editor, and the answer landed in it: name, genres, moods, years,
   * tempo and length all replaced from a brief about something else, with Save
   * one click away. The same defect Jockora-e9a.63 fixed on the advert writer,
   * which this call had not been given.
   */
  private describeSeq = 0;
  readonly said = signal('');

  /** playlist asks the console to open one station's playlist. */
  readonly playlist = output<Station>();

  // WHICH ROW IS OPEN, by station id rather than a boolean, so opening a second
  // row closes the first: two half-edited rows and one Save button between them
  // is a way to write the wrong station's name.
  /** Whether the Add dialog is open. */
  readonly adding = signal(false);
  readonly editing = signal<number | null>(null);

  /**
   * The station being edited, or null.
   *
   * The form left the table in Jockora-e9a.53, so it can no longer read the
   * station out of the row it sits in. Derived rather than a second signal: a
   * copy of the row would go stale the moment the list refreshed under it.
   */
  readonly editingStation = computed(
    () => this.stations().find((s) => s.id === this.editing()) ?? null,
  );
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
    // NEVER BOTH AT ONCE. Two forms open together is how an operator presses
    // Save on the one they were not looking at -- and closed the same way
    // Escape closes it, so a half-written brief does not survive out of sight.
    // closeAdd abandons any derivation in flight, which is what stops the add
    // form's answer landing in this one.
    this.closeAdd();
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
    this.editTempoMin.set(st.tempo_min ?? 0);
    this.editTempoMax.set(st.tempo_max ?? 0);
    this.editLengthMin.set(this.toMinutes(st.duration_min_s));
    this.editLengthMax.set(this.toMinutes(st.duration_max_s));
  }

  /** The jock's NAME, for a row that is not being edited. */
  jockName(st: Station): string {
    return this.jocks().find((j) => j.id === st.jock_id)?.name ?? '— no jock —';
  }

  cancel(): void {
    // The editor's own derivation goes with it, for the same reason the add
    // form's does: an answer to a brief nobody is looking at any more must not
    // fill in the next station opened.
    this.abandonDescribe();
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
        this.stationBody({
          name: this.editName(),
          genres: this.editGenres(),
          moods: this.editMoods(),
          brief: this.editBrief(),
          yearMin: this.editYearMin(),
          yearMax: this.editYearMax(),
          tempoMin: this.editTempoMin(),
          tempoMax: this.editTempoMax(),
          lengthMin: this.editLengthMin(),
          lengthMax: this.editLengthMax(),
        }),
      )
      .subscribe({
        next: (d) => this.saveJock(st, d),
        error: (e: unknown) => this.said.set(serverSaid(e, 'Could not save that station.')),
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
    const seq = ++this.describeSeq;
    this.said.set('');
    this.describing.set(true);
    this.api.deriveStation(this.brief()).subscribe({
      next: (d) => {
        if (seq !== this.describeSeq) {
          return;
        }
        this.describing.set(false);
        this.derived.set(d);
        this.name.set(d.name);
        this.genres.set(d.genres ?? []);
        this.moods.set(d.moods ?? []);
        this.yearMin.set(d.year_min ?? 0);
        this.yearMax.set(d.year_max ?? 0);
        this.tempoMin.set(d.tempo_min ?? 0);
        this.tempoMax.set(d.tempo_max ?? 0);
        this.lengthMin.set(this.toMinutes(d.duration_min_s));
        this.lengthMax.set(this.toMinutes(d.duration_max_s));
      },
      error: (e: { status?: number; error?: { error?: string } }) => {
        // A FAILURE IS STALE THE SAME WAY, and a 503 opening the manual
        // fieldsets on a form the operator has moved on from is worse than the
        // message: it changes what is on screen for a request they abandoned.
        if (seq !== this.describeSeq) {
          return;
        }
        this.describing.set(false);
        this.said.set(serverSaid(e, 'Could not describe that station.'));
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
    const seq = ++this.describeSeq;
    this.said.set('');
    this.editDescribing.set(true);
    this.api.deriveStation(this.editBrief()).subscribe({
      next: (d) => {
        if (seq !== this.describeSeq) {
          return;
        }
        this.editDescribing.set(false);
        this.editName.set(d.name);
        this.editGenres.set(d.genres ?? []);
        this.editMoods.set(d.moods ?? []);
        this.editYearMin.set(d.year_min ?? 0);
        this.editYearMax.set(d.year_max ?? 0);
        this.editTempoMin.set(d.tempo_min ?? 0);
        this.editTempoMax.set(d.tempo_max ?? 0);
        this.editLengthMin.set(this.toMinutes(d.duration_min_s));
        this.editLengthMax.set(this.toMinutes(d.duration_max_s));
      },
      error: (e: { error?: { error?: string } }) => {
        if (seq !== this.describeSeq) {
          return;
        }
        this.editDescribing.set(false);
        this.said.set(serverSaid(e, 'Could not describe that station.'));
      },
    });
  }

  /**
   * Abandon any derivation in flight, because the form it was asked for is
   * gone. Bumping the counter is what makes the answer stale.
   *
   * ONE COUNTER FOR BOTH FORMS on purpose: they cannot be open at once, and a
   * derivation started in one and answered after the other opened is exactly
   * the crossing this exists to stop.
   */
  private abandonDescribe(): void {
    this.describeSeq++;
    this.describing.set(false);
    this.editDescribing.set(false);
  }

  /**
   * The request body, with unbounded years ABSENT rather than zero.
   *
   * A zero is a year the server would have to guess the meaning of, and the
   * schema on the other side already treats absent as unbounded.
   */
  private stationBody(in_: {
    name: string;
    genres: string[];
    moods: string[];
    brief: string;
    yearMin: number;
    yearMax: number;
    tempoMin: number;
    tempoMax: number;
    /** MINUTES. Converted to seconds here, which is the only place it happens. */
    lengthMin: number;
    lengthMax: number;
  }): StationBody {
    const body: StationBody = { name: in_.name, genres: in_.genres, moods: in_.moods };
    if (in_.brief.trim()) {
      body.brief = in_.brief.trim();
    }
    // ABSENT, NOT ZERO, for every bound. The server reads a zero as unbounded
    // too, but sending one is the console deciding rather than the operator --
    // and a cleared box has to stay cleared.
    if (in_.yearMin) {
      body.year_min = in_.yearMin;
    }
    if (in_.yearMax) {
      body.year_max = in_.yearMax;
    }
    if (in_.tempoMin) {
      body.tempo_min = in_.tempoMin;
    }
    if (in_.tempoMax) {
      body.tempo_max = in_.tempoMax;
    }
    if (in_.lengthMin) {
      body.duration_min_s = Math.round(in_.lengthMin * 60);
    }
    if (in_.lengthMax) {
      body.duration_max_s = Math.round(in_.lengthMax * 60);
    }
    return body;
  }

  /**
   * Seconds off the wire, minutes for the box.
   *
   * ROUNDED TO THE STEP THE INPUT ITSELF USES. Dividing by sixty and binding
   * the result renders whatever the float is: 1939 seconds drew
   * "32.3166666666667" in a box whose step is 0.5, reported live with a
   * screenshot. Two decimals is finer than the control and still a number a
   * person can read. Jockora-2mu.
   */
  private toMinutes(seconds: number | undefined): number {
    return seconds ? Math.round((seconds / 60) * 100) / 100 : 0;
  }

  /** Close the add dialog, taking whatever was half-typed with it. */
  closeAdd(): void {
    this.adding.set(false);
    this.clearAddForm();
  }

  /**
   * Empty the add form. ALL OF IT.
   *
   * ONE PLACE, because it is now reached two ways -- a successful Add and a
   * dismissed dialog -- and a form that keeps last time's brief is how a
   * station gets created from a description nobody meant to reuse.
   *
   * EVERY FIELD, not the four the success path used to clear. While the form
   * was a permanent fieldset the leftovers were at least on screen beside the
   * empty name box; behind an "Add a station" button they are not. The genre
   * and mood ticks are the worst of them -- they live inside a details element
   * that is closed again here, so a second station made after a described one
   * inherited its genres, moods, tempo and length with nothing on screen
   * saying so, and the operator only found out from the track count.
   */
  private clearAddForm(): void {
    this.name.set('');
    this.brief.set('');
    this.genres.set([]);
    this.moods.set([]);
    this.yearMin.set(0);
    this.yearMax.set(0);
    this.tempoMin.set(0);
    this.tempoMax.set(0);
    this.lengthMin.set(0);
    this.lengthMax.set(0);
    this.derived.set(null);
    this.manualOpen.set(false);
    this.abandonDescribe();
  }

  add(): void {
    this.said.set('');
    this.api
      .addStation(
        this.stationBody({
          name: this.name(),
          genres: this.genres(),
          moods: this.moods(),
          brief: this.brief(),
          yearMin: this.yearMin(),
          yearMax: this.yearMax(),
          tempoMin: this.tempoMin(),
          tempoMax: this.tempoMax(),
          lengthMin: this.lengthMin(),
          lengthMax: this.lengthMax(),
        }),
      )
      .subscribe({
        next: (made) => {
          // The TRACK COUNT, immediately: the operator sees a number rather
          // than a promise, and finds out at once if the filter selects
          // nothing.
          // AND THE STEP NOBODY WAS TOLD ABOUT. A station is created off the
          // dial on purpose, so the operator can see its track count before
          // listeners can hear it -- but the console used to report only the
          // count, and an operator who had done exactly what the design wanted
          // was left wondering why their station never appeared. Jockora-yv7.
          this.said.set(
            `Added with ${made.tracks} tracks. It is off the dial until you press Enable.` +
              this.moodHint(made.tracks),
          );
          this.clearAddForm();
          this.adding.set(false);
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
