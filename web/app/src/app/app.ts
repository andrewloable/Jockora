import { Component } from '@angular/core';
import { RouterOutlet } from '@angular/router';

/** The shell. Everything else is a route. */
@Component({
  imports: [RouterOutlet],
  selector: 'app-root',
  standalone: true,
  template: `<router-outlet />`,
})
export class App {}
