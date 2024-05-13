import { LitElement, css, html, TemplateResult } from 'lit'
import { customElement, state, property } from 'lit/decorators.js'
import { repeat } from 'lit/directives/repeat.js';
import { ref } from 'lit/directives/ref.js';

import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";
import { StreamUpdateEvent } from "./backend";
import * as common from "./common";
import * as mastodon from "./mastodon";

import "./time";
import "./status";
import "./mainview";

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
  // Return `true` if everything was fetched from Mastodon.
  async fetch(singleFetch = false): Promise<boolean> {
    const stid = this.stid;
    if (!stid) {
      throw new Error("missing stream id");
    }
    const maxCount = singleFetch ? 1 : 10;
    console.log("Fetching...");
    try {
      this.loadingBarUsers++;
      // Limit the number of fetch we're requesting.
      // TODO: do limiting on server side.
      for (let i = 0; i < maxCount; i++) {
        const done = await common.backend.fetch(stid);
        if (done) { return true; }
      }
    } finally {
      this.loadingBarUsers--;
    }
    return false;
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
    // Do a first one, and then let the other run in background.
    const isDone = await this.fetch(true);
    if (!isDone) {
      this.fetch();
    }

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

      // loadedCount are statuses available in the browser, but not yet displayed.
      loadedCount = lastPosition - lastVisible;
      // availableCount are statuses below the last one visible.
      // So that's whatever is in the untriaged pool, plus what is is triaged in the
      // stream (streamInfo.lastPosition - does not have to be loaded), but ignoring
      // what is already visible.
      availableCount = this.streamInfo.remainingPool + this.streamInfo.lastPosition - lastVisible;
    }

    return html`
      <mast-main-view .loadingBarUsers=${this.loadingBarUsers}>
        <span slot="header">Stream</span>
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
