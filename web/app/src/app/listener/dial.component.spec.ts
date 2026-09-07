import { describe, expect, it, beforeEach } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Dial } from './dial.component';

const stations = [
  { id: 1, name: 'ROCK', genre: 'rock', jock_name: 'Dutch', tracks: 412, listeners: 2 },
  { id: 3, name: 'AMBIENT', genre: 'ambient', mood: 'calm', tracks: 96, listeners: 0 },
];

describe('Dial', () => {
  let ctrl: HttpTestingController;

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [Dial],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  function mounted() {
    const fixture = TestBed.createComponent(Dial);
    ctrl.expectOne('/stations.json').flush({ stations });
    fixture.detectChanges();
    return fixture;
  }

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
    const longest = Math.max(...line.trim().split(/\s+/).map((w: string) => w.length));
    expect(longest).toBeLessThanOrEqual(12);
  });
});
