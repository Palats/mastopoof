import { LitElement, css, html, nothing, TemplateResult, unsafeCSS } from 'lit'
import { customElement, state, property } from 'lit/decorators.js'
import { unsafeHTML } from 'lit/directives/unsafe-html.js';
import { ref, createRef } from 'lit/directives/ref.js';
import { repeat } from 'lit/directives/repeat.js';
import { of, catchError, Subject } from 'rxjs';
import { fromFetch } from 'rxjs/fetch';
import { switchMap, takeUntil } from 'rxjs/operators';

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

  connectedCallback(): void {
    super.connectedCallback();
    this.values$.pipe(takeUntil(this.unsubscribe$)).subscribe(v => {
      this.statuses = [];
      for (const s of (v as OpenStatus[])) {
        this.insertStatus(s);
      }
      this.requestUpdate();
    });
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this.unsubscribe$.next(null);
    this.unsubscribe$.complete();
  }

  pageRef = createRef<HTMLDivElement>();

  observer?: IntersectionObserver;

  render() {
    return html`
      <div class="header"></div>
      <div class="page" ${ref(this.pageRef)}>
        <div class="statuses">
          ${repeat(this.statuses, st => st.status.id, st => html`
            <mast-status class="statustrack" .status=${st.status} ${ref(elt => this.refStatusUpdated(st, elt))}></mast-status>
          `)}
        </div>
      </div>
    `;
  }

  // Reference to a Status UI entry changed.
  refStatusUpdated(st: StatusEntry, elt?: Element) {
    if (st.element && st.element != elt) {
      this.observer?.unobserve(st.element);
    }
    st.element = elt;
    if (!elt) {
      return;
    }
    if (this.observer) {
      this.observer.observe(elt);
    }
  }

  firstUpdated(): void {
    // Setup observer on status to detect moving in / out of the window.
    this.observer = new IntersectionObserver(
      (entries: IntersectionObserverEntry[]) => this.onIntersection(entries),
      {
        // root: this.pageRef.value,
        rootMargin: "0px",
      });
  }

  insertStatus(s: OpenStatus, beginning = false) {
    if (this.statuses.length > 20) {
      console.error("too many statuses");
      return;
    }
    const st = {
      status: s.status,
      position: s.position,
      visible: false,
    };

    if (beginning) {
      this.statuses.unshift(st);
    } else {
      this.statuses.push(st);
    }
  }

  onIntersection(entries: IntersectionObserverEntry[]) {
    let visibilityChanged = false;
    for (const entry of entries) {
      // Operator ||= is lazy and might not evaluate right element.
      const v = this.onSingleIntersection(entry);
      visibilityChanged ||= v;
    }
    if (visibilityChanged) {
      this.checkForExtraStatuses();
    }
  }

  // Returns false if no visibility changed, true otherwise.
  onSingleIntersection(entry: IntersectionObserverEntry): boolean {
    const idx = this.statuses.findIndex(st => st.element == entry.target);
    if (idx < 0) {
      console.error("observer element not found", entry);
      return false;
    }
    const st = this.statuses[idx];
    // In doubt, it is better to consider that something is hidden - it avoids
    // infinitely loading by mistake.
    if (!entry.rootBounds) {
      return false;
    }
    if (st.visible == entry.isIntersecting) {
      return false;
    }
    st.visible = entry.isIntersecting;
    return true;
  }

  // Determine whether new statuses should be obtained from the server.
  checkForExtraStatuses() {
    // Let's first find what we have - how many statuses are hidden in the past
    // and vice versa.
    let firstVisible: number | undefined;
    let lastVisible: number | undefined;

    for (let i = 0; i < this.statuses.length; i++) {
      if (this.statuses[i].visible) {
        firstVisible = i;
        break;
      }
    }
    if (firstVisible === undefined) {
      console.log("nothing is visible yet");
      return;
    }
    for (let i = firstVisible; i < this.statuses.length; i++) {
      if (this.statuses[i].visible) {
        lastVisible = i;
      } else {
        break;
      }
    }
    if (lastVisible === undefined) {
      console.error("unable to find display boundaries");
      return;
    }
    for (let i = lastVisible + 1; i < this.statuses.length; i++) {
      if (this.statuses[i].visible) {
        console.error("extra visible statuses", firstVisible, lastVisible, "all:", this.statuses.map(st => st.visible));
        return;
      }
    }
    console.log(`${firstVisible} statuses above, ${this.statuses.length - lastVisible - 1} statuses below`);

    // We have the limits - determine if we need to load more things.
    if (firstVisible < 2) {
      /*this.insertStatus({
        status: mastodon.newFakeStatus(),
        position: this.statuses[0].position - 1,
      }, true);
      this.requestUpdate();*/
    }
    if (lastVisible > this.statuses.length - 3) {
      console.log("adding below");
      this.insertStatus({
        status: mastodon.newFakeStatus(),
        position: this.statuses[this.statuses.length - 1].position + 1,
      });
      this.requestUpdate();
    }
  }

  static styles = [commonCSS, css`
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