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
      // A GUEST IS NOT A LISTENER. While public listening is on, /me answers
      // anyone with role guest and a 200, so the catch below never fires --
      // and sending them to the dial would leave somebody who typed /admin
      // with no way to sign in. They get the form; a real listener does not,
      // because they have already passed it.
      void router.navigate([me.role === 'guest' ? '/login' : '']);
      return false;
    }),
    catchError(() => {
      void router.navigate(['/login']);
      return of(false);
    }),
  );
};
