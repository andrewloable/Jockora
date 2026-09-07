import { HttpErrorResponse, HttpInterceptorFn } from '@angular/common/http';
import { inject } from '@angular/core';
import { Router } from '@angular/router';
import { catchError, throwError } from 'rxjs';

/**
 * A 401 anywhere sends the listener back to the login form.
 *
 * ONLY 401. A 403 means they are signed in and this is not theirs -- bouncing
 * them to a login they have already passed would be a loop with no explanation.
 * A 500 is the server's problem and the component that asked is the one that
 * should say so.
 */
export const authInterceptor: HttpInterceptorFn = (req, next) => {
  const router = inject(Router);
  return next(req).pipe(
    catchError((err: unknown) => {
      if (err instanceof HttpErrorResponse && err.status === 401) {
        // Never from the login request itself: a wrong password would
        // navigate to the page the listener is already on and wipe what they
        // typed.
        if (!req.url.endsWith('/login')) {
          void router.navigate(['/login']);
        }
      }
      return throwError(() => err);
    }),
  );
};
