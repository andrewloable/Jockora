import { describe, expect, it, beforeEach } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { Component, signal } from '@angular/core';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { PAGE, Playlist } from './playlist.component';

@Component({
  standalone: true,
  imports: [Playlist],
  template: `<app-playlist [station]="station()" />`,
})
class Host {
  readonly station = signal(3);
}

const tracks = [
  {
    track_id: 1,
    artist: 'A',
    title: 'One',
    album: 'Nevermind',
    year: 1991,
    pinned: false,
    excluded: false,
    missing: false,
    genres: ['rock', 'grunge'],
    moods: ['angsty'],
    bpm: 128.4,
  },
  {
    track_id: 2,
    artist: 'B',
    title: 'Two',
    pinned: true,
    excluded: false,
    missing: false,
    genres: [],
    moods: [],
  },
  {
    track_id: 3,
    artist: 'C',
    title: 'Three',
    pinned: false,
    excluded: true,
    missing: false,
    genres: [],
    moods: [],
  },
  {
    track_id: 4,
    artist: 'D',
    title: 'Four',
    pinned: false,
    excluded: false,
    missing: true,
    genres: [],
    moods: [],
  },
];

describe('Playlist', () => {
  let ctrl: HttpTestingController;

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [Host],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  function mounted(total = 4) {
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    ctrl.expectOne(`/admin/stations/3/tracks?limit=${PAGE}&offset=0`).flush({ tracks, total });
    fixture.detectChanges();
    return fixture;
  }

  it('lists a page with the total', () => {
    // "Showing 50 of 1,200" rather than guessing when to stop.
    const fixture = mounted(1200);
    expect(fixture.nativeElement.querySelector('[data-count]').textContent).toContain('1200');
    expect(fixture.nativeElement.querySelectorAll('[data-playlist] tbody tr').length).toBe(4);
    expect(fixture.nativeElement.querySelector('[data-playlist]').textContent).toContain('One');
  });

  it('shows a track\'s genres and moods for reference', () => {
    const fixture = mounted();
    expect(fixture.nativeElement.querySelector('[data-playlist]').textContent).toContain(
      'rock, grunge',
    );
    expect(fixture.nativeElement.querySelector('[data-playlist]').textContent).toContain(
      'angsty',
    );
  });

  it('shows an excluded track rather than hiding it', () => {
    // An operator has to be able to find it to put it back.
    const rows = mounted().nativeElement.querySelectorAll('[data-exclude]');
    expect(rows[2].textContent.trim()).toBe('Include');
  });

  it('flags a missing track and refuses to pin it', () => {
    const fixture = mounted();
    expect(fixture.nativeElement.querySelector('[data-gone]')).toBeTruthy();
    const pins = fixture.nativeElement.querySelectorAll('[data-pin]');
    expect(pins[3].disabled).toBe(true);
  });

  it('pins and unpins', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelectorAll('[data-pin]')[0].click();
    ctrl.expectOne('/admin/stations/3/tracks/1/pin').flush(null);
    ctrl.expectOne(`/admin/stations/3/tracks?limit=${PAGE}&offset=0`).flush({ tracks, total: 4 });

    fixture.nativeElement.querySelectorAll('[data-pin]')[1].click();
    ctrl.expectOne('/admin/stations/3/tracks/2/unpin').flush(null);
    ctrl.expectOne(`/admin/stations/3/tracks?limit=${PAGE}&offset=0`).flush({ tracks, total: 4 });
  });

  it('excludes and includes', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelectorAll('[data-exclude]')[0].click();
    ctrl.expectOne('/admin/stations/3/tracks/1/exclude').flush(null);
    ctrl.expectOne(`/admin/stations/3/tracks?limit=${PAGE}&offset=0`).flush({ tracks, total: 4 });

    fixture.nativeElement.querySelectorAll('[data-exclude]')[2].click();
    ctrl.expectOne('/admin/stations/3/tracks/3/unexclude').flush(null);
    ctrl.expectOne(`/admin/stations/3/tracks?limit=${PAGE}&offset=0`).flush({ tracks, total: 4 });
  });

  it('explains why a missing track cannot be pinned', () => {
    const fixture = mounted();
    fixture.componentInstance.station.set(3);
    fixture.nativeElement.querySelectorAll('[data-pin]')[0].click();
    ctrl
      .expectOne('/admin/stations/3/tracks/1/pin')
      .flush(null, { status: 409, statusText: 'Conflict' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('missing');
  });

  it('pages', () => {
    const fixture = mounted(120);
    expect(fixture.nativeElement.querySelector('[data-prev]').disabled).toBe(true);

    fixture.nativeElement.querySelector('[data-next]').click();
    ctrl
      .expectOne(`/admin/stations/3/tracks?limit=${PAGE}&offset=${PAGE}`)
      .flush({ tracks, total: 120 });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-prev]').disabled).toBe(false);

    fixture.nativeElement.querySelector('[data-prev]').click();
    ctrl.expectOne(`/admin/stations/3/tracks?limit=${PAGE}&offset=0`).flush({ tracks, total: 120 });
  });

  it('shows the diff after regenerating', () => {
    // What CHANGED, rather than a redrawn list to compare by eye.
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-regenerate]').click();
    ctrl
      .expectOne('/admin/stations/3/regenerate')
      .flush({ added: 3, removed: 1, kept: 400 });
    ctrl.expectOne(`/admin/stations/3/tracks?limit=${PAGE}&offset=0`).flush({ tracks, total: 4 });
    fixture.detectChanges();
    const said = fixture.nativeElement.querySelector('[data-said]').textContent;
    expect(said).toContain('Added 3');
    expect(said).toContain('removed 1');
    expect(said).toContain('kept 400');
  });

  it('follows the operator to another station', () => {
    const fixture = mounted();
    fixture.componentInstance.station.set(9);
    fixture.detectChanges();
    ctrl.expectOne(`/admin/stations/9/tracks?limit=${PAGE}&offset=0`).flush({ tracks, total: 4 });
  });

  it('says so when anything fails', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelectorAll('[data-pin]')[0].click();
    ctrl
      .expectOne('/admin/stations/3/tracks/1/pin')
      .flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Could not');

    fixture.nativeElement.querySelector('[data-regenerate]').click();
    ctrl
      .expectOne('/admin/stations/3/regenerate')
      .flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Could not');

    fixture.componentInstance.station.set(11);
    fixture.detectChanges();
    ctrl
      .expectOne(`/admin/stations/11/tracks?limit=${PAGE}&offset=0`)
      .flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Could not');
  });

  it('names its columns', () => {
    // A curator needs to know which column is which, and Album is what says
    // whether a run of tracks is one record.
    const fixture = mounted(3);
    const headers = Array.from(
      fixture.nativeElement.querySelectorAll('[data-playlist] thead th'),
    ).map((h) => (h as HTMLElement).textContent?.trim());
    expect(headers).toEqual([
      'Artist',
      'Title',
      'Album',
      'Year',
      'Tempo',
      'Genre',
      'Mood',
      'Status',
      'Actions',
    ]);
  });

  it('shows the album and year it was given', () => {
    const fixture = mounted(1);
    const row = fixture.nativeElement.querySelector('[data-playlist] tbody tr');
    expect(row.textContent).toContain('Nevermind');
    expect(row.textContent).toContain('1991');
  });

  it('says pinned and excluded in words, not only on the buttons', () => {
    const fixture = mounted(3);
    const text = fixture.nativeElement.querySelector('[data-playlist]').textContent;
    expect(text).toContain('pinned');
    expect(text).toContain('excluded');
  });
});

