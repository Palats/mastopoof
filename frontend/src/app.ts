import { LitElement, css, html, nothing, TemplateResult } from 'lit'
import { customElement, state, property } from 'lit/decorators.js'
import { repeat } from 'lit/directives/repeat.js';
import { ref } from 'lit/directives/ref.js';
import { classMap } from 'lit/directives/class-map.js';

import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";
import { StreamUpdateEvent, LoginUpdateEvent, LoginState } from "./backend";
import * as common from "./common";
import * as mastodon from "./mastodon";

import "./auth";
import "./time";
import "./status";


@customElement('app-root')
export class AppRoot extends LitElement {
  @state() private lastLoginUpdate?: LoginUpdateEvent;

  constructor() {
    super();
    common.backend.onEvent.addEventListener("login-update", ((evt: LoginUpdateEvent) => {
      if (evt.state === LoginState.LOGGED && this.lastLoginUpdate?.state !== LoginState.LOGGED) {
        console.log("Logged in");
      }
      this.lastLoginUpdate = evt;
    }) as EventListener);

    // Determine if we're already logged in.
    common.backend.login();
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
    const stid = this.lastLoginUpdate.userInfo?.defaultStid;
    if (!stid) {
      throw new Error("missing stid");
    }
    return html`<mast-stream .stid=${stid}></mast-stream>`;
  }

  static styles = [common.sharedCSS, css``];
}

declare global {
  interface HTMLElementTagNameMap {
    'app-root': AppRoot
  }
}

// Component rendering the main view of Mastopoof, as
// a list of elements such as the stream or search result.
// It contains multiple slots:
//   - `menu`, to add extra element in the UI menu.
//   - `list`, for the main content in the center column (e.g., the stream).
//   - `footer`, for the bottom part content.
@customElement('mast-main-view')
export class MastMainView extends LitElement {
  // Count the number of reasons why the loading bar should be shown.
  // This allows to have multiple things loading, while avoiding having
  // the first one finishing removing the loading bar.
  @property({ attribute: false }) loadingBarUsers = 0;

  @state() private showMenu = false;

  render() {
    return html`
      <div class="header">
        <div class="headercontent">
          <div>
            <button style="font-size: 24px" @click=${() => { this.showMenu = !this.showMenu }}>
              ${this.showMenu ? html`
              <span class="material-symbols-outlined" title="Close menu">close</span>
              `: html`
              <span class="material-symbols-outlined" title="Open menu">menu</span>
              `}
            </button>
            Mastopoof - Stream
          </div>
        </div>
        ${this.showMenu ? html`
          <div class="menucontent">
            <div>plop</div>
            <slot name="menu"></slot>
            <div>
              <button @click=${() => common.backend.logout()}>Logout</button>
            </div>
          </div>` : nothing}
      </div>

      <div class="middlepane">
        <slot name="list"></slot>
      </div>

      <div class="footer">
        <div class="footercontent">
          <div class=${classMap({ loadingbar: true, hidden: this.loadingBarUsers <= 0 })}></div>
          <slot name="footer"></slot>
        </div>
      </div>
    `;
  }

