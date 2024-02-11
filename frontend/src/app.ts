import { LitElement, css, html, nothing, TemplateResult, unsafeCSS } from 'lit'
import { customElement, state, property } from 'lit/decorators.js'
import { unsafeHTML } from 'lit/directives/unsafe-html.js';
import { repeat } from 'lit/directives/repeat.js';
import { ref } from 'lit/directives/ref.js';

import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";
import { Backend, StreamUpdateEvent, LoginUpdateEvent, LoginState } from "./backend";

import normalizeCSSstr from "./normalize.css?inline";
import baseCSSstr from "./base.css?inline";

import * as mastodon from "./mastodon";

// Create a global backend access.
const backend = new Backend();

const commonCSS = [unsafeCSS(normalizeCSSstr), unsafeCSS(baseCSSstr)];

// StatusItem represents the state in the UI of a given status.
interface StatusItem {
  // Position in the stream in the backend.
  position: number;
  // status, if loaded.
  status: mastodon.Status;

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
  private items: StatusItem[] = [];
  private perEltItem = new Map<Element, StatusItem>();

  private backwardPosition: number = 0;
  private backwardState: pb.FetchResponse_State = pb.FetchResponse_State.UNKNOWN;
  private forwardPosition: number = 0;
  private forwardState: pb.FetchResponse_State = pb.FetchResponse_State.UNKNOWN;

  // Set to false when the first fetch of status (after auth) is done.
  private isInitialFetch = true;

  private observer?: IntersectionObserver;

  @state() private lastLoginUpdate?: LoginUpdateEvent;
  @state() private lastRead?: number;
  @state() private lastPosition?: number;
  @state() private remainingPool?: number;

  connectedCallback(): void {
    super.connectedCallback();
    this.observer = new IntersectionObserver(
      (entries: IntersectionObserverEntry[], _: IntersectionObserver) => this.onIntersection(entries), {
      root: null,
      rootMargin: "0px",
      threshold: 0.0,
    });

    // Prevent browser to automatically scroll to random places on load - it
    // does not work well given that the list of elements might have changed.
    if (history.scrollRestoration) {
      history.scrollRestoration = "manual";
    }

    backend.onEvent.addEventListener("login-update", ((evt: LoginUpdateEvent) => {
      if (evt.state === LoginState.LOGGED && this.lastLoginUpdate?.state !== LoginState.LOGGED) {
        this.loadNext();
      }
      this.lastLoginUpdate = evt;
    }) as EventListener);
    backend.onEvent.addEventListener("stream-update", ((evt: StreamUpdateEvent) => {
      this.lastRead = evt.curr.lastRead;
      this.lastPosition = evt.curr.lastPosition;
      this.remainingPool = evt.curr.remaining;
    }) as EventListener);

    backend.login({});
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this.observer?.disconnect();
  }


