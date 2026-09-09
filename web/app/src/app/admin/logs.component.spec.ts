import { describe, expect, it, beforeEach, vi, afterEach } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Logs } from './logs.component';

// Jockora-69n.5. Logs went to stderr and nowhere else, so finding out why a
// station went quiet meant ssh and then docker logs. Three live defects on the
// deployment were found exactly that way.

/** A stand-in for EventSource: the real one opens a socket. */
class FakeSource {
  static last: FakeSource | null = null;
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  onopen: ((e: Event) => void) | null = null;
  closed = false;
  readyState = 0;

  constructor(readonly url: string) {
    FakeSource.last = this;
  }
  close() {
    this.closed = true;
  }
  /** Deliver one record as the server would. */
  send(record: unknown) {
    this.readyState = 1;
    this.onmessage?.({ data: JSON.stringify(record) } as MessageEvent);
  }
  open() {
    this.readyState = 1;
    this.onopen?.(new Event('open'));
  }
  fail(readyState = 0) {
    this.readyState = readyState;
    this.onerror?.(new Event('error'));
  }
}

const records = [
  {
    time: '2026-09-08T03:31:06Z',
    level: 'ERROR',
    message: 'enrichment stopped',
    attrs: { err: 'rejected the API key' },
  },
  {
    time: '2026-09-08T03:28:35Z',
    level: 'WARN',
    message: 'break dropped',
    attrs: { reason: 'llm_error' },
  },
  { time: '2026-09-08T03:27:58Z', level: 'INFO', message: 'break slot offered' },
];

