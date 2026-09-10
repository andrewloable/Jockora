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

  it('keeps a guest on the dial instead of bouncing them to a login', () => {
    // The whole point of the operator's switch. /me answers role guest with a
    // 200, and a redirect here would make the setting do nothing in a browser.
    const el = mounted({ name: '', role: 'guest' }).nativeElement;
    expect(navigate).not.toHaveBeenCalled();
    expect(el.querySelector('[data-dial]')).toBeTruthy();
  });

  it('offers a guest a way in and no way out', () => {
    // A guest has no session to end, and the operator needs a route to the
    // console that is not typing a url.
    const el = mounted({ name: '', role: 'guest' }).nativeElement;
    expect(el.querySelector('[data-sign-out]')).toBeNull();
    expect(el.querySelector('[data-sign-in]').getAttribute('href')).toBe('/login');
  });

  it('hides the thumbs-down from a guest, because a verdict needs an account', () => {
    const guest = mounted({ name: '', role: 'guest' }).nativeElement;
    expect(guest.querySelector('app-feedback')).toBeNull();
    // And a signed-in listener still has it: the control is hidden by WHO is
    // listening, not removed.
    expect(mounted().nativeElement.querySelector('app-feedback')).toBeTruthy();
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

  // ------------------------------------------------------- Jockora-e9a.66 --
  //
  // The listener's device is the shared one by the product's own account: a
  // household tablet, or a phone handed to somebody else. Accounts are
  // admin-created, a listener cannot change their own password, and a session
  // lasts thirty days -- so whoever signed in stayed signed in.

  it('sign out is offered on the listener page as well as the console', async () => {
    const fixture = mounted();
    const out = fixture.nativeElement.querySelector('[data-sign-out]') as HTMLButtonElement;
    expect(out).not.toBeNull();
    expect(out.closest('[data-masthead]')).not.toBeNull();

    out.click();
    const req = ctrl.expectOne('/logout');
    expect(req.request.method).toBe('POST');
    req.flush(null);
    await Promise.resolve();
    fixture.detectChanges();

    expect(navigate).toHaveBeenCalledWith(['/login']);
  });

  it('sign out leaves the page even when the server refuses', async () => {
    const fixture = mounted();
    (fixture.nativeElement.querySelector('[data-sign-out]') as HTMLButtonElement).click();
    ctrl.expectOne('/logout').error(new ProgressEvent('failed'));
    await Promise.resolve();
    fixture.detectChanges();
    expect(navigate).toHaveBeenCalledWith(['/login']);
  });

  // Jockora-9xk. The helper being tested does not prove either route root CALLS
  // it -- removing the call from the component killed nothing until this
  // existed, which is the same wiring gap main.go's LevelVar test was written
  // for on the Go side.
  it('the listener points the browser tab at its own mark', () => {
    const link = document.createElement('link');
    link.rel = 'icon';
    link.href = 'placeholder.svg';
    document.head.appendChild(link);
    try {
      mounted();
      expect(link.getAttribute('href')).toBe('icon.svg');
    } finally {
      link.remove();
    }
  });
});
