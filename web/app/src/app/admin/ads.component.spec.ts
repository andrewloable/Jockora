import { describe, expect, it, beforeEach, vi } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Ads } from './ads.component';

// Jockora-iyw.6. THE OPERATOR SELLS AIRTIME: they describe a product, read the
// blurb the model wrote, edit it, and save it. Every spec here turns on the
// difference between WRITING and SAVING.

const script = 'Stillwater Ceramics. Hand-thrown mugs, one at a time, still here tomorrow.';

const rows = [
  {
    id: 1,
    brand: 'Stillwater Ceramics',
    brief: 'hand-thrown mugs',
    delivery: 'deadpan',
    script,
    last_aired_at: '2026-09-08T14:30:00Z',
    enabled: true,
  },
  {
    id: 2,
    brand: 'Harrow & Fen',
    brief: '',
    delivery: '',
    script: 'Harrow and Fen. Books.',
    enabled: true,
  },
];

describe('ads', () => {
  let ctrl: HttpTestingController;

  beforeEach(async () => {
    vi.stubGlobal('confirm', () => true);
    await TestBed.configureTestingModule({
      imports: [Ads],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  function mounted(list = rows) {
    const fixture = TestBed.createComponent(Ads);
    ctrl.expectOne('/admin/ads').flush(list);
    fixture.detectChanges();
    // THE FORM IS BEHIND A BUTTON since Jockora-e9a.60 moved it into a dialog.
    (fixture.nativeElement.querySelector('[data-add-open]') as HTMLButtonElement).click();
    fixture.detectChanges();
    return fixture;
  }

  type Fixture = { nativeElement: HTMLElement; detectChanges(): void };

  function type(fixture: Fixture, selector: string, value: string) {
    const el = fixture.nativeElement.querySelector(selector) as HTMLInputElement;
    el.value = value;
    el.dispatchEvent(new Event('input'));
    fixture.detectChanges();
  }

  function click(fixture: Fixture, selector: string) {
    (fixture.nativeElement.querySelector(selector) as HTMLButtonElement).click();
    fixture.detectChanges();
  }

  function text(fixture: Fixture) {
    return fixture.nativeElement.textContent ?? '';
  }

  it('lists ads with brand, script and when each last aired', () => {
    const fixture = mounted();
    const cells = fixture.nativeElement.querySelectorAll('[data-ads] tbody tr');
    expect(cells.length).toBe(2);
    expect(text(fixture)).toContain('Stillwater Ceramics');
    expect(text(fixture)).toContain('Hand-thrown mugs');
    // NEVER, not the epoch and not blank. An advert that has not aired yet is
    // a fact the operator wants; "1 January 1970" is a bug report.
    expect(text(fixture)).toContain('never');
  });

  it('writes a blurb from a brief and puts it in an editable textarea', () => {
    const fixture = mounted([]);
    type(fixture, '[data-brand]', 'Stillwater Ceramics');
    type(fixture, '[data-about]', 'hand-thrown mugs');
    type(fixture, '[data-delivery]', 'deadpan');
    click(fixture, '[data-write]');

    const req = ctrl.expectOne('/admin/ads/write');
    expect(req.request.body).toEqual({
      brand: 'Stillwater Ceramics',
      about: 'hand-thrown mugs',
      delivery: 'deadpan',
    });
    req.flush({ brand: 'Stillwater Ceramics', script });
    fixture.detectChanges();

    // ONE TEXTAREA, editable. Not a read-only preview with an edit button:
    // typing over it is the same gesture as accepting it.
    const box = fixture.nativeElement.querySelector('[data-script]') as HTMLTextAreaElement;
    expect(box.tagName).toBe('TEXTAREA');
    expect(box.readOnly).toBe(false);
    expect(box.value).toBe(script);
  });

  it('saves the blurb as edited, not as written', () => {
    const fixture = mounted([]);
    type(fixture, '[data-brand]', 'Stillwater Ceramics');
    type(fixture, '[data-about]', 'hand-thrown mugs');
    click(fixture, '[data-write]');
    ctrl.expectOne('/admin/ads/write').flush({ brand: 'Stillwater Ceramics', script });
    fixture.detectChanges();

    const mine = 'Stillwater Ceramics. Two mugs now, and we are as surprised as you are.';
    type(fixture, '[data-script]', mine);
    click(fixture, '[data-save]');

    const req = ctrl.expectOne('/admin/ads');
    expect(req.request.method).toBe('POST');
    // WHAT IS ON SCREEN. An advert re-written on save would air words the
    // operator never read, which is the whole reason writing and saving are
    // two calls.
    expect(req.request.body.script).toBe(mine);
    expect(req.request.body.brand).toBe('Stillwater Ceramics');
    expect(req.request.body.brief).toBe('hand-thrown mugs');
    req.flush({ id: 3 });
    ctrl.expectOne('/admin/ads').flush([]);
  });

  it('saves a hand-written script with no write call at all', () => {
    const fixture = mounted([]);
    type(fixture, '[data-brand]', 'Stillwater Ceramics');
    type(fixture, '[data-script]', script);
    click(fixture, '[data-save]');

    // WRITING IS OPTIONAL, and this path must work with no model configured.
    const req = ctrl.expectOne('/admin/ads');
    expect(req.request.method).toBe('POST');
    expect(req.request.body.script).toBe(script);
    req.flush({ id: 4 });
    ctrl.expectOne('/admin/ads').flush([]);
    ctrl.verify();
  });

  it('renders the real-brand warning, names the match, and keeps save enabled', () => {
    const fixture = mounted([]);
    type(fixture, '[data-brand]', 'Coca-Cola');
    type(fixture, '[data-about]', 'the drink');
    click(fixture, '[data-write]');
    ctrl.expectOne('/admin/ads/write').flush({
      brand: 'Coca-Cola',
      script: 'Coca-Cola. The one you already know, cold, at the shop on the corner.',
      matched: 'coca-cola',
      warning: 'this advert names coca-cola, which somebody else owns. You can use it anyway.',
    });
    fixture.detectChanges();

    const warn = fixture.nativeElement.querySelector('[data-brand-warning]');
    expect(warn).not.toBeNull();
    expect(warn?.textContent).toContain('coca-cola');
    // AN ADVISORY, NOT A BLOCKER. An operator advertising a real company sees
    // this every time; a red box that means nothing trains them to ignore red
    // boxes, and disabling Save would make the feature useless to them.
    expect(warn?.getAttribute('role')).not.toBe('alert');
    const save = fixture.nativeElement.querySelector('[data-save]') as HTMLButtonElement;
    expect(save.disabled).toBe(false);
  });

  it('shows the server words on a 503 and leaves save enabled', () => {
    const fixture = mounted([]);
    type(fixture, '[data-brand]', 'Stillwater');
    type(fixture, '[data-about]', 'mugs');
    click(fixture, '[data-write]');
    ctrl.expectOne('/admin/ads/write').flush(
      {
        error:
          'no language model is configured, so a blurb cannot be drafted. ' +
          'Choose one on the Model page, or write the advert yourself.',
      },
      { status: 503, statusText: 'Service Unavailable' },
    );
    fixture.detectChanges();

    // THE SERVER'S OWN SENTENCE, not a generic fallback. It names both ways
    // out, and one of them is the form the operator is already looking at.
    expect(text(fixture)).toContain('Choose one on the Model page');
    const save = fixture.nativeElement.querySelector('[data-save]') as HTMLButtonElement;
    expect(save.disabled).toBe(false);
  });

  it('shows a 502 and a 504 and leaves the form as it was', () => {
    const fixture = mounted([]);
    type(fixture, '[data-brand]', 'Stillwater');
    type(fixture, '[data-about]', 'mugs');
    type(fixture, '[data-script]', script);

    click(fixture, '[data-write]');
    ctrl
      .expectOne('/admin/ads/write')
      .flush(
        { error: 'the language model could not write it: 402 payment required' },
        { status: 502, statusText: 'Bad Gateway' },
      );
    fixture.detectChanges();
    expect(text(fixture)).toContain('402 payment required');
    // NOT CLEARED. The operator was not wrong and their words are still theirs.
    expect(
      (fixture.nativeElement.querySelector('[data-script]') as HTMLTextAreaElement).value,
    ).toBe(script);

    click(fixture, '[data-write]');
    ctrl.expectOne('/admin/ads/write').flush(
      {
        error:
          'the language model did not answer in time. Enrichment may be running and using it; ' +
          'you can pause that on the Overview. You can also write the advert yourself.',
      },
      { status: 504, statusText: 'Gateway Timeout' },
    );
    fixture.detectChanges();
    // THE SAME WORDS THE STATION BRIEF USES. Two screens explaining the same
    // delay differently is worse than either explanation.
    expect(text(fixture)).toContain('Enrichment may be running');

    // And a failure that carries no sentence at all still says something. A
    // blank line where the reason should be reads as the console breaking.
    click(fixture, '[data-write]');
    ctrl.expectOne('/admin/ads/write').flush(null, { status: 500, statusText: 'nope' });
    fixture.detectChanges();
    expect(text(fixture)).toContain('Could not write that advert');
  });

  it('counts code points against the slot and flags going over', () => {
    const fixture = mounted([]);
    type(fixture, '[data-script]', 'a'.repeat(10));
    expect(fixture.nativeElement.querySelector('[data-count]')?.textContent).toContain('10');
    expect(
      fixture.nativeElement.querySelector('[data-count]')?.getAttribute('data-over'),
    ).toBeNull();

    // CODE POINTS, NOT UTF-16 UNITS. An emoji is one character to the server
    // and two to JavaScript's .length -- the console would say 340 of 360
    // while the server refused. Jockora-9jo, one field over.
    type(fixture, '[data-script]', '🎧'.repeat(5));
    expect(fixture.nativeElement.querySelector('[data-count]')?.textContent).toContain('5');

    type(fixture, '[data-script]', 'a'.repeat(400));
    expect(fixture.nativeElement.querySelector('[data-count]')?.getAttribute('data-over')).toBe(
      'true',
    );
  });

  it('edits an existing ad, blurb included', () => {
    const fixture = mounted();
    click(fixture, '[data-ads] tbody tr [data-edit]');

    // THE ROW, IN THE FORM. All four fields: an operator who clicks Edit and
    // finds the brief they wrote is blank has lost it, and would retype it.
    const value = (sel: string) =>
      (fixture.nativeElement.querySelector(sel) as HTMLInputElement).value;
    expect(value('[data-brand]')).toBe('Stillwater Ceramics');
    expect(value('[data-about]')).toBe('hand-thrown mugs');
    expect(value('[data-delivery]')).toBe('deadpan');
    expect(value('[data-script]')).toBe(script);

    type(fixture, '[data-about]', 'mugs, more of them');
    click(fixture, '[data-write]');
    ctrl
      .expectOne('/admin/ads/write')
      .flush({ brand: 'Stillwater Ceramics', script: 'Stillwater Ceramics. Two mugs now.' });
    fixture.detectChanges();

    click(fixture, '[data-save]');
    const req = ctrl.expectOne('/admin/ads/1');
    expect(req.request.method).toBe('PUT');
    expect(req.request.body.script).toBe('Stillwater Ceramics. Two mugs now.');
    expect(req.request.body.brief).toBe('mugs, more of them');
    req.flush(null);
    ctrl.expectOne('/admin/ads').flush(rows);
  });

  it('asks before deleting, and does nothing if the operator declines', () => {
    vi.stubGlobal('confirm', () => false);
    const fixture = mounted();
    click(fixture, '[data-ads] tbody tr [data-remove]');
    // DO NOT COPY THE ONE-CLICK DELETE from the other sections; it is already
    // a filed bug (Jockora-e9a.48). Declining does nothing at all.
    ctrl.verify();

    vi.stubGlobal('confirm', () => true);
    click(fixture, '[data-ads] tbody tr [data-remove]');
    ctrl.expectOne('/admin/ads/1').flush(null);
    ctrl.expectOne('/admin/ads').flush([]);
  });

  it('disables write while blank and while in flight, and says which', () => {
    const fixture = mounted([]);
    const write = () => fixture.nativeElement.querySelector('[data-write]') as HTMLButtonElement;
    expect(write().disabled).toBe(true);

    type(fixture, '[data-brand]', 'Stillwater');
    expect(write().disabled).toBe(true); // about is required too
    type(fixture, '[data-about]', 'mugs');
    expect(write().disabled).toBe(false);

    click(fixture, '[data-write]');
    expect(write().disabled).toBe(true);
    expect(write().textContent).toContain('Writing');
    // WHY IT IS SLOW, in the same words the station brief uses.
    expect(text(fixture)).toContain('enrichment may be running');
    ctrl.expectOne('/admin/ads/write').flush({ brand: 'Stillwater', script });
    fixture.detectChanges();
    expect(write().disabled).toBe(false);
  });

  it('shows the server words when a save is refused, and its own when there are none', () => {
    const fixture = mounted([]);
    type(fixture, '[data-brand]', 'Stillwater');
    type(fixture, '[data-script]', 'Stillwater. Mugs.');
    click(fixture, '[data-save]');
    // THE VALIDATOR'S OWN SENTENCE, which already says which rule and by how
    // much. A generic "could not save" would send the operator hunting.
    ctrl
      .expectOne('/admin/ads')
      .flush(
        { field: 'script', error: 'advert is 3 words, too short to be one' },
        { status: 400, statusText: 'Bad Request' },
      );
    fixture.detectChanges();
    expect(text(fixture)).toContain('too short to be one');

    click(fixture, '[data-save]');
    ctrl.expectOne('/admin/ads').flush(null, { status: 500, statusText: 'nope' });
    fixture.detectChanges();
    expect(text(fixture)).toContain('Could not save that advert');
  });

  it('says so when a delete fails, and cancel leaves the form empty', () => {
    const fixture = mounted();
    click(fixture, '[data-ads] tbody tr [data-remove]');
    ctrl.expectOne('/admin/ads/1').flush(null, { status: 500, statusText: 'nope' });
    fixture.detectChanges();
    expect(text(fixture)).toContain('Could not delete');

    // CANCEL IS NOT SAVE. An operator who opened the wrong row leaves the
    // form as they found it, with nothing sent.
    click(fixture, '[data-ads] tbody tr [data-edit]');
    expect((fixture.nativeElement.querySelector('[data-brand]') as HTMLInputElement).value).toBe(
      'Stillwater Ceramics',
    );
    click(fixture, '[data-cancel]');
    // The form is GONE, not merely emptied: since Jockora-e9a.60 it lives in a
    // dialog and cancelling closes it.
    expect(fixture.nativeElement.querySelector('[data-ad-form]')).toBeNull();
    // Reopened it is blank, rather than holding the advert just abandoned.
    (fixture.nativeElement.querySelector('[data-add-open]') as HTMLButtonElement).click();
    fixture.detectChanges();
    expect((fixture.nativeElement.querySelector('[data-brand]') as HTMLInputElement).value).toBe(
      '',
    );
    ctrl.verify();
  });

  it('reads a rubbish timestamp as never rather than as Invalid Date', () => {
    const fixture = mounted([{ ...rows[0], last_aired_at: 'the other day' }]);
    // A row the server should never send, and a cell that must not read
    // "Invalid Date" if it ever does.
    expect(text(fixture)).toContain('never');
    expect(text(fixture)).not.toContain('Invalid Date');
  });

  it('carries the server field caps so the browser stops a paste first', () => {
    // The 4KB body cap fails with a flat 400 that names no box. These stop the
    // round trip happening at all.
    const fixture = mounted([]);
    expect(fixture.nativeElement.querySelector('[data-about]')?.getAttribute('maxlength')).toBe(
      '1500',
    );
    expect(fixture.nativeElement.querySelector('[data-delivery]')?.getAttribute('maxlength')).toBe(
      '200',
    );
  });

  it('tells loading, empty and failed apart', () => {
    const fixture = TestBed.createComponent(Ads);
    fixture.detectChanges();
    expect(text(fixture)).toContain('Loading');

    ctrl.expectOne('/admin/ads').flush([]);
    fixture.detectChanges();
    expect(text(fixture)).toContain('No adverts yet');

    const second = TestBed.createComponent(Ads);
    second.detectChanges();
    ctrl.expectOne('/admin/ads').flush(null, { status: 500, statusText: 'nope' });
    second.detectChanges();
    // A FAILED READ IS NOT AN EMPTY LIBRARY. Jockora-e9a.50.
    expect(second.nativeElement.textContent).not.toContain('No adverts yet');
    expect(second.nativeElement.textContent).toContain('Could not read');
  });

  // ------------------------------------------------------- Jockora-e9a.55 --
  //
  // An advert could only be created or destroyed, never paused, while sources,
  // stations and accounts all disable. The copy is hand-written prose, so
  // Delete-as-the-only-off-switch cost an operator a retype every time an
  // advertiser went quiet for a season. And the form ran labels, controls and
  // helper text together into one line, because a textarea had no rule in the
  // stylesheet at all and stayed inline beside its own label.

  it('advert can be disabled without being deleted', () => {
    const fixture = mounted();
    const row = fixture.nativeElement.querySelectorAll('[data-ads] tbody tr')[0];
    const toggle = row.querySelector('[data-toggle]') as HTMLButtonElement;
    expect(toggle).not.toBeNull();
    // THE SAME WORD sources, stations and accounts use.
    expect(toggle.textContent!.trim()).toBe('Disable');

    toggle.click();
    ctrl.expectOne((r) => r.url === '/admin/ads/1/disable' && r.method === 'POST').flush(null);
    // NOT A DELETE. The list is re-read, and nothing was destroyed.
    ctrl.expectOne('/admin/ads').flush([{ ...rows[0], enabled: false }, rows[1]]);
    fixture.detectChanges();

    const after = fixture.nativeElement.querySelectorAll('[data-ads] tbody tr')[0];
    const back = after.querySelector('[data-toggle]') as HTMLButtonElement;
    expect(back.textContent!.trim()).toBe('Enable');
    // Still on screen, still carrying its script: an advert an operator cannot
    // see is an advert they cannot switch back on.
    expect(after.textContent).toContain('Stillwater Ceramics');

    // AND BACK ON. Reversible is the whole reason this is not Delete, so the
    // return trip is asserted rather than assumed.
    back.click();
    ctrl.expectOne((r) => r.url === '/admin/ads/1/enable' && r.method === 'POST').flush(null);
    ctrl.expectOne('/admin/ads').flush(rows);
    fixture.detectChanges();
    expect(
      fixture.nativeElement.querySelector('[data-ads] tbody tr [data-toggle]')!.textContent!.trim(),
    ).toBe('Disable');
    expect(fixture.nativeElement.querySelector('[data-said]')!.textContent).toContain(
      'back on air',
    );
  });

  it('advert says so when a pause cannot be saved', () => {
    const fixture = mounted();
    (
      fixture.nativeElement.querySelector('[data-ads] tbody tr [data-toggle]') as HTMLButtonElement
    ).click();
    ctrl.expectOne('/admin/ads/1/disable').error(new ProgressEvent('failed'));
    fixture.detectChanges();
    // A row that silently stays On after a failed disable is how an operator
    // concludes the button does nothing.
    expect(fixture.nativeElement.querySelector('[data-said]')!.textContent).toContain(
      'Could not change',
    );
  });

  it('advert that is disabled is marked as off in the list', () => {
    const fixture = mounted([{ ...rows[0], enabled: false }, rows[1]]);
    const [off, on] = fixture.nativeElement.querySelectorAll('[data-ads] tbody tr');
    // The operator has to be able to tell at a glance which of these is on air;
    // the Go side proves it is actually out of the pool.
    expect(off.querySelector('[data-ad-state]')!.textContent).toContain('Off');
    expect(on.querySelector('[data-ad-state]')!.textContent).toContain('On');
  });

  it('advert disable asks no question, because it can be undone', () => {
    // Jockora-e9a.48: a reversible action gets no prompt. Delete asks; this
    // must not, or the two read as equally serious.
    let asked = 0;
    vi.stubGlobal('confirm', () => {
      asked++;
      return true;
    });
    const fixture = mounted();
    (
      fixture.nativeElement.querySelector('[data-ads] tbody tr [data-toggle]') as HTMLButtonElement
    ).click();
    ctrl.expectOne('/admin/ads/1/disable').flush(null);
    ctrl.expectOne('/admin/ads').flush(rows);
    expect(asked).toBe(0);
  });

  it('advert form gives each control its own label above and its helper below', () => {
    const fixture = mounted();
    const form = fixture.nativeElement.querySelector('[data-ad-form]');
    expect(form).not.toBeNull();

    const labels = [...form.querySelectorAll('label')];
    // COUNTED, so the loop cannot pass by iterating nothing.
    expect(labels.length).toBe(4);
    for (const label of labels) {
      const control = label.querySelector('input, textarea, select');
      expect(control).not.toBeNull();
      // The label text comes FIRST, then the control, then its explanation.
      // The old form interleaved all three at one baseline, so nothing said
      // which text belonged to which box.
      const kids = [...label.childNodes].filter(
        (n) => n.nodeType !== 3 || n.textContent!.trim() !== '',
      );
      expect(kids.indexOf(control!)).toBeGreaterThan(0);
      const help = label.querySelector('small');
      if (help) {
        expect(kids.indexOf(help)).toBeGreaterThan(kids.indexOf(control!));
      }
    }
    // AND THE COUNTER KEEPS ITS HOME. Not merely that it still exists -- a
    // mutation that moved it back out of the label passed that check, because
    // "somewhere in the form" is where it was when it was the reported defect.
    // It belongs to the script box, so it has to be INSIDE that box's label.
    const count = form.querySelector('[data-count]');
    expect(count).not.toBeNull();
    const scriptLabel = form.querySelector('[data-script]')!.closest('label');
    expect(scriptLabel!.contains(count)).toBe(true);
  });

  // ------------------------------------------------------- Jockora-e9a.63 --
  //
  // A slow Write it answer landed in whichever advert was open when it
  // returned. Start a blurb for brand A, press Edit on advert B while it works,
  // and A's copy arrives in B's form -- press Save and B is overwritten with
  // copy about A.
  //
  // The window is wide by the product's own account: the form warns that
  // writing queues behind enrichment and may be slow, which is exactly when an
  // operator goes and does something else.

  it('advert write ignores an answer that arrives after the operator moved on', () => {
    const fixture = mounted();
    type(fixture, '[data-brand]', 'Stillwater Ceramics');
    type(fixture, '[data-about]', 'hand-thrown mugs');
    click(fixture, '[data-write]');
    const inFlight = ctrl.expectOne('/admin/ads/write');

    // The operator gives up waiting and edits a different advert.
    click(fixture, '[data-ads] tbody tr:nth-child(2) [data-edit]');
    fixture.detectChanges();
    expect(fixture.componentInstance.editing()!.brand).toBe('Harrow & Fen');
    const openScript = fixture.componentInstance.script();

    // A's answer arrives now.
    inFlight.flush({ brand: 'Stillwater Ceramics', script: 'Stillwater. One mug, still.' });
    fixture.detectChanges();

    // IT MUST NOT LAND. The open form still holds Harrow's own copy.
    expect(fixture.componentInstance.script()).toBe(openScript);
    expect(fixture.componentInstance.script()).not.toContain('Stillwater');
  });

  it('advert write clears the writing state when the form changes', () => {
    const fixture = mounted();
    type(fixture, '[data-brand]', 'Stillwater Ceramics');
    type(fixture, '[data-about]', 'hand-thrown mugs');
    click(fixture, '[data-write]');
    const inFlight = ctrl.expectOne('/admin/ads/write');
    expect(fixture.componentInstance.writing()).toBe(true);

    click(fixture, '[data-ads] tbody tr:nth-child(2) [data-edit]');
    fixture.detectChanges();

    // The label belonged to a request that has nothing to do with this advert,
    // and it disabled this advert's own Write it until the other one returned.
    expect(fixture.componentInstance.writing()).toBe(false);
    const button = fixture.nativeElement.querySelector('[data-write]') as HTMLButtonElement;
    expect(button.textContent!.trim()).toBe('Write it');

    inFlight.flush({ brand: 'Stillwater Ceramics', script: 'Stillwater. One mug, still.' });
    fixture.detectChanges();
    expect(fixture.componentInstance.writing()).toBe(false);
  });

  it('advert write ignores a failure that arrives after the operator moved on', () => {
    const fixture = mounted();
    type(fixture, '[data-brand]', 'Stillwater Ceramics');
    type(fixture, '[data-about]', 'hand-thrown mugs');
    click(fixture, '[data-write]');
    const inFlight = ctrl.expectOne('/admin/ads/write');

    click(fixture, '[data-ads] tbody tr:nth-child(2) [data-edit]');
    fixture.detectChanges();
    inFlight.error(new ProgressEvent('failed'), { status: 502 });
    fixture.detectChanges();

    // A message about the OTHER advert's request, on this advert's form, is the
    // same defect wearing different clothes.
    expect(fixture.nativeElement.querySelector('[data-said]')!.textContent!.trim()).toBe('');
  });

  it('advert write still lands when the operator waited for it', () => {
    // The guard must not throw away the answer in the ordinary case.
    const fixture = mounted();
    type(fixture, '[data-brand]', 'Stillwater Ceramics');
    type(fixture, '[data-about]', 'hand-thrown mugs');
    click(fixture, '[data-write]');
    ctrl
      .expectOne('/admin/ads/write')
      .flush({ brand: 'Stillwater Ceramics', script: 'Stillwater. One mug, still.' });
    fixture.detectChanges();
    expect(fixture.componentInstance.script()).toContain('One mug');
    expect(fixture.componentInstance.writing()).toBe(false);
  });

  it('edit dialog dismissing the advert form forgets what was half-written', () => {
    const fixture = mounted();
    type(fixture, '[data-brand]', 'Vellum Press');
    type(fixture, '[data-about]', 'A tiny press that prints poetry on thick paper.');

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-ad-form]')).toBeNull();

    (fixture.nativeElement.querySelector('[data-add-open]') as HTMLButtonElement).click();
    fixture.detectChanges();
    expect((fixture.nativeElement.querySelector('[data-brand]') as HTMLInputElement).value).toBe(
      '',
    );
  });

  it('station fields puts the write button with the box it fills', () => {
    // Jockora-e9a.61: it sat between the Delivery helper text and The advert
    // label, belonging to neither by position, with no name of its own.
    const fixture = mounted();
    const form = fixture.nativeElement.querySelector('[data-ad-form]');
    const box = form.querySelector('[data-write]')!.closest('[data-field]');
    expect(box).not.toBeNull();
    expect(box.textContent).toContain('Write the advert');

    // IMMEDIATELY BEFORE the field it writes into.
    const script = form.querySelector('[data-script]')!.closest('label');
    expect(box.nextElementSibling).toBe(script);
  });
});
