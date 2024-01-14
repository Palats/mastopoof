import { LitElement, css, html, nothing, TemplateResult, unsafeCSS } from 'lit'
import { customElement, state, property } from 'lit/decorators.js'
import { unsafeHTML } from 'lit/directives/unsafe-html.js';
import { ref, createRef } from 'lit/directives/ref.js';
import { LitVirtualizer, RangeChangedEvent } from '@lit-labs/virtualizer';
import { flow } from '@lit-labs/virtualizer/layouts/flow.js';

import { createPromiseClient, ConnectError } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { Mastopoof } from "mastopoof-proto/gen/mastopoof/mastopoof_connect";

// XXX move that elsewhere
const transport = createConnectTransport({
  baseUrl: "/_rpc/",
});
const client = createPromiseClient(Mastopoof, transport);

// Import the element registration.
import '@lit-labs/virtualizer';

import normalizeCSSstr from "./normalize.css?inline";
import baseCSSstr from "./base.css?inline";

import * as mastodon from "./mastodon";
import { VisibilityChangedEvent } from '../node_modules/@lit-labs/virtualizer/events';

const commonCSS = [unsafeCSS(normalizeCSSstr), unsafeCSS(baseCSSstr)];

// StatusEntry represents the state in the UI of a given status.
interface StatusEntry {
  // Position in the stream in the backend.
  position: number;
  // status, if loaded.
  status?: mastodon.Status;
  // error, if loading did not work.
  error?: string;
}

// https://adrianfaciu.dev/posts/observables-litelement/
// https://github.com/lit/lit/tree/main/packages/labs/virtualizer#readme

@customElement('app-root')
export class AppRoot extends LitElement {
  private statuses: StatusEntry[] = [];
  // startIndex is the arbitrary point in the statuses list where initial status are added.
  private startIndex?: number;
  // Indicates the delta between stream position (backend) and index in the
  // frontend (here) list of statuses.
  // i.e., position + positionOffset == index
  private positionOffset?: number;
  // Position of the last status marked as read.
  private lastRead?: number;

  virtuRef = createRef<LitVirtualizer>();

  connectedCallback(): void {
    super.connectedCallback();
    this.initialFetch();
  }

  disconnectedCallback() {
    super.disconnectedCallback();
  }

  async initialFetch() {
    const resp = await client.initialStatuses({});

    this.lastRead = Number(resp.lastRead);
    // Initial loading.
    if (resp.items.length < 1) {
      console.error("no status");
      return;
    }

    // Calculate offsets.
    // We want to have index 0 (first element in the UI) to match position 1 - first element in the stream.
    // We also have: position + positionOffset == index
    // So: positionOffset = 0 (index) - position (1) = -1
    this.positionOffset = -1;
    // And we want to load the view on v[0].position
    this.startIndex = Number(resp.items[0].position) + this.positionOffset;
    setTimeout(() => {
      console.log("plop");
      // this.virtuRef.value!.scrollBy({ top: 100 });
      // this.virtuRef.value!.element(this.startIndex!)?.scrollIntoView();
      // this.scrollBy({ top: 200 });
      // window.scrollY += 100;
      // window.scrollBy({ top: -60 });
    }, 500);

    for (let i = 0; i < resp.items.length; i++) {
      const item = resp.items[i];
      const position = Number(item.position);
      const status = JSON.parse(item.status!.content) as mastodon.Status;
      this.statuses[position + this.positionOffset] = {
        status: status,
        position: position,
      };
    }
    console.log("initial indexes", this.startIndex, Number(resp.items[resp.items.length - 1].position) + this.positionOffset);
    this.requestUpdate();
  }

