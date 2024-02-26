import { LitElement, css, html, nothing, TemplateResult, unsafeCSS } from 'lit'
import { customElement, state, property } from 'lit/decorators.js'
import { unsafeHTML } from 'lit/directives/unsafe-html.js';
import { repeat } from 'lit/directives/repeat.js';
import { Ref, createRef, ref } from 'lit/directives/ref.js';

import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";
import { Backend, StreamUpdateEvent, LoginUpdateEvent, LoginState } from "./backend";

import normalizeCSSstr from "./normalize.css?inline";
import baseCSSstr from "./base.css?inline";

import * as mastodon from "./mastodon";

// Create a global backend access.
const backend = new Backend();

const commonCSS = [unsafeCSS(normalizeCSSstr), unsafeCSS(baseCSSstr)];

@customElement('app-root')
export class AppRoot extends LitElement {
  @state() private lastLoginUpdate?: LoginUpdateEvent;

  constructor() {
    super();
    backend.onEvent.addEventListener("login-update", ((evt: LoginUpdateEvent) => {
      if (evt.state === LoginState.LOGGED && this.lastLoginUpdate?.state !== LoginState.LOGGED) {
        console.log("userinfo", evt.userInfo);
        // this.loadNext();
      }
      this.lastLoginUpdate = evt;
    }) as EventListener);

    // Determine if we're already logged in.
    backend.login();
  }

  connectedCallback(): void {
    super.connectedCallback();
    // Prevent browser to automatically scroll to random places on load - it
    // does not work well given that the list of elements might have changed.
    if (history.scrollRestoration) {
      history.scrollRestoration = "manual";
    }
  }

  render() {
    if (!this.lastLoginUpdate || this.lastLoginUpdate.state === LoginState.LOADING) {
      return html`Loading...`;
    }

    if (this.lastLoginUpdate.state === LoginState.NOT_LOGGED) {
      return html`<mast-login></mast-login>`;
    }
    return html`<mast-stream></mast-stream>`;
  }

  static styles = [commonCSS, css``];
}

declare global {
  interface HTMLElementTagNameMap {
    'app-root': AppRoot
  }
}


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

@customElement('mast-stream')
export class MastStream extends LitElement {
  private items: StatusItem[] = [];
  private perEltItem = new Map<Element, StatusItem>();

  private backwardPosition: number = 0;
  private backwardState: pb.FetchResponse_State = pb.FetchResponse_State.UNKNOWN;
  private forwardPosition: number = 0;
  private forwardState: pb.FetchResponse_State = pb.FetchResponse_State.UNKNOWN;

  // Set to false when the first fetch of status (after auth) is done.
  private isInitialFetch = true;

  private observer?: IntersectionObserver;

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

    backend.onEvent.addEventListener("stream-update", ((evt: StreamUpdateEvent) => {
      this.lastRead = evt.curr.lastRead;
      this.lastPosition = evt.curr.lastPosition;
      this.remainingPool = evt.curr.remaining;
    }) as EventListener);

