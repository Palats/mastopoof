import { LitElement, css, html, nothing, TemplateResult, unsafeCSS } from 'lit'
import { customElement, state, property } from 'lit/decorators.js'
import { unsafeHTML } from 'lit/directives/unsafe-html.js';
import { repeat } from 'lit/directives/repeat.js';
import { ref } from 'lit/directives/ref.js';

import { createPromiseClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { Mastopoof } from "mastopoof-proto/gen/mastopoof/mastopoof_connect";
import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";

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

const commonCSS = [unsafeCSS(normalizeCSSstr), unsafeCSS(baseCSSstr)];

// StatusEntry represents the state in the UI of a given status.
interface StatusEntry {
  // Position in the stream in the backend.
  position: number;
  // status, if loaded.
  status?: mastodon.Status;
  // error, if loading did not work.
  error?: string;

  // HTML element used to represent this status.
  elt?: Element;
  // Was the status visible fully on the screen at some point?
  wasSeen: boolean;
  // Did the element moved from fully visible to completely invisible?
  disappeared: boolean;
}

// https://adrianfaciu.dev/posts/observables-litelement/
// https://github.com/lit/lit/tree/main/packages/labs/virtualizer#readme
// https://stackoverflow.com/questions/60678734/insert-elements-on-top-of-something-without-changing-visible-scrolled-content

@customElement('app-root')
export class AppRoot extends LitElement {
  private items: StatusEntry[] = [];
  private perEltItem = new Map<Element, StatusEntry>();

  // Position of the last status marked as read.
  // From server info.
  private serverLastRead?: number;
  // From frontend perspective.
  @state() private lastRead: number = 0;

  private backwardPosition: number = 0;
  private backwardState: pb.FetchResponse_State = pb.FetchResponse_State.UNKNOWN;
  private forwardPosition: number = 0;
  private forwardState: pb.FetchResponse_State = pb.FetchResponse_State.UNKNOWN;

  private isInitialLoading = true;

  private observer?: IntersectionObserver;

  connectedCallback(): void {
    super.connectedCallback();
    this.observer = new IntersectionObserver(
      (entries: IntersectionObserverEntry[], _: IntersectionObserver) => {
        // console.log("intersection", entries);
        for (const entry of entries) {
          const targetItem = this.perEltItem.get(entry.target);
          if (targetItem) {
            // console.info("intersection", entry.isIntersecting, "position", targetItem.position);
            if (entry.isIntersecting) {
              targetItem.wasSeen = true;
              targetItem.disappeared = false;
            } else if (targetItem.wasSeen) {
              targetItem.disappeared = true;
            }
          } else {
            console.error("not item found for element", entry);
          }
        }

        // Found until which item things have disappeared.
        let position = 0;
        for (const item of this.items) {
          if (!item.disappeared) {
            break;
          }
          position = item.position;
        }
        console.log("isread until", position);
        if (position > this.lastRead) {
          this.lastRead = position;
        }
      }, {
      root: null,
      rootMargin: "0px",
      threshold: 0.0,
    });

    this.loadNext();
  }

  disconnectedCallback() {
    super.disconnectedCallback();
  }

  async loadPrevious() {
    const resp = await client.fetch({ position: BigInt(this.backwardPosition), direction: pb.FetchRequest_Direction.BACKWARD })

    this.serverLastRead = Number(resp.lastRead);

    if (resp.backwardPosition > 0) {
      this.backwardPosition = Number(resp.backwardPosition);
      this.backwardState = resp.backwardState;
    }
    if (resp.forwardPosition > 0 && this.forwardState === pb.FetchResponse_State.UNKNOWN) {
      this.forwardPosition = Number(resp.forwardPosition);
      this.forwardState = resp.forwardState;
    }

    const newItems = [];
    for (let i = 0; i < resp.items.length; i++) {
      const item = resp.items[i];
      const position = Number(item.position);
      const status = JSON.parse(item.status!.content) as mastodon.Status;
      newItems.push({
        status: status,
        position: position,
        wasSeen: false,
        disappeared: false,
      });
    }
    this.items = [...newItems, ...this.items];
    this.requestUpdate();
  }

  async loadNext() {
    const resp = await client.fetch({ position: BigInt(this.forwardPosition), direction: pb.FetchRequest_Direction.FORWARD })
    console.log(resp);

    this.serverLastRead = Number(resp.lastRead);
    if (resp.backwardPosition > 0 && this.backwardState === pb.FetchResponse_State.UNKNOWN) {
      this.backwardPosition = Number(resp.backwardPosition);
      this.backwardState = resp.backwardState;
    }
    if (resp.forwardPosition > 0) {
      this.forwardPosition = Number(resp.forwardPosition);
      this.forwardState = resp.forwardState;
    }

    for (let i = 0; i < resp.items.length; i++) {
      const item = resp.items[i];
      const position = Number(item.position);
      const status = JSON.parse(item.status!.content) as mastodon.Status;
      this.items.push({
        status: status,
        position: position,
        wasSeen: false,
        disappeared: false,
      });
    }
    // Always indicate that initial loading is done - this is a latch anyway.
    this.isInitialLoading = false;
    this.requestUpdate();
  }

  static styles = [commonCSS, css`
    :host {
      display: flex;
      flex-direction: column;
      height: 100%;

      box-sizing: border-box;
    }

    .page {
      display: flex;
      flex-direction: row;
      justify-content: center;
      top: 40px;

      background-color: #e0e0e0;
    }

    .middlepane {
      min-width: 600px;
      width: 600px;
    }

    .header {
      position: sticky;
      top: 0;
      z-index: 2;
      box-sizing: border-box;
      min-height: 60px;
      background-color: #e0e0e0;

      display: grid;
      grid-template-rows: 1fr;
    }

    /* Header content is separated from header styling. This way, the header
    element can cover everything behind (to pretend it is not there) and let
    options for styling, beyond a basic all encompassing box.
    */
    .headercontent {
      background-color: #f7fdff;
      border-style: solid;
      border-radius: .3rem;
      border-width: .1rem;
      padding: 0.5rem;
      margin: .1rem;
    }

    .content {
      display: flex;
      flex-direction: column;
      justify-content: center;
      align-items: center;
    }

    .contentitem {
      width: 100%;
    }

    mast-status {
      width: 100%;
    }

    .streambeginning {
      background-color: #a1bcdf;
    }

    .streamend {
      background-color: #a1bcdf;
    }

    .lastread {
      background-color: #dfa1a1;
    }

    .statusloading {
      min-height: 200px;
    }

    .noanchor {
      overflow-anchor: none;
    }

    .centered {
      display: flex;
      flex-direction: row;
      justify-content: center;
      align-items: center;
    }
  `];

  render() {
    return html`
      <div class="page">
        <div class="middlepane">
          <div class="header">
            <div class="headercontent">
              Mastopoof
            </div>
          </div>
          <div class="content">
            ${this.isInitialLoading ? html`<div class="contentitem"><div class="centered">Loading...</div></div>` : html``}
            <div class="noanchor contentitem streambeginning">${this.backwardState === pb.FetchResponse_State.DONE ? html`
              <div class="centered">Beginning of stream.</div>
            `: html`
              <button @click=${this.loadPrevious}>Load earlier statuses</button></div>
            `}
            </div>

            ${repeat(this.items, (item) => item.position, (item, _) => { return this.renderStatus(item); })}

            <div class="noanchor contentitem streamend"><div class="centered">${this.forwardState === pb.FetchResponse_State.DONE ? html`
              Nothing more right now. <button @click=${this.loadNext}>Try again</button>
            `: html`
              <button @click=${this.loadNext}>Load more statuses</button></div>
            `}
            </div></div>
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

    const updateRef = (elt?: Element) => {
      if (!this.observer) {
        return;
      }
      if (st.elt === elt) {
        return;
      }

      if (st.elt) {
        this.observer.unobserve(st.elt);
        this.perEltItem.delete(st.elt);
      }
      if (elt) {
        this.observer.observe(elt);
        this.perEltItem.set(elt, st);
      }
      st.elt = elt;
    };

    return html`<div class="contentitem" ${ref(updateRef)}>${content}</div>`;
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