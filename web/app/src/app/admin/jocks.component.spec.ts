import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest';
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
    // DEFAULT: yes. Destructive actions ask now, and every test that was
    // written before they did is still testing what happens after the answer.
    // The tests about the QUESTION stub it themselves.
    vi.stubGlobal('confirm', () => true);
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

  // ------------------------------------------------------ console states --
  // Jockora-e9a.50: loading and empty rendered identically, so a slow first
  // paint told a new operator their library was empty.

  it('console states tells a fresh operator what to do when there are no jocks', () => {
    const fixture = TestBed.createComponent(Jocks);
    ctrl.expectOne('/admin/jocks').flush([]);
    ctrl.expectOne('/admin/voices').flush({ voices: [] });
    fixture.detectChanges();
    const empty = fixture.nativeElement.querySelector('[data-empty]');
    expect(empty).not.toBeNull();
    expect(empty.textContent).toContain('Add a jock');
  });

  it('console states does not call a loading jocks table an empty one', () => {
    const fixture = TestBed.createComponent(Jocks);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-empty]')).toBeNull();
    expect(fixture.nativeElement.querySelector('[data-loading]')).not.toBeNull();
  });

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
    ctrl
      .expectOne('/admin/jocks')
      .flush(
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

  describe('voice preview', () => {
    // A voice is a name like "am_fenrir", which tells an operator nothing.
    // Hearing it before a jock goes on air is the whole point.
    let played: string[];
    let paused: number;

    beforeEach(() => {
      played = [];
      paused = 0;
      vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:fake');
      vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});
      vi.spyOn(HTMLMediaElement.prototype, 'play').mockImplementation(function (
        this: HTMLAudioElement,
      ) {
        played.push(this.src);
        return Promise.resolve();
      });
      vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(() => {
        paused += 1;
      });
    });

    afterEach(() => vi.restoreAllMocks());

    function withVoice() {
      const fixture = mounted();
      type(fixture, '[data-voice]', 'am_fenrir');
      return fixture;
    }

    it('is not offered until a voice is chosen', () => {
      const fixture = mounted();
      expect(fixture.nativeElement.querySelector('[data-preview]').disabled).toBe(true);
      type(fixture, '[data-voice]', 'am_fenrir');
      expect(fixture.nativeElement.querySelector('[data-preview]').disabled).toBe(false);
    });

    it('speaks the chosen voice and plays what comes back', () => {
      const fixture = withVoice();
      fixture.nativeElement.querySelector('[data-preview]').click();
      fixture.detectChanges();
      // Says so while it renders: a cold sidecar takes a few seconds.
      expect(fixture.nativeElement.querySelector('[data-preview]').textContent).toContain(
        'Speaking',
      );

      const req = ctrl.expectOne('/admin/voices/preview');
      expect(req.request.method).toBe('POST');
      expect(req.request.body).toEqual({ voice: 'am_fenrir' });
      req.flush(new Blob(['RIFF'], { type: 'audio/wav' }));
      fixture.detectChanges();

      expect(played).toEqual(['blob:fake']);
      expect(fixture.nativeElement.querySelector('[data-preview]').textContent).toContain(
        'Hear it',
      );
    });

    it('stops the previous clip rather than talking over it', () => {
      const fixture = withVoice();
      fixture.nativeElement.querySelector('[data-preview]').click();
      ctrl.expectOne('/admin/voices/preview').flush(new Blob(['a']));
      fixture.detectChanges();

      fixture.nativeElement.querySelector('[data-preview]').click();
      ctrl.expectOne('/admin/voices/preview').flush(new Blob(['b']));
      fixture.detectChanges();

      expect(paused).toBe(1);
      expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:fake');
    });

    it('lets go of the clip when it finishes', () => {
      // Otherwise every preview leaks a blob for as long as the console is open.
      const fixture = withVoice();
      fixture.nativeElement.querySelector('[data-preview]').click();
      ctrl.expectOne('/admin/voices/preview').flush(new Blob(['a']));
      fixture.detectChanges();
      fixture.componentInstance['audio']!.dispatchEvent(new Event('ended'));
      expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:fake');
    });

    it('says so when the sidecar will not speak', () => {
      const fixture = withVoice();
      fixture.nativeElement.querySelector('[data-preview]').click();
      ctrl
        .expectOne('/admin/voices/preview')
        .flush(null, { status: 502, statusText: 'Bad Gateway' });
      fixture.detectChanges();
      expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
        'sidecar may be down',
      );
      expect(fixture.nativeElement.querySelector('[data-preview]').textContent).toContain(
        'Hear it',
      );
    });

    it('ignores a click with no voice, and one while already speaking', () => {
      const fixture = mounted();
      fixture.componentInstance.preview('');
      ctrl.verify();

      type(fixture, '[data-voice]', 'am_fenrir');
      fixture.nativeElement.querySelector('[data-preview]').click();
      fixture.componentInstance.preview('am_fenrir');
      ctrl.expectOne('/admin/voices/preview').flush(new Blob(['a']));
    });

    it('plays a jock straight from the list', () => {
      // The list is where an operator compares jocks, so it is where hearing
      // them belongs -- not only inside the edit form.
      const fixture = mounted();
      const row = fixture.nativeElement.querySelector('[data-row-preview]');
      expect(row.disabled).toBe(false);
      row.click();
      fixture.detectChanges();

      // Only the button that was pressed says so.
      expect(row.textContent).toContain('Speaking');
      expect(fixture.nativeElement.querySelector('[data-preview]').textContent).toContain(
        'Hear it',
      );

      const req = ctrl.expectOne('/admin/voices/preview');
      expect(req.request.body).toEqual({ voice: 'am_fenrir' });
      req.flush(new Blob(['a']));
      fixture.detectChanges();
      expect(row.textContent).toContain('Hear it');
      expect(played).toEqual(['blob:fake']);
    });
  });

  it('labels its columns', () => {
    // A grid of bare values makes the reader infer what each column is from
    // whatever the first row happens to contain -- and "rock" in a column of
    // its own could be a genre, a tag or a mood.
    const head = mounted().nativeElement.querySelector('[data-jocks] thead').textContent;
    expect(head).toContain('Name');
    expect(head).toContain('Voice');
    expect(head).toContain('Good for');
    expect(head).toContain('Actions');
  });

  // The Jocks table drew Hear it, Edit and Delete to 469px on a 390px phone,
  // with no ancestor able to scroll. Below 48rem each row is a card.
  it('narrow jocks labels every cell with the column it replaces', () => {
    const fixture = mounted();
    const headers = [...fixture.nativeElement.querySelectorAll('[data-jocks] thead th')].map((h) =>
      (h as HTMLElement).textContent?.trim(),
    );
    for (const row of fixture.nativeElement.querySelectorAll('[data-jocks] tbody tr')) {
      const cells = [...row.querySelectorAll('td')];
      expect(cells.length).toBe(headers.length);
      cells.forEach((cell, i) => {
        expect(cell.getAttribute('data-label')).toBe(headers[i]);
      });
    }
  });

  // ONE CLICK, NO PROMPT, NO UNDO, from a button identical to Edit. A persona
  // card is hand-written content, and deleting one unassigns it from every
  // station that used it.
  it('destructive jocks asks before deleting', () => {
    const fixture = mounted();
    const confirmed: string[] = [];
    vi.stubGlobal('confirm', (m: string) => {
      confirmed.push(m);
      return false;
    });
    fixture.nativeElement.querySelector('[data-remove]').click();
    ctrl.expectNone('/admin/jocks/dutch');
    expect(confirmed[0]).toContain('Dutch');
    // It says what else goes: the stations that used the jock lose it.
    expect(confirmed[0].toLowerCase()).toContain('station');

    vi.stubGlobal('confirm', () => true);
    fixture.nativeElement.querySelector('[data-remove]').click();
    ctrl.expectOne('/admin/jocks/dutch').flush({ unassigned: [] });
    vi.unstubAllGlobals();
  });

  it('destructive jocks marks the delete button as destructive', () => {
    // Every action in the console had exactly one background. Nothing told a
    // preview from an edit from an irreversible delete.
    const fixture = mounted();
    expect(
      fixture.nativeElement.querySelector('[data-remove]').getAttribute('data-danger'),
    ).not.toBeNull();
    expect(
      fixture.nativeElement.querySelector('[data-edit]').getAttribute('data-danger'),
    ).toBeNull();
  });
});
