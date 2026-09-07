import { Component, effect, inject, input, signal, untracked } from '@angular/core';
import { AdminApi, Diff, PlaylistTrack } from '../api/api';

/** How many rows a page shows. Matches the server's own default. */
export const PAGE = 50;

/**
 * One station's playlist, with the two decisions an operator can make about a
 * track: keep it whatever the filter says, or keep it off.
 */
@Component({
  selector: 'app-playlist',
  standalone: true,
  template: `
    <h2>Playlist</h2>
    <p data-count>{{ total() }} tracks</p>
    <table data-playlist>
      <thead>
        <tr>
          <th scope="col">Artist</th>
          <th scope="col">Title</th>
          <th scope="col">Album</th>
          <th scope="col">Year</th>
          <th scope="col">Status</th>
          <th scope="col"><span data-sr-only>Actions</span></th>
        </tr>
      </thead>
      <tbody>
      @for (track of tracks(); track track.track_id) {
        <tr [attr.data-missing]="track.missing ? '' : null">
          <td>{{ track.artist }}</td>
          <td>{{ track.title }}</td>
          <td>{{ track.album }}</td>
          <td>{{ track.year || '' }}</td>
          <td>
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
          <td>
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
    <p data-said>{{ said() }}</p>
  `,
})
export class Playlist {
  private readonly api = inject(AdminApi);

  readonly station = input.required<number>();

  readonly tracks = signal<PlaylistTrack[]>([]);
  readonly total = signal(0);
  readonly offset = signal(0);
  readonly said = signal('');

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
    this.api.playlist(this.station(), PAGE, this.offset()).subscribe({
      next: (p) => {
        this.tracks.set(p.tracks);
        this.total.set(p.total);
      },
      error: () => this.said.set('Could not read the playlist.'),
    });
  }

  page(direction: number): void {
    this.offset.set(Math.max(0, this.offset() + direction * PAGE));
    this.load();
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
