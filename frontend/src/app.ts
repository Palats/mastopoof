import { LitElement, css, html, nothing, TemplateResult } from 'lit'
import { customElement, state, property } from 'lit/decorators.js'
import { unsafeHTML } from 'lit/directives/unsafe-html.js';
import { of, catchError, Subject } from 'rxjs';
import { fromFetch } from 'rxjs/fetch';
import { switchMap, takeUntil } from 'rxjs/operators';

import * as mastodon from "./mastodon";

const color1bg = css`#08415C`;
// const color1fg = css`#ffffff`;
const color2bg = css`#CC2936`;
const color2fg = css`#ffffff`;
// const color3bg = css`#EBBAB9`;
// const color3fg = css`#000000`;
const color4bg = css`#388697`;
// const color4fg = css`#000000`;
const color5bg = css`#B5FFE1`;
// const color5fg = css`#000000`;

interface OpenStatus {
  position: number
  status: mastodon.Status
}

// https://adrianfaciu.dev/posts/observables-litelement/

@customElement('app-root')
export class AppRoot extends LitElement {
  unsubscribe$ = new Subject<null>();
  values$ = fromFetch('/_api/opened').pipe(
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
  private data: OpenStatus[] = [];

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
      <div class="header">
      </div>
      <div class="page">
        <div class="statuses">
          ${this.data.map(e => html`
            <mast-status .status=${e.status}></mast-status>
          `)}
        </div>
      </div>
    `
  }

  static styles = css`
    :host {
      display: grid;
      grid-template-rows: 40px 1fr;
      grid-template-columns: 1fr;
    }

    .header {
      grid-row: 1;
      background-color: #efefef;
      position: sticky;
      top: 0;
    }

    .page {
      grid-row: 2;
      display: grid;
      grid-template-columns: 1fr minmax(100px, 600px) 1fr;
    }

    .statuses {
      grid-column: 2;
    }
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
        </div>
        <div class="attachments">
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
      border-style: solid;
      border-color: ${color1bg};
      border-radius: .5rem;
      border-width: .2rem;
      margin: 0.1rem;
      padding: 0;
      background-color: #ffffff;

      overflow: hidden;
      display: grid;
    }

    .rawcontent {
      white-space: pre-wrap;
      word-break: break-all;
    }

    .account {
      display: flex;
      background-color: ${color5bg};
      align-items: center;
      padding: 0.2rem;
    }

    .reblog {
      display: flex;
      background-color: ${color2bg};
      align-items: center;
      padding: 0.2rem;
      color: ${color2fg};
    }

    .avatar {
      width: auto;
      padding-right: 0.2rem;
    }

    .account .avatar {
      max-height: 32px;
    }

    .reblog .avatar {
      max-height: 20px;
    }

    .content {
      padding: 0.2rem;
    }

    .attachments {
      width: 100%;
      display: grid;
      align-items: center;
      justify-items: center;
      grid-template-columns: 1fr;
    }

    .attachments img {
      max-width: 500px;
      max-height: 400px;
    }

    .tools {
      display: flex;
      background-color: ${color4bg};
      align-items: center;
      padding: 0.2rem;
      margin-top: 0.2rem;
    }
  `

}

declare global {
  interface HTMLElementTagNameMap {
    'mast-status': MastStatus
  }
}