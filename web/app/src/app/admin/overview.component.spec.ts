import { describe, expect, it, beforeEach, vi } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Overview, compact, duration, flatten } from './overview.component';

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

  // ------------------------------------------------------- model outage --
  //
  // 2026-09-08: a Cloudflare token stopped being accepted at 02:58. The station
  // dropped nine consecutive breaks and enrichment died, and the ONLY place any
  // of it appeared was the container log. /now.json reported "llm": "ok"
  // throughout. This is the operator's front page; it is where they look.

  it('model outage shows the operator that the model is refusing, in the provider’s words', () => {
    const fixture = mounted({
      library: { tracks: 7595, enriched: 3923 },
      model: { health: 'degraded: rejected the API key', provider: 'cloudflare' },
    });
    const alert = fixture.nativeElement.querySelector('[data-model-health]');
    expect(alert).not.toBeNull();
    // The provider's own reason, because "degraded" alone still sends them to
    // a log to find out what is wrong.
    expect(alert.textContent).toContain('rejected the API key');
    // Announced, not just coloured: this is the one thing on the page that has
    // to reach somebody who is not looking straight at it.
    expect(alert.getAttribute('role')).toBe('alert');
  });

  it('model outage says nothing at all when the model is working', () => {
    const fixture = mounted({
      library: { tracks: 7595, enriched: 3923 },
      model: { health: 'ok', provider: 'cloudflare' },
    });
    expect(fixture.nativeElement.querySelector('[data-model-health]')).toBeNull();
  });

  it('model outage keeps quiet when no model is configured', () => {
    // A library with no model is a working shuffle, not an outage.
    const fixture = mounted({
      library: { tracks: 7595, enriched: 0 },
      model: { health: 'not configured' },
    });
    expect(fixture.nativeElement.querySelector('[data-model-health]')).toBeNull();
  });

  it('model outage says when enrichment gave up, not just that it is not running', () => {
    // Measured on the box at 03:33: "done": 3923, "running": true, with a
    // goroutine that had exited at 03:07. A green light over a dead worker and
    // a number that had not moved in an hour.
    const fixture = mounted({
      library: { tracks: 7595, enriched: 3923 },
      enriching: false,
      enrichment_stopped: 'enrichment gave up: rejected the API key',
    });
    const alert = fixture.nativeElement.querySelector('[data-enrichment-health]');
    expect(alert).not.toBeNull();
    expect(alert.textContent).toContain('rejected the API key');
    expect(alert.getAttribute('role')).toBe('alert');
  });

  it('model outage stays quiet about enrichment the operator paused on purpose', () => {
    const fixture = mounted({
      library: { tracks: 7595, enriched: 3923 },
      enriching: false,
    });
    expect(fixture.nativeElement.querySelector('[data-enrichment-health]')).toBeNull();
  });

  // ------------------------------------------------------ enrichment restart --
  //
  // Jockora-e9a.52. This button has been on the page since the beginning and
  // set a boolean nothing was reading once the worker had exited. During the
  // outage of 2026-09-08 it sat there looking like the fix, did nothing, and
  // reported success.

  it('enrichment restart names what the button is about to do', () => {
    const stopped = mounted({
      library: { tracks: 7595, enriched: 3923 },
      enriching: true,
      enrichment_stopped: 'enrichment gave up: rejected the API key',
    });
    // "Resume" over a dead worker and "Resume" over a paused one are different
    // promises. The operator cannot tell which they are about to get.
    expect(stopped.nativeElement.querySelector('[data-resume]').textContent).toContain('Restart');

    const paused = mounted({ library: { tracks: 7595, enriched: 3923 }, enriching: false });
    expect(paused.nativeElement.querySelector('[data-resume]').textContent).toContain('Resume');
    expect(paused.nativeElement.querySelector('[data-resume]').textContent).not.toContain(
      'Restart',
    );
  });

  it('enrichment restart repeats what the server says it did', () => {
    const fixture = mounted({
      library: { tracks: 7595, enriched: 3923 },
      enriching: true,
      enrichment_stopped: 'enrichment gave up: rejected the API key',
    });
    fixture.nativeElement.querySelector('[data-resume]').click();
    ctrl.expectOne('/admin/enriching').flush({
      said: 'Enrichment restarted. It had stopped; watch the dossier count.',
    });
    // Re-read, because the state it is reporting has just changed.
    ctrl.expectOne('/admin/overview.json').flush({
      library: { tracks: 7595, enriched: 3923 },
      enriching: true,
    });
    fixture.detectChanges();
    // The SERVER's words, not a guess made on this side: a restart that finds
    // nothing to do and one that brings a dead worker back look identical from
    // here, and the difference is exactly what the operator needs.
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('restarted');
    expect(fixture.nativeElement.querySelector('[data-enrichment-health]')).toBeNull();
  });

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
    // The page re-reads after a toggle: what it is reporting has just changed.
    ctrl.expectOne('/admin/overview.json').flush({ library: { tracks: 7595, enriched: 412 } });
    fixture.detectChanges();
    // Saying the stream is unaffected is the point: an operator pausing
    // enrichment is handing the machine back for an evening, not stopping the
    // station.
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('unaffected');

    fixture.nativeElement.querySelector('[data-resume]').click();
    const req = ctrl.expectOne('/admin/enriching');
    expect(req.request.body).toEqual({ enriching: true });
    req.flush(null);
    ctrl.expectOne('/admin/overview.json').flush({ library: { tracks: 7595, enriched: 412 } });
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

  describe('cadence', () => {
    // REPORTED TWICE. The first report was the SERVER forgetting the setting
    // across a restart, which the settings table fixed. This is the second
    // half of the same bug and it looked identical from the outside: the
    // server had the operator's 1, applied it, and logged it -- and the
    // console still drew a box with 4 in it, because the number in that box
    // was a hardcoded default that never once looked at the server's answer.
    it('shows the cadence the station is actually using', () => {
      const fixture = mounted({ library: { tracks: 10, enriched: 10 }, cadence: 1 });
      expect(fixture.nativeElement.querySelector('[data-cadence]').value).toBe('1');
    });

    it('keeps its default when the server does not report one', () => {
      const fixture = mounted({ library: { tracks: 10, enriched: 10 } });
      expect(fixture.nativeElement.querySelector('[data-cadence]').value).toBe('4');
    });

    it('does not overwrite a number the operator is still typing', () => {
      // The page re-reads itself every ten seconds. A poll that clobbered a
      // half-typed value would make the field impossible to use.
      vi.useFakeTimers();
      try {
        const fixture = mounted({ library: { tracks: 10, enriched: 10 }, cadence: 4 });
        const input = fixture.nativeElement.querySelector('[data-cadence]');
        input.value = '9';
        input.dispatchEvent(new Event('input'));

        vi.advanceTimersByTime(10_000);
        ctrl.expectOne('/admin/overview.json').flush({ library: { tracks: 10 }, cadence: 4 });
        fixture.detectChanges();
        expect(fixture.nativeElement.querySelector('[data-cadence]').value).toBe('9');
      } finally {
        vi.useRealTimers();
      }
    });

    it('follows the server again once the change is saved', () => {
      vi.useFakeTimers();
      try {
        const fixture = mounted({ library: { tracks: 10, enriched: 10 }, cadence: 4 });
        const input = fixture.nativeElement.querySelector('[data-cadence]');
        input.value = '1';
        input.dispatchEvent(new Event('input'));
        fixture.nativeElement.querySelector('[data-set-cadence]').click();
        ctrl.expectOne('/admin/cadence').flush(null);

        vi.advanceTimersByTime(10_000);
        ctrl.expectOne('/admin/overview.json').flush({ library: { tracks: 10 }, cadence: 2 });
        fixture.detectChanges();
        expect(fixture.nativeElement.querySelector('[data-cadence]').value).toBe('2');
      } finally {
        vi.useRealTimers();
      }
    });

    it('holds the operator’s number when the server refuses it', () => {
      // A refusal must not silently swap their value for the old one: they
      // need to see what they asked for next to the reason it was refused.
      vi.useFakeTimers();
      try {
        const fixture = mounted({ library: { tracks: 10, enriched: 10 }, cadence: 4 });
        const input = fixture.nativeElement.querySelector('[data-cadence]');
        input.value = '900';
        input.dispatchEvent(new Event('input'));
        fixture.nativeElement.querySelector('[data-set-cadence]').click();
        ctrl.expectOne('/admin/cadence').flush('cadence must be between 1 and 100 tracks', {
          status: 400,
          statusText: 'Bad Request',
        });

        vi.advanceTimersByTime(10_000);
        ctrl.expectOne('/admin/overview.json').flush({ library: { tracks: 10 }, cadence: 4 });
        fixture.detectChanges();
        expect(fixture.nativeElement.querySelector('[data-cadence]').value).toBe('900');
      } finally {
        vi.useRealTimers();
      }
    });
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
      expect(hours.nativeElement.querySelector('[data-enrich-eta]').textContent).toContain(
        '10 hours',
      );

      const mins = withOverview({
        enriching: true,
        library: { tracks: 100, enriched: 95 },
        enrichment_cost: { seconds_per_track: 40 },
      });
      expect(mins.nativeElement.querySelector('[data-enrich-eta]').textContent).toContain(
        '3 minutes',
      );
    });

    it('does not promise a time when it is paused or the rate is unknown', () => {
      // A paused job has no rate to project from, and saying "4 days" about
      // something that is not running would be a lie.
      const paused = withOverview({
        enriching: false,
        library: { tracks: 1000, enriched: 100 },
        enrichment_cost: { seconds_per_track: 40 },
      });
      expect(paused.nativeElement.querySelector('[data-enrich-line]').textContent).toContain(
        'paused',
      );
      const eta = paused.nativeElement.querySelector('[data-enrich-eta]').textContent;
      expect(eta).toContain('900 to go');
      expect(eta).not.toContain('hours');

      const unknown = withOverview({ enriching: true, library: { tracks: 10, enriched: 1 } });
      expect(unknown.nativeElement.querySelector('[data-enrich-eta]').textContent).toContain(
        '9 to go',
      );
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
      expect(fixture.nativeElement.querySelector('[data-enrich-line]').textContent).toContain(
        '0 of 200',
      );
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

  describe('reading it without being the person who wrote it', () => {
    // THE WHOLE PAGE WAS A FLATTENED JSON DUMP. Live, it read
    // "enrichment_cost.seconds_per_track | 17.468946255848465" and
    // "enrichment_cost.wall_seconds | 25015.531038375" beside dotted machine
    // keys and a row for "feedback" that rendered as nothing at all, because
    // an empty array stringifies to an empty string.
    const live = {
      adverts: 0,
      analysis_failures: 0,
      cadence: 1,
      enriching: true,
      enrichment_cost: {
        seconds_per_track: 17.468946255848465,
        tokens: 200933,
        tracks_measured: 1432,
        wall_seconds: 25015.531038375,
      },
      feedback: [],
      library: {
        bpm_measured: 0,
        enriched: 1428,
        loudness_measured: 4011,
        playable: 7595,
        tracks: 7696,
      },
      said_lines: 9,
    };

    it('says what the library holds in words and grouped digits', () => {
      const text = mounted(live).nativeElement.querySelector('[data-library-line]').textContent;
      expect(text).toContain('7,696');
      expect(text).toContain('7,595');
      expect(text).toMatch(/scanned|playable/);
    });

    it('shows loudness coverage as a bar, not a bare integer', () => {
      const fixture = mounted(live);
      const line = fixture.nativeElement.querySelector('[data-loudness-line]').textContent;
      expect(line).toContain('4,011');
      expect(line).toContain('7,595');
      // 4011/7595 = 52.8%
      expect(line).toContain('52.8');
      const bars = fixture.nativeElement.querySelectorAll('progress');
      expect(bars.length).toBe(2);
    });

    it('states the cost in units a person thinks in', () => {
      const text = mounted(live).nativeElement.querySelector('[data-cost-line]').textContent;
      // NOT 17.468946255848465, and NOT 25015.531038375.
      expect(text).not.toContain('17.4689');
      expect(text).not.toContain('25015');
      expect(text).toContain('17s');
      expect(text).toContain('6h 57m');
      expect(text).toContain('201k');
    });

    it('says nobody has complained rather than rendering an empty row', () => {
      expect(mounted(live).nativeElement.querySelector('[data-feedback]').textContent).toMatch(
        /no thumbs-down|nothing/i,
      );
    });

    it('lists thumbs-down when there are any', () => {
      const fixture = mounted({
        ...live,
        feedback: [{ verdict: 'down', jock: 'dutch', text: 'that was rough', at: 1788780000 }],
      });
      const text = fixture.nativeElement.querySelector('[data-feedback]').textContent;
      expect(text).toContain('that was rough');
      expect(text).toContain('dutch');
    });

    it('mentions failures only when there are some', () => {
      expect(mounted(live).nativeElement.querySelector('[data-failures]')).toBeNull();
      const bad = mounted({ ...live, analysis_failures: 12 });
      expect(bad.nativeElement.querySelector('[data-failures]').textContent).toContain('12');
    });

    it('keeps every raw figure, one click away', () => {
      // The dump was unreadable as a front page and is still the thing you
      // want when something is wrong.
      const fixture = mounted(live);
      const raw = fixture.nativeElement.querySelector('details[data-raw]');
      expect(raw).not.toBeNull();
      expect(raw.querySelector('[data-overview]').textContent).toContain('enrichment_cost.tokens');
    });

    it('survives an overview with nothing in it', () => {
      const fixture = mounted({});
      expect(fixture.nativeElement.querySelector('[data-library-line]')).toBeNull();
      expect(fixture.nativeElement.querySelector('[data-cost-line]')).toBeNull();
    });
  });

  it('renders an overview whose blocks are half filled in', () => {
    // The server grows fields over time and an older one will not have them
    // all. Every figure defaults rather than rendering "undefined" at an
    // operator.
    const fixture = mounted({
      library: { tracks: 10 },
      enrichment_cost: { tracks_measured: 5 },
    });
    expect(fixture.nativeElement.querySelector('[data-library-line]').textContent).toContain('10');
    expect(fixture.nativeElement.querySelector('[data-loudness-line]').textContent).toContain('0');
    expect(fixture.nativeElement.querySelector('[data-cost-line]').textContent).toContain('0s');
    expect(fixture.nativeElement.querySelector('[data-dj-line]').textContent).toContain('0');
    expect(fixture.nativeElement.querySelector('[data-feedback]').textContent).toContain(
      'No thumbs-down',
    );
  });

  it('reports no loudness coverage before anything is scanned', () => {
    // Reached directly: the template only draws this bar inside the block that
    // already proved there is a library, so nothing on screen can produce the
    // empty case -- and a division by an absent total is worth pinning anyway.
    expect(mounted({}).componentInstance.loud()).toEqual({ done: 0, total: 0, pct: 0 });
  });

  describe('units a person reads', () => {
    it('turns seconds into hours and minutes', () => {
      expect(duration(25015.531038375)).toBe('6h 57m');
      expect(duration(180)).toBe('3m');
      expect(duration(0)).toBe('0m');
    });

    it('shortens big counts and leaves small ones alone', () => {
      expect(compact(200933)).toBe('201k');
      expect(compact(1500)).toBe('2k');
      expect(compact(999)).toBe('999');
      // A long-running install passes a million tokens; "1000k" is not an
      // improvement on the raw number.
      expect(compact(2_450_000)).toBe('2.5M');
    });
  });

  // ENRICHMENT IS THE MOST EXPENSIVE THING THIS PROGRAM MAKES and it lived in
  // exactly one place. Moving it is a download and an upload, and the REPORT is
  // the half that matters: an import that silently does nothing is the failure
  // this screen exists to make impossible.
  it('enrichment export offers the library as a file', () => {
    const fixture = mounted();
    const link = fixture.nativeElement.querySelector('[data-export]');
    expect(link.getAttribute('href')).toBe('/admin/enrichment/export');
    // download, so the browser saves it rather than navigating to it.
    expect(link.hasAttribute('download')).toBe(true);
  });

  // FOUR DEFECTS SAT IN SIX LINES OF MARKUP. Measured on the running build: the
  // download link computed to rgb(0, 0, 238) with an underline -- the browser
  // default, and the only blue pixel in a console of warm rust and cream -- and
  // it ran straight into the text after it, so the page read "Download this
  // library's enrichmentDossiers, your own tag edits and the audio analysis."
  it('enrichment panel separates the download link from its description', () => {
    const panel = mounted().nativeElement.querySelector('[data-export]').closest('section');
    const link = panel.querySelector('[data-export]');
    const note = panel.querySelector('[data-export-note]');

    // Structure, not textContent and not a computed style. textContent
    // concatenates straight across element boundaries, so it reads the same
    // whether or not the fix is in; the stylesheet is not loaded here at all.
    // What was actually wrong is that these two were INLINE SIBLINGS, and
    // Angular drops the whitespace-only text node between them -- which is how
    // the page came to read "Download this library's enrichmentDossiers".
    expect(note).not.toBeNull();
    expect(note.tagName).toBe('P');
    expect(note.contains(link)).toBe(false);
    expect(link.parentElement).not.toBe(note);
  });

  it('enrichment panel says one track, not one tracks', () => {
    const one = mounted({ library: { tracks: 7595, enriched: 412 }, analysis_failures: 1 });
    expect(one.nativeElement.querySelector('[data-failures]').textContent).toContain('1 track ');
    expect(one.nativeElement.querySelector('[data-failures]').textContent).not.toContain(
      '1 tracks',
    );

    const many = mounted({ library: { tracks: 7595, enriched: 412 }, analysis_failures: 12 });
    expect(many.nativeElement.querySelector('[data-failures]').textContent).toContain('12 tracks');
  });

  it('enrichment panel presents the file input as a control, not a raw widget', () => {
    // The browser's own "Choose File / No file chosen" was the same defect as
    // the vocabulary checkboxes: an unstyled native control in a styled
    // console. The label is the control; the input is hidden behind it.
    const fixture = mounted();
    const label = fixture.nativeElement.querySelector('[data-import-label]');
    expect(label.querySelector('[data-import-button]')).not.toBeNull();
    expect(fixture.nativeElement.querySelector('[data-import-name]').textContent).toContain(
      'No file',
    );
  });

  it('enrichment panel names the file the operator picked', () => {
    const fixture = mounted();
    const input = fixture.nativeElement.querySelector('[data-import]');
    const file = new File(['x'], 'from-the-other-box.jsonl.gz');
    Object.defineProperty(input, 'files', { value: [file] });
    input.dispatchEvent(new Event('change'));
    ctrl.expectOne('/admin/enrichment/import').flush({
      applied: 1,
      had_dossier: 0,
      had_override: 0,
      unmatched: 0,
      wrong_track: 0,
      rejected: 0,
      unreadable: 0,
    });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-import-name]').textContent).toContain(
      'from-the-other-box.jsonl.gz',
    );
  });

  it('enrichment import reports what it did, counter by counter', () => {
    const fixture = mounted();
    const input = fixture.nativeElement.querySelector('[data-import]');
    const file = new File(['x'], 'enrichment.jsonl.gz');
    Object.defineProperty(input, 'files', { value: [file] });
    input.dispatchEvent(new Event('change'));

    const req = ctrl.expectOne('/admin/enrichment/import');
    expect(req.request.method).toBe('POST');
    req.flush({
      applied: 412,
      had_dossier: 9,
      had_override: 2,
      unmatched: 31,
      wrong_track: 1,
      rejected: 0,
      unreadable: 0,
    });
    fixture.detectChanges();

    const text = fixture.nativeElement.querySelector('[data-import-report]').textContent;
    expect(text).toContain('412');
    // EVERY counter, including the ones that are zero: "nothing was rejected"
    // is a different statement from silence about rejections.
    expect(text).toContain('unmatched');
    expect(text).toContain('31');
    expect(text).toContain('already had a dossier');
    expect(text).toContain('kept your own tag edits');
  });

  it('enrichment import says so when the file is refused', () => {
    const fixture = mounted();
    const input = fixture.nativeElement.querySelector('[data-import]');
    Object.defineProperty(input, 'files', { value: [new File(['x'], 'notes.txt')] });
    input.dispatchEvent(new Event('change'));
    ctrl.expectOne('/admin/enrichment/import').flush('not an export', {
      status: 400,
      statusText: 'Bad Request',
    });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
      'could not be read',
    );
  });

  it('enrichment import does nothing when no file is chosen', () => {
    const fixture = mounted();
    const input = fixture.nativeElement.querySelector('[data-import]');
    Object.defineProperty(input, 'files', { value: [] });
    input.dispatchEvent(new Event('change'));
    ctrl.expectNone('/admin/enrichment/import');
  });

  // -------------------------------------------------------- Jockora-0gm --
  //
  // Reported from the deployment: "Dossiers 7,595 of 7,696 (98.7%) - paused".
  // Read from the box: 7696 tracks, 101 of them playable = 0, 7595 dossiers,
  // and ZERO playable tracks without one. Enrichment was COMPLETE. The bar
  // divided by every track in the table while enrichment only ever walks the
  // playable ones, so it could never reach 100 percent -- and the loudness bar
  // directly beneath it already used the right denominator.

  it('enrichment progress counts against the playable library, not every file', () => {
    // The deployment's own numbers.
    const fixture = mounted({
      library: { tracks: 7696, playable: 7595, enriched: 7595, loudness_measured: 7595 },
      enriching: true,
    });
    const line = fixture.nativeElement.querySelector('[data-enrich-line]').textContent;
    expect(line).toContain('7,595 of 7,595');
    expect(line).toContain('100%');
    expect(line).not.toContain('7,696');

    // And the two bars on this screen agree, which is the whole defect: they
    // were reading different denominators out of the same payload.
    const loud = fixture.nativeElement.querySelector('[data-loudness-line]').textContent;
    expect(loud).toContain('7,595 of 7,595');
  });

  it('enrichment progress says complete rather than paused when nothing is left', () => {
    const fixture = mounted({
      library: { tracks: 7696, playable: 7595, enriched: 7595 },
      enriching: false,
    });
    const line = fixture.nativeElement.querySelector('[data-enrich-line]').textContent;
    // "paused" is the operator's own toggle, so it implies somebody stopped it.
    // A pass with no work left is finished, and an operator who reads paused
    // beside a full bar goes looking for something to restart.
    expect(line).toContain('complete');
    expect(line).not.toContain('paused');
    expect(fixture.nativeElement.querySelector('[data-enrich-eta]').textContent).toContain(
      'Every track has a dossier',
    );
  });

  it('enrichment progress still says paused while work remains', () => {
    // The toggle still means what it meant: this is the case it exists for.
    const fixture = mounted({
      library: { tracks: 7696, playable: 7595, enriched: 400 },
      enriching: false,
    });
    const line = fixture.nativeElement.querySelector('[data-enrich-line]').textContent;
    expect(line).toContain('paused');
    expect(line).not.toContain('complete');
  });

  it('enrichment progress falls back to the track count when playable is absent', () => {
    // An older server, or one that has not learned to report it. Better a
    // slightly wrong denominator than a division by zero and a blank panel.
    const fixture = mounted({ library: { tracks: 500, enriched: 250 }, enriching: true });
    expect(fixture.nativeElement.querySelector('[data-enrich-line]').textContent).toContain(
      '250 of 500',
    );
  });
});
