import { ApplicationConfig, provideBrowserGlobalErrorListeners } from '@angular/core';
import { provideHttpClient, withInterceptors } from '@angular/common/http';
import { provideRouter } from '@angular/router';
import { authInterceptor } from './api/auth.interceptor';
import { routes } from './app.routes';

export const appConfig: ApplicationConfig = {
  providers: [
    provideBrowserGlobalErrorListeners(),
    provideRouter(routes),
    // The interceptor is installed ONCE, here: a 401 anywhere sends the
    // listener back to the login form, and a component that forgot to handle
    // it would otherwise fail silently.
    provideHttpClient(withInterceptors([authInterceptor])),
  ],
};
