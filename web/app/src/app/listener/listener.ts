import { Component, inject, signal } from '@angular/core';
import { Router } from '@angular/router';
import { Api, DialStation } from '../api/api';
import { Dial } from './dial.component';
import { Feedback } from './feedback.component';
import { ThemeToggle } from '../theme';
import { NowPlayingView } from './now-playing.component';
import { Player } from './player.component';

/**
 * The listener's page: the dial, the player, what is on air, and the one piece
 * of feedback this product asks for.
 */
@Component({
  selector: 'app-listener',
  standalone: true,
  imports: [Dial, Player, NowPlayingView, Feedback, ThemeToggle],
  template: `
    <header data-masthead>
      <h1>Jockora</h1>
      <app-theme-toggle />
    </header>
    <app-player [src]="hls()" />
    <app-now-playing [station]="stationId()" />
    <app-feedback [station]="stationId()" [transcript]="transcript()" />
    <app-dial (tuned)="onTuned($event)" />
  `,
})
export class Listener {
  private readonly api = inject(Api);
  private readonly router = inject(Router);

  readonly station = signal<DialStation | null>(null);
  readonly hls = signal('');
  readonly transcript = signal('');

  constructor() {
    // Ask who we are before anything else. A listener whose session expired
    // while the tab was open should meet the login form rather than a dial
    // that silently fails to load.
    this.api.me().subscribe({
      error: () => void this.router.navigate(['/login']),
    });
  }

  stationId(): number | null {
    return this.station()?.id ?? null;
  }

  onTuned(event: { station: DialStation; hls: string }): void {
    this.station.set(event.station);
    this.hls.set(event.hls);
  }
}
