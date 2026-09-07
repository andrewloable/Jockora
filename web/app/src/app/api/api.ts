import { HttpClient } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { Observable } from 'rxjs';

/** One position on the dial, as /stations.json reports it. */
export interface DialStation {
  /** False while the station is still filling up as enrichment runs. */
  ready?: boolean;
  preparing?: string;
  id: number;
  name: string;
  genre: string;
  mood?: string;
  jock_name?: string;
  tracks: number;
  listeners: number;
}

/** What /now.json says about one station. */
export interface NowPlaying {
  now?: { artist: string; title: string } | null;
  last_break?: { text: string; aired_at: number; placement: string } | null;
  enrichment?: { done: number; total: number; pct: number } | null;
}

export interface Source {
  id: number;
  kind: string;
  locator: string;
  username?: string;
  enabled: boolean;
}

export interface Station {
  id: number;
  name: string;
  genre: string;
  mood?: string;
  jock_id?: string;
  enabled: boolean;
  tracks: number;
  warning?: string;
}

export interface Jock {
  id: string;
  name: string;
  voice_id: string;
  good_for_genres: string[];
  good_for_moods: string[];
  speech_style: string;
  personality: string;
  forbidden: string[];
}

export interface User {
  id: number;
  name: string;
  role: string;
  disabled: boolean;
}

export interface PlaylistTrack {
  track_id: number;
  artist: string;
  title: string;
  pinned: boolean;
  excluded: boolean;
  missing: boolean;
}

export interface Diff {
  added: number;
  removed: number;
  kept: number;
}

export interface Me {
  name: string;
  role: string;
}

/**
 * The whole server API, in one place.
 *
 * Typed methods rather than raw URLs at every call site: an endpoint that
 * changes shape then breaks the build here, once, instead of at runtime in
 * whichever component happened to call it.
 */
@Injectable({ providedIn: 'root' })
export class Api {
  private readonly http = inject(HttpClient);

  login(name: string, password: string): Observable<void> {
    return this.http.post<void>('/login', { name, password });
  }

  logout(): Observable<void> {
    return this.http.post<void>('/logout', {});
  }

  me(): Observable<Me> {
    return this.http.get<Me>('/me');
  }

  dial(): Observable<{ stations: DialStation[] }> {
    return this.http.get<{ stations: DialStation[] }>('/stations.json');
  }

  /** Tuning is the first heartbeat: the station starts because somebody tuned. */
  tune(stationId: number): Observable<{ hls: string; station_id: number }> {
    return this.http.post<{ hls: string; station_id: number }>('/tune', {
      station_id: stationId,
    });
  }

  now(stationId: number): Observable<NowPlaying> {
    return this.http.get<NowPlaying>(`/now.json?station=${stationId}`);
  }

  feedback(stationId: number, verdict: string): Observable<void> {
    return this.http.post<void>('/feedback', { station_id: stationId, verdict });
  }
}

/**
 * The operator's half of the API.
 *
 * A second service rather than more methods on Api, so the listener bundle
 * never imports the console's calls: the console is a lazy route, and a shared
 * service would pull its surface into everyone's first load.
 */
@Injectable({ providedIn: 'root' })
export class AdminApi {
  private readonly http = inject(HttpClient);

  overview(): Observable<Record<string, unknown>> {
    return this.http.get<Record<string, unknown>>('/admin/overview.json');
  }

  setCadence(n: number): Observable<void> {
    return this.http.post<void>('/admin/cadence', { cadence: n });
  }

  setEnriching(on: boolean): Observable<void> {
    return this.http.post<void>('/admin/enriching', { enriching: on });
  }

  vocab(): Observable<{ genres: string[]; moods: string[] }> {
    return this.http.get<{ genres: string[]; moods: string[] }>('/admin/vocab');
  }

  voices(): Observable<{ voices: string[] }> {
    return this.http.get<{ voices: string[] }>('/admin/voices');
  }

  /** One line spoken in a voice, so a name like "am_fenrir" can be heard. */
  previewVoice(voice: string): Observable<Blob> {
    return this.http.post('/admin/voices/preview', { voice }, { responseType: 'blob' });
  }

  sources(): Observable<Source[]> {
    return this.http.get<Source[]>('/admin/sources');
  }

  addSource(body: {
    kind: string;
    locator: string;
    username?: string;
    password?: string;
  }): Observable<{ id: number }> {
    return this.http.post<{ id: number }>('/admin/sources', body);
  }

  removeSource(id: number): Observable<void> {
    return this.http.delete<void>(`/admin/sources/${id}`);
  }

  setSourceEnabled(id: number, on: boolean): Observable<void> {
    return this.http.post<void>(`/admin/sources/${id}/${on ? 'enable' : 'disable'}`, {});
  }

  rescan(): Observable<unknown> {
    return this.http.post<unknown>('/admin/rescan', {});
  }

  stations(): Observable<Station[]> {
    return this.http.get<Station[]>('/admin/stations');
  }

  addStation(body: { name: string; genre: string; mood?: string }): Observable<{
    id: number;
    tracks: number;
  }> {
    return this.http.post<{ id: number; tracks: number }>('/admin/stations', body);
  }

  removeStation(id: number): Observable<void> {
    return this.http.delete<void>(`/admin/stations/${id}`);
  }

  setStationEnabled(id: number, on: boolean): Observable<{ tracks: number; warning?: string }> {
    return this.http.post<{ tracks: number; warning?: string }>(
      `/admin/stations/${id}/${on ? 'enable' : 'disable'}`,
      {},
    );
  }

  assignJock(id: number, jockId: string | null): Observable<void> {
    return this.http.put<void>(`/admin/stations/${id}/jock`, { jock_id: jockId });
  }

  playlist(
    id: number,
    limit: number,
    offset: number,
  ): Observable<{ tracks: PlaylistTrack[]; total: number }> {
    return this.http.get<{ tracks: PlaylistTrack[]; total: number }>(
      `/admin/stations/${id}/tracks?limit=${limit}&offset=${offset}`,
    );
  }

  flagTrack(stationId: number, trackId: number, action: string): Observable<void> {
    return this.http.post<void>(`/admin/stations/${stationId}/tracks/${trackId}/${action}`, {});
  }

  regenerate(id: number): Observable<Diff> {
    return this.http.post<Diff>(`/admin/stations/${id}/regenerate`, {});
  }

  jocks(): Observable<Jock[]> {
    return this.http.get<Jock[]>('/admin/jocks');
  }

  saveJock(jock: Jock, existing: boolean): Observable<unknown> {
    return existing
      ? this.http.put<unknown>(`/admin/jocks/${jock.id}`, jock)
      : this.http.post<unknown>('/admin/jocks', jock);
  }

  removeJock(id: string): Observable<{ unassigned: number[] }> {
    return this.http.delete<{ unassigned: number[] }>(`/admin/jocks/${id}`);
  }

  users(): Observable<User[]> {
    return this.http.get<User[]>('/admin/users');
  }

  addUser(body: { name: string; password: string; role: string }): Observable<{ id: number }> {
    return this.http.post<{ id: number }>('/admin/users', body);
  }

  setUserEnabled(id: number, on: boolean): Observable<void> {
    return this.http.post<void>(`/admin/users/${id}/${on ? 'enable' : 'disable'}`, {});
  }

  removeUser(id: number): Observable<void> {
    return this.http.delete<void>(`/admin/users/${id}`);
  }

  resetPassword(id: number, password: string): Observable<void> {
    return this.http.post<void>(`/admin/users/${id}/password`, { password });
  }
}
