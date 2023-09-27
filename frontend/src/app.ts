import { LitElement, css, html } from 'lit'
import { customElement, state, property } from 'lit/decorators.js'
import { unsafeHTML } from 'lit/directives/unsafe-html.js';
import { of, catchError, Subject } from 'rxjs';
import { fromFetch } from 'rxjs/fetch';
import { switchMap, takeUntil } from 'rxjs/operators';

import * as mastodon from "./mastodon";

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
  private data: mastodon.Status[] = [];

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
        <mast-status .status=${e}></mast-status>
      `)}
    `
  }

  static styles = css`
  `
}

declare global {
  interface HTMLElementTagNameMap {
    'app-root': AppRoot
  }
}

@customElement('mast-status')
export class MastStatus extends LitElement {
  @property({ attribute: false })
  status?: mastodon.Status;

  @state()
  private showRaw = false;

  render() {
    if (!this.status) {
      return html`<div class="status"></div>`
    }
    return html`
      <div class="status">
        ${this.status.account.display_name}
        ${unsafeHTML(this.status.content)}
        <div class="tools">
          <button @click="${() => { this.showRaw = !this.showRaw }}">Show raw</button>
        </div>
        ${this.showRaw ? html`<pre class="rawcontent">${JSON.stringify(this.status, null, "  ")}</pre>` : ''}
      </div>
    `
  }

  static styles = css`
    .status {
      border: solid;
      border-radius: .5rem;
      border-width: .2rem;
      margin: .5rem;
      padding: 0.4rem;
    }

    .rawcontent {
      white-space: pre-wrap;
    }
  `

}

declare global {
  interface HTMLElementTagNameMap {
    'mast-status': MastStatus
  }
}