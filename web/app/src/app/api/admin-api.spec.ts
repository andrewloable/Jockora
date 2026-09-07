import { describe, expect, it, beforeEach } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { AdminApi, Jock } from './api';

describe('AdminApi', () => {
  let api: AdminApi;
  let http: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    api = TestBed.inject(AdminApi);
    http = TestBed.inject(HttpTestingController);
  });

  // Every method, because each one is a URL a component would otherwise spell
  // out for itself -- and a URL spelled twice is a URL that changes once.
  const cases: Array<[string, () => void, string, string, unknown?]> = [
    ['overview', () => api.overview().subscribe(), 'GET', '/admin/overview.json'],
    ['cadence', () => api.setCadence(8).subscribe(), 'POST', '/admin/cadence', { cadence: 8 }],
    [
      'enriching',
      () => api.setEnriching(false).subscribe(),
      'POST',
      '/admin/enriching',
      { enriching: false },
    ],
    ['vocab', () => api.vocab().subscribe(), 'GET', '/admin/vocab'],
    ['voices', () => api.voices().subscribe(), 'GET', '/admin/voices'],
    ['sources', () => api.sources().subscribe(), 'GET', '/admin/sources'],
    [
      'add source',
      () => api.addSource({ kind: 'folder', locator: '/music' }).subscribe(),
      'POST',
      '/admin/sources',
      { kind: 'folder', locator: '/music' },
    ],
    ['remove source', () => api.removeSource(2).subscribe(), 'DELETE', '/admin/sources/2'],
    [
      'enable source',
      () => api.setSourceEnabled(2, true).subscribe(),
      'POST',
      '/admin/sources/2/enable',
    ],
    [
      'disable source',
      () => api.setSourceEnabled(2, false).subscribe(),
      'POST',
      '/admin/sources/2/disable',
    ],
    ['rescan', () => api.rescan().subscribe(), 'POST', '/admin/rescan'],
    ['stations', () => api.stations().subscribe(), 'GET', '/admin/stations'],
    [
      'add station',
      () => api.addStation({ name: 'ROCK', genre: 'rock' }).subscribe(),
      'POST',
      '/admin/stations',
      { name: 'ROCK', genre: 'rock' },
    ],
    ['remove station', () => api.removeStation(3).subscribe(), 'DELETE', '/admin/stations/3'],
    [
      'enable station',
      () => api.setStationEnabled(3, true).subscribe(),
      'POST',
      '/admin/stations/3/enable',
    ],
    [
      'disable station',
      () => api.setStationEnabled(3, false).subscribe(),
      'POST',
      '/admin/stations/3/disable',
    ],
    [
      'assign jock',
      () => api.assignJock(3, 'dutch').subscribe(),
      'PUT',
      '/admin/stations/3/jock',
      { jock_id: 'dutch' },
    ],
    [
      'unassign jock',
      () => api.assignJock(3, null).subscribe(),
      'PUT',
      '/admin/stations/3/jock',
      { jock_id: null },
    ],
    [
      'playlist',
      () => api.playlist(3, 50, 100).subscribe(),
      'GET',
      '/admin/stations/3/tracks?limit=50&offset=100',
    ],
    [
      'pin',
      () => api.flagTrack(3, 7, 'pin').subscribe(),
      'POST',
      '/admin/stations/3/tracks/7/pin',
    ],
    ['regenerate', () => api.regenerate(3).subscribe(), 'POST', '/admin/stations/3/regenerate'],
    ['jocks', () => api.jocks().subscribe(), 'GET', '/admin/jocks'],
    ['remove jock', () => api.removeJock('dutch').subscribe(), 'DELETE', '/admin/jocks/dutch'],
    ['users', () => api.users().subscribe(), 'GET', '/admin/users'],
    [
      'add user',
      () => api.addUser({ name: 'kim', password: 'p', role: 'listener' }).subscribe(),
      'POST',
      '/admin/users',
      { name: 'kim', password: 'p', role: 'listener' },
    ],
    [
      'disable user',
      () => api.setUserEnabled(4, false).subscribe(),
      'POST',
      '/admin/users/4/disable',
    ],
    ['enable user', () => api.setUserEnabled(4, true).subscribe(), 'POST', '/admin/users/4/enable'],
    ['remove user', () => api.removeUser(4).subscribe(), 'DELETE', '/admin/users/4'],
    [
      'reset password',
      () => api.resetPassword(4, 'a new passphrase').subscribe(),
      'POST',
      '/admin/users/4/password',
      { password: 'a new passphrase' },
    ],
  ];

  for (const [name, call, method, url, body] of cases) {
    it(name, () => {
      call();
      const req = http.expectOne(url);
      expect(req.request.method).toBe(method);
      if (body !== undefined) {
        expect(req.request.body).toEqual(body);
      }
      req.flush(null);
    });
  }

  const card: Jock = {
    id: 'dutch',
    name: 'Dutch',
    voice_id: 'am_fenrir',
    good_for_genres: ['rock'],
    good_for_moods: ['raw'],
    speech_style: 'LOUD',
    personality: 'loud',
    forbidden: [],
  };

  it('creates a jock with POST and edits one with PUT', () => {
    // A card is written WHOLE either way; the difference is only whether the
    // id already exists.
    api.saveJock(card, false).subscribe();
    expect(http.expectOne('/admin/jocks').request.method).toBe('POST');
    http.verify();

    api.saveJock(card, true).subscribe();
    expect(http.expectOne('/admin/jocks/dutch').request.method).toBe('PUT');
  });
});