  // Called when intersections of statuses changes - i.e., that
  // a status becomes visible / invisible.
  onIntersection(entries: IntersectionObserverEntry[]) {
    for (const entry of entries) {
      const targetItem = this.perEltItem.get(entry.target);
      if (targetItem) {
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
    if (this.lastRead !== undefined && position > this.lastRead) {
      backend.setLastRead(position);
    }
  }

  // Load earlier statuses.
  async loadPrevious() {
    const resp = await backend.fetch({ position: BigInt(this.backwardPosition), direction: pb.FetchRequest_Direction.BACKWARD })

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

  // Load newer statuses.
  async loadNext() {
    const resp = await backend.fetch({ position: BigInt(this.forwardPosition), direction: pb.FetchRequest_Direction.FORWARD })

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
    this.isInitialFetch = false;
    this.requestUpdate();
  }

  updateStatusRef(item: StatusItem, elt?: Element) {
    if (!this.observer) {
      return;
    }
    if (item.elt === elt) {
      return;
    }

    if (item.elt) {
      this.observer.unobserve(item.elt);
      this.perEltItem.delete(item.elt);
    }
    if (elt) {
      this.observer.observe(elt);
      this.perEltItem.set(elt, item);
    }
    item.elt = elt;
  }

  renderStatus(item: StatusItem): TemplateResult[] {
    const content: TemplateResult[] = [];
    content.push(html`<mast-status class="statustrack contentitem" ${ref((elt?: Element) => this.updateStatusRef(item, elt))} .app=${this as any} .item=${item as any}></mast-status>`);
    if (item.position == this.lastRead) {
      content.push(html`<div class="lastread contentitem">Last read</div>`);
    }
    return content;
  }

  render() {
    if (!this.lastLoginUpdate || this.lastLoginUpdate.state === LoginState.LOADING) {
      return html`Loading...`;
    }

    if (this.lastLoginUpdate.state === LoginState.NOT_LOGGED) {
      return html`<button @click=${() => backend.login({ tmpStid: BigInt(1) })}>login</button>`;
    }
    return html`
      <div class="page">
        <div class="middlepane">
          <div class="header">
            <div class="headercontent">
              Mastopoof
            </div>
          </div>
          <div class="content">
            ${this.isInitialFetch ? html`<div class="contentitem"><div class="centered">Loading...</div></div>` : html``}
            <div class="noanchor contentitem streambeginning">${this.backwardState === pb.FetchResponse_State.DONE ? html`
              <div class="centered">Beginning of stream.</div>
            `: html`
              <button @click=${this.loadPrevious}>Load earlier statuses</button></div>
            `}
            </div>

            ${repeat(this.items, item => item.position, (item, _) => this.renderStatus(item))}

            <div class="noanchor contentitem streamend"><div class="centered">${this.forwardState === pb.FetchResponse_State.DONE ? html`
              Nothing more right now. <button @click=${this.loadNext}>Try again</button>
            `: html`
              <button @click=${this.loadNext}>Load more statuses</button></div>
            `}
            </div></div>
          </div>
          <div class="footer">
            <div class="footercontent">
              Not yet in stream: ${this.remainingPool} (lastpos=${this.lastPosition})
            </div>
          </div>
        </div>
      </div>
    `;
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

    .footer {
      position: sticky;
      bottom: 0;
      z-index: 2;
      box-sizing: border-box;
      min-height: 30px;
      background-color: #e0e0e0;

      display: grid;
      grid-template-rows: 1fr;
    }
    .footercontent {
      background-color: #c99272;
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
}

declare global {
  interface HTMLElementTagNameMap {
    'app-root': AppRoot
  }
}

@customElement('mast-status')
export class MastStatus extends LitElement {
  @property({ attribute: false })
  item?: StatusItem;

  @property({ attribute: false })
  app?: AppRoot;

  @state()
  private accessor showRaw = false;

  markUnread() {
    if (!this.app || !this.item) {
      console.error("missing connection");
      return;
    }
    // Not sure if doing computation on "position" is fine, but... well.
    backend.setLastRead(this.item?.position - 1);
  }

  render() {
    if (!this.item) {
      return html`<div class="status">oops.</div>`
    }

    // This actual status - i.e., the reblogged one when it is a reblogged, or
    // the basic one.
    const s = this.item.status.reblog ?? this.item.status;
    const isReblog = !!this.item.status.reblog;
    const account = this.item.status.account;

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
            <img class="avatar" src=${account.avatar} alt="avatar of ${account.display_name}"></img>
            reblog by ${account.display_name}
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
          <button @click="${() => this.markUnread()}" title="Mark as unread and move read-marker above">
            <span class="material-symbols-outlined">mark_as_unread</span>
          </button>
        </div>
        ${this.showRaw ? html`<pre class="rawcontent">${JSON.stringify(this.item.status, null, "  ")}</pre>` : nothing}
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

// Login screen
@customElement('mast-login')
export class MastLogin extends LitElement {
  render() {
    return html`login`;
  }

  static styles = [commonCSS, css``];
}

declare global {
  interface HTMLElementTagNameMap {
    'mast-login': MastLogin
  }
}
