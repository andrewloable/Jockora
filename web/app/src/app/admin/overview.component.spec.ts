import { describe, expect, it, beforeEach, vi } from 'vitest';
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

  it('keeps re-reading itself while it is open', () => {
    // Enrichment moves at about a track a minute, so a page that loads once
    // shows a number that never visibly changes -- which is what "enrichment
    // seems stuck" turned out to be.
    vi.useFakeTimers();
    try {
      const fixture = mounted();
      vi.advanceTimersByTime(10_000);
      ctrl.expectOne('/admin/overview.json').flush({ library: { tracks: 7696, enriched: 91 } });
      fixture.detectChanges();
      expect(fixture.nativeElement.querySelector('[data-overview]').textContent).toContain('91');

      vi.advanceTimersByTime(10_000);
      ctrl.expectOne('/admin/overview.json').flush({ library: { tracks: 7696, enriched: 92 } });
      fixture.detectChanges();
      expect(fixture.nativeElement.querySelector('[data-overview]').textContent).toContain('92');
    } finally {
      vi.useRealTimers();
    }
  });

  it('can be closed twice without complaining', () => {
    // Angular calls ngOnDestroy once, but a component that only tolerates one
    // call is a trap for whoever wires it into something else later.
    const fixture = mounted();
    fixture.componentInstance.ngOnDestroy();
    fixture.componentInstance.ngOnDestroy();
    ctrl.verify();
  });

  it('stops polling once the section is closed', () => {
    // The console mounts one section at a time; a timer left running would
    // poll for a page nobody is looking at.
    vi.useFakeTimers();
    try {
      const fixture = mounted();
      fixture.destroy();
      vi.advanceTimersByTime(60_000);
      ctrl.verify();
    } finally {
      vi.useRealTimers();
    }
  });

  describe('enrichment progress', () => {
    // Reported as "enrichment does not show any progress" while it WAS
    // progressing: the numbers were two rows in a flat table, and a job that
    // takes days at a track a minute needs a shape, not a pair of integers.
    function withOverview(o: Record<string, unknown>) {
      const fixture = TestBed.createComponent(Overview);
      ctrl.expectOne('/admin/overview.json').flush(o);
      fixture.detectChanges();
      return fixture;
    }

    it('shows how far along it is, and how long is left', () => {
      const fixture = withOverview({
        enriching: true,
        library: { tracks: 7696, enriched: 130 },
        enrichment_cost: { seconds_per_track: 40.6 },
      });
      const line = fixture.nativeElement.querySelector('[data-enrich-line]').textContent;
      expect(line).toContain('130');
      expect(line).toContain('7,696');
      expect(line).toContain('1.7%');
      expect(line).toContain('running');

      const eta = fixture.nativeElement.querySelector('[data-enrich-eta]').textContent;
      expect(eta).toContain('7,566 to go');
      expect(eta).toContain('4 days');
      expect(eta).toContain('41s per track');

      const bar = fixture.nativeElement.querySelector('progress');
      expect(bar.value).toBe(130);
      expect(bar.max).toBe(7696);
    });

    it('says hours when it is hours, minutes when it is minutes', () => {
      const hours = withOverview({
        enriching: true,
        library: { tracks: 1000, enriched: 100 },
        enrichment_cost: { seconds_per_track: 40 },
      });
      expect(hours.nativeElement.querySelector('[data-enrich-eta]').textContent).toContain('10 hours');

      const mins = withOverview({
        enriching: true,
        library: { tracks: 100, enriched: 95 },
        enrichment_cost: { seconds_per_track: 40 },
      });
      expect(mins.nativeElement.querySelector('[data-enrich-eta]').textContent).toContain('3 minutes');
    });

    it('does not promise a time when it is paused or the rate is unknown', () => {
      // A paused job has no rate to project from, and saying "4 days" about
      // something that is not running would be a lie.
      const paused = withOverview({
        enriching: false,
        library: { tracks: 1000, enriched: 100 },
        enrichment_cost: { seconds_per_track: 40 },
      });
      expect(paused.nativeElement.querySelector('[data-enrich-line]').textContent).toContain('paused');
      const eta = paused.nativeElement.querySelector('[data-enrich-eta]').textContent;
      expect(eta).toContain('900 to go');
      expect(eta).not.toContain('hours');

      const unknown = withOverview({ enriching: true, library: { tracks: 10, enriched: 1 } });
      expect(unknown.nativeElement.querySelector('[data-enrich-eta]').textContent).toContain('9 to go');
    });

    it('says so when every track has a dossier', () => {
      const fixture = withOverview({
        enriching: true,
        library: { tracks: 500, enriched: 500 },
        enrichment_cost: { seconds_per_track: 40 },
      });
      expect(fixture.nativeElement.querySelector('[data-enrich-eta]').textContent).toContain(
        'Every track has a dossier',
      );
    });

    it('treats a library with no enriched count as none done', () => {
      const fixture = withOverview({ enriching: true, library: { tracks: 200 } });
      expect(fixture.nativeElement.querySelector('[data-enrich-line]').textContent).toContain('0 of 200');
    });

    it('says nothing has been scanned rather than dividing by zero', () => {
      const fixture = withOverview({ enriching: true, library: { tracks: 0, enriched: 0 } });
      expect(fixture.nativeElement.querySelector('[data-enrich-progress]').textContent).toContain(
        'Nothing scanned yet',
      );
      expect(fixture.nativeElement.querySelector('progress')).toBeNull();
    });

    it('survives an overview with no library block at all', () => {
      const fixture = withOverview({ enriching: true });
      expect(fixture.nativeElement.querySelector('[data-enrich-progress]').textContent).toContain(
        'Nothing scanned yet',
      );
    });
  });
});
