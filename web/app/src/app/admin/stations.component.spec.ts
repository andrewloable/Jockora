import { describe, expect, it, beforeEach, vi } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Stations } from './stations.component';

const stations = [
  { id: 1, name: 'ROCK', genre: 'rock', tracks: 412, enabled: true },
  {
    id: 3,
    name: 'AMBIENT',
    genre: 'ambient',
    mood: 'calm',
    tracks: 12,
    enabled: false,
    warning: 'fewer tracks than most stations',
  },
];
const jocks = [{ id: 'dutch', name: 'Dutch' }];
const vocab = { genres: ['rock', 'ambient'], moods: ['calm', 'raw'] };

describe('Stations', () => {
  let ctrl: HttpTestingController;

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [Stations],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  function mounted() {
    const fixture = TestBed.createComponent(Stations);
    ctrl.expectOne('/admin/stations').flush(stations);
    ctrl.expectOne('/admin/vocab').flush(vocab);
    ctrl.expectOne('/admin/jocks').flush(jocks);
    fixture.detectChanges();
    return fixture;
  }

  it('lists the stations with their counts and warnings', () => {
    const text = mounted().nativeElement.querySelector('[data-stations]').textContent;
    expect(text).toContain('ROCK');
    expect(text).toContain('412');
    expect(text).toContain('ambient · calm');
    // The console has to say which stations cannot run BEFORE anyone tries.
    expect(text).toContain('fewer tracks');
  });

  it('offers the vocabulary rather than a text box', () => {
    // A station whose genre is a typo is one that will never fill, with
    // nothing on screen to say why.
    const fixture = mounted();
    const genres = fixture.nativeElement.querySelectorAll('[data-genre] option');
    expect(Array.from(genres).map((o) => (o as HTMLOptionElement).value)).toEqual(vocab.genres);
    const moods = fixture.nativeElement.querySelectorAll('[data-mood] option');
    // Plus "any mood", which is what most stations want.
    expect(moods.length).toBe(vocab.moods.length + 1);
  });

  it('creates a station with the genre and mood that were chosen', () => {
    const fixture = mounted();
    const name = fixture.nativeElement.querySelector('[data-name]');
    name.value = 'CALM AMBIENT';
    name.dispatchEvent(new Event('input'));
    const genre = fixture.nativeElement.querySelector('[data-genre]');
    genre.value = 'ambient';
    genre.dispatchEvent(new Event('change'));
    const mood = fixture.nativeElement.querySelector('[data-mood]');
    mood.value = 'calm';
    mood.dispatchEvent(new Event('change'));

    fixture.nativeElement.querySelector('[data-add]').click();
    const req = ctrl.expectOne('/admin/stations');
    expect(req.request.body).toEqual({ name: 'CALM AMBIENT', genre: 'ambient', mood: 'calm' });
    req.flush({ id: 9, tracks: 96 });
    ctrl.expectOne('/admin/stations').flush(stations);
  });

  it('handles a server with an empty vocabulary', () => {
    // A build whose vocabulary lists came back empty must not preselect a
    // genre that does not exist.
    const fixture = TestBed.createComponent(Stations);
    ctrl.expectOne('/admin/stations').flush([]);
    ctrl.expectOne('/admin/vocab').flush({ genres: [], moods: [] });
    ctrl.expectOne('/admin/jocks').flush([]);
    fixture.detectChanges();
    expect(fixture.componentInstance.genre()).toBe('');
  });

  it('says nothing when a comfortable station is enabled', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelectorAll('[data-toggle]')[1].click();
    ctrl.expectOne('/admin/stations/3/enable').flush(null);
    ctrl.expectOne('/admin/stations').flush(stations);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent.trim()).toBe('');
  });

  it('creates a station and says how many tracks it got', () => {
    const fixture = mounted();
    const name = fixture.nativeElement.querySelector('[data-name]');
    name.value = 'ROCK';
    name.dispatchEvent(new Event('input'));
    fixture.nativeElement.querySelector('[data-add]').click();

    const req = ctrl.expectOne('/admin/stations');
    expect(req.request.body).toEqual({ name: 'ROCK', genre: 'rock', mood: '' });
    req.flush({ id: 9, tracks: 412 });
    ctrl.expectOne('/admin/stations').flush(stations);
    fixture.detectChanges();
    // A NUMBER, not a promise.
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('412 tracks');
  });

  it('renders the threshold refusal with its numbers', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelectorAll('[data-toggle]')[1].click();
    ctrl.expectOne('/admin/stations/3/enable').flush(
      { error: 'too few tracks to run', tracks: 9, minimum: 10 },
      { status: 409, statusText: 'Conflict' },
    );
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('9 of 10');
  });

  it('shows the warning when a thin station is enabled anyway', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelectorAll('[data-toggle]')[1].click();
    ctrl.expectOne('/admin/stations/3/enable').flush({ tracks: 12, warning: 'fewer than most' });
    ctrl.expectOne('/admin/stations').flush(stations);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('fewer than');
  });

  it('assigns and unassigns a jock', () => {
    const fixture = mounted();
    const select = fixture.nativeElement.querySelector('[data-jock]');
    select.value = 'dutch';
    select.dispatchEvent(new Event('change'));
    const on = ctrl.expectOne('/admin/stations/1/jock');
    expect(on.request.body).toEqual({ jock_id: 'dutch' });
    on.flush(null);
    ctrl.expectOne('/admin/stations').flush(stations);

    select.value = '';
    select.dispatchEvent(new Event('change'));
    // null UNASSIGNS: a station with no jock still plays music.
    const off = ctrl.expectOne('/admin/stations/1/jock');
    expect(off.request.body).toEqual({ jock_id: null });
    off.flush(null);
    ctrl.expectOne('/admin/stations').flush(stations);
  });

  it('asks before deleting, and does not delete when told no', () => {
    const fixture = mounted();
    const confirmSpy = vi.spyOn(globalThis, 'confirm').mockReturnValue(false);
    fixture.nativeElement.querySelectorAll('[data-remove]')[0].click();
    ctrl.expectNone('/admin/stations/1');

    confirmSpy.mockReturnValue(true);
    fixture.nativeElement.querySelectorAll('[data-remove]')[0].click();
    ctrl.expectOne('/admin/stations/1').flush(null);
    ctrl.expectOne('/admin/stations').flush(stations);
    confirmSpy.mockRestore();
  });

  it('asks the console to open a playlist', () => {
    const fixture = mounted();
    let asked: unknown;
    fixture.componentInstance.edit.subscribe((s: unknown) => (asked = s));
    fixture.nativeElement.querySelectorAll('[data-edit]')[0].click();
    expect(asked).toEqual(stations[0]);
  });

  it('says so when anything fails', () => {
    const fixture = TestBed.createComponent(Stations);
    ctrl.expectOne('/admin/stations').flush(null, { status: 500, statusText: 'Error' });
    ctrl.expectOne('/admin/vocab').flush(null, { status: 500, statusText: 'Error' });
    ctrl.expectOne('/admin/jocks').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Could not');
  });

  it('says so when a create, toggle, delete or assign fails', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-add]').click();
    ctrl.expectOne('/admin/stations').flush(
      { field: 'genre', error: 'not in the vocabulary' },
      { status: 400, statusText: 'Bad Request' },
    );
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('vocabulary');

    fixture.nativeElement.querySelector('[data-add]').click();
    ctrl.expectOne('/admin/stations').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Could not');

    fixture.nativeElement.querySelectorAll('[data-toggle]')[0].click();
    ctrl.expectOne('/admin/stations/1/disable').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Could not');

    const confirmSpy = vi.spyOn(globalThis, 'confirm').mockReturnValue(true);
    fixture.nativeElement.querySelectorAll('[data-remove]')[0].click();
    ctrl.expectOne('/admin/stations/1').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Could not');
    confirmSpy.mockRestore();

    const select = fixture.nativeElement.querySelector('[data-jock]');
    select.value = 'dutch';
    select.dispatchEvent(new Event('change'));
    ctrl.expectOne('/admin/stations/1/jock').flush(null, { status: 400, statusText: 'Bad' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('on air');
  });
});
