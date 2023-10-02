import { LitElement, css, html, nothing, TemplateResult } from 'lit'
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

    // This actual status - i.e., the reblogged one when it is a reblogged, or
    // the basic one.
    const s = this.status.reblog ?? this.status;
    const isReblog = !!this.status.reblog;

    const attachments: TemplateResult[] = [];
    for (const ma of s.media_attachments) {
      if (ma.type === "image") {
        attachments.push(html`
          <img src=${ma.preview_url} alt=${ma.description}></img>
        `);
      }
    }

    return html`
      <div class="status">
        ${isReblog ? html`
          <div class="reblog">
            <img class="avatar" src=${this.status.account.avatar} alt="avatar of ${this.status.account.display_name}"></img>
            reblog by ${this.status.account.display_name}
          </div>
        `: nothing}
        <div class="account">
          <img class="avatar" src=${s.account.avatar} alt="avatar of ${s.account.display_name}"></img>
          ${s.account.display_name} &lt;${s.account.acct}&gt;
        </div>
        <div class="content">
          ${unsafeHTML(s.content)}
          ${attachments}
        </div>
        <div class="tools">
          <button @click="${() => { this.showRaw = !this.showRaw }}">Show raw</button>
        </div>
        ${this.showRaw ? html`<pre class="rawcontent">${JSON.stringify(this.status, null, "  ")}</pre>` : nothing}
      </div>
    `
  }

  static styles = css`
    .status {
      border: solid;
      border-radius: .5rem;
      border-width: .2rem;
      margin: 0.1rem;
      padding: 0;

      overflow: hidden;
      display: grid;
    }

    .rawcontent {
      white-space: pre-wrap;
    }

    .account {
      display: flex;
      background-color: #baffff;
      align-items: center;
      padding: 0.2rem;
    }

    .avatar {
      width: auto;
      max-height: 32px;
    }

    .content {
      padding: 0.2rem;
    }

    .tools {
      display: flex;
      background-color: #e6e6e6;
      align-items: center;
      padding: 0.2rem;
    }
  `

}

declare global {
  interface HTMLElementTagNameMap {
    'mast-status': MastStatus
  }
}