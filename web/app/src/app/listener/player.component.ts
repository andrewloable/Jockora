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
// How long to wait before asking for the playlist again.
const RETRY_MS = 2000;

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
  private retry: ReturnType<typeof setTimeout> | null = null;

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

    // HLS.JS FIRST, AND canPlayType IS NOT A RELIABLE ANSWER.
    //
    // Desktop Chrome, Edge and Firefox all reply "maybe" to
    // canPlayType('application/vnd.apple.mpegurl') and then cannot play it at
    // all. Trusting that string sent every non-Safari listener down the native
    // path, where the audio element sat at readyState 0 forever: the player
    // said "Ready." and nothing ever came out. Measured in a real browser
    // against the live station.
    //
    // The order still protects iOS, which is what the old one was for.
    // Hls.isSupported() is false exactly where hls.js cannot run -- iOS Safari
    // has no Media Source Extensions -- so iOS falls through to native, which
    // is the one place native is both available and necessary.
    if (!Hls.isSupported()) {
      if (audio.canPlayType('application/vnd.apple.mpegurl')) {
        audio.src = src;
        this.status.set('Ready.');
        return;
      }
      this.status.set('This browser cannot play the stream.');
      return;
    }

    this.destroy();
    this.hls = new Hls({ enableWorker: true, lowLatencyMode: false });
    this.hls.loadSource(src);
    this.hls.attachMedia(audio);
    // SAY WHEN IT COMES BACK. Without this the status kept whatever the last
    // failure set -- a stream that had fully recovered and was 12 seconds
    // buffered still read "Reconnecting…", which is indistinguishable from
    // broken to the person looking at it.
    this.hls.on(Hls.Events.MANIFEST_PARSED, () => this.status.set('Ready.'));
    this.hls.on(Hls.Events.ERROR, (_event, data) => {
      if (!data.fatal) {
        return;
      }
      // A live stream restarts its encoder now and then. Recover rather than
      // dropping the listener.
      if (data.type === Hls.ErrorTypes.NETWORK_ERROR) {
        this.status.set('Reconnecting…');
        // loadSource, NOT startLoad.
        //
        // A STATION STARTS ON DEMAND: tuning registers a listener, the manager
        // starts the encoder on its next tick, and the first playlist appears a
        // second or two later. So the very first fetch after tuning to a cold
        // station 404s, which hls.js reports as a FATAL manifestLoadError --
        // and startLoad does not re-request a manifest, so the player sat on
        // "Reconnecting…" forever and never made a sound. It is the normal
        // path, not an edge case: it happens every time somebody is the first
        // listener on a station.
        this.reload(src);
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

  // Retried indefinitely and on a timer, because this is radio: the station
  // may simply be starting, and the right behaviour is to keep asking rather
  // than to hand the listener a dead player. The timer also spaces the
  // requests, which keeps a failing stream from hammering the server.
  private reload(src: string): void {
    if (this.retry !== null) {
      return;
    }
    this.retry = setTimeout(() => {
      this.retry = null;
      this.hls?.loadSource(src);
    }, RETRY_MS);
  }

  private destroy(): void {
    if (this.retry !== null) {
      clearTimeout(this.retry);
      this.retry = null;
    }
    this.hls?.destroy();
    this.hls = null;
  }

  ngOnDestroy(): void {
    this.destroy();
  }
}
