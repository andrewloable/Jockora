import { describe, expect, it, beforeEach, vi, afterEach } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Admin } from './admin';
import { adminRoutes } from './admin.routes';
import { adminGuard } from './admin.guard';

describe('Admin', () => {
  let ctrl: HttpTestingController;

  // The Logs section opens a live connection on mount, and the real
  // EventSource would reach for a socket.
  beforeEach(() => {
    vi.stubGlobal(
      'EventSource',
      class {
        close() {}
        readyState = 0;
      },
    );
  });
  afterEach(() => vi.unstubAllGlobals());

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [Admin],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  // Whatever the section that just mounted asked for. The shell's job is to
  // mount one section; what each of them fetches is its own spec's problem.
  function settle(fixture: { detectChanges(): void }) {
    for (const req of ctrl.match(() => true)) {
      const url = req.request.url;
      req.flush(
        url === '/me'
          ? { name: 'mandark', role: 'admin' }
          : url === '/admin/vocab'
            ? { genres: ['rock'], moods: [] }
            : url === '/admin/voices'
              ? { voices: [] }
              : url.includes('/tracks')
                ? { tracks: [], total: 0 }
                : url === '/admin/overview.json'
                  ? {}
                  : url === '/admin/stations'
                    ? [{ id: 1, name: 'Rock', genre: 'rock', enabled: true, tracks: 90 }]
                    : url === '/admin/logs'
                      ? { records: [], dropped: 0, level: 'INFO' }
                      : url === '/admin/llm'
                        ? { providers: [], current: { has_key: false }, health: 'ok' }
                        : [],
      );
    }
    fixture.detectChanges();
  }

  function mounted() {
    const fixture = TestBed.createComponent(Admin);
    fixture.detectChanges();
    settle(fixture);
    return fixture;
  }

  it('opens on the overview', () => {
    const fixture = mounted();
    expect(fixture.componentInstance.showing()).toBe('overview');
    expect(fixture.nativeElement.querySelector('app-overview')).not.toBeNull();
  });

  it('offers every section', () => {
    const nav = mounted().nativeElement.querySelectorAll('nav button');
    expect(Array.from(nav).map((b) => (b as HTMLElement).textContent?.trim())).toEqual([
      'overview',
      'sources',
      'stations',
      'playlist',
      'jocks',
      'ads',
      'accounts',
      'model',
      'logs',
    ]);
  });

  it('mounts one section at a time', () => {
    // NINE sections polling the server at once for pages nobody is looking at
    // is a load the operator did not ask for -- and Logs holds an open
    // EventSource, so one per section would be nine live connections against
    // a server that caps them at eight.
    const fixture = mounted();
    for (const [section, tag] of [
      ['sources', 'app-sources'],
      ['stations', 'app-stations'],
      ['playlist', 'app-playlist'],
      ['jocks', 'app-jocks'],
      ['ads', 'app-ads'],
      ['accounts', 'app-users'],
      ['model', 'app-llm'],
      ['logs', 'app-logs'],
    ] as const) {
      fixture.nativeElement.querySelector(`[data-section="${section}"]`).click();
      fixture.detectChanges();
      settle(fixture);
      // The playlist editor needs a station picked before it mounts at all.
      settle(fixture);
      expect(fixture.nativeElement.querySelector(tag)).not.toBeNull();
      expect(fixture.nativeElement.querySelectorAll('app-overview').length).toBe(0);
    }
  });

  it('picks a station for the playlist editor, and lets it be changed', () => {
    // The editor edits ONE station, so the console has to say which.
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-section="playlist"]').click();
    fixture.detectChanges();
    ctrl.expectOne('/admin/stations').flush([
      { id: 1, name: 'Rock', genre: 'rock', enabled: true, tracks: 90 },
      { id: 7, name: 'Calm', genre: 'ambient', enabled: true, tracks: 60 },
    ]);
    fixture.detectChanges();
    expect(fixture.componentInstance.picked()).toBe(1);
    settle(fixture);

    const select = fixture.nativeElement.querySelector('[data-pick-station]');
    expect(Array.from(select.options).map((o) => (o as HTMLOptionElement).value)).toEqual([
      '1',
      '7',
    ]);
    select.value = '7';
    select.dispatchEvent(new Event('change'));
    fixture.detectChanges();
    expect(fixture.componentInstance.picked()).toBe(7);
    settle(fixture);
  });

  it('says so rather than showing an empty editor', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-section="playlist"]').click();
    fixture.detectChanges();
    ctrl.expectOne('/admin/stations').flush([]);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('app-playlist')).toBeNull();
    expect(fixture.nativeElement.querySelector('[data-no-stations]').textContent).toContain(
      'No stations yet',
    );
  });

  it('shows nothing to edit when the stations cannot be read', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-section="playlist"]').click();
    fixture.detectChanges();
    ctrl.expectOne('/admin/stations').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('app-playlist')).toBeNull();
  });

  it('opens the playlist ON the station whose row was clicked', () => {
    // The Playlist button on a station row emits an output the shell never
    // bound, so it did nothing at all. And when it does something, it has to
    // land on THAT station -- not on whichever happens to be first.
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-section="stations"]').click();
    fixture.detectChanges();
    settle(fixture);

    // Clicked for real, so the output BINDING is exercised too -- it was the
    // binding that was missing, not the handler.
    const row = fixture.nativeElement.querySelector('[data-playlist]');
    expect(row).not.toBeNull();
    row.click();
    fixture.detectChanges();
    ctrl.expectOne('/admin/stations').flush([
      { id: 1, name: 'Rock', genre: 'rock', enabled: true, tracks: 90 },
      { id: 7, name: 'Calm', genre: 'ambient', enabled: true, tracks: 60 },
    ]);
    fixture.detectChanges();

    expect(fixture.componentInstance.showing()).toBe('playlist');
    // The station whose row was clicked, not whichever is first.
    expect(fixture.componentInstance.picked()).toBe(1);
    settle(fixture);
  });

  it('marks the open section for a screen reader', () => {
    const fixture = mounted();
    expect(
      fixture.nativeElement.querySelector('[data-section="overview"]').getAttribute('aria-current'),
    ).toBe('page');
    expect(
      fixture.nativeElement.querySelector('[data-section="jocks"]').getAttribute('aria-current'),
    ).toBeNull();
  });

  it('is guarded', () => {
    // Without this the console renders for a listener for as long as it takes
    // the redirect to happen, and every one of its calls 403s in the console.
    expect(adminRoutes[0].path).toBe('');
    expect(adminRoutes[0].component).toBe(Admin);
    expect(adminRoutes[0].canActivate).toEqual([adminGuard]);
  });

  it('says who is signed in, so the title is not read as a name', () => {
    // Reported as "i logged in as mandark but the admin page shows operator":
    // the title said "Jockora — operator", meaning the operator CONSOLE, and a
    // page that never named the account left that as the only candidate.
    const fixture = mounted();
    const who = fixture.nativeElement.querySelector('[data-whoami]').textContent;
    expect(who).toContain('mandark');
    expect(who).toContain('admin');
    expect(fixture.nativeElement.querySelector('h1').textContent).toContain('console');
  });

  it('names nobody when the session has gone', () => {
    const fixture = TestBed.createComponent(Admin);
    fixture.detectChanges();
    ctrl.expectOne('/me').flush(null, { status: 401, statusText: 'Unauthorized' });
    for (const req of ctrl.match(() => true)) {
      req.flush({});
    }
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-whoami]')).toBeNull();
  });

  it('opens the model page', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-section="model"]').click();
    fixture.detectChanges();
    ctrl.expectOne('/admin/llm').flush({ providers: [], current: { has_key: false } });
    fixture.detectChanges();
    expect(fixture.nativeElement.textContent).toContain('Language model');
  });
});
