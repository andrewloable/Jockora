import { Component, ElementRef, OnDestroy, effect, inject, input, signal } from '@angular/core';
// The LIGHT build. The full one carries subtitle, alt-audio and EME support
// this product has no use for, and costs 300kB of a listener's first load on a
// phone to do it.
import Hls from 'hls.js/dist/hls.light.min.mjs';

/**
 * The player.
 *
 * NO SKIP, NO SEEK, NO NEXT. Changing station is the escape hatch this product
 * gives instead, and that is what lets a break safely make a forward reference:
 * nothing a listener can press invalidates the lookahead buffer.
 */
@Component({
  selector: 'app-player',
  standalone: true,
  template: `
    <audio data-player controls preload="none"></audio>
    <p data-status>{{ status() }}</p>
  `,
})
export class Player implements OnDestroy {
  /** The stream to play, or empty before anyone has tuned. */
  readonly src = input('');

  // The host element rather than a viewChild: one element, found by the
  // attribute the tests also use, and no signal to read at the wrong time.
  private readonly host = inject(ElementRef<HTMLElement>);
  private hls: Hls | null = null;

  readonly status = signal('Pick a station to start listening.');

  constructor() {
    effect(() => {
      const src = this.src();
      if (src) {
        this.play(src);
      }
    });
  }

  private play(src: string): void {
    const audio: HTMLAudioElement = this.host.nativeElement.querySelector('[data-player]');

    // NATIVE HLS FIRST, wherever it exists. iOS Safari has no Media Source
    // Extensions at all, so hls.js cannot work there under any circumstances,
    // and iOS is a required target.
    if (audio.canPlayType('application/vnd.apple.mpegurl')) {
      audio.src = src;
      this.status.set('Ready.');
      return;
    }
    if (!Hls.isSupported()) {
      this.status.set('This browser cannot play the stream.');
      return;
    }

    this.destroy();
    this.hls = new Hls({ enableWorker: true, lowLatencyMode: false });
    this.hls.loadSource(src);
    this.hls.attachMedia(audio);
    this.hls.on(Hls.Events.ERROR, (_event, data) => {
      if (!data.fatal) {
        return;
      }
      // A live stream restarts its encoder now and then. Recover rather than
      // dropping the listener.
      if (data.type === Hls.ErrorTypes.NETWORK_ERROR) {
        this.status.set('Reconnecting…');
        this.hls?.startLoad();
      } else if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
        this.status.set('Recovering…');
        this.hls?.recoverMediaError();
      } else {
        this.status.set('Stream unavailable.');
        this.destroy();
      }
    });
    this.status.set('Ready.');
  }

  private destroy(): void {
    this.hls?.destroy();
    this.hls = null;
  }

  ngOnDestroy(): void {
    this.destroy();
  }
}
