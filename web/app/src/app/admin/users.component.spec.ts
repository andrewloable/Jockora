import { describe, expect, it, beforeEach, vi } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Users } from './users.component';

const people = [
  { id: 1, name: 'andrew', role: 'admin', disabled: false },
  { id: 2, name: 'guest', role: 'listener', disabled: true },
];

describe('Users', () => {
  let ctrl: HttpTestingController;

  beforeEach(async () => {
    // DEFAULT: yes. Destructive actions ask now, and every test that was
    // written before they did is still testing what happens after the answer.
    // The tests about the QUESTION stub it themselves.
    vi.stubGlobal('confirm', () => true);
    await TestBed.configureTestingModule({
      imports: [Users],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  function mounted(list = people) {
    const fixture = TestBed.createComponent(Users);
    ctrl.expectOne('/admin/users').flush(list);
    fixture.detectChanges();
    // THE CREATE FORM IS BEHIND A BUTTON since Jockora-e9a.60 moved it into a
    // dialog. It used to be always on screen, which is what every test below
    // was written against -- including the ones that check it stays empty.
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

  it('console states tells a fresh operator what to do when there is only them', () => {
    const empty = mounted([]).nativeElement.querySelector('[data-empty]');
    expect(empty).not.toBeNull();
    // Accounts are admin-created; there is no self-registration to wait for.
    expect(empty.textContent).toContain('Add an account');
  });

  it('console states does not call a loading accounts table an empty one', () => {
    const fixture = TestBed.createComponent(Users);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-empty]')).toBeNull();
    expect(fixture.nativeElement.querySelector('[data-loading]')).not.toBeNull();
  });

  it('lists the accounts without any trace of a hash', () => {
    const html = mounted().nativeElement.innerHTML;
    expect(html).toContain('andrew');
    expect(html).toContain('admin');
    expect(html).toContain('disabled');
    // A password hash is a target. It never reaches the browser and never
    // reaches the page.
    expect(html).not.toContain('pw_hash');
  });

  it('creates an account and says the password will not be readable later', () => {
    const fixture = mounted();
    type(fixture, '[data-name]', 'roxy');
    type(fixture, '[data-password]', 'correct horse battery');
    type(fixture, '[data-role]', 'admin');
    fixture.nativeElement.querySelector('[data-add]').click();

    const req = ctrl.expectOne('/admin/users');
    expect(req.request.method).toBe('POST');
    expect(req.request.body).toEqual({
      name: 'roxy',
      password: 'correct horse battery',
      role: 'admin',
    });
    req.flush({ id: 3 });
    ctrl.expectOne('/admin/users').flush(people);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('not stored');
    // A SUCCESSFUL CREATE CLOSES THE DIALOG since Jockora-e9a.60, and reopening
    // it starts blank -- so the next create cannot silently reuse the password.
    expect(fixture.nativeElement.querySelector('[data-new-account]')).toBeNull();
    (fixture.nativeElement.querySelector('[data-add-open]') as HTMLButtonElement).click();
    fixture.detectChanges();
    expect((fixture.nativeElement.querySelector('[data-password]') as HTMLInputElement).value).toBe(
      '',
    );
  });

  it('disables an active account and enables a disabled one', () => {
    const fixture = mounted();
    const buttons = fixture.nativeElement.querySelectorAll('[data-toggle]');
    expect(buttons[0].textContent.trim()).toBe('Disable');
    expect(buttons[1].textContent.trim()).toBe('Enable');

    buttons[0].click();
    ctrl.expectOne('/admin/users/1/disable').flush(null);
    ctrl.expectOne('/admin/users').flush(people);

    buttons[1].click();
    ctrl.expectOne('/admin/users/2/enable').flush(null);
    ctrl.expectOne('/admin/users').flush(people);
  });

  it('resets a password', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-reset]').click();
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-reset-form]').textContent).toContain(
      'andrew',
    );

    type(fixture, '[data-new-password]', 'a whole new one');
    fixture.nativeElement.querySelector('[data-save-password]').click();
    const req = ctrl.expectOne('/admin/users/1/password');
    expect(req.request.method).toBe('POST');
    expect(req.request.body).toEqual({ password: 'a whole new one' });
    req.flush(null);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-reset-form]')).toBeNull();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
      'stays signed in',
    );
  });

  it('can back out of a reset', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-reset]').click();
    fixture.detectChanges();
    fixture.nativeElement.querySelector('[data-cancel-reset]').click();
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-reset-form]')).toBeNull();
    ctrl.verify();
  });

  it('deletes an account', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-remove]').click();
    const req = ctrl.expectOne('/admin/users/1');
    expect(req.request.method).toBe('DELETE');
    req.flush(null);
    ctrl.expectOne('/admin/users').flush([people[1]]);
  });

  it('shows the refusal to delete the last admin', () => {
    // A console that swallows the 409 looks broken; the operator retries,
    // and still nothing happens.
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-remove]').click();
    ctrl
      .expectOne('/admin/users/1')
      .flush({ error: 'that is the last admin' }, { status: 409, statusText: 'Conflict' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('last admin');
  });

  it('says so when anything fails', () => {
    const fixture = TestBed.createComponent(Users);
    ctrl.expectOne('/admin/users').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
      'read the accounts',
    );

    // The create form is in a dialog now, so Create has to be reached through it.
    (fixture.nativeElement.querySelector('[data-add-open]') as HTMLButtonElement).click();
    fixture.detectChanges();
    fixture.nativeElement.querySelector('[data-add]').click();
    ctrl.expectOne('/admin/users').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
      'create that account',
    );
  });

  it('says so when a toggle, a reset or a delete fails without a reason', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-toggle]').click();
    ctrl.expectOne('/admin/users/1/disable').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
      'change that account',
    );

    fixture.nativeElement.querySelector('[data-reset]').click();
    fixture.detectChanges();
    fixture.nativeElement.querySelector('[data-save-password]').click();
    ctrl.expectOne('/admin/users/1/password').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
      'change that password',
    );

    fixture.nativeElement.querySelector('[data-remove]').click();
    ctrl.expectOne('/admin/users/1').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
      'delete that account',
    );
  });

  it('server words the role goes back to listener after an admin is created', () => {
    const fixture = mounted();
    type(fixture, '[data-name]', 'second');
    type(fixture, '[data-password]', 'a long enough password');
    type(fixture, '[data-role]', 'admin');
    (fixture.nativeElement.querySelector('[data-add]') as HTMLButtonElement).click();
    const made = ctrl.expectOne('/admin/users');
    expect(made.request.body.role).toBe('admin');
    made.flush({ id: 3 });
    ctrl.expectOne('/admin/users').flush(people);
    fixture.detectChanges();

    (fixture.nativeElement.querySelector('[data-add-open]') as HTMLButtonElement).click();
    fixture.detectChanges();
    type(fixture, '[data-name]', 'third');
    type(fixture, '[data-password]', 'a long enough password');
    (fixture.nativeElement.querySelector('[data-add]') as HTMLButtonElement).click();

    // NOT ADMIN. Role is the privilege decision on this form, and an operator
    // who just made an admin would make another one by not touching a box.
    expect(ctrl.expectOne('/admin/users').request.body.role).toBe('listener');
  });

  // ------------------------------------------------- what the server said --
  // The handlers refuse in two shapes and the console read one at a time:
  // users_api writes twelve text/plain refusals through http.Error and nine
  // JSON ones through writeFieldError. Both of these were measured wrong.

  it("server words names the taken name rather than a sentence of its own", () => {
    const fixture = mounted();
    type(fixture, '[data-name]', 'guest');
    type(fixture, '[data-password]', 'a long enough password');
    (fixture.nativeElement.querySelector('[data-add]') as HTMLButtonElement).click();
    // A DUPLICATE NAME IS text/plain: http.Error, not writeFieldError. The
    // console printed its own generic line instead, so the operator was told
    // nothing about which of the two boxes to change.
    ctrl.expectOne('/admin/users').flush('that name is taken\n', {
      status: 409,
      statusText: 'Conflict',
    });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
      'that name is taken',
    );
  });

  it("server words says which field a save was refused for, not object Object", () => {
    const fixture = mounted();
    fixture.nativeElement.querySelectorAll('[data-edit]')[0].click();
    fixture.detectChanges();
    type(fixture, '[data-edit-name]', '');
    (fixture.nativeElement.querySelector('[data-save]') as HTMLButtonElement).click();
    // A REFUSED FIELD IS JSON. Read as text it stringified to "[object
    // Object]", which is the console telling an operator nothing twice over.
    ctrl.expectOne('/admin/users/1').flush(
      { field: 'name', error: 'a name is required' },
      { status: 400, statusText: 'Bad Request' },
    );
    fixture.detectChanges();
    const said = fixture.nativeElement.querySelector('[data-said]').textContent;
    expect(said).toContain('a name is required');
    expect(said).not.toContain('object Object');
  });

  it('labels its columns', () => {
    // A grid of bare values makes the reader infer what each column is from
    // whatever the first row happens to contain -- and "rock" in a column of
    // its own could be a genre, a tag or a mood.
    const head = mounted().nativeElement.querySelector('[data-users] thead').textContent;
    expect(head).toContain('Name');
    expect(head).toContain('Role');
    expect(head).toContain('Status');
    expect(head).toContain('Actions');
  });

  // ------------------------------------------------------- Jockora-e9a.62 --
  //
  // ONE password signal was bound to BOTH forms. The New account fieldset is
  // always rendered and the reset fieldset appears beside it, so typing a new
  // password for an existing user silently filled the create form with it --
  // and Cancel did not clear it, because it only closed the reset form.
  //
  // REPRODUCED IN A BROWSER before it was fixed: type SECRET-FOR-USER-A into
  // the reset field, press Cancel, and the new-account password field still
  // holds SECRET-FOR-USER-A. Both are type=password, so it renders as dots in a
  // form nobody is looking at. The next account created gets that password, and
  // the operator hands over a different one.

  it('password field keeps the reset form separate from the create form', () => {
    // ONE FIXTURE, BOTH FORMS, one after the other. They used to be on screen
    // together bound to the same signal; since Jockora-e9a.60 they are separate
    // dialogs and cannot both be open, so the leak shows on the way BACK rather
    // than side by side. The signals are still what makes them independent.
    const fixture = mounted();
    type(fixture, '[data-password]', 'for-the-new-account');

    (fixture.nativeElement.querySelector('[data-reset]') as HTMLButtonElement).click();
    fixture.detectChanges();
    type(fixture, '[data-new-password]', 'SECRET-FOR-USER-A');

    // THE TYPING GOES TO ONE SIGNAL ONLY. That is the independence this fix is
    // about, and with one shared signal the create form would now hold
    // SECRET-FOR-USER-A.
    expect(fixture.componentInstance.newPassword()).toBe('SECRET-FOR-USER-A');
    expect(fixture.componentInstance.password()).toBe('');

    // AND THE RESET'S PASSWORD NEVER REACHES THE CREATE FORM.
    (fixture.nativeElement.querySelector('[data-cancel-reset]') as HTMLButtonElement).click();
    fixture.detectChanges();
    (fixture.nativeElement.querySelector('[data-add-open]') as HTMLButtonElement).click();
    fixture.detectChanges();
    expect((fixture.nativeElement.querySelector('[data-password]') as HTMLInputElement).value).toBe(
      '',
    );
  });

  it('password field leaves nothing behind when a reset is cancelled', () => {
    const fixture = mounted();
    const resets = () =>
      [...fixture.nativeElement.querySelectorAll('[data-reset]')] as HTMLButtonElement[];
    resets()[0].click();
    fixture.detectChanges();
    type(fixture, '[data-new-password]', 'SECRET-FOR-USER-A');

    (fixture.nativeElement.querySelector('[data-cancel-reset]') as HTMLButtonElement).click();
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-reset-form]')).toBeNull();
    // CANCEL DROPS IT, rather than leaving a password for one account sitting
    // in memory behind a closed form.
    expect(fixture.componentInstance.newPassword()).toBe('');
    expect(fixture.componentInstance.password()).toBe('');
  });

  it('password field does not carry one account password into another reset', () => {
    // SWITCHING DIRECTLY, without cancelling in between -- the path Cancel's
    // own clearing does not cover, and the one that would show user A's
    // password in user B's form.
    const fixture = mounted();
    const resets = () =>
      [...fixture.nativeElement.querySelectorAll('[data-reset]')] as HTMLButtonElement[];
    resets()[0].click();
    fixture.detectChanges();
    type(fixture, '[data-new-password]', 'SECRET-FOR-USER-A');

    resets()[1].click();
    fixture.detectChanges();
    const reopened = fixture.nativeElement.querySelector('[data-new-password]') as HTMLInputElement;
    expect(reopened.value).toBe('');
    expect(fixture.componentInstance.newPassword()).toBe('');
  });

  it('password field keeps what was typed when a reset fails', () => {
    const fixture = mounted();
    (fixture.nativeElement.querySelector('[data-reset]') as HTMLButtonElement).click();
    fixture.detectChanges();
    type(fixture, '[data-new-password]', 'SECRET-FOR-USER-A');
    (fixture.nativeElement.querySelector('[data-save-password]') as HTMLButtonElement).click();
    ctrl
      .expectOne((r) => r.url.includes('/password'))
      .flush({ error: 'that password is too short' }, { status: 400, statusText: 'Bad Request' });
    fixture.detectChanges();

    // THE FORM STAYS OPEN AND KEEPS IT. The usual failure is the server saying
    // the password is too short, which is the worst possible moment to make
    // somebody retype a long one. It cannot reach the create form any more --
    // that is what the separate signal is for, and it is asserted here too.
    expect(fixture.nativeElement.querySelector('[data-reset-form]')).not.toBeNull();
    const still = fixture.nativeElement.querySelector('[data-new-password]') as HTMLInputElement;
    expect(still.value).toBe('SECRET-FOR-USER-A');
    expect(fixture.componentInstance.password()).toBe('');
    expect(fixture.nativeElement.querySelector('[data-said]')!.textContent).toContain('too short');
  });

  describe('editing an account', () => {
    // A name or a role was fixed at creation: a typo meant deleting the account
    // and building it again, and deleting the last admin is refused outright --
    // so on a one-operator install there was no route at all.
    function editing(fixture: ReturnType<typeof mounted>) {
      fixture.nativeElement.querySelectorAll('[data-edit]')[0].click();
      fixture.detectChanges();
      return fixture;
    }

    it('opens a row with the account values in it', () => {
      const fixture = editing(mounted());
      expect(fixture.nativeElement.querySelector('[data-edit-name]').value).toBe(people[0].name);
      expect(fixture.nativeElement.querySelector('[data-edit-role]').value).toBe(people[0].role);
    });

    it('saves the new name and role', () => {
      const fixture = editing(mounted());
      type(fixture, '[data-edit-name]', 'andrew2');
      type(fixture, '[data-edit-role]', 'listener');
      fixture.nativeElement.querySelector('[data-save]').click();

      const req = ctrl.expectOne('/admin/users/' + people[0].id);
      expect(req.request.method).toBe('PUT');
      expect(req.request.body).toEqual({ name: 'andrew2', role: 'listener' });
      req.flush(null);
      ctrl.expectOne('/admin/users').flush(people);
      fixture.detectChanges();
      expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Saved');
      expect(fixture.nativeElement.querySelector('[data-edit-name]')).toBeNull();
    });

    it('cancels without sending anything', () => {
      const fixture = editing(mounted());
      type(fixture, '[data-edit-name]', 'nope');
      fixture.nativeElement.querySelector('[data-cancel]').click();
      fixture.detectChanges();
      expect(fixture.nativeElement.querySelector('[data-edit-name]')).toBeNull();
      ctrl.verify();
    });

    it("repeats the server's reason and keeps the row open", () => {
      // The refusals here are the ones that matter: a taken name, and the
      // console refusing to let an operator take admin off themselves.
      const fixture = editing(mounted());
      fixture.nativeElement.querySelector('[data-save]').click();
      ctrl
        .expectOne('/admin/users/' + people[0].id)
        .flush('you cannot take admin off your own account', {
          status: 409,
          statusText: 'Conflict',
        });
      fixture.detectChanges();
      expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
        'your own account',
      );
      expect(fixture.nativeElement.querySelector('[data-edit-name]')).not.toBeNull();

      // And a refusal with no message still says something.
      fixture.nativeElement.querySelector('[data-save]').click();
      ctrl
        .expectOne('/admin/users/' + people[0].id)
        .flush(null, { status: 500, statusText: 'Error' });
      fixture.detectChanges();
      expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Could not');
    });
  });

  it('destructive accounts asks before deleting', () => {
    const fixture = mounted();
    const confirmed: string[] = [];
    vi.stubGlobal('confirm', (m: string) => {
      confirmed.push(m);
      return false;
    });
    fixture.nativeElement.querySelector('[data-remove]').click();
    ctrl.expectNone('/admin/users/1');
    expect(confirmed[0]).toContain('andrew');

    vi.stubGlobal('confirm', () => true);
    fixture.nativeElement.querySelector('[data-remove]').click();
    ctrl.expectOne('/admin/users/1').flush(null);
    vi.unstubAllGlobals();
  });

  it('destructive accounts marks the delete button as destructive', () => {
    const fixture = mounted();
    expect(
      fixture.nativeElement.querySelector('[data-remove]').getAttribute('data-danger'),
    ).not.toBeNull();
  });

  it('edit dialog dismissing the create form takes its password with it', () => {
    const fixture = mounted();
    type(fixture, '[data-name]', 'rosa');
    type(fixture, '[data-password]', 'a-secret-for-rosa');

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-new-account]')).toBeNull();

    // A PASSWORD LEFT IN A CLOSED FORM is the whole family of bug this and
    // Jockora-e9a.62 are about, so dismissing clears it rather than hiding it.
    expect(fixture.componentInstance.password()).toBe('');
    expect(fixture.componentInstance.name()).toBe('');
  });

  it('edit dialog dismissing a reset takes its password with it', () => {
    const fixture = mounted();
    (fixture.nativeElement.querySelector('[data-reset]') as HTMLButtonElement).click();
    fixture.detectChanges();
    type(fixture, '[data-new-password]', 'SECRET-FOR-USER-A');

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-reset-form]')).toBeNull();
    expect(fixture.componentInstance.newPassword()).toBe('');
  });
});
