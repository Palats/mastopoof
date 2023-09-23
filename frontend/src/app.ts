import { LitElement, css, html } from 'lit'
import { customElement, state } from 'lit/decorators.js'
import { of, catchError } from 'rxjs';
import { fromFetch } from 'rxjs/fetch';
import { switchMap } from 'rxjs/operators';

type JSONListEntry = {
  URI: string;
}

@customElement('app-root')
export class AppRoot extends LitElement {
  // values$ = interval(1000).pipe(take(10));
  values$ = fromFetch('/list').pipe(
    switchMap(response => {
      if (response.ok) {
        // OK return data
        return response.json();
      } else {
        // Server is returning a status requiring the client to try something else.
        return of({ error: true, message: `Error ${response.status}` });
      }
    }),
    catchError(err => {
      // Network or other error, handle appropriately
      console.error(err);
      return of({ error: true, message: err.message })
    })
  );;

  @state()
  private data: JSONListEntry[] = [];

  connectedCallback(): void {
    super.connectedCallback();
    this.values$.subscribe(v => {
      this.data = v;
      this.requestUpdate();
    });
  }

  render() {
    return html`
      <table>
      ${this.data.map(e => html`
      <tr><td>${e.URI}</td></tr>
      `)}
      </table>
    `
  }

  static styles = css``
}

declare global {
  interface HTMLElementTagNameMap {
    'app-root': AppRoot
  }
}
