import { Component, OnDestroy, effect, inject, input, signal } from '@angular/core';
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
    @if (transcript()) {
      <p data-transcript>“{{ transcript() }}”</p>
    }
    @if (enrichment(); as e) {
      <p data-enrichment>Enriched {{ e.done }} of {{ e.total }} ({{ e.pct }}%)</p>
    }
  `,
})
export class NowPlayingView implements OnDestroy {
  private readonly api = inject(Api);

  readonly station = input<number | null>(null);

  readonly label = signal('Nothing playing yet.');
  readonly transcript = signal('');
  readonly enrichment = signal<NowPlaying['enrichment']>(null);

  private timer: ReturnType<typeof setInterval> | null = null;

  constructor() {
    effect(() => {
      const id = this.station();
      this.stop();
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
        this.transcript.set(n.last_break?.text ?? '');
        this.enrichment.set(n.enrichment ?? null);
      },
      // A poll that fails leaves the LAST answer on screen. Blanking it would
      // make one dropped request look like the stream stopping.
      error: () => undefined,
    });
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
