import { LitElement, css, html } from 'lit'
import { customElement, state } from 'lit/decorators.js'
import { of, catchError, Subject } from 'rxjs';
import { fromFetch } from 'rxjs/fetch';
import { switchMap, takeUntil } from 'rxjs/operators';

import { mastodon } from "masto";

// https://adrianfaciu.dev/posts/observables-litelement/

@customElement('app-root')
export class AppRoot extends LitElement {
  unsubscribe$ = new Subject<null>();
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
  );

  @state()
  private data: mastodon.v1.Status[] = [];

  connectedCallback(): void {
    super.connectedCallback();
    this.values$.pipe(takeUntil(this.unsubscribe$)).subscribe(v => {
      this.data = v;
      this.requestUpdate();
    });
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this.unsubscribe$.next(null);
    this.unsubscribe$.complete();
  }

  render() {
    return html`
      ${this.data.map(e => html`
      <div>
        <h2>${e.uri}</h2>
        ${e.content}
      </div>
      `)}
    `
  }

  static styles = css``
}

declare global {
  interface HTMLElementTagNameMap {
    'app-root': AppRoot
  }
}