describe('logs', () => {
  let ctrl: HttpTestingController;

  beforeEach(async () => {
    FakeSource.last = null;
    vi.stubGlobal('EventSource', FakeSource);
    vi.stubGlobal('confirm', () => true);
    await TestBed.configureTestingModule({
      imports: [Logs],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  afterEach(() => vi.unstubAllGlobals());

  function mounted(body: object = { records, dropped: 0, level: 'INFO' }) {
    const fixture = TestBed.createComponent(Logs);
    fixture.detectChanges();
    ctrl.expectOne((r) => r.url === '/admin/logs').flush(body);
    fixture.detectChanges();
    return fixture;
  }

  const rows = (f: { nativeElement: HTMLElement }) =>
    Array.from(f.nativeElement.querySelectorAll('[data-log-row]'));

  /**
   * Show every level.
   *
   * Jockora-e9a.57 made the console open on warnings and errors, so a test
   * about what a RECORD renders as had quietly become a test about the default
   * level too. These say which level they mean instead of inheriting it.
   */
  function showingEverything<T extends { nativeElement: HTMLElement; detectChanges(): void }>(
    fixture: T,
  ): T {
    const show = fixture.nativeElement.querySelector('[data-log-show]') as HTMLSelectElement;
    show.value = 'DEBUG';
    show.dispatchEvent(new Event('change'));
    fixture.detectChanges();
    return fixture;
  }

  // ------------------------------------------------------------ log level --
  //
  // Jockora-e9a.57, reported by the operator. Both selectors opened on "info
  // and above", which on a scanning library is more than twenty consecutive
  // "scanning elapsed=Ns files=N" lines plus break scheduled, break slot
  // offered and station refilled every few minutes. Buried one row in forty was
  // the single WARN that matters: SERVING WITHOUT AUTHENTICATION ON A
  // NON-LOOPBACK ADDRESS.
  //
  // A security-relevant warning at the same visual weight as a progress tick.

  it('log level shows warnings and errors before anything is chosen', () => {
    const show = mounted().nativeElement.querySelector('[data-log-show]') as HTMLSelectElement;
    expect(show.value).toBe('WARN');
  });

  it('log level records warnings and errors before anything is chosen', () => {
    // A server that says nothing about its level must not be read as info.
    const fixture = mounted({ records, dropped: 0 });
    const record = fixture.nativeElement.querySelector('[data-log-record]') as HTMLSelectElement;
    expect(record.value).toBe('WARN');
    expect(fixture.componentInstance.recorded()).toBe('WARN');

    // AND BEFORE THE SERVER HAS ANSWERED AT ALL, which is the only time the
    // signal's own initial value is on screen -- and the state a failed first
    // read leaves the page in for good. Falsification found this: setting the
    // signal back to INFO killed no test, because every other one flushes a
    // response first and the response overwrites it.
    const fresh = TestBed.createComponent(Logs);
    fresh.detectChanges();
    expect(fresh.componentInstance.recorded()).toBe('WARN');
    expect(fresh.componentInstance.showing()).toBe('WARN');
    ctrl.expectOne((r) => r.url === '/admin/logs').error(new ProgressEvent('failed'));
    fresh.detectChanges();
    expect(fresh.componentInstance.recorded()).toBe('WARN');
  });

  it('log level still offers info when an operator asks for it', () => {
    // NOTHING IS REMOVED. A default is not a restriction: an operator chasing
    // something drops to info or debug deliberately, and the list still says so.
    const fixture = mounted();
    const show = fixture.nativeElement.querySelector('[data-log-show]') as HTMLSelectElement;
    expect([...show.options].map((o) => o.value)).toEqual(['DEBUG', 'INFO', 'WARN', 'ERROR']);

    show.value = 'INFO';
    show.dispatchEvent(new Event('change'));
    fixture.detectChanges();
    expect(fixture.componentInstance.showing()).toBe('INFO');
    expect(rows(fixture).length).toBe(3);
  });

  it('renders records newest first with time, level and message', () => {
    const fixture = showingEverything(mounted());
    const got = rows(fixture);
    expect(got.length).toBe(3);
    expect(got[0].textContent).toContain('enrichment stopped');
    expect(got[0].textContent).toContain('ERROR');
    // A time a person can read, not an ISO string.
    expect(got[0].textContent).toMatch(/\d\d:\d\d:\d\d/);
    expect(got[2].textContent).toContain('break slot offered');
  });

  it('filters what is shown without changing what is recorded', () => {
    const fixture = mounted();
    const select = fixture.nativeElement.querySelector('[data-log-show]') as HTMLSelectElement;
    select.value = 'WARN';
    select.dispatchEvent(new Event('change'));
    fixture.detectChanges();

    expect(rows(fixture).length).toBe(2);
    // AND NO REQUEST. The display filter is a client-side view of what is
    // already here; the RECORDED level is a different control entirely, and
    // conflating them is how an operator concludes the feature is broken.
    ctrl.expectNone((r) => r.url === '/admin/logs/level');
  });

  it('the debug toggle changes what the server records, and says what it costs', () => {
    const fixture = mounted();
    const record = fixture.nativeElement.querySelector('[data-log-record]') as HTMLSelectElement;
    record.value = 'DEBUG';
    record.dispatchEvent(new Event('change'));

    const req = ctrl.expectOne('/admin/logs/level');
    expect(req.request.body).toEqual({ level: 'DEBUG' });
    req.flush({ level: 'DEBUG' });
    fixture.detectChanges();

    // A SENTENCE NEXT TO THE CONTROL, not a dialog.
    const warn = fixture.nativeElement.querySelector('[data-log-debug-warning]');
    expect(warn).not.toBeNull();
    expect(warn.textContent.toLowerCase()).toContain('debug');
  });

  it('says what the server said when the session has gone, not its own sentence', () => {
    // TWO SHAPES REACH THIS SCREEN. Its own handler refuses with JSON through
    // writeJSON and writeFieldError, but every admin route is wrapped by
    // require, which refuses an expired session with http.Error -- text/plain.
    // A reader written for one of them silently mangles the other, and the
    // half this component was written for was the JSON half. Jockora-gq6.
    const fixture = mounted();
    const record = fixture.nativeElement.querySelector('[data-log-record]') as HTMLSelectElement;
    record.value = 'DEBUG';
    record.dispatchEvent(new Event('change'));

    ctrl
      .expectOne('/admin/logs/level')
      .flush('sign in\n', { status: 401, statusText: 'Unauthorized' });
    fixture.detectChanges();

    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('sign in');
  });

  it('a streamed record appends to the list', () => {
    const fixture = mounted();
    FakeSource.last!.send({
      time: '2026-09-08T03:35:00Z',
      level: 'WARN',
      message: 'a new one',
    });
    fixture.detectChanges();
    expect(rows(fixture)[0].textContent).toContain('a new one');
  });

  it('paused, the list holds still and new records are counted', () => {
    const fixture = showingEverything(mounted());
    fixture.nativeElement.querySelector('[data-log-pause]').click();
    fixture.detectChanges();

    for (let i = 0; i < 14; i++) {
      FakeSource.last!.send({ time: '2026-09-08T03:35:00Z', level: 'WARN', message: `new ${i}` });
    }
    fixture.detectChanges();

    // THE LIST DID NOT MOVE. The operator's actual task is reading one record,
    // and a list that reflows underneath them is worse than no live view.
    expect(rows(fixture).length).toBe(3);
    expect(rows(fixture)[0].textContent).toContain('enrichment stopped');
    // COUNTED, not dropped.
    const pending = fixture.nativeElement.querySelector('[data-log-pending]');
    expect(pending.textContent).toContain('14');

    // Resumed by the COUNT ITSELF, which is the thing an operator reaches for
    // -- "14 new" is both the notice and the way to see them.
    fixture.nativeElement.querySelector('[data-log-pending]').click();
    fixture.detectChanges();
    expect(rows(fixture).length).toBe(17);
    expect(fixture.nativeElement.querySelector('[data-log-pending]')).toBeNull();
  });

  it('says when records were lost, and stays quiet when none were', () => {
    expect(mounted().nativeElement.querySelector('[data-log-dropped]')).toBeNull();

    const lossy = mounted({ records, dropped: 42, level: 'INFO' });
    const note = lossy.nativeElement.querySelector('[data-log-dropped]');
    expect(note).not.toBeNull();
    // A gap in a log that presents itself as complete is worse than a gap that
    // admits it.
    expect(note.textContent).toContain('42');
  });

  it('clear asks first and does nothing if declined', () => {
    const fixture = showingEverything(mounted());
    vi.stubGlobal('confirm', () => false);
    fixture.nativeElement.querySelector('[data-log-clear]').click();
    ctrl.expectNone((r) => r.method === 'DELETE');
    expect(rows(fixture).length).toBe(3);

    const asked: string[] = [];
    vi.stubGlobal('confirm', (q: string) => {
      asked.push(q);
      return true;
    });
    fixture.nativeElement.querySelector('[data-log-clear]').click();
    // It NAMES WHAT GOES: the live view and the stored warnings.
    expect(asked[0].toLowerCase()).toContain('stored');
    ctrl
      .expectOne((r) => r.method === 'DELETE' && r.url === '/admin/logs')
      .flush({ cleared: true });
    // IT RE-READS. Clearing and then showing the same list is a button that
    // reports success and changes nothing on screen -- and what the server
    // logged about the clear itself belongs in the new list.
    ctrl
      .expectOne((r) => r.method === 'GET' && r.url === '/admin/logs')
      .flush({
        records: [{ time: '2026-09-08T03:40:00Z', level: 'WARN', message: 'log records cleared' }],
        dropped: 0,
        level: 'INFO',
      });
    fixture.detectChanges();
    expect(rows(fixture).length).toBe(1);
    expect(rows(fixture)[0].textContent).toContain('log records cleared');
  });

  it('copy puts the visible records on the clipboard', async () => {
    const written: string[] = [];
    vi.stubGlobal('navigator', {
      clipboard: {
        writeText: (t: string) => {
          written.push(t);
          return Promise.resolve();
        },
      },
    });
    const fixture = mounted();

    // FILTERED FIRST, which is the whole point of the assertion below: with
    // nothing filtered out, "the visible records" and "every record" are the
    // same list and a copy that ignored the filter would pass anyway.
    const select = fixture.nativeElement.querySelector('[data-log-show]') as HTMLSelectElement;
    select.value = 'WARN';
    select.dispatchEvent(new Event('change'));
    fixture.detectChanges();

    fixture.nativeElement.querySelector('[data-log-copy]').click();
    await Promise.resolve();

    expect(written.length).toBe(1);
    expect(written[0]).toContain('enrichment stopped');
    expect(written[0]).toContain('break dropped');
    // THE VISIBLE ones: the next thing that happens after an operator finds an
    // error is that they paste it to somebody, and a line they had filtered
    // out would confuse both of them.
    expect(written[0]).not.toContain('break slot offered');
    // The attributes go too -- "rejected the API key" is the half that says
    // what to do about it.
    expect(written[0]).toContain('rejected the API key');

    // AND A RECORD WITH NO ATTRIBUTES copies as its own line with nothing
    // trailing. Most records have none. This used to be covered by accident,
    // because the console opened on info and the attr-less line was on screen;
    // at the warnings-and-errors default of Jockora-e9a.57 it has to be asked
    // for, so it is asked for here rather than left to another test's default.
    showingEverything(fixture);
    fixture.nativeElement.querySelector('[data-log-copy]').click();
    await Promise.resolve();
    expect(written[1].endsWith('break slot offered')).toBe(true);
  });

  it('says whether the connection is up, so a quiet station is not a dead one', () => {
    const fixture = mounted();
    const state = () => fixture.nativeElement.querySelector('[data-log-connection]').textContent;

    FakeSource.last!.open();
    fixture.detectChanges();
    expect(state().toLowerCase()).toContain('live');

    // EventSource reconnects on its own, and readyState says which.
    FakeSource.last!.fail(0);
    fixture.detectChanges();
    expect(state().toLowerCase()).toContain('reconnect');

    FakeSource.last!.fail(2);
    fixture.detectChanges();
    expect(state().toLowerCase()).toContain('disconnect');
  });

  it('a failed stream leaves the records already on screen', () => {
    const fixture = showingEverything(mounted());
    FakeSource.last!.fail(2);
    fixture.detectChanges();
    // Blanking the list on a dropped connection throws away the very thing the
    // operator opened the page to read.
    expect(rows(fixture).length).toBe(3);
  });

  it('says so when the first read fails, rather than looking like an empty log', () => {
    const fixture = TestBed.createComponent(Logs);
    fixture.detectChanges();
    ctrl
      .expectOne((r) => r.url === '/admin/logs')
      .flush(null, { status: 500, statusText: 'Server Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Could not');
    expect(fixture.nativeElement.querySelector('[data-empty]')).toBeNull();
  });

  it('says so when a control fails, rather than pretending it worked', () => {
    const fixture = mounted();
    const said = () => fixture.nativeElement.querySelector('[data-said]').textContent;

    // Changing what is RECORDED can fail: the level is persisted, and a
    // read-only database is a real state.
    const record = fixture.nativeElement.querySelector('[data-log-record]') as HTMLSelectElement;
    record.value = 'ERROR';
    record.dispatchEvent(new Event('change'));
    ctrl
      .expectOne('/admin/logs/level')
      .flush({ error: 'disk is full' }, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(said()).toContain('disk is full');
    // AND THE CONTROL DID NOT MOVE: showing a level the server refused would
    // leave the operator believing debug is on when it is not.
    expect(fixture.componentInstance.recorded()).toBe('INFO');

    // A refusal with no words at all still says something.
    record.value = 'WARN';
    record.dispatchEvent(new Event('change'));
    ctrl.expectOne('/admin/logs/level').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(said()).toContain('Could not');

    // Clearing can fail too.
    fixture.nativeElement.querySelector('[data-log-clear]').click();
    ctrl.expectOne((r) => r.method === 'DELETE').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(said()).toContain('Could not clear');

    // And it can succeed while the re-read fails, which is not the same thing
    // and must not read as a clear that did nothing.
    fixture.nativeElement.querySelector('[data-log-clear]').click();
    ctrl.expectOne((r) => r.method === 'DELETE').flush({ cleared: true });
    ctrl.expectOne((r) => r.method === 'GET').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(said()).toContain('Cleared');

    // A re-read that answers with an empty object -- an older server, or one
    // mid-upgrade -- must leave an empty list rather than an undefined one.
    fixture.nativeElement.querySelector('[data-log-clear]').click();
    ctrl.expectOne((r) => r.method === 'DELETE').flush({ cleared: true });
    ctrl.expectOne((r) => r.method === 'GET').flush({});
    fixture.detectChanges();
    expect(fixture.componentInstance.records()).toEqual([]);
    expect(fixture.componentInstance.dropped()).toBe(0);
  });

  it('says so when the clipboard refuses', async () => {
    vi.stubGlobal('navigator', {
      clipboard: { writeText: () => Promise.reject(new Error('denied')) },
    });
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-log-copy]').click();
    await Promise.resolve();
    await Promise.resolve();
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('clipboard');
  });

  it('renders the odd record without falling over', () => {
    const fixture = showingEverything(mounted({
      records: [
        // No attrs at all, which is most records.
        { time: '2026-09-08T03:31:06Z', level: 'WARN', message: 'plain' },
        // A time that is not a time, from a hand-edited row.
        { time: 'whenever', level: 'ERROR', message: 'bad clock' },
        // A level this build does not know, from a newer server. It sorts with
        // info rather than vanishing from every filter.
        { time: '2026-09-08T03:31:06Z', level: 'CATASTROPHE', message: 'from the future' },
      ],
      dropped: 0,
      level: 'INFO',
    }));
    const got = rows(fixture);
    expect(got.length).toBe(3);
    expect(got[0].querySelector('[data-log-attrs]')).toBeNull();
    expect(got[1].textContent).toContain('whenever');
    expect(got[2].textContent).toContain('from the future');
  });

  it('answers an empty log with what to do rather than silence', () => {
    const fixture = mounted({ records: [], dropped: 0, level: 'INFO' });
    const empty = fixture.nativeElement.querySelector('[data-empty]');
    expect(empty).not.toBeNull();
    expect(empty.textContent).toContain('Record');
  });

  it('a server that sends nothing at all does not blank the page', () => {
    const fixture = TestBed.createComponent(Logs);
    fixture.detectChanges();
    ctrl.expectOne((r) => r.url === '/admin/logs').flush({});
    fixture.detectChanges();
    expect(fixture.componentInstance.records()).toEqual([]);
    // Jockora-e9a.57: a server that says nothing about its level must not be
    // read as info.
    expect(fixture.componentInstance.recorded()).toBe('WARN');
  });

  it('closes the stream when the section is left', () => {
    const fixture = mounted();
    const source = FakeSource.last!;
    fixture.destroy();
    // A leaked EventSource is a subscriber the server keeps and drops into.
    expect(source.closed).toBe(true);
  });

  it('opens no stream when the operator leaves before the first read returns', () => {
    // ONE SECTION IS MOUNTED AT A TIME, so switching away destroys this one --
    // and on a slow box the first read is still in flight when that happens.
    // The response then arrived and opened an EventSource that ngOnDestroy had
    // already run for: a subscriber the server keeps for ever, against a cap of
    // eight, counting every record it never reads as dropped.
    FakeSource.last = null;
    const fixture = TestBed.createComponent(Logs);
    fixture.detectChanges();
    const req = ctrl.expectOne('/admin/logs?limit=200&level=DEBUG');

    fixture.destroy();
    req.flush({ records: [], dropped: 0, level: 'INFO' });

    expect(FakeSource.last).toBeNull();

    // AND THE SAME ON A FAILURE. A read that errors after the operator has
    // left must not write to signals nobody is rendering; the guard is on
    // both halves or it is on neither.
    const second = TestBed.createComponent(Logs);
    second.detectChanges();
    const failing = ctrl.expectOne('/admin/logs?limit=200&level=DEBUG');
    second.destroy();
    failing.flush(null, { status: 500, statusText: 'nope' });
    expect(FakeSource.last).toBeNull();
    expect(second.componentInstance.failed()).toBe(false);
  });
});
