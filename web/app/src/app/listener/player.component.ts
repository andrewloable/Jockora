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
    <!-- NO "controls" ATTRIBUTE. The browser's own widget hands a live stream a
         draggable scrub bar and a running duration -- measured seekable
         [0, 60.01] on the live station, with a readout of "0:28 / 0:44" that is
         the HLS buffer window and not a length any listener could mean. What is
         left is the two controls this product allows. -->
    <audio
      data-player
      preload="none"
      (play)="playing.set(true)"
      (pause)="playing.set(false)"
    ></audio>
    <p data-transport>
      <button type="button" data-playpause (click)="toggle()">
        {{ playing() ? 'Stop' : 'Listen' }}
      </button>
      <label data-volume-label>
        Volume
        <input
          data-volume
          type="range"
          min="0"
          max="1"
          step="0.01"
          [value]="volume()"
          (input)="setVolume($any($event.target).value)"
        />
      </label>
      <!-- WHERE THE TIME USED TO BE. A live stream has no position, so it says
           the one true thing about where the listener is in it. -->
      <span data-live>Live</span>
    </p>
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

  /**
   * Whether sound is coming out, followed from the ELEMENT rather than set on
   * the click: a stream that stops on its own would otherwise leave the button
   * claiming it was still playing.
   */
  readonly playing = signal(false);
  readonly volume = signal(1);

  private audio(): HTMLAudioElement {
    return this.host.nativeElement.querySelector('[data-player]');
  }

  /** The only transport this product has: on, or off. */
  toggle(): void {
    const audio = this.audio();
    if (!this.playing()) {
      // A rejected play() is a browser refusing sound without a gesture, or a
      // stream that is not there yet. The status line already says which, and
      // an unhandled rejection in the console helps nobody.
      void audio.play().catch(() => undefined);
      return;
    }
    audio.pause();
  }

  setVolume(value: string): void {
    const level = Number(value);
    this.volume.set(level);
    this.audio().volume = level;
  }

  constructor() {
    effect(() => {
      const src = this.src();
      if (src) {
        this.play(src);
      }
    });
  }

  private play(src: string): void {
    const audio = this.audio();

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
