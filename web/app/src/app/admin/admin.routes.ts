import { Routes } from '@angular/router';
import { Admin } from './admin';
import { adminGuard } from './admin.guard';

/**
 * The operator console's routes, loaded only when somebody opens it.
 *
 * The guard sits on the console's own route rather than on the lazy route in
 * app.routes.ts. A listener who types /admin therefore downloads the chunk
 * before being sent back to the dial — a few kB, once, on a page they went
 * looking for, which is cheaper than importing the guard into the eager bundle
 * that everybody loads.
 */
export const adminRoutes: Routes = [{ path: '', component: Admin, canActivate: [adminGuard] }];
