import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { Component, signal } from '@angular/core';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { NowPlayingView, POLL_MS } from './now-playing.component';

@Component({
  standalone: true,
  imports: [NowPlayingView],
  template: `<app-now-playing [station]="station()" />`,
})
class Host {
  readonly station = signal<number | null>(null);
}

describe('NowPlayingView', () => {
  let ctrl: HttpTestingController;

  beforeEach(async () => {
    vi.useFakeTimers();
    await TestBed.configureTestingModule({
      imports: [Host],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('asks nothing until a station is picked', () => {
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    ctrl.expectNone('/now.json?station=1');
    expect(fixture.nativeElement.querySelector('[data-nowplaying]').textContent).toContain(
      'Nothing playing',
    );
  });

  it('renders the track, the transcript and the enrichment', () => {
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    fixture.componentInstance.station.set(3);
    fixture.detectChanges();

    ctrl.expectOne('/now.json?station=3').flush({
      now: { artist: 'New Order', title: 'Blue Monday' },
      last_break: { text: 'Three in the morning and that one still holds up.' },
      enrichment: { done: 412, total: 4197, pct: 9.8 },
    });
    fixture.detectChanges();

    const el = fixture.nativeElement;
    expect(el.querySelector('[data-nowplaying]').textContent).toContain('Blue Monday');
    expect(el.querySelector('[data-transcript]').textContent).toContain('still holds up');
    expect(el.querySelector('[data-enrichment]').textContent).toContain('412 of 4197');
  });

  it('polls on a timer', () => {
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    fixture.componentInstance.station.set(3);
    fixture.detectChanges();
    ctrl.expectOne('/now.json?station=3').flush({});

    vi.advanceTimersByTime(POLL_MS);
    ctrl.expectOne('/now.json?station=3').flush({});
    vi.advanceTimersByTime(POLL_MS);
    ctrl.expectOne('/now.json?station=3').flush({});
  });

  it('keeps the last answer when a poll fails', () => {
    // Blanking it would make one dropped request look like the stream
    // stopping.
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    fixture.componentInstance.station.set(3);
    fixture.detectChanges();
    ctrl.expectOne('/now.json?station=3').flush({ now: { artist: 'A', title: 'B' } });
    fixture.detectChanges();

    vi.advanceTimersByTime(POLL_MS);
    ctrl.expectOne('/now.json?station=3').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-nowplaying]').textContent).toContain('B');
  });

  it('follows the listener to another station', () => {
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    fixture.componentInstance.station.set(3);
    fixture.detectChanges();
    ctrl.expectOne('/now.json?station=3').flush({});

    fixture.componentInstance.station.set(1);
    fixture.detectChanges();
    ctrl.expectOne('/now.json?station=1').flush({});

    // And the old station's timer is gone, not doubled up.
    vi.advanceTimersByTime(POLL_MS);
    ctrl.expectNone('/now.json?station=3');
    ctrl.expectOne('/now.json?station=1').flush({});
  });

  it('stops polling when the page goes away', () => {
    // A timer left running polls forever on a page nobody is looking at.
    const clear = vi.spyOn(globalThis, 'clearInterval');
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    fixture.componentInstance.station.set(3);
    fixture.detectChanges();
    ctrl.expectOne('/now.json?station=3').flush({});

    fixture.destroy();
    expect(clear).toHaveBeenCalled();

    vi.advanceTimersByTime(POLL_MS * 3);
    ctrl.expectNone('/now.json?station=3');
    clear.mockRestore();
  });

  // A break that is coming, one still being written, and one that was never
  // scheduled are the same silence from here -- so a quiet station and a broken
  // one read alike.
  function tuned(payload: object) {
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    fixture.componentInstance.station.set(3);
    fixture.detectChanges();
    ctrl.expectOne('/now.json?station=3').flush(payload);
    fixture.detectChanges();
    return fixture;
  }

  it('says the DJ speaks after this track', () => {
    const fixture = tuned({ now: null, next_break: 'ready' });
    expect(fixture.nativeElement.querySelector('[data-nextbreak]').textContent).toContain(
      'speaks after this track',
    );
  });

  it('says the DJ is still writing', () => {
    const fixture = tuned({ now: null, next_break: 'writing' });
    expect(fixture.nativeElement.querySelector('[data-nextbreak]').textContent).toContain(
      'writing a break',
    );
  });

  it('says nothing when no break is coming', () => {
    expect(
      tuned({ now: null, next_break: 'none' }).nativeElement.querySelector('[data-nextbreak]'),
    ).toBeNull();
    // And a server that does not report it at all is not a crash.
    expect(tuned({ now: null }).nativeElement.querySelector('[data-nextbreak]')).toBeNull();
  });

  it('forgets the old station when the listener moves', () => {
    // A stale transcript is a thumbs-down button offering to rate a break from
    // the station they just left -- and a track name that is not playing.
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    fixture.componentInstance.station.set(3);
    fixture.detectChanges();
    ctrl.expectOne('/now.json?station=3').flush({
      now: { artist: 'A', title: 'B' },
      last_break: { text: 'Three in the morning.' },
      enrichment: { done: 412, total: 4197, pct: 9.8 },
    });
    fixture.detectChanges();

    fixture.componentInstance.station.set(1);
    fixture.detectChanges();
    // BEFORE the new station has answered.
    const el = fixture.nativeElement;
    expect(el.querySelector('[data-transcript]')).toBeNull();
    expect(el.querySelector('[data-nowplaying]').textContent).not.toContain('B');
    // The enrichment figure is LIBRARY-WIDE and identical on every station, so
    // blanking it would flicker a true number off the screen for a whole poll.
    expect(el.querySelector('[data-enrichment]').textContent).toContain('412 of 4197');
    ctrl.expectOne('/now.json?station=1').flush({});
  });
});
