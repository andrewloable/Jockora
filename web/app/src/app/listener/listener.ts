import { Component, inject, signal } from '@angular/core';
import { Router } from '@angular/router';
import { LISTENER_ICON, setFavicon } from '../favicon';
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
      <!-- THE SHARED DEVICE IS THE INTENDED ONE: a household tablet, or a
           phone handed to somebody else. Accounts are admin-created, a listener
           cannot change their own password, and a session lasts thirty days --
           so before this, whoever signed in stayed signed in. Jockora-e9a.66. -->
      @if (guest()) {
        <!-- A GUEST HAS NOTHING TO SIGN OUT OF, and the operator still needs a
             way in: without this, an install with public listening on has no
             route to the console except typing a url. Jockora-vpd.

             A PLAIN href, deliberately, and not routerLink. Sign-out beside it
             goes through the Router because it has an API call to make first;
             this has none. routerLink would pull ActivatedRoute into a page
             that otherwise needs no router providers, and buy one cached
             bundle re-parse on a page the visitor is leaving anyway. -->
        <a data-sign-in href="/login">Sign in</a>
      } @else {
        <button type="button" data-sign-out (click)="signOut()">Sign out</button>
      }
      <app-theme-toggle />
    </header>
    <app-player [src]="hls()" />
    <!-- The transcript comes from the component that POLLS for it. It used to
         be a signal here that nothing ever wrote to, so the thumbs-down could
         not appear in the running app however well it was unit-tested. -->
    <app-now-playing [station]="stationId()" (spoke)="transcript.set($event)" />
    <!-- A thumbs-down is recorded AGAINST AN ACCOUNT, and a guest has none, so
         the control is absent rather than present and failing. -->
    @if (!guest()) {
      <app-feedback [station]="stationId()" [transcript]="transcript()" />
    }
    <app-dial (tuned)="onTuned($event)" />
  `,
})
export class Listener {
  private readonly api = inject(Api);
  private readonly router = inject(Router);

  readonly station = signal<DialStation | null>(null);
  readonly hls = signal('');
  readonly transcript = signal('');
  /** True for somebody listening without an account, while the operator allows it. */
  readonly guest = signal(false);

  constructor() {
    // THE LISTENER'S MARK. index.html already ships it, so this only matters
    // coming BACK from the console in the same tab -- which is exactly what an
    // operator does after checking something. Jockora-9xk.
    setFavicon(LISTENER_ICON);
    // Ask who we are before anything else. A listener whose session expired
    // while the tab was open should meet the login form rather than a dial
    // that silently fails to load.
    this.api.me().subscribe({
      // A GUEST STAYS. /me answers role guest with a 200 while the operator
      // has public listening on, and bouncing them would make the switch do
      // nothing at all in the browser.
      next: (me) => this.guest.set(me.role === 'guest'),
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

  /** End this session and go back to the login page. See Admin.signOut. */
  signOut(): void {
    const leave = () => void this.router.navigate(['/login']);
    this.api.logout().subscribe({ next: leave, error: leave });
  }
}
