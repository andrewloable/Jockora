import { bootstrapApplication } from '@angular/platform-browser';
import { appConfig } from './app/app.config';
import { App } from './app/app';
import { applyStoredTheme } from './app/theme';

// BEFORE bootstrap: the attribute has to be on <html> when the first paint
// happens, or a viewer who chose dark gets a white flash on every load.
applyStoredTheme();

bootstrapApplication(App, appConfig)
  .catch((err) => console.error(err));
