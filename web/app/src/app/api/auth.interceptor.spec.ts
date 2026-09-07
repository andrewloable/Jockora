import { describe, expect, it, beforeEach, vi } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { HttpClient, provideHttpClient, withInterceptors } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Router } from '@angular/router';
import { authInterceptor } from './auth.interceptor';

describe('authInterceptor', () => {
  let http: HttpClient;
  let ctrl: HttpTestingController;
  const navigate = vi.fn();

  beforeEach(() => {
    navigate.mockClear();
    TestBed.configureTestingModule({
      providers: [
        provideHttpClient(withInterceptors([authInterceptor])),
        provideHttpClientTesting(),
        { provide: Router, useValue: { navigate } },
      ],
    });
    http = TestBed.inject(HttpClient);
    ctrl = TestBed.inject(HttpTestingController);
  });

  it('sends a 401 back to the login form', () => {
    http.get('/stations.json').subscribe({ error: () => undefined });
    ctrl.expectOne('/stations.json').flush(null, { status: 401, statusText: 'Unauthorized' });
    expect(navigate).toHaveBeenCalledWith(['/login']);
  });

  it('leaves a 403 alone', () => {
    // They ARE signed in and this is not theirs. Bouncing them to a login they
    // have already passed would be a loop with no explanation.
    http.get('/admin/users').subscribe({ error: () => undefined });
    ctrl.expectOne('/admin/users').flush(null, { status: 403, statusText: 'Forbidden' });
    expect(navigate).not.toHaveBeenCalled();
  });

  it('leaves a 500 alone', () => {
    http.get('/stations.json').subscribe({ error: () => undefined });
    ctrl.expectOne('/stations.json').flush(null, { status: 500, statusText: 'Server Error' });
    expect(navigate).not.toHaveBeenCalled();
  });

  it('does not bounce the login request itself', () => {
    // A wrong password would navigate to the page they are already on and wipe
    // what they typed.
    http.post('/login', {}).subscribe({ error: () => undefined });
    ctrl.expectOne('/login').flush(null, { status: 401, statusText: 'Unauthorized' });
    expect(navigate).not.toHaveBeenCalled();
  });

  it('passes a success through untouched', () => {
    let body: unknown;
    http.get('/me').subscribe((v) => (body = v));
    ctrl.expectOne('/me').flush({ name: 'andrew' });
    expect(body).toEqual({ name: 'andrew' });
    expect(navigate).not.toHaveBeenCalled();
  });
});
