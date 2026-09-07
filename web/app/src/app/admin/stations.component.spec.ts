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

  function type(fixture: { nativeElement: HTMLElement }, selector: string, value: string) {
    const el = fixture.nativeElement.querySelector(selector) as HTMLInputElement;
    el.value = value;
    el.dispatchEvent(new Event(el.tagName === 'SELECT' ? 'change' : 'input'));
    (fixture as unknown as { detectChanges(): void }).detectChanges();
  }

  function mounted(list: unknown[] = stations) {
    const fixture = TestBed.createComponent(Stations);
    ctrl.expectOne('/admin/stations').flush(list);
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

  it('shows the jock already on air, even when the jocks arrive last', () => {
    // THE RACE. The select's [value] binding was applied before @for had
    // rendered any options, so the browser fell back to the first one and the
    // binding never re-ran -- the station's jock simply vanished from the
    // picker on every reload, while the server had it all along. Reported as
    // "i set the station jock then went to playlists then went back to
    // stations and the dj i selected is gone".
    const fixture = TestBed.createComponent(Stations);
    // Stations first, jocks LAST, which is the order that broke it.
    ctrl.expectOne('/admin/stations').flush([
      { id: 1, name: 'Night Rock', genre: 'rock', jock_id: 'dutch', enabled: true, tracks: 134 },
    ]);
    ctrl.expectOne('/admin/vocab').flush(vocab);
    fixture.detectChanges();
    ctrl.expectOne('/admin/jocks').flush(jocks);
    fixture.detectChanges();

    const select: HTMLSelectElement = fixture.nativeElement.querySelector('[data-jock]');
    expect(select.value).toBe('dutch');
    expect(select.selectedOptions[0].textContent?.trim()).toBe('Dutch');
  });

  it('keeps the chosen genre and mood selected', () => {
    // Same race as the jock picker: these options come from the vocabulary
    // over HTTP. Pinned so a later edit cannot reintroduce [value] on a select
    // whose options are async.
    const fixture = mounted();
    type(fixture, '[data-genre]', 'ambient');
    type(fixture, '[data-mood]', 'raw');

    const genre: HTMLSelectElement = fixture.nativeElement.querySelector('[data-genre]');
    const mood: HTMLSelectElement = fixture.nativeElement.querySelector('[data-mood]');
    expect(genre.value).toBe('ambient');
    expect(mood.value).toBe('raw');
    expect(genre.selectedOptions[0].textContent?.trim()).toBe('ambient');
  });

  it('shows no jock when a station has none', () => {
    const fixture = mounted([
      { id: 1, name: 'Rock', genre: 'rock', enabled: true, tracks: 90 },
    ]);
    const select: HTMLSelectElement = fixture.nativeElement.querySelector('[data-jock]');
    expect(select.value).toBe('');
  });

  it('says so when a jock is put on air, because there is no Save button', () => {
    // Reported as "there is no way to save a selected jockey": it saves on
    // change, correctly, but said nothing, so it looked like nothing happened.
    const fixture = mounted();
    const select = fixture.nativeElement.querySelector('[data-jock]');
    select.value = 'dutch';
    select.dispatchEvent(new Event('change'));
    ctrl.expectOne('/admin/stations/1/jock').flush(null);
    ctrl.expectOne('/admin/stations').flush(stations);
    fixture.detectChanges();

    const said = fixture.nativeElement.querySelector('[data-said]').textContent;
    expect(said).toContain('Dutch');
    expect(said).toContain('next break');
  });

  it('says so when a jock is taken off a station', () => {
    const fixture = mounted();
    const select = fixture.nativeElement.querySelector('[data-jock]');
    select.value = '';
    select.dispatchEvent(new Event('change'));
    ctrl.expectOne('/admin/stations/1/jock').flush(null);
    ctrl.expectOne('/admin/stations').flush(stations);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
      'music only',
    );
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

  it('blames the mood when a genre+mood station comes back empty', () => {
    // Measured against a real library: the model is TOLD to leave mood empty
    // when nothing fits, so a genre+mood station can be empty while the same
    // genre alone is full. "0 of 10 needed" sent the operator looking at the
    // genre, which was never the problem.
    const fixture = mounted();
    type(fixture, '[data-name]', 'Night Rock');
    type(fixture, '[data-mood]', 'calm');
    fixture.nativeElement.querySelector('[data-add]').click();
    ctrl.expectOne('/admin/stations').flush({ id: 9, tracks: 0 });
    ctrl.expectOne('/admin/stations').flush([]);
    fixture.detectChanges();

    const said = fixture.nativeElement.querySelector('[data-said]').textContent;
    expect(said).toContain('Added with 0 tracks');
    expect(said).toContain('calm');
    expect(said).toContain('Clearing the mood');
  });

  it('says nothing about mood when the station filled', () => {
    const fixture = mounted();
    type(fixture, '[data-name]', 'Rock');
    type(fixture, '[data-mood]', 'calm');
    fixture.nativeElement.querySelector('[data-add]').click();
    ctrl.expectOne('/admin/stations').flush({ id: 9, tracks: 40 });
    ctrl.expectOne('/admin/stations').flush([]);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).not.toContain(
      'Clearing the mood',
    );
  });

  it('blames the mood when enabling is refused for too few tracks', () => {
    const fixture = mounted([
      { id: 3, name: 'Night Rock', genre: 'rock', mood: 'nocturnal', enabled: false, tracks: 4 },
    ]);
    fixture.nativeElement.querySelector('[data-toggle]').click();
    ctrl
      .expectOne('/admin/stations/3/enable')
      .flush({ tracks: 4, minimum: 10 }, { status: 409, statusText: 'Conflict' });
    fixture.detectChanges();
    const said = fixture.nativeElement.querySelector('[data-said]').textContent;
    expect(said).toContain('4 of 10 needed');
    expect(said).toContain('nocturnal');
  });

  it('does not blame a mood a station does not have', () => {
    const fixture = mounted([
      { id: 4, name: 'Rock', genre: 'rock', enabled: false, tracks: 4 },
    ]);
    fixture.nativeElement.querySelector('[data-toggle]').click();
    ctrl
      .expectOne('/admin/stations/4/enable')
      .flush({ tracks: 4, minimum: 10 }, { status: 409, statusText: 'Conflict' });
    fixture.detectChanges();
    const said = fixture.nativeElement.querySelector('[data-said]').textContent;
    expect(said).toContain('4 of 10 needed');
    expect(said).not.toContain('Clearing the mood');
  });

  it('labels its columns', () => {
    // A grid of bare values makes the reader infer what each column is from
    // whatever the first row happens to contain -- and "rock" in a column of
    // its own could be a genre, a tag or a mood.
    const head = mounted().nativeElement.querySelector('[data-stations] thead').textContent;
    expect(head).toContain('Name');
    expect(head).toContain('Genre');
    expect(head).toContain('Tracks');
    expect(head).toContain('Jock');
    expect(head).toContain('Actions');
  });
});