  static styles = [common.sharedCSS, css`
    :host {
      display: flex;
      flex-direction: column;
      align-items: center;
      box-sizing: border-box;
      min-height: 100%;

      background-color: var(--color-grey-300);
    }

    .middlepane {
      z-index: 0;
      flex-grow: 1;
      min-width: var(--stream-min-width);
      width: 100%;
      max-width: var(--stream-max-width);

      background-color: var(--color-grey-150);

      display: flex;
      flex-direction: column;
    }

    .header {
      z-index: 1;
      position: sticky;
      top: 0;

      box-sizing: border-box;
      min-width: var(--stream-min-width);
      width: 100%;
      max-width: var(--stream-max-width);
    }

    .headercontent {
      background-color: var(--color-blue-25);
      padding: 8px;

      box-sizing: border-box;
      border-bottom-style: double;
      border-bottom-width: 3px;

      display: flex;
      align-items: center;
      justify-content: space-between;
    }

    .menucontent {
      grid-column: 2;
      padding: 8px;
      background-color: var(--color-blue-25);
      box-shadow: rgb(0 0 0 / 80%) 0px 16px 12px;
    }

    .footer {
      z-index: 1;
      position: sticky;
      bottom: 0;

      box-sizing: border-box;
      min-width: var(--stream-min-width);
      width: 100%;
      max-width: var(--stream-max-width);
    }

    .footercontent {
      background-color: var(--color-blue-25);
      padding: 5px;
      box-sizing: border-box;
      border-top-style: double;
      border-top-width: 3px;
    }

    .loadingbar {
      width: 10%;
      height: 3px;
      position: relative;
      /* Alignement is related to footercontent padding, and
         footer border-top-width*/
      top: -8px;
      background-color: var(--color-grey-999);
      animation: loadinganim 2s infinite linear;
    }
    @keyframes loadinganim {
      0% { transform:  translateX(0); }
      50% { transform:  translateX(900%); }
      100% { transform:  translateX(0%); }
    }

    ::slotted([slot=list]) {
      display: flex;
      flex-direction: column;
      justify-content: center;
      align-items: stretch;
    }
  `];
}

declare global {
  interface HTMLElementTagNameMap {
    'mast-main-view': MastMainView
  }
}


// StatusItem represents the state in the UI of a given status.
interface StatusItem {
  // Position in the stream in the backend.
  position: bigint;
  // status, if loaded.
  status: mastodon.Status;
  // The account where this status was obtained from.
  account: pb.Account;

  // HTML element used to represent this status.
  elt?: Element;
  // Is the status currently partially visible?
  isVisible: boolean;
  // Was the status visible (partially or fully) on the screen at some point?
  wasSeen: boolean;
  // Did the element moved from fully visible to completely invisible?
  disappeared: boolean;
}

// Page displaying the main mastodon stream.
@customElement('mast-stream')
export class MastStream extends LitElement {
  // Which stream to display.
  // TODO: support changing it.
  @property({ attribute: false }) stid?: bigint;

  private items: StatusItem[] = [];
  private perEltItem = new Map<Element, StatusItem>();

  // Set to true when the first list of status (after auth) is done.
  private firstListDone = false;

  private observer?: IntersectionObserver;

  // Status with the highest position value which is partially visible on the
  // screen.
  @state() private lastVisiblePosition?: bigint;
  @state() private streamInfo?: pb.StreamInfo;
  @state() loadingBarUsers = 0;

  connectedCallback(): void {
    super.connectedCallback();
    this.observer = new IntersectionObserver(
      (entries: IntersectionObserverEntry[], _: IntersectionObserver) => this.onIntersection(entries), {
      root: null,
      rootMargin: "0px",
      threshold: 0.0,
    });

    common.backend.onEvent.addEventListener("stream-update", ((evt: StreamUpdateEvent) => {
      if (evt.curr) {
        this.streamInfo = evt.curr;
      }
    }) as EventListener);

    // Trigger loading of content.
    this.listNext();
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
        targetItem.isVisible = entry.isIntersecting;
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

    // TODO: do not rescan everything on each events, as high item count will
    // make things slow.

    // Find the boundaries of which statuses are visible.
    let lastVisiblePosition: bigint | undefined;
    let firstVisiblePosition: bigint | undefined;
    for (const item of this.items) {
      if (item.isVisible) {
        if (firstVisiblePosition === undefined) {
          firstVisiblePosition = item.position;
        }
        lastVisiblePosition = item.position;
      }
    }
    this.lastVisiblePosition = lastVisiblePosition;

    // Scan items to see which one have disappeared - i.e., are above the current
    // view and can be marked as seen.
    let disappearedPosition = 0n;
    for (const item of this.items) {
      if (!item.disappeared) {
        break;
      }
      disappearedPosition = item.position;
    }
    if (this.streamInfo !== undefined && disappearedPosition > this.streamInfo.lastRead) {
      if (!this.stid) {
        throw new Error("missing stid");
      }
      common.backend.advanceLastRead(this.stid, disappearedPosition);
    }
  }

