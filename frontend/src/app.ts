import { LitElement, css, html, nothing, TemplateResult, unsafeCSS } from 'lit'
import { customElement, state, property } from 'lit/decorators.js'
import { unsafeHTML } from 'lit/directives/unsafe-html.js';
import { ref, createRef } from 'lit/directives/ref.js';
import { of, catchError, Subject } from 'rxjs';
import { fromFetch } from 'rxjs/fetch';
import { switchMap, takeUntil } from 'rxjs/operators';
import { LitVirtualizer, RangeChangedEvent } from '@lit-labs/virtualizer';

// Import the element registration.
import '@lit-labs/virtualizer';

import normalizeCSSstr from "./normalize.css?inline";
import baseCSSstr from "./base.css?inline";

import * as mastodon from "./mastodon";

const commonCSS = [unsafeCSS(normalizeCSSstr), unsafeCSS(baseCSSstr)];

// OpenStatus is the information sent from the backend.
interface OpenStatus {
  position: number
  status: mastodon.Status
}

// StatusEntry represents the state in the UI of a given status.
interface StatusEntry {
  status: mastodon.Status;
  // Position in the stream in the backend.
  position: number;
  // HTML element.
  element?: Element;

  // Is it currently visible on the screen of the user?
  visible: boolean;
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

  private statuses: StatusEntry[] = [];
  private startPosition = 1000;
  virtuRef = createRef<LitVirtualizer>();

  connectedCallback(): void {
    super.connectedCallback();
    this.values$.pipe(takeUntil(this.unsubscribe$)).subscribe((v: OpenStatus[]) => {
      // Initial loading.
      this.statuses = [];
      // for (const s of (v as OpenStatus[])) {
      for (let i = 0; i < v.length; i++) {
        this.statuses[i + this.startPosition] = {
          status: v[i].status,
          position: v[i].position,
          visible: false,
        };
      }
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
      <div class="header"></div>
      <div class="page">
        <lit-virtualizer
          class="statuses"
          .items=${this.statuses}
          scroller
          ${ref(this.virtuRef)}
          @rangeChanged=${(e: RangeChangedEvent) => this.rangeChanged(e)}
          .layout=${{ pin: { index: this.startPosition, block: 'start' } } as any}
          .renderItem=${(st: StatusEntry, _: number): TemplateResult => st ? html`
            <mast-status class="statustrack" .status=${st.status}></mast-status>
        `: html`<div>empty</div>`}
        ></lit-virtualizer>
      </div>
    `;
  }

  rangeChanged(e: RangeChangedEvent) {
    console.log("range", e.first, e.last, e);
    if (e.last > this.statuses.length - 3) {
      console.log("adding below");
      this.statuses.push({
        status: mastodon.newFakeStatus(),
        position: this.statuses[this.statuses.length - 1].position + 1,
        visible: false,
      });
    }
    for (let i = e.first; i < e.last; i++) {
      if (this.statuses[i] === undefined) {
        console.log("filling", i);
        this.statuses[i] = {
          status: mastodon.newFakeStatus(),
          position: i,
          visible: false,
        };
      }
    }
  }

  firstUpdated(): void {
    // setTimeout(() => this.virtuRef.value!.scrollToIndex(this.startPosition, 'start'), 1000);
    console.log("firstUpdated");
    // this.virtuRef.value!.scrollToIndex(this.startPosition, 'start')
  }

  static styles = [commonCSS, css`
    :host {
      display: grid;
      grid-template-rows: 40px 1fr;
      grid-template-columns: 1fr;
      height: 100%;
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

    mast-status {
      width: 100%;
    }
  `];
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
  private accessor showRaw = false;

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
      <div class="status bg-blue-800">
        ${isReblog ? html`
          <div class="reblog bg-red-400 text-light">
            <img class="avatar" src=${this.status.account.avatar} alt="avatar of ${this.status.account.display_name}"></img>
            reblog by ${this.status.account.display_name}
          </div>
        `: nothing}
        <div class="account bg-blue-100">
          <img class="avatar" src=${s.account.avatar} alt="avatar of ${s.account.display_name}"></img>
          ${s.account.display_name} &lt;${s.account.acct}&gt;
        </div>
        <div class="content">
          ${unsafeHTML(s.content)}
        </div>
        <div class="attachments">
          ${attachments}
        </div>
        <div class="tools bg-blue-400 text-light">
          <button><span class="material-symbols-outlined" title="Favorite">star</span></button>
          <button><span class="material-symbols-outlined" title="Boost">repeat</span></button>
          <button><span class="material-symbols-outlined" title="Reply...">reply</span></button>
          <button @click="${() => { this.showRaw = !this.showRaw }}" title="Show raw status">
            <span class="material-symbols-outlined">${this.showRaw ? 'collapse_all' : 'expand_all'}</span>
          </button>
        </div>
        ${this.showRaw ? html`<pre class="rawcontent">${JSON.stringify(this.status, null, "  ")}</pre>` : nothing}
      </div>
    `
  }

  static styles = [commonCSS, css`
    .status {
      border-style: solid;
      border-radius: .3rem;
      border-width: .1rem;
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
      align-items: center;
      padding: 0.2rem;
    }

    .reblog {
      display: flex;
      align-items: center;
      padding: 0.2rem;
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
      align-items: center;
      padding: 0.2rem;
      margin-top: 0.2rem;
    }
  `];
}

declare global {
  interface HTMLElementTagNameMap {
    'mast-status': MastStatus
  }
}