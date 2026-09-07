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
      stations: [{ id: 3, name: 'AMBIENT', genre: 'ambient', tracks: 96, listeners: 0 }],
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
});
