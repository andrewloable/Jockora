import { describe, expect, it, beforeEach, vi } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Stations } from './stations.component';
import { Derived } from '../api/api';

const stations = [
  { id: 1, name: 'ROCK', genre: 'rock', genres: ['rock'], moods: [], tracks: 412, enabled: true },
  {
    id: 3,
    name: 'AMBIENT',
    genre: 'ambient',
    mood: 'calm',
    genres: ['ambient'],
    moods: ['calm'],
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
    // DEFAULT: yes. Destructive actions ask now, and every test that was
    // written before they did is still testing what happens after the answer.
    // The tests about the QUESTION stub it themselves.
    vi.stubGlobal('confirm', () => true);
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

  // Tick exactly these boxes in a checkbox group, clicking any that need to
  // change. Clicking rather than assigning .checked, because the component
  // reads the event target and an assignment fires no event.
  function choose(
    fixture: { detectChanges(): void; nativeElement: HTMLElement },
    sel: string,
    values: string[],
  ) {
    const boxes = Array.from(
      fixture.nativeElement.querySelectorAll(sel + ' input[type=checkbox]'),
    ) as HTMLInputElement[];
    for (const box of boxes) {
      if (box.checked !== values.includes(box.value)) {
        box.click();
        fixture.detectChanges();
      }
    }
  }

  function mounted(list: unknown[] = stations) {
    const fixture = TestBed.createComponent(Stations);
    ctrl.expectOne('/admin/stations').flush(list);
    ctrl.expectOne('/admin/vocab').flush(vocab);
    ctrl.expectOne('/admin/jocks').flush(jocks);
    fixture.detectChanges();
    return fixture;
  }

  // ------------------------------------------------------ console states --
  // Jockora-e9a.50: loading and empty rendered identically, so a slow first
  // paint told a new operator their library was empty.

  it('console states tells a fresh operator what to do when there are no stations', () => {
    const empty = mounted([]).nativeElement.querySelector('[data-empty]');
    expect(empty).not.toBeNull();
    expect(empty.textContent).toContain('Add a station');
  });

  it('console states does not call a loading stations table an empty one', () => {
    const fixture = TestBed.createComponent(Stations);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-empty]')).toBeNull();
    expect(fixture.nativeElement.querySelector('[data-loading]')).not.toBeNull();
    ctrl.expectOne('/admin/stations').flush([]);
    ctrl.expectOne('/admin/vocab').flush(vocab);
    ctrl.expectOne('/admin/jocks').flush(jocks);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-loading]')).toBeNull();
    expect(fixture.nativeElement.querySelector('[data-empty]')).not.toBeNull();
  });

  it('console states shows the warning when a station is below the track threshold', () => {
    // e9a.25 set minimum 10 and warn 50. The column existed and was empty in
    // every screenshot taken, because the install reviewed had no thin station.
    const thin = mounted([
      {
        id: 9,
        name: 'SEA SHANTY',
        genre: 'folk',
        genres: ['folk'],
        moods: [],
        tracks: 12,
        enabled: false,
        warning: 'fewer tracks than most stations',
      },
    ]);
    const cell = thin.nativeElement.querySelector('[data-warning]');
    expect(cell).not.toBeNull();
    expect(cell.textContent).toContain('fewer tracks');
  });

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
    const genres = fixture.nativeElement.querySelectorAll('[data-genre] input[type=checkbox]');
    expect(Array.from(genres).map((o) => (o as HTMLInputElement).value)).toEqual(vocab.genres);
    // CHECKBOXES, not a multiple select: picking two things that are not next
    // to each other needed ctrl-click, which is a keyboard trick people do not
    // know and cannot see.
    const moods = fixture.nativeElement.querySelectorAll('[data-mood] input[type=checkbox]');
    expect(moods.length).toBe(vocab.moods.length);
    // Ticking NOTHING is how you say any, for both, and the form says so.
    expect(fixture.nativeElement.querySelector('[data-genre]').textContent).toContain('any genre');
  });

  it('creates a station with the genre and mood that were chosen', () => {
    const fixture = mounted();
    const name = fixture.nativeElement.querySelector('[data-name]');
    name.value = 'CALM AMBIENT';
    name.dispatchEvent(new Event('input'));
    choose(fixture, '[data-genre]', ['ambient', 'rock']);
    choose(fixture, '[data-mood]', ['calm']);

    fixture.nativeElement.querySelector('[data-add]').click();
    const req = ctrl.expectOne('/admin/stations');
    // VOCABULARY ORDER, not click order: the selection is rebuilt by filtering
    // the vocabulary, so the same station saved twice is the same string.
    expect(req.request.body).toEqual({
      name: 'CALM AMBIENT',
      genres: ['rock', 'ambient'],
      moods: ['calm'],
    });
    req.flush({ id: 9, tracks: 96 });
    ctrl.expectOne('/admin/stations').flush(stations);
  });

  it('handles a server with an empty vocabulary', () => {
    // A build whose vocabulary lists came back empty must not preselect a
    // genre that does not exist. Nothing is preselected now in any case: no
    // genre means every genre.
    const fixture = TestBed.createComponent(Stations);
    ctrl.expectOne('/admin/stations').flush([]);
    ctrl.expectOne('/admin/vocab').flush({ genres: [], moods: [] });
    ctrl.expectOne('/admin/jocks').flush([]);
    fixture.detectChanges();
    expect(fixture.componentInstance.genres()).toEqual([]);
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
    // NOTHING CHOSEN IS A REAL ANSWER: every genre, every mood.
    expect(req.request.body).toEqual({ name: 'ROCK', genres: [], moods: [] });
    req.flush({ id: 9, tracks: 412 });
    ctrl.expectOne('/admin/stations').flush(stations);
    fixture.detectChanges();
    // A NUMBER, not a promise.
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('412 tracks');
  });

  it('renders the threshold refusal with its numbers', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelectorAll('[data-toggle]')[1].click();
    ctrl
      .expectOne('/admin/stations/3/enable')
      .flush(
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
    // Through the EDIT form, which is where a station's settings now live: the
    // jock used to be a live select that saved on change while the genre beside
    // it did not, so half the row committed instantly and half needed a button.
    const fixture = mounted();
    fixture.nativeElement.querySelectorAll('[data-edit]')[0].click();
    fixture.detectChanges();
    type(fixture, '[data-jock]', 'dutch');
    fixture.nativeElement.querySelector('[data-save]').click();
    ctrl.expectOne('/admin/stations/1').flush({ added: 0, removed: 0, kept: 412 });
    const on = ctrl.expectOne('/admin/stations/1/jock');
    expect(on.request.body).toEqual({ jock_id: 'dutch' });
    on.flush(null);
    ctrl.expectOne('/admin/stations').flush([{ ...stations[0], jock_id: 'dutch' }, stations[1]]);
    fixture.detectChanges();

    fixture.nativeElement.querySelectorAll('[data-edit]')[0].click();
    fixture.detectChanges();
    type(fixture, '[data-jock]', '');
    fixture.nativeElement.querySelector('[data-save]').click();
    ctrl.expectOne('/admin/stations/1').flush({ added: 0, removed: 0, kept: 412 });
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
    ctrl
      .expectOne('/admin/stations')
      .flush([
        { id: 1, name: 'Night Rock', genre: 'rock', jock_id: 'dutch', enabled: true, tracks: 134 },
      ]);
    ctrl.expectOne('/admin/vocab').flush(vocab);
    fixture.detectChanges();
    ctrl.expectOne('/admin/jocks').flush(jocks);
    fixture.detectChanges();

    // The row shows the jock by NAME when it is not being edited.
    expect(fixture.nativeElement.querySelector('[data-stations]').textContent).toContain('Dutch');

    fixture.nativeElement.querySelectorAll('[data-edit]')[0].click();
    fixture.detectChanges();
    const select: HTMLSelectElement = fixture.nativeElement.querySelector('[data-jock]');
    expect(select.value).toBe('dutch');
    expect(select.selectedOptions[0].textContent?.trim()).toBe('Dutch');
  });

  it('keeps the ticked genres and moods ticked', () => {
    // The vocabulary arrives over HTTP, so the boxes are rendered from data
    // that lands after the component does. What is ticked has to survive that.
    const fixture = mounted();
    choose(fixture, '[data-genre]', ['ambient']);
    choose(fixture, '[data-mood]', ['raw']);

    const ticked = (sel: string) =>
      Array.from(fixture.nativeElement.querySelectorAll(sel + ' input[type=checkbox]'))
        .filter((b) => (b as HTMLInputElement).checked)
        .map((b) => (b as HTMLInputElement).value);
    expect(ticked('[data-genre]')).toEqual(['ambient']);
    expect(ticked('[data-mood]')).toEqual(['raw']);
  });

  it('shows no jock when a station has none', () => {
    const fixture = mounted([{ id: 1, name: 'Rock', genre: 'rock', enabled: true, tracks: 90 }]);
    expect(fixture.nativeElement.querySelector('[data-stations]').textContent).toContain('no jock');
    fixture.nativeElement.querySelectorAll('[data-edit]')[0].click();
    fixture.detectChanges();
    const select: HTMLSelectElement = fixture.nativeElement.querySelector('[data-jock]');
    expect(select.value).toBe('');
  });

  it('names the jock it put on air and says when it takes effect', () => {
    // Reported once as "there is no way to save a selected jockey": the jock
    // saved on change and said nothing, so it looked like nothing happened.
    // There is a Save button now, but naming the jock still matters.
    const fixture = mounted();
    fixture.nativeElement.querySelectorAll('[data-edit]')[0].click();
    fixture.detectChanges();
    type(fixture, '[data-jock]', 'dutch');
    fixture.nativeElement.querySelector('[data-save]').click();
    ctrl.expectOne('/admin/stations/1').flush({ added: 0, removed: 0, kept: 412 });
    ctrl.expectOne('/admin/stations/1/jock').flush(null);
    ctrl.expectOne('/admin/stations').flush(stations);
    fixture.detectChanges();

    const said = fixture.nativeElement.querySelector('[data-said]').textContent;
    expect(said).toContain('Dutch');
    expect(said).toContain('next break');
  });

  it('says so when a jock is taken off a station', () => {
    const fixture = mounted([{ ...stations[0], jock_id: 'dutch' }]);
    fixture.nativeElement.querySelectorAll('[data-edit]')[0].click();
    fixture.detectChanges();
    type(fixture, '[data-jock]', '');
    fixture.nativeElement.querySelector('[data-save]').click();
    ctrl.expectOne('/admin/stations/1').flush({ added: 0, removed: 0, kept: 412 });
    ctrl.expectOne('/admin/stations/1/jock').flush(null);
    ctrl.expectOne('/admin/stations').flush(stations);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('music only');
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
    fixture.componentInstance.playlist.subscribe((s: unknown) => (asked = s));
    fixture.nativeElement.querySelectorAll('[data-playlist]')[0].click();
    expect(asked).toEqual(stations[0]);
  });

  describe('editing a station', () => {
    // A station's name, genre and mood were fixed at creation: the only way to
    // change one was to delete it and build it again, losing every pin and
    // exclude on its playlist. The SERVER could already do it -- PUT
    // /admin/stations/{id} validates, saves and regenerates -- and the console
    // simply never called it.
    function editing(fixture: ReturnType<typeof mounted>) {
      fixture.nativeElement.querySelectorAll('[data-edit]')[0].click();
      fixture.detectChanges();
      return fixture;
    }

    it("opens a row for editing with the station's values in it", () => {
      const fixture = editing(mounted());
      expect(fixture.nativeElement.querySelector('[data-edit-name]').value).toBe('ROCK');
      // The SELECT must have the station's genre chosen, not the first option:
      // the vocabulary arrives over HTTP, and this is the same trap the add
      // form documents.
      expect(
        Array.from(fixture.nativeElement.querySelectorAll('[data-edit-genre] input[type=checkbox]'))
          .filter((b) => (b as HTMLInputElement).checked)
          .map((b) => (b as HTMLInputElement).value),
      ).toEqual(['rock']);
    });

    it('saves the change and reports what regenerating did to the playlist', () => {
      const fixture = editing(mounted());
      type(fixture, '[data-edit-name]', 'CLASSIC ROCK');
      choose(fixture, '[data-edit-genre]', ['ambient']);
      choose(fixture, '[data-edit-mood]', ['calm']);
      fixture.nativeElement.querySelector('[data-save]').click();

      const req = ctrl.expectOne('/admin/stations/1');
      expect(req.request.method).toBe('PUT');
      expect(req.request.body).toEqual({
        name: 'CLASSIC ROCK',
        genres: ['ambient'],
        moods: ['calm'],
      });
      // CHANGING THE GENRE RESTATES THE PLAYLIST, which is the whole reason an
      // edit is not just a rename. The operator has to be told.
      req.flush({ added: 40, removed: 380, kept: 32 });
      ctrl.expectOne('/admin/stations').flush(stations);
      fixture.detectChanges();

      const said = fixture.nativeElement.querySelector('[data-said]').textContent;
      expect(said).toContain('40');
      expect(said).toContain('380');
      expect(said).toContain('32');
      // And the row is closed again.
      expect(fixture.nativeElement.querySelector('[data-edit-name]')).toBeNull();
    });

    it('cancels without sending anything', () => {
      const fixture = editing(mounted());
      type(fixture, '[data-edit-name]', 'SOMETHING ELSE');
      fixture.nativeElement.querySelector('[data-cancel]').click();
      fixture.detectChanges();
      expect(fixture.nativeElement.querySelector('[data-edit-name]')).toBeNull();
      // The name in the table is untouched, and no request was made.
      expect(fixture.nativeElement.querySelector('[data-stations]').textContent).toContain('ROCK');
      ctrl.verify();
    });

    it("repeats the server's reason for refusing an edit", () => {
      const fixture = editing(mounted());
      fixture.nativeElement.querySelector('[data-save]').click();
      ctrl
        .expectOne('/admin/stations/1')
        .flush({ error: 'name is already taken' }, { status: 400, statusText: 'Bad Request' });
      fixture.detectChanges();
      expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
        'already taken',
      );
      // STILL OPEN, so the operator can fix it rather than retype it.
      expect(fixture.nativeElement.querySelector('[data-edit-name]')).not.toBeNull();

      // And a refusal that carries no message at all still says something.
      fixture.nativeElement.querySelector('[data-save]').click();
      ctrl.expectOne('/admin/stations/1').flush(null, { status: 500, statusText: 'Error' });
      fixture.detectChanges();
      expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
        'Could not save',
      );
    });

    it('edits only the row that was opened', () => {
      const fixture = editing(mounted());
      expect(fixture.nativeElement.querySelectorAll('[data-edit-name]').length).toBe(1);
    });

    it('leaves the jock alone when it was not touched', () => {
      // Saving a rename must not rewrite the jock: an unchanged value still
      // costs a request, and a request that writes is a request that can fail.
      const fixture = editing(mounted());
      type(fixture, '[data-edit-name]', 'ROCK II');
      fixture.nativeElement.querySelector('[data-save]').click();
      ctrl.expectOne('/admin/stations/1').flush({ added: 1, removed: 2, kept: 3 });
      ctrl.expectOne('/admin/stations').flush(stations);
      ctrl.verify();
    });

    it('says so when the jock will not save', () => {
      const fixture = editing(mounted());
      type(fixture, '[data-jock]', 'dutch');
      fixture.nativeElement.querySelector('[data-save]').click();
      ctrl.expectOne('/admin/stations/1').flush({ added: 0, removed: 0, kept: 412 });
      ctrl.expectOne('/admin/stations/1/jock').flush(null, { status: 500, statusText: 'Error' });
      fixture.detectChanges();
      expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('jock');
    });
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
    ctrl
      .expectOne('/admin/stations')
      .flush(
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

    fixture.nativeElement.querySelectorAll('[data-edit]')[0].click();
    fixture.detectChanges();
    type(fixture, '[data-jock]', 'dutch');
    fixture.nativeElement.querySelector('[data-save]').click();
    // The STATION saved and the jock did not, which has to read as a different
    // outcome from the whole edit failing.
    ctrl.expectOne('/admin/stations/1').flush({ added: 0, removed: 0, kept: 412 });
    ctrl.expectOne('/admin/stations/1/jock').flush(null, { status: 400, statusText: 'Bad' });
    ctrl.expectOne('/admin/stations').flush(stations);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
      'jock could not be assigned',
    );
  });

  it('blames the mood when a genre+mood station comes back empty', () => {
    // Measured against a real library: the model is TOLD to leave mood empty
    // when nothing fits, so a genre+mood station can be empty while the same
    // genre alone is full. "0 of 10 needed" sent the operator looking at the
    // genre, which was never the problem.
    const fixture = mounted();
    type(fixture, '[data-name]', 'Night Rock');
    choose(fixture, '[data-mood]', ['calm']);
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
    const fixture = mounted([{ id: 4, name: 'Rock', genre: 'rock', enabled: false, tracks: 4 }]);
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

  // THE PICKER RENDERS THE WHOLE VOCABULARY. It always did; what it did not do
  // was let the reader SEE it -- a rule meant for text fields sized every
  // checkbox like an entry box, and the grid was capped at three visible rows
  // of 42 options with no sign there was more. The markup contract is what the
  // stylesheet keys off, so it is pinned here and the rules themselves are
  // pinned by test/console_css_test.go.
  it('tag picker offers every option in the vocabulary, not a scrolled few', () => {
    const fixture = mounted();
    const genres = fixture.nativeElement.querySelectorAll('[data-genre] label');
    const moods = fixture.nativeElement.querySelectorAll('[data-mood] label');
    expect(genres.length).toBe(vocab.genres.length);
    expect(moods.length).toBe(vocab.moods.length);
  });

  it('tag picker uses checkboxes, which is what keeps them out of the field sizing', () => {
    const fixture = mounted();
    const inputs = [...fixture.nativeElement.querySelectorAll('[data-genre] input')];
    expect(inputs.length).toBeGreaterThan(0);
    expect(inputs.every((i: HTMLInputElement) => i.type === 'checkbox')).toBe(true);
  });

  // ON A PHONE every admin table becomes a card. These tables drew their
  // action buttons past the right edge of a 390px viewport with nothing to
  // scroll -- rendered and unreachable, so a station could not be edited,
  // disabled, deleted or have its playlist opened at all.
  it('narrow stations labels every cell with the column it replaces', () => {
    const fixture = mounted();
    const headers = [...fixture.nativeElement.querySelectorAll('[data-stations] thead th')].map(
      (h) => (h as HTMLElement).textContent?.trim(),
    );
    for (const row of fixture.nativeElement.querySelectorAll('[data-stations] tbody tr')) {
      const cells = [...row.querySelectorAll('td')];
      expect(cells.length).toBe(headers.length);
      cells.forEach((cell, i) => {
        expect(cell.getAttribute('data-label')).toBe(headers[i]);
      });
    }
  });

  it('narrow stations labels the cells of a row being edited too', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-edit]').click();
    fixture.detectChanges();
    const row = fixture.nativeElement.querySelector('[data-stations] tbody tr');
    for (const cell of row.querySelectorAll('td')) {
      expect(cell.getAttribute('data-label')).toBeTruthy();
    }
  });
});

