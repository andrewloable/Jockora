import { describe, expect, it, beforeEach } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Overview, flatten } from './overview.component';

describe('Overview', () => {
  let ctrl: HttpTestingController;

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [Overview],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  function mounted(overview: object = { library: { tracks: 7595, enriched: 412 } }) {
    const fixture = TestBed.createComponent(Overview);
    ctrl.expectOne('/admin/overview.json').flush(overview);
    fixture.detectChanges();
    return fixture;
  }

  it('renders the overview as rows', () => {
    const text = mounted().nativeElement.querySelector('[data-overview]').textContent;
    expect(text).toContain('library.tracks');
    expect(text).toContain('7595');
    expect(text).toContain('412');
  });

  it('sets the cadence and says when it takes effect', () => {
    const fixture = mounted();
    const input = fixture.nativeElement.querySelector('[data-cadence]');
    input.value = '8';
    input.dispatchEvent(new Event('input'));
    fixture.nativeElement.querySelector('[data-set-cadence]').click();

    const req = ctrl.expectOne('/admin/cadence');
    expect(req.request.body).toEqual({ cadence: 8 });
    req.flush(null);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('next break');
  });

  it('repeats the server’s reason for refusing a cadence', () => {
    // It refuses one outside its range and says why; repeating that is more
    // use than "failed".
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-set-cadence]').click();
    ctrl.expectOne('/admin/cadence').flush('cadence must be between 2 and 12', {
      status: 400,
      statusText: 'Bad Request',
    });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('between');

    // And a refusal with no message at all still says something.
    fixture.nativeElement.querySelector('[data-set-cadence]').click();
    ctrl.expectOne('/admin/cadence').flush(null, { status: 400, statusText: 'Bad Request' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Could not');
  });

  it('pauses and resumes enrichment', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-pause]').click();
    const paused = ctrl.expectOne('/admin/enriching');
    expect(paused.request.body).toEqual({ enriching: false });
    paused.flush(null);
    fixture.detectChanges();
    // Saying the stream is unaffected is the point: an operator pausing
    // enrichment is handing the machine back for an evening, not stopping the
    // station.
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('unaffected');

    fixture.nativeElement.querySelector('[data-resume]').click();
    const req = ctrl.expectOne('/admin/enriching');
    expect(req.request.body).toEqual({ enriching: true });
    req.flush(null);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('running');
  });

  it('says so when the overview or a toggle fails', () => {
    const fixture = TestBed.createComponent(Overview);
    ctrl.expectOne('/admin/overview.json').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Could not');

    fixture.nativeElement.querySelector('[data-pause]').click();
    ctrl.expectOne('/admin/enriching').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Could not');
  });

  it('flattens arrays and nulls without losing them', () => {
    // The overview grows fields over time; a renderer that dropped one would
    // hide it silently.
    expect(flatten({ a: { b: 1 }, c: [1, 2], d: null })).toEqual([
      { key: 'a.b', value: '1' },
      { key: 'c', value: '1,2' },
      { key: 'd', value: 'null' },
    ]);
  });
});