// RE-TAGGING A TRACK FROM THE PLAYLIST. This is the screen an operator is
// already on when they notice a track is filed wrong; sending them elsewhere to
// fix it is how it never gets fixed.
describe('Playlist tag editing', () => {
  let ctrl: HttpTestingController;

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [Host],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  const vocab = {
    genres: ['rock', 'grunge', 'synthwave', 'pop', 'jazz', 'folk'],
    moods: ['angsty', 'nocturnal', 'calm'],
  };

  function open(row = 0, flushVocab = true) {
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    ctrl.expectOne(`/admin/stations/3/tracks?limit=${PAGE}&offset=0`).flush({ tracks, total: 4 });
    fixture.detectChanges();

    fixture.nativeElement.querySelectorAll('[data-tags]')[row].click();
    if (flushVocab) {
      ctrl.expectOne('/admin/vocab').flush(vocab);
    }
    fixture.detectChanges();
    return fixture;
  }

  function tick(fixture: { nativeElement: HTMLElement }, selector: string, value: string) {
    const box = fixture.nativeElement.querySelector(
      `${selector} input[value="${value}"]`,
    ) as HTMLInputElement;
    box.checked = !box.checked;
    box.dispatchEvent(new Event('change'));
  }

  it('opens on the track\'s current tags', () => {
    const fixture = open();
    const ticked = [...fixture.nativeElement.querySelectorAll('[data-edit-genres] input')].filter(
      (i: HTMLInputElement) => i.checked,
    );
    expect(ticked.map((i: HTMLInputElement) => i.value)).toEqual(['rock', 'grunge']);
  });

  it('asks for the vocabulary once, not on every page view', () => {
    // 42 genres on a page load nobody edits is a request nobody asked for.
    const fixture = open();
    fixture.nativeElement.querySelector('[data-cancel-tags]').click();
    fixture.detectChanges();
    fixture.nativeElement.querySelectorAll('[data-tags]')[1].click();
    ctrl.expectNone('/admin/vocab');
  });

  it('saves the ticked tags and redraws the row from the answer', () => {
    const fixture = open();
    tick(fixture, '[data-edit-genres]', 'synthwave');
    tick(fixture, '[data-edit-moods]', 'nocturnal');
    fixture.detectChanges();

    fixture.nativeElement.querySelector('[data-save-tags]').click();
    const req = ctrl.expectOne('/admin/tracks/1/tags');
    expect(req.request.method).toBe('PUT');
    expect(req.request.body).toEqual({
      genres: ['rock', 'grunge', 'synthwave'],
      moods: ['angsty', 'nocturnal'],
    });
    req.flush({ genres: ['synthwave'], moods: ['nocturnal'], overridden: true });
    fixture.detectChanges();

    // The SERVER'S answer, not what was sent: a revert has no idea what it is
    // going back to until the server says.
    const row = fixture.nativeElement.querySelectorAll('[data-playlist] tbody tr')[0];
    expect(row.textContent).toContain('synthwave');
    expect(row.querySelector('[data-edited]')).toBeTruthy();
    expect(fixture.nativeElement.querySelector('[data-tag-editor]')).toBeNull();
    // And the operator is told the station has not moved yet.
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Regenerate');
  });

  it('offers to undo only where there is an edit to undo', () => {
    const fixture = open();
    expect(fixture.nativeElement.querySelector('[data-revert-tags]')).toBeNull();

    fixture.nativeElement.querySelector('[data-save-tags]').click();
    ctrl
      .expectOne('/admin/tracks/1/tags')
      .flush({ genres: ['synthwave'], moods: [], overridden: true });
    fixture.detectChanges();

    fixture.nativeElement.querySelectorAll('[data-tags]')[0].click();
    fixture.detectChanges();
    fixture.nativeElement.querySelector('[data-revert-tags]').click();
    const req = ctrl.expectOne('/admin/tracks/1/tags');
    expect(req.request.method).toBe('DELETE');
    req.flush({ genres: ['rock', 'grunge'], moods: ['angsty'], overridden: false });
    fixture.detectChanges();

    const row = fixture.nativeElement.querySelectorAll('[data-playlist] tbody tr')[0];
    expect(row.textContent).toContain('rock, grunge');
    expect(row.querySelector('[data-edited]')).toBeNull();
  });

  it('stops at five tags, which is what a dossier itself may carry', () => {
    const fixture = open(1);
    for (const g of ['rock', 'grunge', 'synthwave', 'pop', 'jazz']) {
      tick(fixture, '[data-edit-genres]', g);
      fixture.detectChanges();
    }
    const spare = fixture.nativeElement.querySelector(
      '[data-edit-genres] input[value="folk"]',
    ) as HTMLInputElement;
    expect(spare.disabled).toBe(true);
    // A ticked box stays clickable, or the fifth tag could never be removed.
    const ticked = fixture.nativeElement.querySelector(
      '[data-edit-genres] input[value="jazz"]',
    ) as HTMLInputElement;
    expect(ticked.disabled).toBe(false);
  });

  it('says so when the vocabulary, a save or an undo fails', () => {
    const fixture = open(0, false);
    const said = () => fixture.nativeElement.querySelector('[data-said]').textContent;

    ctrl.expectOne('/admin/vocab').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(said()).toContain('Could not read the list of genres');

    fixture.nativeElement.querySelector('[data-save-tags]').click();
    ctrl.expectOne('/admin/tracks/1/tags').flush(null, { status: 400, statusText: 'Bad' });
    fixture.detectChanges();
    expect(said()).toContain('Could not save those tags');
    // The editor STAYS OPEN on a failure, with the ticks the operator made:
    // closing it would throw away the edit and leave them to redo it blind.
    expect(fixture.nativeElement.querySelector('[data-tag-editor]')).toBeTruthy();

    // An undo that fails. The row has to be an edited one for the button to be
    // offered at all, so the save lands first.
    fixture.nativeElement.querySelector('[data-save-tags]').click();
    ctrl
      .expectOne('/admin/tracks/1/tags')
      .flush({ genres: ['synthwave'], moods: [], overridden: true });
    fixture.detectChanges();
    fixture.nativeElement.querySelectorAll('[data-tags]')[0].click();
    fixture.detectChanges();
    fixture.nativeElement.querySelector('[data-revert-tags]').click();
    ctrl.expectOne('/admin/tracks/1/tags').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(said()).toContain('Could not undo that edit');
  });
});