  static styles = [commonCSS, css`
    :host {
      /*display: grid;
      grid-template-rows: 40px 1fr;
      grid-template-columns: 1fr;*/
      display: flex;
      flex-direction: column;
      height: 100%;
    }

    .header {
      /*grid-row: 1;*/
      background-color: #ddf4ff;
      position: fixed;
      top: 0;
      min-height: 40px;
      width: 100%;
      z-index: 3;
      box-sizing: border-box;
    }

    .page {
      /*grid-row: 2;*/
      /*display: grid;
      grid-template-columns: 1fr minmax(100px, 600px) 1fr;*/
      display: flex;
      flex-direction: row;
      top: 40px;
    }

    .leftpane {
      /*grid-column: 1;*/
      background-color: #f3aeae;
      /*top: 60px;*/
      min-width: 285px;
    }

    .leftpanecontent {
      /* Somehow, fixed without any coordinate does the right thing, while
       * even with top: 0, it shifts things around */
      position: fixed;
      top: 60px;
    }

    .middlepane {
      /*grid-column: 2;*/
      background-color: #bef3ae;
      min-width: 600px;
      /*position: relative;
      top: 120px;*/
    }

    .rightpane {
      /*grid-column: 3;*/
      background-color: #aec0f3;
      /*top: 60px;
      right: 0;*/
      min-width: 285px;
    }

    .rightpanecontent {
      position: fixed;
      top: 60px;
    }


    .contenthead {
      position: sticky;
      top: 40px;
      z-index: 2;
      background-color: #f3aeed;
      box-sizing: border-box;
    }

    .content {
      /*display: grid;
      grid-template-rows: 1fr;*/
      /*position: relative;
      top: 60px;*/
    }

    .buffer {
      height: 60px;
    }

    .statuses {
      /*grid-row: 1;*/
      scrollbar-width: none;
      -ms-overflow-style: none;
      &::-webkit-scrollbar {
        display: none;
      }
      /* https://css-tricks.com/fixed-headers-and-jump-links-the-solution-is-scroll-margin-top/ */
      /* scroll-margin-top: 200px; */
    }

    .virtualizer-item {
      position: relative;
      top: 60px;
    }

    mast-status {
      width: 100%;
    }

    .lastread {
      background-color: #dfa1a1;
      width: 100%;
    }

    .statusloading {
      min-height: 200px;
    }
  `];

  render() {
    return html`
      <div class="header">Mastopoof</div>
      <div class="page">
        <div class="leftpane">
          <div class="leftpanecontent">
          The left pane
          <ul>
            <li>A</li>
            <li>B</li>
          </ul>
          Something.
          </div>
        </div>
        <div class="middlepane">
          <div class="contenthead">
            Main
          </div>
          <div class="content">
            ${this.startIndex === undefined ? html`Loading...` : html`
            <lit-virtualizer
              class="statuses"
              .items=${this.statuses}
              ${ref(this.virtuRef)}
              @rangeChanged=${(e: RangeChangedEvent) => this.rangeChanged(e)}
              @visibilityChanged=${(e: VisibilityChangedEvent) => this.visibilityChanged(e)}
              @unpinned=${() => console.log("unpinned")}
              .layout=${flow({ pin: { index: this.startIndex, block: 'start' } })}
              .renderItem=${(st: StatusEntry, _: number): TemplateResult => this.renderStatus(st)}
            ></lit-virtualizer>
            `}
          </div>
        </div>
        <div class="rightpane">
          <div class="rightpanecontent">
          The right pane
          <ul>
            <li>1</li>
            <li>2</li>
          </ul>
          Something else.
          </div>
        </div>
      </div>
    `;
  }

  renderStatus(st: StatusEntry): TemplateResult {
    if (!st) { return html`<div>empty</div>` }

    const content: TemplateResult[] = [];

    if (st.error) { content.push(html`<div>error: ${st.error}</div>`); }

    if (st.status) {
      content.push(html`<mast-status class="statustrack" .status=${st.status as any}></mast-status>`);
    } else {
      content.push(html`<div class="statusloading">loading</div>`);
    }

    if (st.position == this.lastRead) {
      content.push(html`<div class="lastread">Last read</div>`);
    }

    return html`<div style="width: 100%"><div class="virtualizer-item"> ${content}</div></div>`;
  }

  rangeChanged(e: RangeChangedEvent) {
    if (this.positionOffset === undefined) { return; }  // Not started.
    if (e.first == -1 || e.last == -1) { return; }

    // Make sure that statuses in range are loaded. Include a bit of margin to
    // accelerate stuff.
    for (let i = e.first - 2; i <= e.last + 2; i++) {
      if (i < 0) { continue; }
      if (this.statuses[i] === undefined) {
        this.loadStatusAtIdx(i);
      }
    }
  }

  visibilityChanged(e: VisibilityChangedEvent) {
    console.log("visibility indexes", e.first, e.last);
  }

  async loadStatusAtIdx(idx: number) {
    console.log("load status index", idx);
    if (this.positionOffset === undefined) {
      console.error("shoud not have been called");
      return;
    }
    const position = idx - this.positionOffset;

    const st: StatusEntry = {
      position: position,
    }
    this.statuses[idx] = st;

    if (position < 1) {
      st.error = "negative position";
      return;
    }

    // Issue the request.
    try {
      const resp = await client.getStatus({ position: BigInt(position) });
      st.status = JSON.parse(resp.item!.status!.content) as mastodon.Status;
    } catch (err) {
      st.error = ConnectError.from(err).message;
    }
  }
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
    for (const ma of (s.media_attachments ?? [])) {
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