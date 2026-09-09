import { Component, OnDestroy, effect, inject, input, output, signal } from '@angular/core';
import { Api, NowPlaying } from '../api/api';

/** How often the page asks what is playing. */
export const POLL_MS = 4000;

/**
 * What is on air, and what the DJ last said.
 *
 * Polled rather than pushed: a websocket for one small object that changes
 * every few minutes is a connection to keep alive, reconnect and get wrong.
 */
@Component({
  selector: 'app-now-playing',
  standalone: true,
  template: `
    <p data-nowplaying>{{ label() }}</p>
    <!-- WHETHER THE DJ IS ABOUT TO SPEAK. A break that is coming, one still
         being written, and one that was never scheduled are the same silence
         from here -- so a quiet station and a broken one read alike. -->
    @if (breakLabel(); as b) {
      <p data-nextbreak>{{ b }}</p>
    }
    <!-- WHAT THE DJ SAID IS NOT PRINTED. Asked for 2026-09-09: radio is heard,
         and a transcript under the player turns a break into something to read
         along with. The signal stays -- it is what the thumbs-down has to rate,
         and this component owns the poll that carries it. Jockora-do2. -->
  `,
})
export class NowPlayingView implements OnDestroy {
  private readonly api = inject(Api);

  readonly station = input<number | null>(null);

  readonly label = signal('Nothing playing yet.');
  readonly transcript = signal('');

  /**
   * The break the DJ last said, for whoever else needs it.
   *
   * This component owns the /now.json poll, so the transcript arrives HERE
   * while the button that rates it lives one component sideways. An output
   * rather than a second poll or a service: the parent already composes both,
   * and asking the server the same question twice to get the same string is how
   * two views end up disagreeing about what is on air.
   *
   * Emitted only when it CHANGES. The poll runs every few seconds and one break
   * stands for minutes.
   */
  readonly spoke = output<string>();
  readonly nextBreak = signal('');

  /** What the DJ is about to do, in words rather than a state name. */
  breakLabel(): string {
    switch (this.nextBreak()) {
      case 'ready':
        return 'The DJ speaks after this track.';
      case 'writing':
        return 'The DJ is writing a break…';
      default:
        return '';
    }
  }

  private timer: ReturnType<typeof setInterval> | null = null;

  constructor() {
    effect(() => {
      const id = this.station();
      this.stop();
      // NOTHING FROM THE STATION THEY JUST LEFT. Until the new one answers --
      // up to a whole poll interval -- the old transcript would still be on
      // screen with a thumbs-down offering to rate it, and the old track name
      // would still read as what is playing.
      this.forget();
      if (id === null) {
        return;
      }
      this.poll(id);
      this.timer = setInterval(() => this.poll(id), POLL_MS);
    });
  }

  private poll(id: number): void {
    this.api.now(id).subscribe({
      next: (n) => {
        this.label.set(n.now ? `${n.now.artist} — ${n.now.title}` : 'Nothing playing yet.');
        this.nextBreak.set(n.next_break ?? '');
        const said = n.last_break?.text ?? '';
        if (said !== this.transcript()) {
          this.transcript.set(said);
          this.spoke.emit(said);
        }
      },
      // A poll that fails leaves the LAST answer on screen. Blanking it would
      // make one dropped request look like the stream stopping.
      error: () => undefined,
    });
  }

  /**
   * Drop everything the previous station said.
   *
   * A stale transcript is a thumbs-down button offering to rate a break from
   * the station they just left.
   */
  private forget(): void {
    this.label.set('Nothing playing yet.');
    this.transcript.set('');
    this.nextBreak.set('');
    this.spoke.emit('');
  }

  private stop(): void {
    if (this.timer !== null) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }

  ngOnDestroy(): void {
    // A timer left running polls forever on a page nobody is looking at.
    this.stop();
  }
}
