import { DecimalPipe } from '@angular/common';
import { Component, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { AdminApi, Diff, PlaylistTrack, TrackTags } from '../api/api';
import { FormDialog } from './form-dialog';
import { SortState } from './table-view';

/** The cap a dossier itself carries, enforced here so a doomed save is not sent. */
export const MAX_TAGS = 5;

/** How many rows a page shows. Matches the server's own default. */
export const PAGE = 50;

/**
 * One station's playlist, with the two decisions an operator can make about a
 * track: keep it whatever the filter says, or keep it off.
 */
@Component({
  selector: 'app-playlist',
  standalone: true,
  imports: [FormDialog, DecimalPipe],
  template: `
    <h2>Playlist</h2>
    <p data-count>{{ total() }} tracks</p>
    <table data-playlist>
      <thead>
        <!-- SORTED BY THE SERVER. These headers order the whole playlist and
             come back on the first page, because the table holds fifty rows out
             of thousands: sorting what is on screen would answer a question
             about the page and look like an answer about the library.
             Jockora-22s. -->
        <tr>
          @for (c of COLUMNS; track c.key) {
            <th scope="col" [attr.aria-sort]="sort.ariaSort(c.key)">
              <button type="button" [attr.data-sort]="c.key" (click)="sortBy(c.key)">
                {{ c.label }} <span data-sort-marker>{{ sort.marker(c.key) }}</span>
              </button>
            </th>
          }
          <!-- NOT SORTABLE. Genre and mood are lists rather than values, and a
               column of buttons has no order at all. -->
          <th scope="col">Genre</th>
          <th scope="col">Mood</th>
          <th scope="col"><span data-sr-only>Actions</span></th>
        </tr>
      </thead>
      <tbody>
        @for (track of tracks(); track track.track_id) {
          <tr [attr.data-missing]="track.missing ? '' : null">
            <td data-label="Artist">{{ track.artist }}</td>
            <!-- PINNED, EXCLUDED AND MISSING ARE STATES OF THE TRACK, so they
                 read as markers on the track rather than as a column heading
                 that is blank for everybody else. The Status column was empty
                 on all 50 rows on the reported install -- correct, because the
                 operator had pinned nothing -- while holding 73px on every row
                 for ever. -->
            <td data-label="Title">
              {{ track.title }}
              @if (track.pinned) {
                <span data-flag>pinned</span>
              }
              @if (track.excluded) {
                <span data-flag>excluded</span>
              }
              @if (track.missing) {
                <span data-gone>missing</span>
              }
            </td>
            <td data-label="Album">{{ track.album }}</td>
            <td data-label="Year">{{ track.year || '' }}</td>
            <!-- ROUNDED, and BLANK when unmeasured. A tempo to one decimal is a
               measurement rather than a fact about the music, and "0" would
               read as a fact about a track nobody has analysed yet. -->
            <td data-bpm data-label="Tempo">
              {{ track.bpm ? (track.bpm | number: '1.0-0') : '' }}
            </td>
            <!-- The tags are a BUTTON, not text: this is the screen an operator is
               already on when they notice a track is filed wrong, and sending
               them somewhere else to fix it is how it never gets fixed. -->
            <td data-label="Genre">
              <button type="button" data-tags (click)="edit(track)">
                {{ track.genres.join(', ') || 'none' }}
              </button>
              @if (track.overridden) {
                <span data-edited>edited</span>
              }
            </td>
            <td data-label="Mood">{{ track.moods.join(', ') || '' }}</td>
            <!-- LABELLED like the rest, because below 48rem every row becomes
                 a card and these two were the rightmost of seven columns:
                 measured 654px into a 390px window, inside a table that
                 scrolled sideways with nothing on screen to say so. -->
            <td data-label="Actions">
              <button
                type="button"
                data-pin
                [disabled]="track.missing && !track.pinned"
                (click)="flag(track, track.pinned ? 'unpin' : 'pin')"
              >
                {{ track.pinned ? 'Unpin' : 'Pin' }}
              </button>
              <button
                type="button"
                data-exclude
                (click)="flag(track, track.excluded ? 'unexclude' : 'exclude')"
              >
                {{ track.excluded ? 'Include' : 'Exclude' }}
              </button>
            </td>
          </tr>
        } @empty {
          <!-- LOADING AND EMPTY ARE NOT THE SAME THING and both drew a table
               with no rows, so a slow first paint told a new operator their
               library was empty. Jockora-e9a.50. -->
          <tr>
            <td colspan="8">
              @if (!loaded()) {
                <span data-loading>Loading…</span>
              } @else if (!failed()) {
                <span data-empty
                  ><strong>This station matched no tracks.</strong> Widen its genre or mood, or
                  check that enrichment has reached this part of the library.</span
                >
              }
            </td>
          </tr>
        }
      </tbody>
    </table>

    <button type="button" data-prev [disabled]="offset() === 0" (click)="page(-1)">Previous</button>
    <button
      type="button"
      data-next
      [disabled]="offset() + tracks().length >= total()"
      (click)="page(1)"
    >
      Next
    </button>
    <button type="button" data-regenerate (click)="regenerate()">Regenerate</button>

    <!-- THE TAG EDITOR IS A DIALOG TOO, and not nested: the playlist table it
         opens from is the page itself, not something already in a dialog, so
         the DO NOT about two dialogs deep does not apply. Jockora-e9a.60. -->
    <app-form-dialog
      [title]="editingTrack() ? 'Tags for ' + editingTrack()!.title : ''"
      [open]="editing() !== null"
      (closed)="editing.set(null)"
    >
      @if (editingTrack(); as track) {
        <fieldset data-tag-editor>
          <!-- Checkboxes, not a multi-select: picking several
                   non-contiguous values with ctrl-click is a keyboard trick
                   nobody can see. The ticked boxes ARE the readout. -->
          <fieldset data-edit-genres>
            <legend>Genre</legend>
            @for (g of vocab().genres; track g) {
              <label>
                <input
                  type="checkbox"
                  [value]="g"
                  [checked]="genres().includes(g)"
                  [disabled]="full(genres(), g)"
                  (change)="genres.set(pick(vocab().genres, genres(), g, $event))"
                />{{ g }}
              </label>
            }
          </fieldset>
          <fieldset data-edit-moods>
            <legend>Mood</legend>
            @for (m of vocab().moods; track m) {
              <label>
                <input
                  type="checkbox"
                  [value]="m"
                  [checked]="moods().includes(m)"
                  [disabled]="full(moods(), m)"
                  (change)="moods.set(pick(vocab().moods, moods(), m, $event))"
                />{{ m }}
              </label>
            }
          </fieldset>
          <button type="button" data-save-tags (click)="saveTags(track)">Save</button>
          <button type="button" data-cancel-tags (click)="editing.set(null)">Cancel</button>
          @if (track.overridden) {
            <!-- Only when there IS an edit to undo. Offering it otherwise
                     promises the enrichment can be restored over itself. -->
            <!-- DESTRUCTIVE, AND IT SAYS SO. This discards the
                       operator's own tags on the server, in an editor where
                       every other change waits for Save -- and it used to look
                       identical to the safe button beside it and read like a
                       view toggle. Jockora-e9a.65, under the rule
                       Jockora-e9a.48 set for the rest of the console. -->
            <button type="button" data-revert-tags data-danger (click)="revertTags(track)">
              Discard my tags
            </button>
          }
          <small data-tag-note>Regenerate to move it between stations.</small>
        </fieldset>
      }
    </app-form-dialog>
    <p data-said>{{ said() }}</p>
  `,
})
export class Playlist {
  private readonly api = inject(AdminApi);

  readonly station = input.required<number>();

  readonly tracks = signal<PlaylistTrack[]>([]);

  /**
   * True once the first answer has arrived, success OR failure. Jockora-e9a.50.
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
  readonly total = signal(0);
  readonly offset = signal(0);

  /**
   * The five columns a server-side sort can order by, and what they are called.
   *
   * THE KEYS ARE THE SERVER'S, not the column headings: the store owns the list
   * of columns it can sort by, and a name it does not know is answered with the
   * default order rather than an error.
   */
  readonly COLUMNS: readonly { key: string; label: string }[] = [
    { key: 'artist', label: 'Artist' },
    { key: 'title', label: 'Title' },
    { key: 'album', label: 'Album' },
    { key: 'year', label: 'Year' },
    { key: 'bpm', label: 'Tempo' },
  ];
  readonly sort = new SortState();
  readonly said = signal('');

  /** The track whose tags are open for editing, by id. */
  readonly editing = signal<number | null>(null);

  /**
   * The track being tagged, or null.
   *
   * The editor left the table in Jockora-e9a.60, so it can no longer read the
   * track out of the row it sat in. Derived rather than copied: a copy would go
   * stale the moment the page refreshed under it.
   */
  readonly editingTrack = computed(
    () => this.tracks().find((t) => t.track_id === this.editing()) ?? null,
  );
  readonly genres = signal<string[]>([]);
  readonly moods = signal<string[]>([]);
  /**
   * The closed vocabularies, fetched the first time an operator opens an
   * editor. Most visits to this page never edit a tag, and a list of 42 genres
   * on every page load is a request nobody asked for.
   */
  readonly vocab = signal<{ genres: string[]; moods: string[] }>({ genres: [], moods: [] });

  constructor() {
    effect(() => {
      this.station();
      // UNTRACKED. load() reads offset, so without this the effect would take
      // a dependency on it -- and paging would set offset, re-run the effect,
      // and snap straight back to the first page.
      untracked(() => {
        this.offset.set(0);
        this.load();
      });
    });
  }

  load(): void {
    this.api
      .playlist(this.station(), PAGE, this.offset(), this.sort.sortKey(), this.sort.ascending())
      .subscribe({
        next: (p) => {
          this.tracks.set(p.tracks);
          this.total.set(p.total);
          this.loaded.set(true);
          this.failed.set(false);
        },
        error: () => {
          this.loaded.set(true);
          this.failed.set(true);
          this.said.set('Could not read the playlist.');
        },
      });
  }

  page(direction: number): void {
    this.offset.set(Math.max(0, this.offset() + direction * PAGE));
    this.load();
  }

  /**
   * Sort the whole playlist by one column, from the top.
   *
   * BACK TO THE FIRST PAGE. Page 4 of a title sort has nothing to do with page
   * 4 of the order it replaced, so staying put would drop the operator somewhere
   * arbitrary in a list they just asked to reorder.
   */
  sortBy(key: string): void {
    this.sort.toggle(key);
    this.offset.set(0);
    this.load();
  }

  /** Open the tag editor on one row, seeded with what the track counts as now. */
  edit(track: PlaylistTrack): void {
    this.said.set('');
    this.editing.set(track.track_id);
    this.genres.set([...track.genres]);
    this.moods.set([...track.moods]);
    if (this.vocab().genres.length === 0) {
      this.api.vocab().subscribe({
        next: (v) => this.vocab.set(v),
        error: () => this.said.set('Could not read the list of genres.'),
      });
    }
  }

  /**
   * Tick or untick one value, keeping the vocabulary's own order.
   *
   * Filtering the full list rather than pushing and splicing: an operator who
   * unticks and reticks should not find their tags reordered underneath them.
   */
  pick(all: string[], current: string[], value: string, e: Event): string[] {
    const on = (e.target as HTMLInputElement).checked;
    return all.filter((v) => (v === value ? on : current.includes(v)));
  }

  /** True for a box that cannot be ticked because the cap is reached. */
  full(current: string[], value: string): boolean {
    return current.length >= MAX_TAGS && !current.includes(value);
  }

  saveTags(track: PlaylistTrack): void {
    this.api.setTrackTags(track.track_id, this.genres(), this.moods()).subscribe({
      next: (t) => this.applyTags(track, t, 'Saved. Regenerate to apply it to this station.'),
      error: () => this.said.set('Could not save those tags.'),
    });
  }

  revertTags(track: PlaylistTrack): void {
    // ASKED, and it names what is lost rather than saying "are you sure". The
    // operator's tags go from the SERVER on this click, with no undo, so this
    // is the last moment anything can stop it.
    if (
      !confirm(
        `Discard your tags for ${track.title} and go back to what the enrichment decided? ` +
          `Your edit cannot be recovered.`,
      )
    ) {
      return;
    }
    this.api.revertTrackTags(track.track_id).subscribe({
      next: (t) => this.applyTags(track, t, 'Back to what the enrichment decided.'),
      error: () => this.said.set('Could not undo that edit.'),
    });
  }

  /**
   * Redraw the row from the SERVER'S answer rather than from what was sent.
   *
   * A revert has no idea what it is going back to until the server says, and
   * re-fetching the whole page to find out would lose the operator's place in a
   * playlist of twelve hundred.
   */
  private applyTags(track: PlaylistTrack, tags: TrackTags, note: string): void {
    this.tracks.update((rows) =>
      rows.map((r) =>
        r.track_id === track.track_id
          ? { ...r, genres: tags.genres, moods: tags.moods, overridden: tags.overridden }
          : r,
      ),
    );
    this.editing.set(null);
    this.said.set(note);
  }

  flag(track: PlaylistTrack, action: string): void {
    this.said.set('');
    this.api.flagTrack(this.station(), track.track_id, action).subscribe({
      next: () => this.load(),
      error: (e: { status?: number }) =>
        this.said.set(
          // A pin means "keep this whatever the filter says", and keeping a
          // file the library has lost puts a hole in the station.
          e.status === 409
            ? 'That track is missing from the library, so it cannot be pinned.'
            : 'Could not change that track.',
        ),
    });
  }

  regenerate(): void {
    this.api.regenerate(this.station()).subscribe({
      next: (d: Diff) => {
        // The DIFF, so the operator can see what changed rather than being
        // handed a redrawn list to compare by eye.
        this.said.set(`Added ${d.added}, removed ${d.removed}, kept ${d.kept}.`);
        this.load();
      },
      error: () => this.said.set('Could not regenerate that playlist.'),
    });
  }
}
