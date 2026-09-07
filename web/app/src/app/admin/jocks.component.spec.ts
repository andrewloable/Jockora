import { describe, expect, it, beforeEach } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Jocks } from './jocks.component';

const jocks = [
  {
    id: 'dutch',
    name: 'Dutch',
    voice_id: 'am_fenrir',
    good_for_genres: ['rock', 'metal'],
    good_for_moods: ['raw'],
    speech_style: 'LOUD',
    personality: 'Shouts about rock.',
    forbidden: [],
  },
];

describe('Jocks', () => {
  let ctrl: HttpTestingController;

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [Jocks],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  function mounted() {
    const fixture = TestBed.createComponent(Jocks);
    ctrl.expectOne('/admin/jocks').flush(jocks);
    ctrl.expectOne('/admin/voices').flush({ voices: ['am_fenrir', 'af_heart'] });
    fixture.detectChanges();
    return fixture;
  }

  function type(fixture: { nativeElement: HTMLElement }, selector: string, value: string) {
    const el = fixture.nativeElement.querySelector(selector) as HTMLInputElement;
    el.value = value;
    el.dispatchEvent(new Event(el.tagName === 'SELECT' ? 'change' : 'input'));
    // Zoneless: a signal write schedules change detection, so run it here
    // too. Without it Angular's tracked binding value stays stale and a
    // later clear of the field would not reach the DOM.
    (fixture as unknown as { detectChanges(): void }).detectChanges();
  }

  it('lists the roster', () => {
    const text = mounted().nativeElement.querySelector('[data-jocks]').textContent;
    expect(text).toContain('Dutch');
    expect(text).toContain('am_fenrir');
    expect(text).toContain('rock, metal');
  });

  it('offers only voices the sidecar can produce', () => {
    // A jock nobody can voice fails as a silent break, minutes later, on air.
    const options = mounted().nativeElement.querySelectorAll('[data-voice] option');
    expect(Array.from(options).map((o) => (o as HTMLOptionElement).value)).toEqual([
      '',
      'am_fenrir',
      'af_heart',
    ]);
  });

  it('creates a jock', () => {
    const fixture = mounted();
    type(fixture, '[data-id]', 'roxy');
    type(fixture, '[data-name]', 'Roxy');
    type(fixture, '[data-voice]', 'af_heart');
    type(fixture, '[data-style]', 'dry');
    type(fixture, '[data-personality]', 'Deadpan.');
    fixture.nativeElement.querySelector('[data-save]').click();

    const req = ctrl.expectOne('/admin/jocks');
    expect(req.request.method).toBe('POST');
    expect(req.request.body.id).toBe('roxy');
    expect(req.request.body.voice_id).toBe('af_heart');
    req.flush({ id: 'roxy' });
    ctrl.expectOne('/admin/jocks').flush(jocks);
    fixture.detectChanges();
    // Said out loud: the break already being generated airs in the old voice.
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('next break');
  });

  it('edits an existing card whole', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-edit]').click();
    fixture.detectChanges();
    expect(fixture.componentInstance.editing()).toBe(true);
    expect((fixture.nativeElement.querySelector('[data-name]') as HTMLInputElement).value).toBe(
      'Dutch',
    );

    type(fixture, '[data-personality]', 'Now whispers.');
    fixture.nativeElement.querySelector('[data-save]').click();
    const req = ctrl.expectOne('/admin/jocks/dutch');
    expect(req.request.method).toBe('PUT');
    expect(req.request.body.personality).toBe('Now whispers.');
    // The whole card, not a patch.
    expect(req.request.body.good_for_genres).toEqual(['rock', 'metal']);
    req.flush(null);
    ctrl.expectOne('/admin/jocks').flush(jocks);
  });

  it('can back out of an edit', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-edit]').click();
    fixture.detectChanges();
    fixture.nativeElement.querySelector('[data-cancel]').click();
    fixture.detectChanges();
    expect(fixture.componentInstance.editing()).toBe(false);
    expect((fixture.nativeElement.querySelector('[data-id]') as HTMLInputElement).value).toBe('');
  });

  it('says which stations lost their jock', () => {
    // One that went quiet without anyone saying so is the hardest kind of
    // change to trace back.
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-remove]').click();
    ctrl.expectOne('/admin/jocks/dutch').flush({ unassigned: [1, 3] });
    ctrl.expectOne('/admin/jocks').flush([]);
    fixture.detectChanges();
    const said = fixture.nativeElement.querySelector('[data-said]').textContent;
    expect(said).toContain('2 stations');
    expect(said).toContain('music only');
  });

  it('says plainly when nothing was using it', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-remove]').click();
    ctrl.expectOne('/admin/jocks/dutch').flush({ unassigned: [] });
    ctrl.expectOne('/admin/jocks').flush([]);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent.trim()).toBe('Deleted.');
  });

  it('says one station in the singular', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-remove]').click();
    ctrl.expectOne('/admin/jocks/dutch').flush({ unassigned: [1] });
    ctrl.expectOne('/admin/jocks').flush([]);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('1 station ');
  });

  it('repeats the server’s refusal', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-save]').click();
    ctrl.expectOne('/admin/jocks').flush(
      { field: 'voice_id', error: 'no voice by that name: am_fenrir, af_heart' },
      { status: 400, statusText: 'Bad Request' },
    );
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('no voice');
  });

  it('says so when anything fails', () => {
    const fixture = TestBed.createComponent(Jocks);
    ctrl.expectOne('/admin/jocks').flush(null, { status: 500, statusText: 'Error' });
    ctrl.expectOne('/admin/voices').flush(null, { status: 502, statusText: 'Bad Gateway' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('sidecar');

    fixture.nativeElement.querySelector('[data-save]').click();
    ctrl.expectOne('/admin/jocks').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Could not');
  });

  it('says so when a delete fails or answers nothing', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-remove]').click();
    ctrl.expectOne('/admin/jocks/dutch').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Could not');

    fixture.nativeElement.querySelector('[data-remove]').click();
    ctrl.expectOne('/admin/jocks/dutch').flush(null);
    ctrl.expectOne('/admin/jocks').flush([]);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent.trim()).toBe('Deleted.');
  });
});
