import { describe, expect, it, beforeEach, vi } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Router } from '@angular/router';
import { Listener } from './listener';

describe('Listener', () => {
  const navigate = vi.fn();
  let ctrl: HttpTestingController;

  beforeEach(async () => {
    navigate.mockClear();
    await TestBed.configureTestingModule({
      imports: [Listener],
      providers: [
        provideHttpClient(),
        provideHttpClientTesting(),
        { provide: Router, useValue: { navigate } },
      ],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  function mounted(me: object = { name: 'andrew', role: 'listener' }) {
    const fixture = TestBed.createComponent(Listener);
    fixture.detectChanges();
    ctrl.expectOne('/me').flush(me);
    ctrl.expectOne('/stations.json').flush({
      stations: [
        { id: 3, name: 'AMBIENT', genre: 'ambient', tracks: 96, listeners: 0 },
        { id: 7, name: 'NIGHT ROCK', genre: 'rock', tracks: 515, listeners: 0 },
      ],
    });
    fixture.detectChanges();
    return fixture;
  }

  it('shows the player, the dial and what is on air', () => {
    const el = mounted().nativeElement;
    expect(el.querySelector('[data-player]')).toBeTruthy();
    expect(el.querySelector('[data-dial]')).toBeTruthy();
    expect(el.querySelector('[data-nowplaying]')).toBeTruthy();
  });

  it('sends an expired session back to the login form', () => {
    // A listener whose session expired while the tab was open should meet the
    // login form rather than a dial that silently fails to load.
    const fixture = TestBed.createComponent(Listener);
    fixture.detectChanges();
    ctrl.expectOne('/me').flush(null, { status: 401, statusText: 'Unauthorized' });
    ctrl.expectOne('/stations.json').flush({ stations: [] });
    expect(navigate).toHaveBeenCalledWith(['/login']);
  });

  it('carries the tuned station to the player and the poll', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-station]').click();
    ctrl.expectOne('/tune').flush({ hls: '/hls/3/stream.m3u8', station_id: 3 });
    fixture.detectChanges();

    expect(fixture.componentInstance.stationId()).toBe(3);
    expect(fixture.componentInstance.hls()).toBe('/hls/3/stream.m3u8');
    // The now-playing poll follows the station that was picked.
    ctrl.expectOne('/now.json?station=3').flush({});
  });

  it('offers nothing to rate before a station is picked', () => {
    const fixture = mounted();
    expect(fixture.componentInstance.stationId()).toBeNull();
    expect(fixture.nativeElement.querySelector('[data-thumbsdown]')).toBeNull();
  });

  // THE FEATURE WAS BUILT AND NEVER CONNECTED. The transcript lives in the
  // component that polls /now.json; the button that rates it lives one
  // component sideways, reading a signal nothing ever wrote to. Unit-tested at
  // 100% against a directly-supplied input, and impossible to reach in the
  // running app.
  function tuned() {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-station]').click();
    ctrl.expectOne('/tune').flush({ hls: '/hls/3/stream.m3u8', station_id: 3 });
    fixture.detectChanges();
    return fixture;
  }

  it('rating a break offers nothing before a break has aired', () => {
    const fixture = tuned();
    ctrl.expectOne('/now.json?station=3').flush({ now: { artist: 'A', title: 'B' } });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-thumbsdown]')).toBeNull();
  });

  it('rating a break offers the thumbs-down once a break has aired', () => {
    const fixture = tuned();
    ctrl.expectOne('/now.json?station=3').flush({
      now: { artist: 'A', title: 'B' },
      last_break: { text: 'Three in the morning and that one still holds up.' },
    });
    fixture.detectChanges();

    const button = fixture.nativeElement.querySelector('[data-thumbsdown]');
    expect(button).toBeTruthy();
    button.click();
    const req = ctrl.expectOne('/feedback');
    expect(req.request.body).toEqual({ station_id: 3, verdict: 'down' });
    req.flush(null);
  });

  it('rating a break stops offering the old one after a station change', () => {
    // The thumbs-down is what makes this matter: rating what looks like the
    // previous station's break records it against the NEW station, because the
    // server attributes it by station id.
    const fixture = tuned();
    ctrl.expectOne('/now.json?station=3').flush({
      now: { artist: 'A', title: 'B' },
      last_break: { text: 'Three in the morning.' },
    });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-thumbsdown]')).toBeTruthy();

    // Tune ELSEWHERE -- a different station, which is the case that matters;
    // re-picking the same one is not a change and correctly resets nothing.
    fixture.nativeElement.querySelectorAll('[data-station]')[1].click();
    ctrl.expectOne('/tune').flush({ hls: '/hls/7/stream.m3u8', station_id: 7 });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-thumbsdown]')).toBeNull();
    ctrl.expectOne('/now.json?station=7').flush({});
  });
});
