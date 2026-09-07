import { describe, expect, it, beforeEach } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Sources } from './sources.component';

const sources = [
  { id: 1, kind: 'folder', locator: '/music', enabled: true },
  { id: 2, kind: 'subsonic', locator: 'http://nas:4533', username: 'andrew', enabled: false },
];

describe('Sources', () => {
  let ctrl: HttpTestingController;

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [Sources],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  function mounted() {
    const fixture = TestBed.createComponent(Sources);
    ctrl.expectOne('/admin/sources').flush(sources);
    fixture.detectChanges();
    return fixture;
  }

  function type(fixture: { nativeElement: HTMLElement }, selector: string, value: string) {
    const el = fixture.nativeElement.querySelector(selector) as HTMLInputElement;
    el.value = value;
    el.dispatchEvent(new Event(el.tagName === 'SELECT' ? 'change' : 'input'));
  }

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
    ctrl.expectOne('/admin/sources').flush(
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
});
