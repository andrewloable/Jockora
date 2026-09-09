import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Sources, SCAN_POLL_MS } from './sources.component';

const sources = [
  { id: 1, kind: 'folder', locator: '/music', enabled: true },
  { id: 2, kind: 'subsonic', locator: 'http://nas:4533', username: 'andrew', enabled: false },
];

describe('Sources', () => {
  let ctrl: HttpTestingController;

  beforeEach(async () => {
    // DEFAULT: yes. Destructive actions ask now, and every test that was
    // written before they did is still testing what happens after the answer.
    // The tests about the QUESTION stub it themselves.
    vi.stubGlobal('confirm', () => true);
    vi.useFakeTimers();
    await TestBed.configureTestingModule({
      imports: [Sources],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  function mounted() {
    const fixture = TestBed.createComponent(Sources);
    ctrl.expectOne('/admin/sources').flush(sources);
    fixture.detectChanges();
    // THE FORM IS BEHIND A BUTTON since Jockora-e9a.60 moved it into a dialog.
    (fixture.nativeElement.querySelector('[data-add-open]') as HTMLButtonElement).click();
    fixture.detectChanges();
    return fixture;
  }

  function type(fixture: { nativeElement: HTMLElement }, selector: string, value: string) {
    const el = fixture.nativeElement.querySelector(selector) as HTMLInputElement;
    el.value = value;
    el.dispatchEvent(new Event(el.tagName === 'SELECT' ? 'change' : 'input'));
  }

  // ------------------------------------------------------ console states --
  //
  // Jockora-e9a.50. The install this console was reviewed on has 7,595 tracks,
  // 9 jocks and a station, so every screen was only ever seen full. A fresh
  // install reaches NONE of that, which is why these are specified rather than
  // discovered.
  //
  // The one that matters is the difference between LOADING and EMPTY: both
  // rendered as a table with no rows, so a slow first paint told a new
  // operator their library was empty and left them nothing to do about it.

  it('console states tells a fresh operator what to do when there are no sources', () => {
    const fixture = TestBed.createComponent(Sources);
    ctrl.expectOne('/admin/sources').flush([]);
    fixture.detectChanges();
    const empty = fixture.nativeElement.querySelector('[data-empty]');
    expect(empty).not.toBeNull();
    // It names the control that fixes it, not just the absence.
    expect(empty.textContent).toContain('Add a source');
  });

  it('console states does not call a loading table an empty library', () => {
    const fixture = TestBed.createComponent(Sources);
    fixture.detectChanges();
    // The answer has not arrived. Nothing here knows the library is empty.
    expect(fixture.nativeElement.querySelector('[data-empty]')).toBeNull();
    expect(fixture.nativeElement.querySelector('[data-loading]')).not.toBeNull();
    ctrl.expectOne('/admin/sources').flush([]);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-loading]')).toBeNull();
    expect(fixture.nativeElement.querySelector('[data-empty]')).not.toBeNull();
  });

  it('console states says so when a request fails', () => {
    const fixture = TestBed.createComponent(Sources);
    ctrl.expectOne('/admin/sources').flush(null, { status: 500, statusText: 'Server Error' });
    fixture.detectChanges();
    // A failed read is not an empty library either.
    expect(fixture.nativeElement.querySelector('[data-empty]')).toBeNull();
    expect(fixture.nativeElement.querySelector('[data-loading]')).toBeNull();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Could not');
  });

  it('lists the sources without their passwords', () => {
    const text = mounted().nativeElement.querySelector('[data-sources]').textContent;
    expect(text).toContain('/music');
    expect(text).toContain('andrew');
    expect(text).toContain('off');
    expect(text).not.toContain('hunter2');
  });

  it('adds a folder', () => {
    const fixture = mounted();
    type(fixture, '[data-locator]', '/more-music');
    fixture.nativeElement.querySelector('[data-add]').click();

    const req = ctrl.expectOne('/admin/sources');
    expect(req.request.body.kind).toBe('folder');
    expect(req.request.body.locator).toBe('/more-music');
    req.flush({ id: 3 });
    ctrl.expectOne('/admin/sources').flush(sources);
  });

  it('asks for credentials only for a subsonic source', () => {
    const fixture = mounted();
    expect(fixture.nativeElement.querySelector('[data-password]')).toBeNull();

    type(fixture, '[data-kind]', 'subsonic');
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-password]')).toBeTruthy();

    type(fixture, '[data-locator]', 'http://nas:4533');
    type(fixture, '[data-username]', 'andrew');
    type(fixture, '[data-password]', 'hunter2');
    fixture.nativeElement.querySelector('[data-add]').click();

    const req = ctrl.expectOne('/admin/sources');
    expect(req.request.body).toEqual({
      kind: 'subsonic',
      locator: 'http://nas:4533',
      username: 'andrew',
      password: 'hunter2',
    });
    req.flush({ id: 3 });
    ctrl.expectOne('/admin/sources').flush(sources);
  });

  it('shows the server’s reason for refusing a source', () => {
    // It stats the folder and pings the server; its refusal says which one
    // failed and why, and repeating that is the whole value of validating at
    // create time.
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-add]').click();
    ctrl
      .expectOne('/admin/sources')
      .flush(
        { field: 'locator', error: 'that folder cannot be read: no such file' },
        { status: 400, statusText: 'Bad Request' },
      );
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
      'cannot be read',
    );
  });

  it('says the tracks survive a removed source', () => {
    // They are marked missing and keep their dossiers -- minutes of model time
    // each -- and an operator who does not know that will not add it back.
    const fixture = mounted();
    fixture.nativeElement.querySelectorAll('[data-remove]')[0].click();
    ctrl.expectOne('/admin/sources/1').flush(null);
    ctrl.expectOne('/admin/sources').flush(sources);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('kept');
  });

  it('enables and disables', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelectorAll('[data-toggle]')[0].click();
    ctrl.expectOne('/admin/sources/1/disable').flush(null);
    ctrl.expectOne('/admin/sources').flush(sources);

    fixture.nativeElement.querySelectorAll('[data-toggle]')[1].click();
    ctrl.expectOne('/admin/sources/2/enable').flush(null);
    ctrl.expectOne('/admin/sources').flush(sources);
  });

  it('starts a rescan and shows its progress', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-rescan]').click();
    ctrl.expectOne('/admin/rescan').flush({ running: true });
    ctrl
      .expectOne('/now.json?station=0')
      .flush({ rescan: { running: true, found: 900, skipped: 12, missing: 3 } });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-progress]').textContent).toContain('900');
  });

  it('says a scan is already running rather than failing', () => {
    // Two walks at once is a race with no correct answer, and the operator
    // should be told which of the two things happened.
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-rescan]').click();
    ctrl.expectOne('/admin/rescan').flush(null, { status: 409, statusText: 'Conflict' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
      'already running',
    );
  });

  it('says so when anything else fails', () => {
    const fixture = TestBed.createComponent(Sources);
    ctrl.expectOne('/admin/sources').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Could not');

    fixture.nativeElement.querySelector('[data-rescan]').click();
    ctrl.expectOne('/admin/rescan').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
      'Could not start',
    );
  });

  it('says so when a remove or a toggle fails', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelectorAll('[data-remove]')[0].click();
    ctrl.expectOne('/admin/sources/1').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Could not');

    fixture.nativeElement.querySelectorAll('[data-toggle]')[0].click();
    ctrl.expectOne('/admin/sources/1/disable').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Could not');
  });

  it('shows a finished scan and a missing progress block', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-rescan]').click();
    ctrl.expectOne('/admin/rescan').flush({});
    ctrl.expectOne('/now.json?station=0').flush({
      rescan: { running: false, found: 7595, skipped: 7500, missing: 2 },
    });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-progress]').textContent).toContain(
      'Last scan',
    );

    // A server that reports no rescan block at all has never run one.
    fixture.componentInstance.pollProgress();
    ctrl.expectOne('/now.json?station=0').flush({});
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-progress]')).toBeNull();
  });

  it('shows the server’s refusal when it has no message', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-add]').click();
    ctrl.expectOne('/admin/sources').flush(null, { status: 400, statusText: 'Bad Request' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Could not');
  });

  it('leaves the progress line alone when the poll fails', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-rescan]').click();
    ctrl.expectOne('/admin/rescan').flush({});
    ctrl.expectOne('/now.json?station=0').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-progress]')).toBeNull();
  });

  it('destructive sources asks before removing', () => {
    const fixture = mounted();
    const confirmed: string[] = [];
    vi.stubGlobal('confirm', (m: string) => {
      confirmed.push(m);
      return false;
    });
    fixture.nativeElement.querySelector('[data-remove]').click();
    expect(confirmed.length).toBe(1);
    // It says what is NOT destroyed: the tracks are kept and marked missing.
    expect(confirmed[0].toLowerCase()).toContain('kept');
    vi.unstubAllGlobals();
  });

  it('destructive sources marks the remove button as destructive', () => {
    const fixture = mounted();
    expect(
      fixture.nativeElement.querySelector('[data-remove]').getAttribute('data-danger'),
    ).not.toBeNull();
  });

  // ------------------------------------------------------- Jockora-e9a.64 --
  //
  // pollProgress called the endpoint EXACTLY ONCE. Its comment said progress
  // rides on now.json "which the page already polls elsewhere" -- but that
  // polling is on the LISTENER page, not this one. So Rescan said "Scanning.
  // Progress appears below", one snapshot of the count appeared, and it never
  // moved again. On a large library the operator watches a frozen number and
  // cannot tell a running scan from a finished one.

  it('stale view follows a rescan to completion and then stops', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-rescan]').click();
    ctrl.expectOne('/admin/rescan').flush({});
    ctrl.expectOne((r) => r.url.includes('/now.json')).flush({
      rescan: { running: true, found: 10, skipped: 0, missing: 0 },
    });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-progress]').textContent).toContain('10');

    // IT MOVES.
    vi.advanceTimersByTime(SCAN_POLL_MS);
    ctrl.expectOne((r) => r.url.includes('/now.json')).flush({
      rescan: { running: true, found: 240, skipped: 2, missing: 0 },
    });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-progress]').textContent).toContain('240');

    // AND IT STOPS when the scan does, rather than polling a finished answer
    // for as long as the tab is open.
    vi.advanceTimersByTime(SCAN_POLL_MS);
    ctrl.expectOne((r) => r.url.includes('/now.json')).flush({
      rescan: { running: false, found: 512, skipped: 3, missing: 1 },
    });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-progress]').textContent).toContain(
      'Last scan',
    );

    vi.advanceTimersByTime(SCAN_POLL_MS * 3);
    ctrl.expectNone((r) => r.url.includes('/now.json'));
  });

  it('stale view asks nothing while no scan is running', () => {
    // An idle scanner should make no requests at all.
    mounted();
    vi.advanceTimersByTime(SCAN_POLL_MS * 3);
    ctrl.expectNone((r) => r.url.includes('/now.json'));
  });

  it('stale view leaves no timer behind when the page goes away', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-rescan]').click();
    ctrl.expectOne('/admin/rescan').flush({});
    ctrl.expectOne((r) => r.url.includes('/now.json')).flush({
      rescan: { running: true, found: 10, skipped: 0, missing: 0 },
    });
    fixture.detectChanges();

    fixture.destroy();
    vi.advanceTimersByTime(SCAN_POLL_MS * 3);
    ctrl.expectNone((r) => r.url.includes('/now.json'));
  });

  it('edit dialog dismissing the source form takes its credentials with it', () => {
    const fixture = mounted();
    const set = (sel: string, v: string) => {
      const el = fixture.nativeElement.querySelector(sel) as HTMLInputElement | HTMLSelectElement;
      el.value = v;
      el.dispatchEvent(new Event(el.tagName === 'SELECT' ? 'change' : 'input'));
      fixture.detectChanges();
    };
    set('[data-kind]', 'subsonic');
    set('[data-locator]', 'http://nas:4533');
    set('[data-username]', 'andrew');
    set('[data-password]', 'a-real-credential');

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-source-form]')).toBeNull();

    // A CREDENTIAL LEFT IN A CLOSED FORM is a credential nobody can see and
    // nobody meant to keep.
    expect(fixture.componentInstance.password()).toBe('');
    expect(fixture.componentInstance.locator()).toBe('');
    expect(fixture.componentInstance.username()).toBe('');
  });
});
