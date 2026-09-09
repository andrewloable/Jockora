import { describe, expect, it, afterEach, beforeEach, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { TestBed } from '@angular/core/testing';
import { Component, signal } from '@angular/core';
// The LIGHT build. The full one carries subtitle, alt-audio and EME support
// this product has no use for, and costs 300kB of a listener's first load on a
// phone to do it.
import Hls from 'hls.js/dist/hls.light.min.mjs';
import { Player } from './player.component';

@Component({
  standalone: true,
  imports: [Player],
  template: `<app-player [src]="src()" />`,
})
class Host {
  readonly src = signal('');
}

describe('Player', () => {
  afterEach(() => {
    // Without this, vi.spyOn on an already-spied method hands back the SAME
    // mock and its call count carries into the next test -- which made a
    // "called twice" assertion see four. Order-dependent tests are worse than
    // no tests.
    vi.restoreAllMocks();
  });

  beforeEach(async () => {
    await TestBed.configureTestingModule({ imports: [Host] }).compileComponents();
  });

  function mount() {
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    return fixture;
  }

  // REPRODUCTION, reported 2026-09-09: pick a station, press Listen, pick a
  // different station -- the button still reads Stop, pressing it does nothing,
  // and no sound comes from the new station.
  it('player transport keeps playing when the listener changes station', () => {
    vi.spyOn(Hls, 'isSupported').mockReturnValue(true);
    vi.spyOn(Hls.prototype, 'loadSource').mockImplementation(() => undefined);
    vi.spyOn(Hls.prototype, 'attachMedia').mockImplementation(() => undefined);
    const fixture = mount();
    const audio = fixture.nativeElement.querySelector('[data-player]') as HTMLAudioElement;

    // THE ELEMENT'S OWN paused, modelled honestly: jsdom's is read-only, and
    // the whole defect lives in WHEN it flips.
    let paused = true;
    Object.defineProperty(audio, 'paused', { configurable: true, get: () => paused });
    const play = vi.spyOn(HTMLMediaElement.prototype, 'play').mockImplementation(async () => {
      paused = false;
      audio.dispatchEvent(new Event('play'));
    });

    fixture.componentInstance.src.set('/hls/1/live.m3u8');
    fixture.detectChanges();
    fixture.nativeElement.querySelector('[data-playpause]').click();
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-playpause]').textContent.trim()).toBe('Stop');
    play.mockClear();

    // THE SWAP, IN THE ORDER A BROWSER DOES IT. The element is still playing
    // when the new source arrives; hls.destroy detaches the media, which runs
    // the element's load algorithm, and THAT sets paused to true without firing
    // a pause event. So the signal that follows the element had nothing to
    // follow and went stale: the button kept claiming it was playing, and
    // pressing it called pause() on an already-paused element, which does
    // nothing. Hence "could not click listen".
    vi.spyOn(Hls.prototype, 'destroy').mockImplementation(() => {
      paused = true;
    });
    fixture.componentInstance.src.set('/hls/2/live.m3u8');
    fixture.detectChanges();

    // CHANGING STATION IS THIS PRODUCT'S ONLY ESCAPE HATCH. Somebody who was
    // listening and picks another station is still listening.
    expect(play).toHaveBeenCalled();
    expect(fixture.nativeElement.querySelector('[data-playpause]').textContent.trim()).toBe('Stop');
  });

  it('player transport tells the truth when the browser refuses the new station', async () => {
    // A REFUSED RESUME IS NOT A CRASH AND NOT A LIE. If the browser declines to
    // carry sound across the swap, the rejection is swallowed the same way
    // toggle() swallows it -- and the button must then say Listen, because the
    // element really is paused. The alternative is the original bug wearing a
    // different hat.
    vi.spyOn(Hls, 'isSupported').mockReturnValue(true);
    vi.spyOn(Hls.prototype, 'loadSource').mockImplementation(() => undefined);
    vi.spyOn(Hls.prototype, 'attachMedia').mockImplementation(() => undefined);
    const fixture = mount();
    const audio = fixture.nativeElement.querySelector('[data-player]') as HTMLAudioElement;

    let paused = true;
    Object.defineProperty(audio, 'paused', { configurable: true, get: () => paused });
    const play = vi.spyOn(HTMLMediaElement.prototype, 'play').mockImplementation(async () => {
      paused = false;
      audio.dispatchEvent(new Event('play'));
    });
    fixture.componentInstance.src.set('/hls/1/live.m3u8');
    fixture.detectChanges();
    fixture.nativeElement.querySelector('[data-playpause]').click();
    fixture.detectChanges();

    // Now the browser says no.
    play.mockRejectedValue(new DOMException('NotAllowedError'));
    vi.spyOn(Hls.prototype, 'destroy').mockImplementation(() => {
      paused = true;
    });
    fixture.componentInstance.src.set('/hls/2/live.m3u8');
    fixture.detectChanges();
    await Promise.resolve();

    expect(play).toHaveBeenCalled();
    expect(fixture.nativeElement.querySelector('[data-playpause]').textContent.trim()).toBe(
      'Listen',
    );
  });

  it('player transport does not start playing a station nobody asked to hear', () => {
    // The other half: tuning while STOPPED must stay stopped, or picking a
    // station to read about it starts sound the listener did not ask for.
    vi.spyOn(Hls, 'isSupported').mockReturnValue(true);
    vi.spyOn(Hls.prototype, 'loadSource').mockImplementation(() => undefined);
    vi.spyOn(Hls.prototype, 'attachMedia').mockImplementation(() => undefined);
    const play = vi.spyOn(HTMLMediaElement.prototype, 'play').mockResolvedValue(undefined);
    const fixture = mount();

    fixture.componentInstance.src.set('/hls/1/live.m3u8');
    fixture.detectChanges();
    fixture.componentInstance.src.set('/hls/2/live.m3u8');
    fixture.detectChanges();

    expect(play).not.toHaveBeenCalled();
    expect(fixture.nativeElement.querySelector('[data-playpause]').textContent.trim()).toBe(
      'Listen',
    );
  });

  // THE INVARIANT. Changing station is the escape hatch this product gives
  // instead of a skip, and that is what lets a break make a forward reference.
  it('has no skip, next or seek control', () => {
    const fixture = mount();
    const el = fixture.nativeElement;
    expect(el.querySelectorAll('[data-skip], .skip, [data-seek], [data-next]').length).toBe(0);
    expect(el.textContent.toLowerCase()).not.toContain('skip');
    expect(el.textContent.toLowerCase()).not.toContain('next track');
  });

  it('player transport exposes no seek control', () => {
    // The NATIVE controls gave a live stream a draggable scrub bar and a
    // running duration: measured seekable [0, 60.01] on the live station, and a
    // readout of "0:28 / 0:44" that was the HLS buffer window rather than any
    // length a listener could mean. The component's own docstring forbids
    // exactly this.
    const el = mount().nativeElement;
    const audio = el.querySelector('[data-player]');
    expect(audio.hasAttribute('controls')).toBe(false);
    // No autoplay: browsers block it, and the block looks like a broken stream.
    expect(audio.hasAttribute('autoplay')).toBe(false);
    expect(audio.getAttribute('preload')).toBe('none');
    expect(el.querySelector('progress, input[type="range"][data-seek]')).toBeNull();
    // A live stream has no meaningful position, so nothing pretends otherwise.
    expect(el.textContent).not.toMatch(/\d:\d\d/);
  });

  it('player transport toggles play and pause', () => {
    const play = vi.spyOn(HTMLMediaElement.prototype, 'play').mockResolvedValue(undefined);
    const pause = vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(() => {});
    const fixture = mount();
    const button = fixture.nativeElement.querySelector('[data-playpause]');
    const audio = fixture.nativeElement.querySelector('[data-player]');

    expect(button.textContent.trim()).toBe('Listen');
    button.click();
    expect(play).toHaveBeenCalled();
    // The BUTTON follows the element, not the click: a stream that stops on its
    // own would otherwise leave the label saying it was still playing.
    audio.dispatchEvent(new Event('play'));
    fixture.detectChanges();
    expect(button.textContent.trim()).toBe('Stop');

    button.click();
    expect(pause).toHaveBeenCalled();
    audio.dispatchEvent(new Event('pause'));
    fixture.detectChanges();
    expect(button.textContent.trim()).toBe('Listen');
  });

  it('player transport survives a play the browser refuses', async () => {
    // A browser that will not make sound without a gesture rejects play(). The
    // status line already says what is wrong; an unhandled rejection in the
    // console helps nobody.
    const play = vi
      .spyOn(HTMLMediaElement.prototype, 'play')
      .mockRejectedValue(new Error('NotAllowedError'));
    const fixture = mount();
    fixture.nativeElement.querySelector('[data-playpause]').click();
    await Promise.resolve();
    expect(play).toHaveBeenCalled();
    expect(fixture.nativeElement.querySelector('[data-playpause]').textContent.trim()).toBe(
      'Listen',
    );
  });

  it('player transport carries a volume control and nothing else', () => {
    const fixture = mount();
    const volume = fixture.nativeElement.querySelector('[data-volume]');
    const audio = fixture.nativeElement.querySelector('[data-player]');
    expect(volume.type).toBe('range');

    volume.value = '0.4';
    volume.dispatchEvent(new Event('input'));
    fixture.detectChanges();
    expect(audio.volume).toBeCloseTo(0.4);
  });

  it('player transport says it is live instead of showing a position', () => {
    expect(mount().nativeElement.querySelector('[data-live]').textContent).toContain('Live');
  });

  it('uses native HLS only where hls.js cannot run', () => {
    // iOS Safari has no Media Source Extensions, so hls.js cannot work there
    // at all, and iOS is a required target.
    //
    // isSupported is mocked EXPLICITLY. Without that this test passed under
    // either ordering -- happy-dom reports no MSE, so native won regardless --
    // and it was cited as proof of an order it never checked.
    const fixture = mount();
    const audio: HTMLAudioElement = fixture.nativeElement.querySelector('[data-player]');
    audio.canPlayType = () => 'maybe';
    vi.spyOn(Hls, 'isSupported').mockReturnValue(false);
    const attach = vi.spyOn(Hls.prototype, 'attachMedia');

    fixture.componentInstance.src.set('/hls/1/stream.m3u8');
    fixture.detectChanges();

    expect(audio.src).toContain('/hls/1/stream.m3u8');
    expect(attach).not.toHaveBeenCalled();
    attach.mockRestore();
  });

  it('uses hls.js on a browser that only CLAIMS it can play HLS', () => {
    // THE BUG THIS FILE MISSED. Desktop Chrome, Edge and Firefox all answer
    // "maybe" to canPlayType('application/vnd.apple.mpegurl') and then cannot
    // play it. Trusting that string put the m3u8 straight on the audio
    // element, where it sat at readyState 0 forever: the player said "Ready."
    // and no sound ever came out. Measured in a real browser against the live
    // station before this test existed.
    const fixture = mount();
    const audio: HTMLAudioElement = fixture.nativeElement.querySelector('[data-player]');
    audio.canPlayType = () => 'maybe';
    vi.spyOn(Hls, 'isSupported').mockReturnValue(true);
    const attach = vi.spyOn(Hls.prototype, 'attachMedia').mockImplementation(() => {});
    const load = vi.spyOn(Hls.prototype, 'loadSource').mockImplementation(() => {});

    fixture.componentInstance.src.set('/hls/1/stream.m3u8');
    fixture.detectChanges();

    expect(load).toHaveBeenCalledWith('/hls/1/stream.m3u8');
    expect(attach).toHaveBeenCalled();
    // And the raw playlist never reaches the element, which is what silence
    // looked like.
    expect(audio.src).not.toContain('.m3u8');
  });

  it('falls back to hls.js where it does not', () => {
    const fixture = mount();
    const audio: HTMLAudioElement = fixture.nativeElement.querySelector('[data-player]');
    audio.canPlayType = () => '';
    vi.spyOn(Hls, 'isSupported').mockReturnValue(true);
    const load = vi.spyOn(Hls.prototype, 'loadSource').mockImplementation(() => undefined);
    const attach = vi.spyOn(Hls.prototype, 'attachMedia').mockImplementation(() => undefined);

    fixture.componentInstance.src.set('/hls/2/stream.m3u8');
    fixture.detectChanges();

    expect(load).toHaveBeenCalledWith('/hls/2/stream.m3u8');
    expect(attach).toHaveBeenCalled();
    vi.restoreAllMocks();
  });

  it('says so when the browser can do neither', () => {
    const fixture = mount();
    const audio: HTMLAudioElement = fixture.nativeElement.querySelector('[data-player]');
    audio.canPlayType = () => '';
    vi.spyOn(Hls, 'isSupported').mockReturnValue(false);

    fixture.componentInstance.src.set('/hls/1/stream.m3u8');
    fixture.detectChanges();
    fixture.detectChanges();

    expect(fixture.nativeElement.querySelector('[data-status]').textContent).toContain('cannot');
    vi.restoreAllMocks();
  });

  it('recovers rather than dropping the listener', () => {
    const fixture = mount();
    const audio: HTMLAudioElement = fixture.nativeElement.querySelector('[data-player]');
    audio.canPlayType = () => '';
    vi.spyOn(Hls, 'isSupported').mockReturnValue(true);
    vi.spyOn(Hls.prototype, 'loadSource').mockImplementation(() => undefined);
    vi.spyOn(Hls.prototype, 'attachMedia').mockImplementation(() => undefined);
    const startLoad = vi.spyOn(Hls.prototype, 'startLoad').mockImplementation(() => undefined);
    const recover = vi
      .spyOn(Hls.prototype, 'recoverMediaError')
      .mockImplementation(() => undefined);
    const destroy = vi.spyOn(Hls.prototype, 'destroy').mockImplementation(() => undefined);

    let handler: (e: unknown, d: unknown) => void = () => undefined;
    vi.spyOn(Hls.prototype, 'on').mockImplementation(((e: unknown, h: never) => {
      if (e === Hls.Events.ERROR) {
        handler = h;
      }
    }) as never);

    fixture.componentInstance.src.set('/hls/1/stream.m3u8');
    fixture.detectChanges();

    // A live stream restarts its encoder now and then.
    handler(null, { fatal: false });
    expect(startLoad).not.toHaveBeenCalled();

    // A network error now RE-REQUESTS THE MANIFEST rather than calling
    // startLoad, which cannot recover a manifest that was never fetched.
    handler(null, { fatal: true, type: Hls.ErrorTypes.NETWORK_ERROR });
    expect(startLoad).not.toHaveBeenCalled();

    handler(null, { fatal: true, type: Hls.ErrorTypes.MEDIA_ERROR });
    expect(recover).toHaveBeenCalled();

    handler(null, { fatal: true, type: Hls.ErrorTypes.OTHER_ERROR });
    fixture.detectChanges();
    expect(destroy).toHaveBeenCalled();
    expect(fixture.nativeElement.querySelector('[data-status]').textContent).toContain(
      'unavailable',
    );
    vi.restoreAllMocks();
  });

  it('does nothing until somebody tunes', () => {
    // The player is on the page before any station is picked, and must not
    // reach for a stream that has not been named yet.
    const fixture = mount();
    const audio: HTMLAudioElement = fixture.nativeElement.querySelector('[data-player]');
    expect(audio.getAttribute('src')).toBeNull();
    expect(fixture.nativeElement.querySelector('[data-status]').textContent).toContain('Pick');

    // And tearing it down before it ever played is a no-op, not a crash.
    fixture.destroy();
  });

  it('lets go of the stream when it goes away', () => {
    const fixture = mount();
    const audio: HTMLAudioElement = fixture.nativeElement.querySelector('[data-player]');
    audio.canPlayType = () => '';
    vi.spyOn(Hls, 'isSupported').mockReturnValue(true);
    vi.spyOn(Hls.prototype, 'loadSource').mockImplementation(() => undefined);
    vi.spyOn(Hls.prototype, 'attachMedia').mockImplementation(() => undefined);
    vi.spyOn(Hls.prototype, 'on').mockImplementation((() => undefined) as never);
    const destroy = vi.spyOn(Hls.prototype, 'destroy').mockImplementation(() => undefined);

    fixture.componentInstance.src.set('/hls/1/stream.m3u8');
    fixture.detectChanges();
    fixture.destroy();

    // A player left attached keeps fetching segments for a station nobody is
    // watching, which counts as a listener and holds it on air.
    expect(destroy).toHaveBeenCalled();
    vi.restoreAllMocks();
  });

  it('says it is ready again once the stream comes back', () => {
    // A stream that had fully recovered and was seconds-buffered still read
    // "Reconnecting…", which is indistinguishable from broken to the person
    // looking at it. Seen on the live station.
    const fixture = mount();
    const audio: HTMLAudioElement = fixture.nativeElement.querySelector('[data-player]');
    audio.canPlayType = () => '';
    vi.spyOn(Hls, 'isSupported').mockReturnValue(true);
    vi.spyOn(Hls.prototype, 'loadSource').mockImplementation(() => undefined);
    vi.spyOn(Hls.prototype, 'attachMedia').mockImplementation(() => undefined);

    const handlers = new Map<unknown, (e: unknown, d: unknown) => void>();
    vi.spyOn(Hls.prototype, 'on').mockImplementation(((e: unknown, h: never) => {
      handlers.set(e, h);
    }) as never);

    fixture.componentInstance.src.set('/hls/1/stream.m3u8');
    fixture.detectChanges();

    handlers.get(Hls.Events.ERROR)?.({}, { fatal: true, type: Hls.ErrorTypes.NETWORK_ERROR });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-status]').textContent).toContain(
      'Reconnecting',
    );

    handlers.get(Hls.Events.MANIFEST_PARSED)?.({}, {});
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-status]').textContent).toContain('Ready');
  });

  it('keeps asking for the playlist when the station is still starting', () => {
    // THE NORMAL PATH, not an edge case. A station starts on demand: tuning
    // registers a listener, the manager starts the encoder on its next tick,
    // and the first playlist lands a second or two later. So the first fetch
    // after tuning to a cold station 404s -- a FATAL manifestLoadError, which
    // startLoad does not recover, because it does not re-request a manifest.
    // The player sat on "Reconnecting..." forever and never made a sound.
    vi.useFakeTimers();
    try {
      const fixture = mount();
      const audio: HTMLAudioElement = fixture.nativeElement.querySelector('[data-player]');
      audio.canPlayType = () => '';
      vi.spyOn(Hls, 'isSupported').mockReturnValue(true);
      vi.spyOn(Hls.prototype, 'attachMedia').mockImplementation(() => {});
      const load = vi.spyOn(Hls.prototype, 'loadSource').mockImplementation(() => {});
      const start = vi.spyOn(Hls.prototype, 'startLoad').mockImplementation(() => {});
      let handler: (e: unknown, d: unknown) => void = () => undefined;
      vi.spyOn(Hls.prototype, 'on').mockImplementation(((e: unknown, h: never) => {
        if (e === Hls.Events.ERROR) {
          handler = h;
        }
      }) as never);

      fixture.componentInstance.src.set('/hls/1/stream.m3u8');
      fixture.detectChanges();
      expect(load).toHaveBeenCalledTimes(1);

      // The station is not up yet: hls.js reports a fatal network error.
      handler({}, { fatal: true, type: Hls.ErrorTypes.NETWORK_ERROR });
      fixture.detectChanges();

      expect(fixture.nativeElement.querySelector('[data-status]').textContent).toContain(
        'Reconnecting',
      );
      // startLoad is not enough for a manifest that was never there.
      vi.advanceTimersByTime(2000);
      expect(load).toHaveBeenCalledTimes(2);
      expect(load).toHaveBeenLastCalledWith('/hls/1/stream.m3u8');
      expect(start).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  it('does not stack reloads while one is already pending', () => {
    // hls.js reports the same failure repeatedly; one timer, not a queue.
    vi.useFakeTimers();
    try {
      const fixture = mount();
      const audio: HTMLAudioElement = fixture.nativeElement.querySelector('[data-player]');
      audio.canPlayType = () => '';
      vi.spyOn(Hls, 'isSupported').mockReturnValue(true);
      vi.spyOn(Hls.prototype, 'attachMedia').mockImplementation(() => {});
      const load = vi.spyOn(Hls.prototype, 'loadSource').mockImplementation(() => {});
      let handler: (e: unknown, d: unknown) => void = () => undefined;
      vi.spyOn(Hls.prototype, 'on').mockImplementation(((e: unknown, h: never) => {
        if (e === Hls.Events.ERROR) {
          handler = h;
        }
      }) as never);

      fixture.componentInstance.src.set('/hls/1/stream.m3u8');
      fixture.detectChanges();
      handler({}, { fatal: true, type: Hls.ErrorTypes.NETWORK_ERROR });
      handler({}, { fatal: true, type: Hls.ErrorTypes.NETWORK_ERROR });
      handler({}, { fatal: true, type: Hls.ErrorTypes.NETWORK_ERROR });

      vi.advanceTimersByTime(2000);
      expect(load).toHaveBeenCalledTimes(2); // the first, and exactly one retry
    } finally {
      vi.useRealTimers();
    }
  });

  it('drops a pending retry when the player goes away', () => {
    vi.useFakeTimers();
    try {
      const fixture = mount();
      const audio: HTMLAudioElement = fixture.nativeElement.querySelector('[data-player]');
      audio.canPlayType = () => '';
      vi.spyOn(Hls, 'isSupported').mockReturnValue(true);
      vi.spyOn(Hls.prototype, 'attachMedia').mockImplementation(() => {});
      const load = vi.spyOn(Hls.prototype, 'loadSource').mockImplementation(() => {});
      let handler: (e: unknown, d: unknown) => void = () => undefined;
      vi.spyOn(Hls.prototype, 'on').mockImplementation(((e: unknown, h: never) => {
        if (e === Hls.Events.ERROR) {
          handler = h;
        }
      }) as never);

      fixture.componentInstance.src.set('/hls/1/stream.m3u8');
      fixture.detectChanges();
      handler({}, { fatal: true, type: Hls.ErrorTypes.NETWORK_ERROR });

      fixture.destroy();
      vi.advanceTimersByTime(5000);
      expect(load).toHaveBeenCalledTimes(1); // the retry never fired
    } finally {
      vi.useRealTimers();
    }
  });
});

// The two guarantees below were carried over from web/web_test.go when
// web/index.html was deleted in the row 15 gate. They are read off the SOURCE
// rather than the rendered DOM, because both are about what the code may
// contain at all -- an `.play()` added in a branch no test happens to enter is
// still autoplay, and a CDN url in a file nothing imports is still a CDN url.
describe('Player, as written', () => {
  const source = readFileSync(
    join(import.meta.dirname, 'src', 'app', 'listener', 'player.component.ts'),
    'utf8',
  );

  it('never starts playing by itself', () => {
    // Browsers block autoplay, and the block looks exactly like a broken
    // stream: a play button that does nothing and no error anywhere.
    //
    // Asserted on BEHAVIOUR now rather than on the absence of ".play()" in the
    // source. The player has a play button, so the string is there on purpose;
    // what must stay true is that tuning alone never calls it.
    const play = vi.spyOn(HTMLMediaElement.prototype, 'play').mockResolvedValue(undefined);
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    fixture.componentInstance.src.set('/hls/3/stream.m3u8');
    fixture.detectChanges();
    expect(play).not.toHaveBeenCalled();

    expect(source).not.toContain('autoplay');
    expect(source).toContain('preload="none"');
  });

  it('loads nothing from a CDN', () => {
    // Self-hosted means self-hosted. This has to work on a machine with no
    // route to the internet, which is a normal way to run a music server.
    const shell = readFileSync(join(import.meta.dirname, 'src', 'index.html'), 'utf8');
    for (const cdn of ['//cdn.', 'unpkg', 'jsdelivr', 'cdnjs']) {
      expect(source, cdn).not.toContain(cdn);
      expect(shell, cdn).not.toContain(cdn);
    }
  });
});