  // Load earlier statuses.
  async loadPrevious() {
    const stid = this.stid;
    if (!stid) {
      throw new Error("missing stream id");
    }

    if (this.items.length === 0) {
      throw new Error("loading previous status without successful forward loading");
    }
    const position = this.items[0].position;
    const resp = await common.backend.list({ stid: stid, position: position, direction: pb.ListRequest_Direction.BACKWARD })

    const newItems = [];
    for (let i = 0; i < resp.items.length; i++) {
      const item = resp.items[i];
      const position = item.position;
      const status = JSON.parse(item.status!.content) as mastodon.Status;
      newItems.push({
        status: status,
        position: position,
        account: item.account!,
        isVisible: false,
        wasSeen: false,
        disappeared: false,
      });
    }
    this.items = [...newItems, ...this.items];
    this.requestUpdate();
  }

  // List newer statuses.
  // This does NOT trigger a mastodon->mastopoof fetch, it just
  // list what's available from mastopoof.
  async listNext() {
    const stid = this.stid;
    if (!stid) {
      throw new Error("missing stream id");
    }

    let position = 0n;
    if (this.items.length > 0) {
      position = this.items[this.items.length - 1].position;
    }

    let resp: pb.ListResponse;
    try {
      this.loadingBarUsers++;
      resp = await common.backend.list({ stid: stid, position: position, direction: pb.ListRequest_Direction.FORWARD })
    } finally {
      this.loadingBarUsers--;
    }

    for (let i = 0; i < resp.items.length; i++) {
      const item = resp.items[i];
      const position = item.position;
      const status = JSON.parse(item.status!.content) as mastodon.Status;
      this.items.push({
        status: status,
        position: position,
        account: item.account!,
        isVisible: false,
        wasSeen: false,
        disappeared: false,
      });
    }
    // Always indicate that initial loading is done - this is a latch anyway.
    this.firstListDone = true;
    this.requestUpdate();
  }

  // Just trigger a fetch of status mastodon->mastopoof.
  async fetch() {
    const stid = this.stid;
    if (!stid) {
      throw new Error("missing stream id");
    }
    console.log("Fetching...");
    try {
      this.loadingBarUsers++;
      // Limit the number of fetch we're requesting.
      // TODO: do limiting on server side.
      for (let i = 0; i < 10; i++) {
        const done = await common.backend.fetch(stid);
        if (done) { break; }
      }
    } finally {
      this.loadingBarUsers--;
    }
  }

