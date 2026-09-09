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
    // THE FORM IS BEHIND A BUTTON since Jockora-e9a.60 moved it into a dialog.
    // It used to be always on screen, which is the precondition every test
    // below was written against.
    (fixture.nativeElement.querySelector('[data-add-open]') as HTMLButtonElement).click();
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
    // The form is GONE, not merely emptied: since Jockora-e9a.60 it lives in a
    // dialog, and backing out closes it.
    expect(fixture.nativeElement.querySelector('[data-jock-form]')).toBeNull();
    // Reopened, it starts blank rather than holding the jock just abandoned.
    (fixture.nativeElement.querySelector('[data-add-open]') as HTMLButtonElement).click();
    fixture.detectChanges();
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

    // The form is in a dialog now, so a save has to be reached through it.
    (fixture.nativeElement.querySelector('[data-add-open]') as HTMLButtonElement).click();
    fixture.detectChanges();
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

    it('stops when the operator leaves the jocks section', () => {
      // THE SECTIONS ARE A SWITCH, so leaving Jocks destroys this component --
      // and a preview left playing goes on talking over a console that no
      // longer shows the jock it belongs to, holding its blob until the tab
      // closes. The dial and the rescan poller stop themselves for the same
      // reason; this one did not.
      const fixture = withVoice();
      fixture.nativeElement.querySelector('[data-preview]').click();
      ctrl.expectOne('/admin/voices/preview').flush(new Blob(['a']));
      fixture.detectChanges();
      expect(played).toEqual(['blob:fake']);

      fixture.destroy();

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

  // ------------------------------------------------------- Jockora-e9a.54 --
  //
  // The list showed Name, Voice and Good for -- so SPEECH STYLE and
  // PERSONALITY, the two fields that decide how a DJ actually sounds and the
  // whole point of a persona in this product, were invisible. Telling two jocks
  // apart meant opening Edit on each in turn.
  //
  // And the first thing the form asked for was an id: a free-text box whose
  // placeholder was the word "id", asking a person to invent a stable machine
  // key by hand with no stated rules and no sign of what happens on a clash.

  it('jock list shows speech style and personality without opening an editor', () => {
    const fixture = mounted();
    const row = fixture.nativeElement.querySelector('[data-jocks] tbody tr');
    expect(row.textContent).toContain('LOUD');
    expect(row.textContent).toContain('Shouts about rock.');
    // And the FULL text is on the row even where the column truncates it, so a
    // phone card and a hover both have it.
    const personality = row.querySelector('[data-personality-cell]');
    expect(personality.getAttribute('title')).toBe('Shouts about rock.');
    // Read without opening anything: the editor is still closed.
    expect(fixture.componentInstance.editing()).toBe(false);
  });

  it('jock list derives an id from the name for a new jock', () => {
    const fixture = mounted();
    type(fixture, '[data-name]', 'Sunny Marchetti');

    const id = fixture.nativeElement.querySelector('[data-id]') as HTMLInputElement;
    // The convention the seeded jocks already follow, and the same string the
    // listener feedback writes as an attribution.
    expect(id.value).toBe('sunny_marchetti');

    (fixture.nativeElement.querySelector('[data-save]') as HTMLButtonElement).click();
    const req = ctrl.expectOne('/admin/jocks');
    expect(req.request.body.id).toBe('sunny_marchetti');
    req.flush({});
    // Saving re-reads the list. Voices are read once, on mount.
    ctrl.expectOne('/admin/jocks').flush(jocks);
  });

  it('jock list keeps a hand-set id when one is given', () => {
    const fixture = mounted();
    type(fixture, '[data-id]', 'the_captain');
    // Typed FIRST, then the name changes. An operator who set an id must not
    // have it overwritten by a later keystroke in another box.
    type(fixture, '[data-name]', 'Sunny Marchetti');

    const id = fixture.nativeElement.querySelector('[data-id]') as HTMLInputElement;
    expect(id.value).toBe('the_captain');

    (fixture.nativeElement.querySelector('[data-save]') as HTMLButtonElement).click();
    const req = ctrl.expectOne('/admin/jocks');
    expect(req.request.body.id).toBe('the_captain');
    req.flush({});
    ctrl.expectOne('/admin/jocks').flush(jocks);
  });

  it('jock list never re-derives the id of a jock that already exists', () => {
    // The id is the stable key that persona files and station assignments refer
    // to. Renaming a jock must not silently move it.
    const fixture = mounted();
    (fixture.nativeElement.querySelector('[data-edit]') as HTMLButtonElement).click();
    fixture.detectChanges();
    type(fixture, '[data-name]', 'Dutch Van Der Linde');

    const id = fixture.nativeElement.querySelector('[data-id]') as HTMLInputElement;
    expect(id.value).toBe('dutch');
  });

  it('jock list labels every control in its form', () => {
    // Jockora-e9a.58 counts five unlabelled controls on this tab. A placeholder
    // is not a label: it disappears the moment there is a value, so a
    // half-filled form gives no field names at all.
    const fixture = mounted();
    const form = fixture.nativeElement.querySelector('fieldset');
    const controls = [...form.querySelectorAll('input, select, textarea')];
    expect(controls.length).toBeGreaterThan(4);
    for (const c of controls) {
      const name =
        c.closest('label')?.textContent?.trim() ||
        c.getAttribute('aria-label') ||
        (c.id && form.querySelector(`label[for="${c.id}"]`)?.textContent?.trim());
      expect(name, c.getAttribute('data-name') ?? c.outerHTML.slice(0, 60)).toBeTruthy();
    }
  });

  // -------------------------------------------------------- Jockora-lph --
  //
  // Reported by the operator: the New jock elements are not aligned. Measured
  // at 1280px, row one -- Name label top 469 control 496, Identifier label 447
  // control 473, Voice label 469 control 496. Three fields, two different
  // heights, because "fieldset {align-items: end}" bottom-aligns each label BOX
  // and a label carrying helper text is taller, so it starts higher.
  //
  // The stylesheet is not loaded in this environment, so this pins the markup
  // contract the sheet lays out: a row is a run of labels of the SAME SHAPE.
  // test/console_css_test.go pins the rule, and the real geometry was measured
  // on a browser.

  it('jock list gives every field on a row the same shape', () => {
    const fixture = mounted();
    const form = fixture.nativeElement.querySelector('[data-jock-form]');
    // Labels AND the action box: the Hear it button is a field on this row too,
    // and it is the item that hung low. It cannot be a real label -- a button is
    // itself labelable, so a label wrapping one is invalid -- so it is a
    // [data-field] box of the same shape.
    const fields = [...form.querySelectorAll(':scope > label, :scope > [data-field]')];
    expect(fields.length).toBe(6);
    expect(form.querySelector(':scope > button'), 'bare button outside a field box').toBeNull();

    // EVERY field carries a description, or none does. A row that mixes the two
    // is the row that misaligned: helper text changes a box's height, and the
    // fieldset aligns on the bottom of the box rather than on the control in it.
    const withHelp = fields.filter((f) => f.querySelector('small')).length;
    expect(withHelp, 'fields carrying helper text').toBe(fields.length);

    // And each field is the same three things in the same order, so nothing on
    // the row is a different shape from its neighbours.
    for (const field of fields) {
      const control = field.querySelector('input, select, button');
      const help = field.querySelector('small');
      expect(control).not.toBeNull();
      expect(help).not.toBeNull();
      expect(help.previousElementSibling).toBe(control);
    }
  });

  it('edit dialog dismissing the jock form forgets what was half-typed', () => {
    const fixture = mounted();
    type(fixture, '[data-name]', 'Rosa Del Fierro');
    expect((fixture.nativeElement.querySelector('[data-id]') as HTMLInputElement).value).toBe(
      'rosa_del_fierro',
    );

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-jock-form]')).toBeNull();

    // Reopened it is blank, rather than holding a persona nobody meant to keep.
    (fixture.nativeElement.querySelector('[data-add-open]') as HTMLButtonElement).click();
    fixture.detectChanges();
    expect((fixture.nativeElement.querySelector('[data-name]') as HTMLInputElement).value).toBe('');
  });

  // Jockora-9g7: a search box and sortable headers on a table the browser holds
  // entirely.
  it('jocks table searches every column it draws', () => {
    const fixture = mounted();
    const search = fixture.nativeElement.querySelector('[data-search]') as HTMLInputElement;
    const shown = () => fixture.componentInstance.view.shown().map((j) => j.id);

    // One per column, so every accessor is exercised by something a person
    // would actually type: a voice id, a genre, the speech style, the persona.
    for (const q of ['Dutch', 'am_fenrir', 'metal', 'LOUD', 'Shouts']) {
      search.value = q;
      search.dispatchEvent(new Event('input'));
      fixture.detectChanges();
      expect(shown(), q).toEqual(['dutch']);
    }
    search.value = 'nothing like this';
    search.dispatchEvent(new Event('input'));
    fixture.detectChanges();
    expect(shown()).toEqual([]);
  });

  it('jocks table marks the sorted header for a screen reader', () => {
    const fixture = mounted();
    const header = fixture.nativeElement.querySelector('[data-sort="name"]') as HTMLButtonElement;
    expect(header.closest('th')!.getAttribute('aria-sort')).toBe('none');
    header.click();
    fixture.detectChanges();
    expect(header.closest('th')!.getAttribute('aria-sort')).toBe('ascending');
    header.click();
    fixture.detectChanges();
    expect(header.closest('th')!.getAttribute('aria-sort')).toBe('descending');
  });

  it('jocks table gives every sortable header its own column', () => {
    // COPY-PASTED HEADERS ARE THE BUG THIS CATCHES. Each one carries the key it
    // sorts by, and a header wired to its neighbour's key looks right and sorts
    // the wrong column -- which nothing else here would notice.
    const fixture = mounted();
    const headers = [
      ...fixture.nativeElement.querySelectorAll('[data-jocks] thead [data-sort]'),
    ] as HTMLButtonElement[];
    expect(headers.length).toBe(5);
    const keys = new Set<string>();
    for (const h of headers) {
      const key = h.getAttribute('data-sort')!;
      expect(keys.has(key), `two headers claim ${key}`).toBe(false);
      keys.add(key);
      h.click();
      fixture.detectChanges();
      expect(fixture.componentInstance.view.sortKey()).toBe(key);
      expect(h.closest('th')!.getAttribute('aria-sort')).toBe('ascending');
    }
  });
});
