import { describe, expect, it, beforeEach, vi } from 'vitest';
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
  beforeEach(async () => {
    await TestBed.configureTestingModule({ imports: [Host] }).compileComponents();
  });

  function mount() {
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    return fixture;
  }

  // THE INVARIANT. Changing station is the escape hatch this product gives
  // instead of a skip, and that is what lets a break make a forward reference.
  it('has no skip, next or seek control', () => {
    const fixture = mount();
    const el = fixture.nativeElement;
    expect(el.querySelectorAll('[data-skip], .skip, [data-seek], [data-next]').length).toBe(0);
    expect(el.textContent.toLowerCase()).not.toContain('skip');
    expect(el.textContent.toLowerCase()).not.toContain('next track');
  });

  it('offers the browser its own play, pause and volume', () => {
    const audio = mount().nativeElement.querySelector('[data-player]');
    expect(audio.hasAttribute('controls')).toBe(true);
    // No autoplay: browsers block it, and the block looks like a broken stream.
    expect(audio.hasAttribute('autoplay')).toBe(false);
    expect(audio.getAttribute('preload')).toBe('none');
  });

  it('uses native HLS where the browser has it', () => {
    // iOS Safari has no Media Source Extensions, so hls.js cannot work there
    // at all, and iOS is a required target.
    const fixture = mount();
    const audio: HTMLAudioElement = fixture.nativeElement.querySelector('[data-player]');
    audio.canPlayType = () => 'maybe';
    const attach = vi.spyOn(Hls.prototype, 'attachMedia');

    fixture.componentInstance.src.set('/hls/1/stream.m3u8');
    fixture.detectChanges();

    expect(audio.src).toContain('/hls/1/stream.m3u8');
    expect(attach).not.toHaveBeenCalled();
    attach.mockRestore();
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
    vi.spyOn(Hls.prototype, 'on').mockImplementation(((_e: unknown, h: never) => {
      handler = h;
    }) as never);

    fixture.componentInstance.src.set('/hls/1/stream.m3u8');
    fixture.detectChanges();

    // A live stream restarts its encoder now and then.
    handler(null, { fatal: false });
    expect(startLoad).not.toHaveBeenCalled();

    handler(null, { fatal: true, type: Hls.ErrorTypes.NETWORK_ERROR });
    expect(startLoad).toHaveBeenCalled();

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
    expect(source).not.toContain('autoplay');
    expect(source).toContain('preload="none"');
    expect(source).not.toContain('.play()');
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
