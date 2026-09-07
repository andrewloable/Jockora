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
  { track_id: 1, artist: 'A', title: 'One', pinned: false, excluded: false, missing: false },
  { track_id: 2, artist: 'B', title: 'Two', pinned: true, excluded: false, missing: false },
  { track_id: 3, artist: 'C', title: 'Three', pinned: false, excluded: true, missing: false },
  { track_id: 4, artist: 'D', title: 'Four', pinned: false, excluded: false, missing: true },
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
    expect(fixture.nativeElement.querySelectorAll('[data-playlist] tr').length).toBe(4);
    expect(fixture.nativeElement.querySelector('[data-playlist]').textContent).toContain('One');
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
});
