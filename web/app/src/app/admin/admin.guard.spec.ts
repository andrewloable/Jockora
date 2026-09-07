import { describe, expect, it, beforeEach, vi } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Router } from '@angular/router';
import { adminGuard } from './admin.guard';

describe('adminGuard', () => {
  const navigate = vi.fn();
  let ctrl: HttpTestingController;

  beforeEach(() => {
    navigate.mockClear();
    TestBed.configureTestingModule({
      providers: [
        provideHttpClient(),
        provideHttpClientTesting(),
        { provide: Router, useValue: { navigate } },
      ],
    });
    ctrl = TestBed.inject(HttpTestingController);
  });

  function run(): Promise<boolean> {
    return new Promise((resolve) => {
      TestBed.runInInjectionContext(() => {
        const result = adminGuard({} as never, {} as never);
        (result as { subscribe: (f: (v: boolean) => void) => void }).subscribe(resolve);
      });
    });
  }

  it('lets an operator in', async () => {
    const allowed = run();
    ctrl.expectOne('/me').flush({ name: 'andrew', role: 'admin' });
    expect(await allowed).toBe(true);
    expect(navigate).not.toHaveBeenCalled();
  });

  it('sends a listener to the dial, not to a login they have passed', async () => {
    const allowed = run();
    ctrl.expectOne('/me').flush({ name: 'kim', role: 'listener' });
    expect(await allowed).toBe(false);
    expect(navigate).toHaveBeenCalledWith(['']);
  });

  it('sends a stranger to the login form', async () => {
    const allowed = run();
    ctrl.expectOne('/me').flush(null, { status: 401, statusText: 'Unauthorized' });
    expect(await allowed).toBe(false);
    expect(navigate).toHaveBeenCalledWith(['/login']);
  });
});