  async getMoreStatuses() {
    if (!this.streamInfo) {
      throw new Error("missing streaminfo");
    }
    // Still has some statuses to list, so just get those.
    if (this.streamInfo.remainingPool > 0n) {
      await this.listNext();
      return;
    }

    // No more statuses to list, so some fetching is needed.
    const stid = this.stid;
    if (!stid) {
      throw new Error("missing stream id");
    }

    // Trigger fetching.
    // TODO: do a first one, the trigger the other one in background.
    // However that first requires having the backend retry transaction, as it conflicts
    // otherwise.
    await this.fetch();

    // And get those we already got listed.
    await this.listNext();
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

  render() {
    if (!this.firstListDone || !this.streamInfo) {
      // TODO: better presentation on loading
      return html`Loading...`;
    }

    let availableCount = 0n;
    let loadedCount = 0n;
    if (this.items.length === 0) {
      // Initial loading was done, so if items is empty, it means nothing is available.
      availableCount = this.streamInfo.remainingPool;
    } else {
      const lastPosition = this.items[this.items.length - 1].position;
      const lastVisible = this.lastVisiblePosition ?? 0n;
      // We've got:
      //   - visible statuses which are already on stream but not yet on screen/loaded.
      //   - statuses still in pool and not yet sorted in stream.
      loadedCount = lastPosition - lastVisible;
      availableCount = this.streamInfo.remainingPool + loadedCount;
    }

    return html`
      <mast-main-view .loadingBarUsers=${this.loadingBarUsers}>
        <div slot="menu">
          <div>
            <button @click=${this.fetch}>Fetch now</button>
          </div>
        </div>
        <div slot="list">
          ${this.renderStreamContent()}
        </div>
        <div slot="footer" class="footer">
          <div class="remaining">
            <span title="${availableCount} remaining statuses, incl. ${loadedCount} already loaded">
              <span class="material-symbols-outlined">arrow_downward</span>
              ${loadedCount}/${availableCount}
              <span class="material-symbols-outlined">arrow_downward</span>
            </span>
          </div>
          <div class="fetchtime">Last check: <time-since .unix=${this.streamInfo?.lastFetchSecs}></time-since></div>
        </div>
      </mast-main-view>
    `;
  }

  renderStreamContent(): TemplateResult {
    if (!this.streamInfo) {
      throw new Error("should not have been called");
    }

    // This function is called only if the initial loading is done - so if there is no items, it means that
    // the stream was empty at that time, and thus we're at its beginning.
    const isBeginning = this.items.length == 0 || (this.items[0].position === this.streamInfo.firstPosition)

    const buttonName = (this.streamInfo.remainingPool === 0n) ? "Look for statuses" : "Load more statuses";

    return html`
      <div class="noanchor stream-beginning centered">
      ${isBeginning ? html`
        ${this.items.length === 0 ?
          html`<div>No statuses.</div>` :
          html`<div>Beginning of stream.</div>`
        }
      `: html`
        <button @click=${this.loadPrevious}>
          <span>Load earlier statuses</span>
        </button>
      `}
      </div>

      ${repeat(this.items, item => item.position, (item, _) => this.renderStatus(item))}

      <div class="noanchor stream-end">
        <div class="centered">
          <div>
            <button class="loadmore" @click=${this.getMoreStatuses} ?disabled=${this.loadingBarUsers > 0}>
              ${buttonName}
            </button>
          </div>
        </div>
      </div>
    `;
  }

  renderStatus(item: StatusItem): TemplateResult[] {
    const lastRead = this.streamInfo?.lastRead ?? 0;
    const pos = item.position;
    const content: TemplateResult[] = [];
    content.push(html`<mast-status ?isRead=${pos <= lastRead} ${ref((elt?: Element) => this.updateStatusRef(item, elt))} .stid=${this.stid} .item=${item as any}></mast-status>`);
    if (item.position == lastRead) {
      content.push(html`<div class="lastread centered">The bookmark</div>`);
    }
    return content;
  }

  static styles = [common.sharedCSS, css`
    .footer {
      display: grid;
      grid-template-columns: 0.6fr 1fr 0.6fr;
      align-items: center;
    }

    .remaining {
      grid-column: 2;
      justify-self: center;
    }

    .fetchtime {
      grid-column: 3;
      font-size: 0.6rem;
      justify-self: right;
    }

    mast-status {
      width: 100%;
      margin-bottom: 1px;
    }

    .stream-beginning {
      margin-bottom: 1px;
      background-color: var(--color-blue-300);
    }

    .stream-end {
      background-color: var(--color-blue-300);
    }

    .lastread {
      background-color: var(--color-red-200);
      margin-bottom: 1px;
      font-style: italic;
    }

    .loadmore {
      padding-top: 4px;
      padding-bottom: 4px;
    }
  `];
}

declare global {
  interface HTMLElementTagNameMap {
    'mast-stream': MastStream
  }
}
