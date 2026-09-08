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
    // Cleared, so the next create cannot silently reuse it.
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
});
