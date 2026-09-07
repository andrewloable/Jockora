import { describe, expect, it, beforeEach } from 'vitest';
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
    expect(fixture.nativeElement.querySelector('[data-reset-form]').textContent).toContain('andrew');

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
});