    // Trigger loading of content.
    this.loadNext();
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
    content.push(html`<mast-status class="statustrack contentitem" ${ref((elt?: Element) => this.updateStatusRef(item, elt))} .item=${item as any}></mast-status>`);
    if (item.position == this.lastRead) {
      content.push(html`<div class="lastread contentitem centered">You were here.</div>`);
    }
    return content;
  }

  render() {
    return html`
      <div class="page">
        <div class="middlepane">
          <div class="header">
            <div class="headercontent">
              <div>
                <button style="font-size: 24px"><span class="material-symbols-outlined" title="More...">menu</span></button>
                Mastopoof - Stream
              </div>
              <div>
                <button @click=${() => backend.logout()}>Logout</button>
            </div>
            </div>
          </div>
          <div class="content">
            ${this.isInitialFetch ? html`<div class="contentitem"><div class="centered">Loading...</div></div>` : html``}
            <div class="noanchor contentitem stream-beginning bg-blue-300 centered">${this.backwardState === pb.FetchResponse_State.DONE ? html`
              <div>Beginning of stream.</div>
            `: html`
              <button @click=${this.loadPrevious}>
                <span class="material-symbols-outlined">arrow_upward</span>
                Load earlier statuses
                <span class="material-symbols-outlined">arrow_upward</span>
              </button>
            `}
            </div>

            ${repeat(this.items, item => item.position, (item, _) => this.renderStatus(item))}

            <div class="noanchor contentitem bg-blue-300 stream-end"><div class="centered">${this.forwardState === pb.FetchResponse_State.DONE ? html`
              Nothing more right now. <button @click=${this.loadNext}>Try again</button>
            `: html`
              <button @click=${this.loadNext}>
                <span class="material-symbols-outlined">arrow_downward</span>
                Load more statuses
                <span class="material-symbols-outlined">arrow_downward</span>
              </button>
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
      min-width: 100px;
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
      padding: 0.5rem;

      border-bottom-style: double;
      border-bottom-width: .2rem;

      display: flex;
      align-items: center;
      justify-content: space-between;
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
      background-color: #f7fdff;
      border-top-style: double;
      border-top-width: .2rem;
      padding: 0.5rem;
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
      margin-bottom: 0.1rem;
    }

    .stream-beginning {
      margin-bottom: 0.1rem;
    }

    .stream-end { }

    .lastread {
      background-color: #dfa1a1;
      margin-bottom: 0.1rem;
      font-style: italic;
    }
  `];
}

declare global {
  interface HTMLElementTagNameMap {
    'mast-stream': MastStream
  }
}

function qualifiedAccount(account: mastodon.Account): string {
  if (account.acct !== account.username) {
    return account.acct;
  }

  // This is a short account name - i.e., probably on the same server as the
  // Mastodon user which fetched it.
  // In theory, should probably get the server name from the backend. In practice,
  // let's just look up the rest of the account info.
  if (!account.url.endsWith(`/@${account.username}`)) {
    // Not the expected format, return just something.
    return account.acct;
  }
  const u = new URL(account.url);
  return `${account.username}@${u.hostname}`;
}

@customElement('mast-status')
export class MastStatus extends LitElement {
  @property({ attribute: false })
  item?: StatusItem;

  @state()
  private accessor showRaw = false;

  markUnread() {
    if (!this.item) {
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
        <div class="account bg-blue-100">
          <span class="centered">
            <img class="avatar" src=${s.account.avatar}></img>
            ${s.account.display_name} &lt;${qualifiedAccount(s.account)}&gt;
          </span>
          <a href=${s.url!} target="_blank"><span class="material-symbols-outlined" title="Open status on original server">open_in_new</span></a>
        </div>
        ${isReblog ? html`
          <div class="reblog bg-blue-50">
            <img class="avatar" src=${account.avatar}></img>
            Reblog by ${account.display_name} &lt;${qualifiedAccount(account)}&gt;
          </div>
        `: nothing}
        <div class="content">
          ${unsafeHTML(s.content)}
        </div>
        <div class="attachments">
          ${attachments}
        </div>
        <div class="tools bg-blue-400 text-light">
          <div>
            <button><span class="material-symbols-outlined" title="Favorite">star</span></button>
            <button><span class="material-symbols-outlined" title="Boost">repeat</span></button>
            <button><span class="material-symbols-outlined" title="Reply...">reply</span></button>
          </div>
          <div>
            <button @click="${() => this.markUnread()}" title="Mark as unread and move read-marker above">
              <span class="material-symbols-outlined">mark_as_unread</span>
            </button>
            <button @click="${() => { this.showRaw = !this.showRaw }}" title="Show raw status">
              <span class="material-symbols-outlined">${this.showRaw ? 'collapse_all' : 'expand_all'}</span>
            </button>
          </div>
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
      justify-content: space-between;
    }

    .reblog {
      display: flex;
      align-items: center;
      padding: 0.2rem;
      font-size: 0.8rem;
      font-style: italic;
    }

    .avatar {
      width: auto;
      padding-right: 0.2rem;
    }

    .account .avatar {
      width: 32px;
      max-height: 32px;
      min-height: 32px;
    }

    .reblog .avatar {
      max-height: 20px;
      min-height: 20px;
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
      justify-content: space-between;
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

  @state() private authURI: string = "";
  // Server address as used to get the authURI.
  @state() private serverAddr: string = "";

  private serverAddrRef: Ref<HTMLInputElement> = createRef();
  private authCodeRef: Ref<HTMLInputElement> = createRef();

  async startLogin() {
    const serverAddr = this.serverAddrRef.value?.value;
    if (!serverAddr) {
      return;
    }
    this.authURI = await backend.authorize(serverAddr);
    this.serverAddr = serverAddr;
    console.log("authURI", this.authURI);
  }

  async doLogin() {
    const authCode = this.authCodeRef.value?.value;
    if (!authCode) {
      // TODO: surface error
      console.error("invalid authorization code");
      return;
    }
    const userInfo = await backend.token(this.serverAddr, authCode);

    console.log("stream ID", userInfo);
  }

  openMastodonAuth() {
    window.open(this.authURI, "Auth");
    /*if (window.focus) {
      newWindow.focus();
    } */
  }

  render() {
    if (!this.authURI) {
      return html`
        <div>
          <label for="server-addr">Mastodon server address (must start with https)</label>
          <input type="url" id="server-addr" ${ref(this.serverAddrRef)} value="https://mastodon.social" required autofocus></input>
          <button @click=${this.startLogin}>Auth</button>
        </div>
      `;
    }

    return html`
      <div>
        <button @click="${this.openMastodonAuth}">Open Mastodon Auth</button>
      </div>
      <div>
        <label for="auth-code">Authorization code</label>
        <input type="text" id="auth-code" ${ref(this.authCodeRef)} required autofocus></input>
        <button @click=${this.doLogin}>Auth</button>
      </div>
    `;
  }

  static styles = [commonCSS, css``];
}

declare global {
  interface HTMLElementTagNameMap {
    'mast-login': MastLogin
  }
}
