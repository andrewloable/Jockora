import { Routes } from '@angular/router';
import { Listener } from './listener/listener';
import { Login } from './listener/login.component';

/**
 * The two halves of the product.
 *
 * The listener is EAGER because it is what almost everyone loads, and a dial
 * that arrives after a second chunk download is a second of blank screen on a
 * phone. The console is LAZY because most people never open it, and its forms,
 * tables and vocabulary lists are weight a listener should not pay for.
 */
export const routes: Routes = [
  { path: '', component: Listener },
  { path: 'login', component: Login },
  {
    path: 'admin',
    loadChildren: () => import('./admin/admin.routes').then((m) => m.adminRoutes),
  },
  // Anything else is the listener's page rather than a 404: a stale bookmark
  // should land somebody on the dial, not on an error.
  { path: '**', redirectTo: '' },
];
