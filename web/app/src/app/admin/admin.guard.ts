import { inject } from '@angular/core';
import { CanActivateFn, Router } from '@angular/router';
import { catchError, map, of } from 'rxjs';
import { Api } from '../api/api';

/**
 * The console is for operators.
 *
 * A LISTENER IS SENT TO THE DIAL, not to the login form: they are signed in
 * perfectly well, and asking them to sign in again would be a loop they cannot
 * escape. Somebody with no session at all goes to the form.
 */
export const adminGuard: CanActivateFn = () => {
  const api = inject(Api);
  const router = inject(Router);

  return api.me().pipe(
    map((me) => {
      if (me.role === 'admin') {
        return true;
      }
      void router.navigate(['']);
      return false;
    }),
    catchError(() => {
      void router.navigate(['/login']);
      return of(false);
    }),
  );
};