// -------------------------------------------------------- stations brief --
//
// Jockora-g1t.6. MAKING A STATION MEANS DESCRIBING IT. Ticking boxes against
// two closed vocabularies is a form that makes the operator do the model's job;
// the boxes stay, they just stop being where you start.
describe('stations brief', () => {
  let ctrl: HttpTestingController;

  beforeEach(async () => {
    vi.stubGlobal('confirm', () => true);
    await TestBed.configureTestingModule({
      imports: [Stations],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  function mounted(list: unknown[] = []) {
    const fixture = TestBed.createComponent(Stations);
    ctrl.expectOne('/admin/stations').flush(list);
    ctrl.expectOne('/admin/vocab').flush(vocab);
    ctrl.expectOne('/admin/jocks').flush(jocks);
    fixture.detectChanges();
    return fixture;
  }

  function typeBrief(f: { nativeElement: HTMLElement; detectChanges(): void }, text: string) {
    const box = f.nativeElement.querySelector('[data-brief]') as HTMLTextAreaElement;
    box.value = text;
    box.dispatchEvent(new Event('input'));
    f.detectChanges();
  }

  const derived: Derived = {
    name: 'Night Rock',
    genres: ['rock'],
    moods: ['raw'],
    year_min: 1975,
    year_max: 2005,
    // The server always answers all four -- they are required in the schema, so
    // an absent bound and an unbounded one cannot be confused. Zero is
    // unbounded.
    tempo_min: 0,
    tempo_max: 0,
    duration_min_s: 0,
    duration_max_s: 0,
    tracks: 412,
  };

  function describeIt(f: { nativeElement: HTMLElement; detectChanges(): void }, answer = derived) {
    (f.nativeElement.querySelector('[data-describe]') as HTMLButtonElement).click();
    const req = ctrl.expectOne('/admin/stations/derive');
    req.flush(answer);
    f.detectChanges();
    return req;
  }

  it('stations brief describes a brief and shows the tags, years and count', () => {
    const fixture = mounted();
    typeBrief(fixture, 'late-night rock for driving, nothing after 2005');
    const req = describeIt(fixture);

    expect(req.request.body).toEqual({ brief: 'late-night rock for driving, nothing after 2005' });

    // THE TAGS STAY VISIBLE. An operator who cannot see the filter cannot fix
    // it, and this feature's failure mode is a plausible-looking wrong answer.
    expect(fixture.componentInstance.genres()).toEqual(['rock']);
    expect(fixture.componentInstance.moods()).toEqual(['raw']);
    expect(fixture.componentInstance.yearMin()).toBe(1975);
    expect(fixture.componentInstance.yearMax()).toBe(2005);
    expect(fixture.nativeElement.querySelector('[data-derived]')!.textContent).toContain('412');
  });

  it('stations brief pre-fills the suggested name and lets it be typed over', () => {
    const fixture = mounted();
    typeBrief(fixture, 'late-night rock');
    describeIt(fixture);
    expect(fixture.componentInstance.name()).toBe('Night Rock');

    const nameBox = fixture.nativeElement.querySelector('[data-name]') as HTMLInputElement;
    nameBox.value = 'AFTER HOURS';
    nameBox.dispatchEvent(new Event('input'));
    fixture.detectChanges();
    expect(fixture.componentInstance.name()).toBe('AFTER HOURS');
  });

  // THE WHOLE POINT OF PREVIEW-THEN-CONFIRM: what is saved is what is ON
  // SCREEN, including whatever the operator changed after the derive.
  it('stations brief saves the parameters on screen, not the brief alone', () => {
    const fixture = mounted();
    typeBrief(fixture, 'late-night rock');
    describeIt(fixture);

    // The operator disagrees with one of them.
    fixture.componentInstance.moods.set(['calm']);
    fixture.componentInstance.yearMax.set(1999);
    fixture.detectChanges();

    (fixture.nativeElement.querySelector('[data-add]') as HTMLButtonElement).click();
    const req = ctrl.expectOne('/admin/stations');
    expect(req.request.body).toEqual({
      name: 'Night Rock',
      genres: ['rock'],
      moods: ['calm'],
      brief: 'late-night rock',
      year_min: 1975,
      year_max: 1999,
    });
    req.flush({ id: 1, tracks: 88 });
    ctrl.expectOne('/admin/stations').flush([]);
    fixture.detectChanges();
  });

  it('stations brief does not call derive on save', () => {
    const fixture = mounted();
    typeBrief(fixture, 'late-night rock');
    describeIt(fixture);

    (fixture.nativeElement.querySelector('[data-add]') as HTMLButtonElement).click();
    ctrl.expectOne('/admin/stations').flush({ id: 1, tracks: 5 });
    ctrl.expectOne('/admin/stations').flush([]);
    // Re-deriving would produce a DIFFERENT station from the one approved.
    ctrl.expectNone('/admin/stations/derive');
  });

  // A 503 is "no model", and the server's sentence names both ways out. Leaving
  // the operator at a dead button is the failure this opens the door against.
  it('stations brief opens the manual path when there is no model', () => {
    const fixture = mounted();
    typeBrief(fixture, 'late-night rock');
    (fixture.nativeElement.querySelector('[data-describe]') as HTMLButtonElement).click();
    ctrl.expectOne('/admin/stations/derive').flush(
      {
        error:
          'no language model is configured. Choose one on the Model page, or set the ' +
          'genres and moods by hand.',
      },
      { status: 503, statusText: 'Service Unavailable' },
    );
    fixture.detectChanges();

    expect(fixture.nativeElement.querySelector('[data-said]')!.textContent).toContain('by hand');
    const manual = fixture.nativeElement.querySelector('[data-manual]') as HTMLDetailsElement;
    expect(manual.open).toBe(true);
  });

  it('stations brief leaves the form alone when the model itself fails', () => {
    const fixture = mounted();
    typeBrief(fixture, 'late-night rock');
    fixture.componentInstance.genres.set(['ambient']);
    (fixture.nativeElement.querySelector('[data-describe]') as HTMLButtonElement).click();
    ctrl
      .expectOne('/admin/stations/derive')
      .flush(
        { error: 'the language model could not do it: rejected the API key' },
        { status: 502, statusText: 'Bad Gateway' },
      );
    fixture.detectChanges();

    expect(fixture.nativeElement.querySelector('[data-said]')!.textContent).toContain('API key');
    // NOTHING WAS TOUCHED: a 502 is the model failing, not the operator being
    // wrong, and wiping their tags would punish them for it.
    expect(fixture.componentInstance.genres()).toEqual(['ambient']);
    const manual = fixture.nativeElement.querySelector('[data-manual]') as HTMLDetailsElement;
    expect(manual.open).toBe(false);
  });

  it('stations brief still creates a station by hand with no brief at all', () => {
    const fixture = mounted();
    fixture.componentInstance.name.set('ROCK');
    fixture.componentInstance.genres.set(['rock']);
    fixture.detectChanges();

    (fixture.nativeElement.querySelector('[data-add]') as HTMLButtonElement).click();
    const req = ctrl.expectOne('/admin/stations');
    expect(req.request.body).toEqual({ name: 'ROCK', genres: ['rock'], moods: [] });
    req.flush({ id: 1, tracks: 9 });
    ctrl.expectOne('/admin/stations').flush([]);
  });

  it('stations brief loads a stored brief into the row editor and re-describes it', () => {
    const fixture = mounted([
      {
        id: 3,
        name: 'AMBIENT',
        genre: 'ambient',
        genres: ['ambient'],
        moods: ['calm'],
        brief: 'Plays ambient. Feels calm.',
        year_min: 0,
        year_max: 0,
        tracks: 12,
        enabled: true,
      },
    ]);
    (fixture.nativeElement.querySelector('[data-edit]') as HTMLButtonElement).click();
    fixture.detectChanges();
    expect(fixture.componentInstance.editBrief()).toBe('Plays ambient. Feels calm.');

    (fixture.nativeElement.querySelector('[data-edit-describe]') as HTMLButtonElement).click();
    ctrl.expectOne('/admin/stations/derive').flush(derived);
    fixture.detectChanges();

    // The suggestion REPLACES the ticked boxes and the years.
    expect(fixture.componentInstance.editGenres()).toEqual(['rock']);
    expect(fixture.componentInstance.editYearMin()).toBe(1975);
  });

  it('stations brief will not describe nothing, and says so while it is working', () => {
    const fixture = mounted();
    const button = () =>
      fixture.nativeElement.querySelector('[data-describe]') as HTMLButtonElement;
    // BLANK: there is nothing to derive from.
    expect(button().disabled).toBe(true);

    typeBrief(fixture, 'late-night rock');
    expect(button().disabled).toBe(false);

    button().click();
    fixture.detectChanges();
    // IN FLIGHT: a live LLM call takes seconds on a local model, and an
    // unlabelled button that does nothing reads as broken.
    expect(button().disabled).toBe(true);
    expect(button().textContent!.toLowerCase()).toContain('describ');
    ctrl.expectOne('/admin/stations/derive').flush(derived);
    fixture.detectChanges();
    expect(button().disabled).toBe(false);
  });

  it('stations brief round-trips the years and sends none when they are cleared', () => {
    const fixture = mounted();
    fixture.componentInstance.name.set('N');
    fixture.componentInstance.genres.set(['rock']);
    fixture.componentInstance.yearMin.set(1980);
    fixture.componentInstance.yearMax.set(1989);
    fixture.detectChanges();

    (fixture.nativeElement.querySelector('[data-add]') as HTMLButtonElement).click();
    const withYears = ctrl.expectOne('/admin/stations');
    expect(withYears.request.body).toMatchObject({ year_min: 1980, year_max: 1989 });
    withYears.flush({ id: 1, tracks: 4 });
    ctrl.expectOne('/admin/stations').flush([]);
    fixture.detectChanges();

    // CLEARED means unbounded, and an unbounded side is simply not sent --
    // a zero would be a year the server has to guess the meaning of.
    fixture.componentInstance.name.set('N2');
    fixture.componentInstance.genres.set(['rock']);
    fixture.componentInstance.yearMin.set(0);
    fixture.componentInstance.yearMax.set(0);
    fixture.detectChanges();
    (fixture.nativeElement.querySelector('[data-add]') as HTMLButtonElement).click();
    const noYears = ctrl.expectOne('/admin/stations');
    expect(noYears.request.body).not.toHaveProperty('year_min');
    expect(noYears.request.body).not.toHaveProperty('year_max');
    noYears.flush({ id: 2, tracks: 4 });
    ctrl.expectOne('/admin/stations').flush([]);
  });

  it('stations brief takes the years and the description from the boxes themselves', () => {
    const fixture = mounted();
    const type = (sel: string, value: string) => {
      const el = fixture.nativeElement.querySelector(sel) as HTMLInputElement;
      el.value = value;
      el.dispatchEvent(new Event('input'));
      fixture.detectChanges();
    };
    type('[data-year-min]', '1980');
    type('[data-year-max]', '1989');
    expect(fixture.componentInstance.yearMin()).toBe(1980);
    expect(fixture.componentInstance.yearMax()).toBe(1989);
  });

  it('stations brief takes the row editor’s boxes too', () => {
    const fixture = mounted([
      {
        id: 3,
        name: 'AMBIENT',
        genre: 'ambient',
        genres: ['ambient'],
        moods: [],
        brief: 'Plays ambient.',
        tracks: 12,
        enabled: true,
      },
    ]);
    (fixture.nativeElement.querySelector('[data-edit]') as HTMLButtonElement).click();
    fixture.detectChanges();

    const type = (sel: string, value: string) => {
      const el = fixture.nativeElement.querySelector(sel) as HTMLInputElement;
      el.value = value;
      el.dispatchEvent(new Event('input'));
      fixture.detectChanges();
    };
    type('[data-edit-brief]', 'something else entirely');
    type('[data-edit-year-min]', '1990');
    type('[data-edit-year-max]', '1999');
    expect(fixture.componentInstance.editBrief()).toBe('something else entirely');
    expect(fixture.componentInstance.editYearMin()).toBe(1990);
    expect(fixture.componentInstance.editYearMax()).toBe(1999);
  });

  // A server mid-upgrade, or one that answered with only what it was sure of.
  // A partial answer must leave empty lists rather than undefined ones, or the
  // checkbox loop renders nothing and the form looks broken.
  it('stations brief survives a derive that answers with almost nothing', () => {
    const fixture = mounted();
    typeBrief(fixture, 'late-night rock');
    describeIt(fixture, { name: 'Sparse' } as unknown as typeof derived);
    expect(fixture.componentInstance.genres()).toEqual([]);
    expect(fixture.componentInstance.moods()).toEqual([]);
    expect(fixture.componentInstance.yearMin()).toBe(0);
    expect(fixture.componentInstance.yearMax()).toBe(0);
  });

  it('stations brief reports a failed re-describe without losing the row', () => {
    const fixture = mounted([
      {
        id: 3,
        name: 'AMBIENT',
        genre: 'ambient',
        genres: ['ambient'],
        moods: ['calm'],
        brief: 'Plays ambient.',
        tracks: 12,
        enabled: true,
      },
    ]);
    (fixture.nativeElement.querySelector('[data-edit]') as HTMLButtonElement).click();
    fixture.detectChanges();
    (fixture.nativeElement.querySelector('[data-edit-describe]') as HTMLButtonElement).click();
    ctrl
      .expectOne('/admin/stations/derive')
      .flush(null, { status: 502, statusText: 'Bad Gateway' });
    fixture.detectChanges();

    expect(fixture.nativeElement.querySelector('[data-said]')!.textContent).toContain(
      'Could not describe',
    );
    // THE ROW STAYS OPEN with what it had: a failed suggestion is not a reason
    // to throw away the operator's station.
    expect(fixture.componentInstance.editGenres()).toEqual(['ambient']);
    expect(fixture.componentInstance.editing()).toBe(3);
  });

  it('stations brief survives an edit derive that answers with almost nothing', () => {
    const fixture = mounted([
      {
        id: 3,
        name: 'AMBIENT',
        genre: 'ambient',
        genres: ['ambient'],
        moods: ['calm'],
        brief: 'Plays ambient.',
        tracks: 12,
        enabled: true,
      },
    ]);
    (fixture.nativeElement.querySelector('[data-edit]') as HTMLButtonElement).click();
    fixture.detectChanges();
    (fixture.nativeElement.querySelector('[data-edit-describe]') as HTMLButtonElement).click();
    ctrl.expectOne('/admin/stations/derive').flush({ name: 'Sparse' });
    fixture.detectChanges();
    expect(fixture.componentInstance.editGenres()).toEqual([]);
    expect(fixture.componentInstance.editYearMin()).toBe(0);
  });

  it('stations brief says the row editor is working, not sitting there', () => {
    const fixture = mounted([
      {
        id: 3,
        name: 'AMBIENT',
        genre: 'ambient',
        genres: ['ambient'],
        moods: ['calm'],
        brief: 'Plays ambient.',
        tracks: 12,
        enabled: true,
      },
    ]);
    (fixture.nativeElement.querySelector('[data-edit]') as HTMLButtonElement).click();
    fixture.detectChanges();
    const button = () =>
      fixture.nativeElement.querySelector('[data-edit-describe]') as HTMLButtonElement;

    button().click();
    fixture.detectChanges();
    // A live model call takes seconds on a local model, and an unlabelled
    // button that does nothing reads as broken -- in the row editor exactly as
    // in the add form.
    expect(button().disabled).toBe(true);
    expect(button().textContent!.toLowerCase()).toContain('describing');

    ctrl.expectOne('/admin/stations/derive').flush(derived);
    fixture.detectChanges();
    expect(button().disabled).toBe(false);
    expect(button().textContent!.toLowerCase()).not.toContain('describing');
  });

  it('stations brief says so when the derive fails with no words at all', () => {
    const fixture = mounted();
    typeBrief(fixture, 'late-night rock');
    (fixture.nativeElement.querySelector('[data-describe]') as HTMLButtonElement).click();
    ctrl.expectOne('/admin/stations/derive').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]')!.textContent).toContain(
      'Could not describe',
    );
  });

  it('stations brief says whether the count is enough to run', () => {
    const fixture = mounted();
    typeBrief(fixture, 'something obscure');
    describeIt(fixture, { ...derived, tracks: 7, warning: 'too few tracks to run' });

    const warn = fixture.nativeElement.querySelector('[data-derived-warning]');
    expect(warn).not.toBeNull();
    expect(warn!.textContent).toContain('too few');
    // ADVICE, NOT A REFUSAL: the save button is untouched, because twelve deep
    // cuts is a real station somebody may want.
    expect((fixture.nativeElement.querySelector('[data-add]') as HTMLButtonElement).disabled).toBe(
      false,
    );
  });

  it('stations brief stays quiet when the count is comfortable', () => {
    const fixture = mounted();
    typeBrief(fixture, 'late-night rock');
    describeIt(fixture, { ...derived, tracks: 412, warning: '' });
    expect(fixture.nativeElement.querySelector('[data-derived-warning]')).toBeNull();
  });

  it('stations brief explains a slow derive while it is still running', () => {
    const fixture = mounted();
    typeBrief(fixture, 'late-night rock');
    (fixture.nativeElement.querySelector('[data-describe]') as HTMLButtonElement).click();
    fixture.detectChanges();

    // The operator started enrichment this morning and has no way to connect
    // the two on their own.
    const note = fixture.nativeElement.querySelector('[data-describing-note]');
    expect(note).not.toBeNull();
    expect(note!.textContent!.toLowerCase()).toContain('enrichment');
    expect(note!.textContent!.toLowerCase()).toContain('overview');

    ctrl.expectOne('/admin/stations/derive').flush(derived);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-describing-note]')).toBeNull();
  });

  it('stations brief shows the server’s words when the model runs out of time', () => {
    const fixture = mounted();
    typeBrief(fixture, 'late-night rock');
    (fixture.nativeElement.querySelector('[data-describe]') as HTMLButtonElement).click();
    ctrl.expectOne('/admin/stations/derive').flush(
      {
        error:
          'the language model did not answer in time. Enrichment may be running and using ' +
          'it; you can pause that on the Overview. The genres and moods can also be set by hand.',
      },
      { status: 504, statusText: 'Gateway Timeout' },
    );
    fixture.detectChanges();

    // THE SERVER'S SENTENCE, not a generic fallback. A status-code-only test
    // passes happily while those words are thrown away.
    const said = fixture.nativeElement.querySelector('[data-said]')!.textContent!;
    expect(said).toContain('Enrichment may be running');
    expect(said).toContain('Overview');
  });

  it('stations brief stops an over-long description in the box', () => {
    const fixture = mounted();
    const box = fixture.nativeElement.querySelector('[data-brief]') as HTMLTextAreaElement;
    // Before the round trip, so the operator never meets the flat 4KB refusal.
    expect(box.maxLength).toBe(fixture.componentInstance.maxBrief);
    const edit = mounted([
      {
        id: 3,
        name: 'A',
        genre: 'ambient',
        genres: ['ambient'],
        moods: [],
        brief: 'x',
        tracks: 1,
        enabled: true,
      },
    ]);
    (edit.nativeElement.querySelector('[data-edit]') as HTMLButtonElement).click();
    edit.detectChanges();
    expect(
      (edit.nativeElement.querySelector('[data-edit-brief]') as HTMLTextAreaElement).maxLength,
    ).toBe(edit.componentInstance.maxBrief);
  });

  // Jockora-g1t.13. The stations table has carried tempo and length since
  // migration 10; the derive returns them since g1t.11 and the API carries them
  // since g1t.12. Without this they reach the console and stop there.

  const paced: Derived = {
    name: 'Runners',
    genres: ['rock'],
    moods: ['raw'],
    year_min: 0,
    year_max: 0,
    tempo_min: 150,
    tempo_max: 180,
    duration_min_s: 0,
    duration_max_s: 240,
    tracks: 90,
  };

  function value(f: { nativeElement: HTMLElement }, sel: string) {
    return (f.nativeElement.querySelector(sel) as HTMLInputElement).value;
  }

  function set(f: { nativeElement: HTMLElement; detectChanges(): void }, sel: string, v: string) {
    const el = f.nativeElement.querySelector(sel) as HTMLInputElement;
    el.value = v;
    el.dispatchEvent(new Event('input'));
    f.detectChanges();
  }

  it('stations brief shows the tempo and length the AI chose', () => {
    const fixture = mounted();
    typeBrief(fixture, 'something to run to, nothing over four minutes');
    describeIt(fixture, paced);

    expect(value(fixture, '[data-tempo-min]')).toBe('150');
    expect(value(fixture, '[data-tempo-max]')).toBe('180');
    // MINUTES ON SCREEN. Nobody describes a song as 240 seconds.
    expect(value(fixture, '[data-length-max]')).toBe('4');
    // An unbounded side stays BLANK. A zero in the box reads as a bound the
    // operator did not set, and clearing it would then be their job.
    expect(value(fixture, '[data-length-min]')).toBe('');
  });

  it('stations brief sends the length in seconds and the tempo in BPM', () => {
    const fixture = mounted();
    typeBrief(fixture, 'something to run to');
    describeIt(fixture, paced);

    // The AI gave a ceiling and no floor; the operator adds one. Both boxes
    // are typed into, so the binding is proved rather than assumed.
    set(fixture, '[data-length-min]', '2');
    set(fixture, '[data-length-max]', '4');
    (fixture.nativeElement.querySelector('[data-add]') as HTMLButtonElement).click();

    const req = ctrl.expectOne('/admin/stations');
    expect(req.request.body.tempo_min).toBe(150);
    expect(req.request.body.tempo_max).toBe(180);
    // Minutes in the box, seconds on the wire.
    expect(req.request.body.duration_min_s).toBe(120);
    expect(req.request.body.duration_max_s).toBe(240);
    req.flush({ id: 3, tracks: 90 });
    ctrl.expectOne('/admin/stations').flush([]);
  });

  it('stations brief keeps a cleared range cleared', () => {
    const fixture = mounted();
    typeBrief(fixture, 'something to run to');
    describeIt(fixture, paced);

    // The operator decides the pace was wrong and empties both boxes.
    set(fixture, '[data-tempo-min]', '');
    set(fixture, '[data-tempo-max]', '');
    (fixture.nativeElement.querySelector('[data-add]') as HTMLButtonElement).click();

    const req = ctrl.expectOne('/admin/stations');
    // ABSENT, NOT ZERO. The server reads a zero as unbounded too, but sending
    // one is the console deciding rather than the operator -- and a cleared
    // box has to stay cleared.
    expect('tempo_min' in req.request.body).toBe(false);
    expect('tempo_max' in req.request.body).toBe(false);
    expect('duration_min_s' in req.request.body).toBe(false);
    req.flush({ id: 3, tracks: 90 });
    ctrl.expectOne('/admin/stations').flush([]);
  });

  it('stations brief carries the ranges through the row editor', () => {
    const fixture = mounted([
      {
        id: 1,
        name: 'RUNNERS',
        genre: 'rock',
        genres: ['rock'],
        moods: [],
        enabled: true,
        tracks: 90,
        tempo_min: 150,
        tempo_max: 180,
        duration_max_s: 240,
      },
    ]);
    (fixture.nativeElement.querySelector('[data-edit]') as HTMLButtonElement).click();
    fixture.detectChanges();

    // THE ROW, IN THE FORM. An operator who opens an edit and finds the ranges
    // blank has lost them, and saving would widen the station silently.
    expect(value(fixture, '[data-edit-tempo-min]')).toBe('150');
    expect(value(fixture, '[data-edit-tempo-max]')).toBe('180');
    expect(value(fixture, '[data-edit-length-max]')).toBe('4');
    expect(value(fixture, '[data-edit-length-min]')).toBe('');

    // AND THEY ARE EDITABLE, not merely displayed. Typing into every one of
    // them proves the binding both ways -- a box that shows the right value
    // and ignores what is typed into it is the worse of the two failures,
    // because it looks like it worked.
    set(fixture, '[data-edit-tempo-min]', '140');
    set(fixture, '[data-edit-tempo-max]', '170');
    set(fixture, '[data-edit-length-min]', '1.5');
    set(fixture, '[data-edit-length-max]', '5');

    (fixture.nativeElement.querySelector('[data-save]') as HTMLButtonElement).click();
    const req = ctrl.expectOne('/admin/stations/1');
    expect(req.request.method).toBe('PUT');
    expect(req.request.body.tempo_min).toBe(140);
    expect(req.request.body.tempo_max).toBe(170);
    // 1.5 minutes is 90 seconds, and 5 is 300. The conversion happens in one
    // place and this is the only test that can see it round a half.
    expect(req.request.body.duration_min_s).toBe(90);
    expect(req.request.body.duration_max_s).toBe(300);
    req.flush({ added: 0, removed: 0, kept: 90 });
  });

  it('stations brief shows the server sentence when a range is refused', () => {
    const fixture = mounted();
    set(fixture, '[data-name]', 'Runners');
    set(fixture, '[data-tempo-min]', '900');
    (fixture.nativeElement.querySelector('[data-add]') as HTMLButtonElement).click();
    ctrl
      .expectOne('/admin/stations')
      .flush(
        { field: 'tempo_min', error: 'station: impossible range: tempo 900 is outside 30 to 250' },
        { status: 400, statusText: 'Bad Request' },
      );
    fixture.detectChanges();
    expect(fixture.nativeElement.textContent).toContain('tempo 900 is outside 30 to 250');
  });
});