// A mood's tempo range decides whether a track is on this playlist at all, and
// until now nothing on the row said how fast it was.
describe('Playlist mood tempo', () => {
  let ctrl: HttpTestingController;

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [Host],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  it('shows the measured tempo, and nothing where it is unmeasured', () => {
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    ctrl.expectOne(`/admin/stations/3/tracks?limit=${PAGE}&offset=0`).flush({ tracks, total: 4 });
    fixture.detectChanges();

    const cells = fixture.nativeElement.querySelectorAll('[data-bpm]');
    // Rounded: a tempo to one decimal place is a measurement, not a fact about
    // the music, and the extra digit is noise in a column of a hundred rows.
    expect(cells[0].textContent.trim()).toBe('128');
    // ZERO IS UNMEASURED, not a silent track. Showing "0" would read as a fact.
    expect(cells[1].textContent.trim()).toBe('');
  });
});

// ON A PHONE the actions were the rightmost of seven columns, 654px into a
// 390px window, inside a table that scrolls sideways with nothing on screen to
// say so. The fix is a stacked card per row, and a card can only name its
// fields if every cell carries its label.
describe('Playlist narrow', () => {
  let ctrl: HttpTestingController;

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [Host],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  function rows(fixture: { nativeElement: HTMLElement }) {
    return [...fixture.nativeElement.querySelectorAll('[data-playlist] tbody tr')];
  }

  function listed() {
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    ctrl.expectOne(`/admin/stations/3/tracks?limit=${PAGE}&offset=0`).flush({ tracks, total: 4 });
    fixture.detectChanges();
    return fixture;
  }

  it('narrow playlist labels every cell so a stacked row can name its columns', () => {
    const fixture = listed();
    const headers = [...fixture.nativeElement.querySelectorAll('[data-playlist] thead th')].map(
      (h) => (h as HTMLElement).textContent?.trim(),
    );
    for (const row of rows(fixture)) {
      const cells = [...row.querySelectorAll('td')];
      expect(cells.length).toBe(headers.length);
      cells.forEach((cell, i) => {
        // The label matches the column it replaces: a card headed "Year" over
        // an album title is worse than no card at all.
        expect(cell.getAttribute('data-label')).toBe(headers[i]);
      });
    }
  });

  it('narrow playlist keeps pin and exclude in every row', () => {
    for (const row of rows(listed())) {
      expect(row.querySelector('[data-pin]')).toBeTruthy();
      expect(row.querySelector('[data-exclude]')).toBeTruthy();
    }
  });
});
