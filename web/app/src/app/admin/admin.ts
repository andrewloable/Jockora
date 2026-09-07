import { Component, inject, signal } from '@angular/core';
import { AdminApi, Station } from '../api/api';
import { Overview } from './overview.component';
import { Sources } from './sources.component';
import { Stations } from './stations.component';
import { Playlist } from './playlist.component';
import { Jocks } from './jocks.component';
import { Users } from './users.component';

const sections = ['overview', 'sources', 'stations', 'playlist', 'jocks', 'people'] as const;
type Section = (typeof sections)[number];

/**
 * The operator console.
 *
 * One section is mounted at a time rather than all six hidden behind CSS: each
 * loads on mount, and six sections polling the server at once for pages nobody
 * is looking at is a load the operator did not ask for.
 */
@Component({
  selector: 'app-admin',
  standalone: true,
  imports: [Overview, Sources, Stations, Playlist, Jocks, Users],
  template: `
    <h1>Jockora — operator</h1>
    <nav>
      @for (section of sections; track section) {
        <button
          type="button"
          [attr.data-section]="section"
          [attr.aria-current]="section === showing() ? 'page' : null"
          (click)="open(section)"
        >
          {{ section }}
        </button>
      }
    </nav>

    @switch (showing()) {
      @case ('overview') {
        <app-overview />
      }
      @case ('sources') {
        <app-sources />
      }
      @case ('stations') {
        <app-stations />
      }
      @case ('playlist') {
        <select data-pick-station [value]="picked() ?? ''" (change)="pick($event)">
          @for (station of stations(); track station.id) {
            <option [value]="station.id">{{ station.name }}</option>
          }
        </select>
        @if (picked(); as id) {
          <app-playlist [station]="id" />
        } @else {
          <p data-no-stations>No stations yet. Make one first.</p>
        }
      }
      @case ('jocks') {
        <app-jocks />
      }
      @default {
        <!-- 'people'. @default rather than @case so the switch is exhaustive:
             Section is a closed union, and a case for every member leaves a
             fall-through nothing can ever reach. -->
        <app-users />
      }
    }
  `,
})
export class Admin {
  private readonly api = inject(AdminApi);

  readonly sections = sections;
  readonly showing = signal<Section>('overview');
  readonly stations = signal<Station[]>([]);
  readonly picked = signal<number | null>(null);

  open(section: Section): void {
    this.showing.set(section);
    // The playlist editor edits ONE station, so the console has to say which.
    // Read fresh each time: a station created a minute ago in the section next
    // door should be here.
    if (section === 'playlist') {
      this.api.stations().subscribe({
        next: (list) => {
          this.stations.set(list);
          this.picked.set(list[0]?.id ?? null);
        },
        error: () => this.picked.set(null),
      });
    }
  }

  pick(event: Event): void {
    this.picked.set(Number((event.target as HTMLSelectElement).value));
  }
}
