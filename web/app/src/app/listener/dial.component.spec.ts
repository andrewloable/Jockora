import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Dial, DIAL_POLL_MS } from './dial.component';

const stations = [
  { id: 1, name: 'ROCK', genre: 'rock', jock_name: 'Dutch', tracks: 412, listeners: 2 },
  { id: 3, name: 'AMBIENT', genre: 'ambient', mood: 'calm', tracks: 96, listeners: 0 },
];

describe('Dial', () => {
  let ctrl: HttpTestingController;
  /**
   * What document.visibilityState reports. jsdom's is read-only and always
   * "visible", so the tests that need a backgrounded tab redefine it here and
   * put it back afterwards.
   */
  let hidden: DocumentVisibilityState;
  let realVisibility: PropertyDescriptor | undefined;

  beforeEach(async () => {
    vi.useFakeTimers();
    hidden = 'visible';
    realVisibility = Object.getOwnPropertyDescriptor(Document.prototype, 'visibilityState');
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      get: () => hidden,
    });
    await TestBed.configureTestingModule({
      imports: [Dial],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    vi.useRealTimers();
    Reflect.deleteProperty(document, 'visibilityState');
    if (realVisibility) {
      Object.defineProperty(Document.prototype, 'visibilityState', realVisibility);
    }
  });

  function mounted() {
    const fixture = TestBed.createComponent(Dial);
    ctrl.expectOne('/stations.json').flush({ stations });
    fixture.detectChanges();
    return fixture;
  }

  // ------------------------------------------------------ console states --
  //
  // Jockora-e9a.50. The dial IS the listener's UI, so its empty and loading
  // states are the whole first impression of a fresh install -- and both drew
  // the same thing, so a listener on a slow connection was told there were no
  // stations while the answer was still in flight.

  it('console states tells a listener the dial is empty rather than showing nothing', () => {
    const fixture = TestBed.createComponent(Dial);
    ctrl.expectOne('/stations.json').flush({ stations: [] });
    fixture.detectChanges();
    const empty = fixture.nativeElement.querySelector('[data-empty]');
    expect(empty).not.toBeNull();
    // A listener cannot fix this and should not be sent to a console they
    // cannot open. It says who can.
    expect(empty.textContent).toContain('operator');
  });

  it('console states does not tell a listener the dial is empty while it is loading', () => {
    const fixture = TestBed.createComponent(Dial);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-empty]')).toBeNull();
    expect(fixture.nativeElement.querySelector('[data-loading]')).not.toBeNull();
    ctrl.expectOne('/stations.json').flush({ stations: [] });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-loading]')).toBeNull();
    expect(fixture.nativeElement.querySelector('[data-empty]')).not.toBeNull();
  });

  it('console states says so when a request fails and does not claim the dial is empty', () => {
    const fixture = TestBed.createComponent(Dial);
    ctrl.expectOne('/stations.json').flush(null, { status: 500, statusText: 'Server Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-empty]')).toBeNull();
    expect(fixture.nativeElement.querySelector('[data-loading]')).toBeNull();
    expect(fixture.nativeElement.querySelector('[data-error]').textContent).toContain('Could not');
  });

  it('renders what a person picks a station by', () => {
    const fixture = mounted();
    const buttons = fixture.nativeElement.querySelectorAll('[data-station]');
    expect(buttons.length).toBe(2);
    expect(buttons[0].textContent).toContain('ROCK');
    expect(buttons[0].textContent).toContain('412');
    expect(buttons[0].textContent).toContain('Dutch');
    // Listeners, because a station with somebody on it is already playing.
    expect(buttons[0].textContent).toContain('2 listening');
    expect(buttons[1].textContent).toContain('calm');
    // And nothing invented for a station with nobody on it.
    expect(buttons[1].textContent).not.toContain('listening');
  });

  it('tunes by station id and says where to listen', () => {
    const fixture = mounted();
    let tuned: unknown;
    fixture.componentInstance.tuned.subscribe((v: unknown) => (tuned = v));

    fixture.nativeElement.querySelectorAll('[data-station]')[1].click();
    const req = ctrl.expectOne('/tune');
    expect(req.request.body).toEqual({ station_id: 3 });
    req.flush({ hls: '/hls/3/stream.m3u8', station_id: 3 });
    fixture.detectChanges();

    expect(tuned).toEqual({ station: stations[1], hls: '/hls/3/stream.m3u8' });
    expect(
      fixture.nativeElement.querySelectorAll('[data-station]')[1].getAttribute('aria-pressed'),
    ).toBe('true');
  });

  it('says so when a station will not tune', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelectorAll('[data-station]')[0].click();
    ctrl.expectOne('/tune').flush(null, { status: 404, statusText: 'Not Found' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-error]').textContent).toContain(
      'not available',
    );
  });

  it('explains an empty dial rather than showing nothing', () => {
    const fixture = TestBed.createComponent(Dial);
    ctrl.expectOne('/stations.json').flush({ stations: [] });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-empty]').textContent).toContain('console');
  });

  it('survives a dial that will not load', () => {
    // A listener already tuned keeps hearing their station; the dial failing
    // is not a reason to take the player away.
    const fixture = TestBed.createComponent(Dial);
    ctrl.expectOne('/stations.json').flush(null, { status: 500, statusText: 'Server Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-error]')).toBeTruthy();
  });

  it('shows a station that is still filling, and will not tune to it', () => {
    // A station grows while enrichment classifies the library. Twelve tracks
    // on a loop is a worse first impression than the station not being ready
    // yet, so it is shown with its progress and cannot be picked.
    const fixture = TestBed.createComponent(Dial);
    ctrl.expectOne('/stations.json').flush({
      stations: [
        { id: 1, name: 'UNSORTED', genre: 'unsorted', tracks: 7595, listeners: 0, ready: true },
        { id: 2, name: 'Night Rock', genre: 'rock', tracks: 12, listeners: 0, ready: false },
      ],
    });
    fixture.detectChanges();

    const buttons = fixture.nativeElement.querySelectorAll('[data-station]');
    expect(buttons[0].disabled).toBe(false);
    expect(buttons[1].disabled).toBe(true);
    expect(buttons[1].getAttribute('aria-disabled')).toBe('true');
    expect(buttons[1].textContent).toContain('still filling');
    expect(buttons[1].textContent).toContain('12 tracks so far');

    // And clicking it asks for nothing.
    buttons[1].click();
    ctrl.verify();
  });

  it('treats a station with no readiness field as tunable', () => {
    // The field is optional, so a server that does not send it must not make
    // every station unpickable.
    const fixture = TestBed.createComponent(Dial);
    ctrl.expectOne('/stations.json').flush({
      stations: [{ id: 1, name: 'Rock', genre: 'rock', tracks: 90, listeners: 0 }],
    });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-station]').disabled).toBe(false);
  });

  // THE MOOD LINE RAN OUTSIDE ITS OWN CARD. The server joins moods with bare
  // commas, and in the monospace face
  // "melancholic,euphoric,calm,nocturnal,lonely,hypnotic" is one unbreakable
  // 51-character token: measured 377px of text inside a 206px card at 1280px,
  // and 20px of sideways scroll on the whole page at 390px.
  it('dial card writes moods with breaks between them', () => {
    const fixture = TestBed.createComponent(Dial);
    ctrl.expectOne('/stations.json').flush({
      stations: [
        {
          id: 1,
          name: 'NIGHT ROCK',
          genre: 'rock,alternative',
          mood: 'melancholic,euphoric,calm,nocturnal,lonely,hypnotic',
          tracks: 515,
          listeners: 0,
        },
      ],
    });
    fixture.detectChanges();

    const line = fixture.nativeElement.querySelector('[data-station] small').textContent;
    expect(line).toContain('melancholic, euphoric, calm, nocturnal, lonely, hypnotic');
    // Not truncated: a listener choosing a station is exactly who needs to know
    // what is on it.
    expect(line).not.toContain('…');
    // And no token long enough to push the card open again.
    const longest = Math.max(
      ...line
        .trim()
        .split(/\s+/)
        .map((w: string) => w.length),
    );
    expect(longest).toBeLessThanOrEqual(12);
  });

  // ------------------------------------------------------- Jockora-e9a.64 --
  //
  // The dial fetched once and never again. A station still building its
  // playlist comes back ready:false and renders as a DISABLED button reading
  // "still filling" -- and when it finished, nothing told the page. The button
  // stayed dead for as long as the tab was open, on the listener's whole UI,
  // and the only way to hear the station was to guess that reloading helped.

  it('stale view re-enables a station once it has finished filling', () => {
    const filling = [{ ...stations[0], ready: false, tracks: 3 }];
    const fixture = TestBed.createComponent(Dial);
    ctrl.expectOne('/stations.json').flush({ stations: filling });
    fixture.detectChanges();
    expect(
      (fixture.nativeElement.querySelector('[data-station]') as HTMLButtonElement).disabled,
    ).toBe(true);

    vi.advanceTimersByTime(DIAL_POLL_MS);
    ctrl
      .expectOne('/stations.json')
      .flush({ stations: [{ ...stations[0], ready: true, tracks: 412 }] });
    fixture.detectChanges();

    // WITHOUT A RELOAD, which is the whole point.
    const button = fixture.nativeElement.querySelector('[data-station]') as HTMLButtonElement;
    expect(button.disabled).toBe(false);
    expect(button.textContent).not.toContain('still filling');
  });

  it('stale view keeps asking while somebody is looking at the dial', () => {
    // THE LISTENER COUNT IS THE ONE NUMBER HERE THAT CHANGES ON ITS OWN. The
    // server recomputes it from the presence tracker on every request, and the
    // poll used to stop the moment every station was ready -- so the count
    // froze at whatever it was when the page loaded. Reported live.
    // Jockora-1ge.
    const fixture = mounted();
    vi.advanceTimersByTime(DIAL_POLL_MS);
    ctrl.expectOne('/stations.json').flush({
      stations: [
        { ...stations[0], listeners: 7 },
        { ...stations[1], listeners: 1 },
      ],
    });
    fixture.detectChanges();

    expect(fixture.componentInstance.stations()[0].listeners).toBe(7);
    expect(fixture.nativeElement.textContent).toContain('7 listening');
  });

  it('stale view goes quiet while the tab is in the background', () => {
    // THE REASON BEHIND e9a.64's DO NOT IS KEPT: what it defended against was a
    // four second timer running for ever on a page nobody is looking at, and
    // that is exactly the question the Page Visibility API answers.
    const fixture = mounted();
    hidden = 'hidden';
    document.dispatchEvent(new Event('visibilitychange'));
    fixture.detectChanges();

    vi.advanceTimersByTime(DIAL_POLL_MS * 3);
    ctrl.expectNone('/stations.json');
  });

  it('stale view asks again the moment the tab comes back', () => {
    // Returning to a tab is exactly when the count on screen is most stale, so
    // it is asked at once rather than up to four seconds later.
    const fixture = mounted();
    hidden = 'hidden';
    document.dispatchEvent(new Event('visibilitychange'));
    fixture.detectChanges();
    ctrl.expectNone('/stations.json');

    hidden = 'visible';
    document.dispatchEvent(new Event('visibilitychange'));
    fixture.detectChanges();
    ctrl.expectOne('/stations.json').flush({ stations: [{ ...stations[0], listeners: 4 }] });
    fixture.detectChanges();
    expect(fixture.nativeElement.textContent).toContain('4 listening');
  });

  it('stale view does not restart the timer for an answer that lands after the tab hides', () => {
    // THE RACE BETWEEN THE TWO. A poll is already in flight when the listener
    // switches tabs, and the answer arrives afterwards -- so the check has to
    // be where the answer is handled as well as on the visibility event, or a
    // backgrounded tab quietly schedules itself again.
    const fixture = TestBed.createComponent(Dial);
    const inFlight = ctrl.expectOne('/stations.json');

    hidden = 'hidden';
    inFlight.flush({ stations });
    fixture.detectChanges();

    vi.advanceTimersByTime(DIAL_POLL_MS * 3);
    ctrl.expectNone('/stations.json');
  });

  it('stale view leaves no timer behind when the dial goes away', () => {
    const filling = [{ ...stations[0], ready: false, tracks: 3 }];
    const fixture = TestBed.createComponent(Dial);
    ctrl.expectOne('/stations.json').flush({ stations: filling });
    fixture.detectChanges();

    fixture.destroy();
    vi.advanceTimersByTime(DIAL_POLL_MS * 3);
    // A timer left running polls for ever on a page nobody is looking at.
    ctrl.expectNone('/stations.json');
  });
});
