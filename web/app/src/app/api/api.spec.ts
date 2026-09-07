import { describe, expect, it, beforeEach } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Api } from './api';

describe('Api', () => {
  let api: Api;
  let http: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    api = TestBed.inject(Api);
    http = TestBed.inject(HttpTestingController);
  });

  it('signs in with a name and password', () => {
    api.login('andrew', 'correct horse battery').subscribe();
    const req = http.expectOne('/login');
    expect(req.request.method).toBe('POST');
    expect(req.request.body).toEqual({ name: 'andrew', password: 'correct horse battery' });
    req.flush(null);
  });

  it('signs out', () => {
    api.logout().subscribe();
    const req = http.expectOne('/logout');
    expect(req.request.method).toBe('POST');
    req.flush(null);
  });

  it('asks who it is', () => {
    let me: unknown;
    api.me().subscribe((v) => (me = v));
    http.expectOne('/me').flush({ name: 'andrew', role: 'listener' });
    expect(me).toEqual({ name: 'andrew', role: 'listener' });
  });

  it('reads the dial', () => {
    let stations: unknown;
    api.dial().subscribe((v) => (stations = v.stations));
    http.expectOne('/stations.json').flush({ stations: [{ id: 1, name: 'ROCK' }] });
    expect(stations).toEqual([{ id: 1, name: 'ROCK' }]);
  });

  it('tunes by station id', () => {
    // The station id, never a tag: 13g made the dial a table the operator owns.
    let answer: unknown;
    api.tune(3).subscribe((v) => (answer = v));
    const req = http.expectOne('/tune');
    expect(req.request.body).toEqual({ station_id: 3 });
    req.flush({ hls: '/hls/3/stream.m3u8', station_id: 3 });
    expect(answer).toEqual({ hls: '/hls/3/stream.m3u8', station_id: 3 });
  });

  it('asks what one station is playing', () => {
    // Per station: with several running there is no global now-playing.
    api.now(3).subscribe();
    expect(http.expectOne('/now.json?station=3').request.method).toBe('GET');
  });

  it('sends attributed feedback', () => {
    api.feedback(3, 'down').subscribe();
    const req = http.expectOne('/feedback');
    expect(req.request.body).toEqual({ station_id: 3, verdict: 'down' });
    req.flush(null);
  });
});
