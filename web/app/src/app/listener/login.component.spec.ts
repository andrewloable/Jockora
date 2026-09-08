import { describe, expect, it, beforeEach, vi } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Router } from '@angular/router';
import { Login } from './login.component';

describe('Login', () => {
  const navigate = vi.fn();
  let ctrl: HttpTestingController;

  beforeEach(async () => {
    navigate.mockClear();
    await TestBed.configureTestingModule({
      imports: [Login],
      providers: [
        provideHttpClient(),
        provideHttpClientTesting(),
        { provide: Router, useValue: { navigate } },
      ],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  // type drives the real inputs rather than the fields behind them, so the
  // bindings are exercised too: a form whose model is wired backwards passes
  // every test that sets the properties directly.
  function type(fixture: ReturnType<typeof TestBed.createComponent>, values: string[]): void {
    fixture.detectChanges();
    const inputs: HTMLInputElement[] = Array.from(fixture.nativeElement.querySelectorAll('input'));
    inputs.forEach((input, i) => {
      input.value = values[i];
      input.dispatchEvent(new Event('input'));
    });
    fixture.detectChanges();
  }

  it('posts what was typed and goes to the dial', () => {
    const fixture = TestBed.createComponent(Login);
    const login = fixture.componentInstance;
    type(fixture, ['andrew', 'correct horse battery']);
    expect(login.name()).toBe('andrew');
    expect(login.password()).toBe('correct horse battery');

    fixture.nativeElement.querySelector('form').dispatchEvent(new Event('submit'));
    fixture.detectChanges();

    const req = ctrl.expectOne('/login');
    expect(req.request.body).toEqual({ name: 'andrew', password: 'correct horse battery' });
    req.flush(null);
    expect(navigate).toHaveBeenCalledWith(['']);
  });

  it('keeps the form and says so on a refusal', () => {
    const fixture = TestBed.createComponent(Login);
    const login = fixture.componentInstance;
    login.name.set('andrew');
    login.password.set('wrong');
    login.submit();
    ctrl.expectOne('/login').flush(null, { status: 401, statusText: 'Unauthorized' });
    fixture.detectChanges();

    expect(navigate).not.toHaveBeenCalled();
    // The form STAYS: clearing it means typing the name again.
    expect(login.name()).toBe('andrew');
    expect(login.error()).toContain('Wrong');
    expect(fixture.nativeElement.querySelector('[data-error]')).toBeTruthy();
  });

  it('has no sign-up or reset link', () => {
    // Accounts are admin-created. Either link would be a door the design does
    // not have.
    const fixture = TestBed.createComponent(Login);
    fixture.detectChanges();
    const text = fixture.nativeElement.textContent.toLowerCase();
    expect(text).not.toContain('sign up');
    expect(text).not.toContain('register');
    expect(text).not.toContain('forgot');
  });

  it('does not leave the button pressable while it waits', () => {
    const fixture = TestBed.createComponent(Login);
    fixture.componentInstance.submit();
    fixture.detectChanges();
    expect(fixture.componentInstance.busy()).toBe(true);
    expect(fixture.nativeElement.querySelector('button').disabled).toBe(true);
    ctrl.expectOne('/login').flush(null);
    fixture.detectChanges();
    expect(fixture.componentInstance.busy()).toBe(false);
    expect(fixture.nativeElement.querySelector('button').disabled).toBe(false);
  });
});
