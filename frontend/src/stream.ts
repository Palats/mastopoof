import { LitElement, css, html, TemplateResult } from 'lit'
import { customElement, state, property } from 'lit/decorators.js'
import { repeat } from 'lit/directives/repeat.js';
import { classMap } from 'lit/directives/class-map.js';
import { ref } from 'lit/directives/ref.js';

import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";
import { StreamUpdateEvent, fuzzy } from "./backend";
import * as common from "./common";
import * as mastodon from "./mastodon";
import * as status from "./status";

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
  statusMeta: pb.StatusMeta;
  streamStatusState: pb.StreamStatusState;

  // HTML element used to represent this status.
  elt?: Element;
  // Is the status currently partially visible?
  isVisible: boolean;
  // Was the status visible (partially or fully) on the screen at some point?
  wasSeen: boolean;
  // Did the element moved from fully visible to completely invisible?
  disappeared: boolean;
}

// Ensure that StatusItem can act as status.StatusItem.
function assertSubtype(): status.StatusItem { return {} as StatusItem; }
assertSubtype();

// Page displaying the main mastodon stream.
@customElement('mast-stream')
export class MastStream extends LitElement {
  // User currently logged in.
  @property({ attribute: false }) userInfo?: pb.UserInfo;

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
  @state() private loadingBarUsers = 0;
  @state() private isFetching = false;

  // Continuation position for future list requests.
  private backwardPosition?: bigint;
  private forwardPosition?: bigint;

  // Background fetch management
  private triggerFetchResolve: (() => void) | undefined;
  private triggerFetchWaiters: (() => void)[] = [];
  private clearBackgroundFetch: (() => void) | undefined;

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

    // Start background fetching.
    this.backgroundFetch();
    // ... and request an initial fetch.
    this.triggerFetch();

    // Trigger loading of content.
    this.listNext(true /* isInitial */);
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this.observer?.disconnect();
    if (this.clearBackgroundFetch) {
      this.clearBackgroundFetch();
    }
  }

  async backgroundFetch() {
    const exitRequest = new Promise<boolean>(resolve => {
      this.clearBackgroundFetch = () => resolve(true);
    });

    const prepTrigger = () => {
      return new Promise<boolean>(resolve => {
        this.triggerFetchResolve = () => resolve(false);
      });
    }

    // Create the trigger resolve before any await - this way a caller of
    // backgroundFetch() can immediately set a trigger afterward.
    let trigger = prepTrigger();
    while (true) {
      // Delay is from the last fetch - explicit fetch trigger fetch request disrupt the
      // delay-based fetching and resets the delay.
      let delayTimeoutID: ReturnType<typeof setTimeout> | undefined;
      let delay = new Promise<boolean>(resolve => {
        delayTimeoutID = setTimeout(resolve, fuzzy(60_000, 0.1), false);
      });

      // Wait for some time, or an explicit request to fetch.
      // It also reacts if the infinite loop is requested to terminate.
      const shallExit = await Promise.race([delay, trigger, exitRequest]);
      if (shallExit) {
        console.log("stopped background fetch")
        break;
      }

      // Remove the setTimeout, whether it was triggered or not.
      clearTimeout(delayTimeoutID);

      // Start fetching - that might take a while.
      console.log("Fetching...");

      const stid = this.stid;
      if (!stid) {
        throw new Error("missing stream id");
      }
      try {
        this.isFetching = true;

        // Limit the number of fetch we're requesting.
        // TODO: do limiting on server side.
        for (let i = 0; i < 10; i++) {
          // Get the list of waiters before starting a single fetch . If we get
          // a fetch request while we're in the middle of it, there might be a
          // race condition - i.e, part of the fetching was already done while
          // the fetch was requested. Also, do that on every attempt - a request
          // can happen in the middle of a series of attempts, and any content
          // is fine to notify for.
          const waiters = this.triggerFetchWaiters;
          this.triggerFetchWaiters = [];
          // Reset the trigger promise now, whether it was resolved or not. We
          // need to do that at the same moment we're getting the waiters list -
          // otherwise some of them might get dropped.
          trigger = prepTrigger();

          // Do the actual fetch.
          const done = await common.backend.fetch(stid);

          // Notify all that were waiting for their fetch trigger to be done.
          for (const w of waiters) {
            w();
          }

          if (done) { break; }
        }
      } finally {
        this.isFetching = false;
      }
    }
  }

  async triggerFetch() {
    await new Promise<void>(resolve => {
      if (!this.triggerFetchResolve) {
        throw new Error("invalid trigger");
      }
      this.triggerFetchWaiters.push(resolve);
      this.triggerFetchResolve();
    });
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
    const resp = await common.backend.list({ stid: stid, position: this.backwardPosition, direction: pb.ListRequest_Direction.BACKWARD })
    this.backwardPosition = resp.backwardPosition;

    const newItems: StatusItem[] = [];
    for (let i = 0; i < resp.items.length; i++) {
      const item = resp.items[i];
      const position = item.position;
      const status = common.parseStatus(item.status!);
      newItems.push({
        status: status,
        position: position,
        account: item.account!,
        statusMeta: item.meta!,   // TODO: check presence
        streamStatusState: item.streamStatusState!,
        isVisible: false,
        wasSeen: false,
        disappeared: false,
      });
    }
    this.items = [...newItems, ...this.items];
    // TODO: make more efficient
    let prevPosition: bigint | undefined;
    for (const item of this.items) {
      if (prevPosition !== undefined) {
        if (prevPosition + 1n != item.position) {
          console.error(`out of order items: prev=${prevPosition}, item=${item.position}`);
        }
      }
      prevPosition = item.position;
    }
    this.requestUpdate();
  }

  // List newer statuses.
  // This does NOT trigger a mastodon->mastopoof fetch, it just
  // list what's available from mastopoof.
  async listNext(isInitial = false) {
    const stid = this.stid;
    if (!stid) {
      throw new Error("missing stream id");
    }

    let resp: pb.ListResponse;
    try {
      this.loadingBarUsers++;
      resp = await common.backend.list({
        stid: stid,
        position: this.forwardPosition,
        direction: isInitial ? pb.ListRequest_Direction.INITIAL : pb.ListRequest_Direction.FORWARD,
      })
    } finally {
      this.loadingBarUsers--;
    }
    this.forwardPosition = resp.forwardPosition;
    if (this.backwardPosition === undefined) {
      this.backwardPosition = resp.backwardPosition;
    }

    for (let i = 0; i < resp.items.length; i++) {
      const item = resp.items[i];
      const position = item.position;
      if (this.items.length > 0) {
        const prevPosition = this.items[this.items.length - 1].position;
        if (position != prevPosition + 1n) {
          console.error(`missing position; previous=${prevPosition}, item=${position}`);
        }
      }
      const status = common.parseStatus(item.status!);
      this.items.push({
        status: status,
        position: position,
        account: item.account!,
        statusMeta: item.meta!,  // TODO: check presence
        streamStatusState: item.streamStatusState!,
        isVisible: false,
        wasSeen: false,
        disappeared: false,
      });
    }
    // Always indicate that initial loading is done - this is a latch anyway.
    this.firstListDone = true;
    this.requestUpdate();
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

    try {
      this.loadingBarUsers++;
      // Trigger fetching.
      // This returns once the first fetch is done, even if more are on-going.
      await this.triggerFetch();

      // And get those we already got listed.
      await this.listNext();
    } finally {
      this.loadingBarUsers--;
    }
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
    if (!this.firstListDone || !this.streamInfo || !this.userInfo) {
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

    let notifs = this.streamInfo.notificationsCount.toString();
    let notifsAlert = this.streamInfo.notificationsCount > 0;
    if (this.streamInfo.notificationState === pb.StreamInfo_NotificationsState.NOTIF_UNKNOWN) {
      notifs = "";
    } else if (this.streamInfo.notificationState === pb.StreamInfo_NotificationsState.NOTIF_MORE) {
      notifs = notifs + "+";
    }
    // TODO: support multiple account
    const notifAddr = `${this.userInfo.accounts[0].serverAddr}/notifications`;

    return html`
      <mast-main-view .loadingBarUsers=${this.loadingBarUsers} selectedView="stream">
        <div slot="header" class="header">
          <div class="title">Stream</div>
          <div class=${classMap({ "notifs": true, "notifs-alert": notifsAlert })}>
            <a href=${notifAddr}>${notifs}</a>
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
          <div class="fetchtime">
            ${this.isFetching ? html`Checking...` : html`
              <button class="refresh" @click=${this.triggerFetch} title="Check Mastodon for new statuses">
                <span class="material-symbols-outlined">refresh</span>
              </button>
            `}
          </div>
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
    .header {
      width: 100%;
      display: flex;
    }

    .title {
      flex-grow: 1;
    }

    .notifs {
      width: 30px;
      border: 1px dashed;
      border-radius: 4px;
      padding-left: 2px;
      padding-right: 2px;

      display: flex;
      justify-content: center;
    }

    .notifs-alert {
      background-color: var(--color-red-100);
    }

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
      height: 40px;
    }

    .refresh {
      height: 16px;
    }
  `];
}

declare global {
  interface HTMLElementTagNameMap {
    'mast-stream': MastStream
  }
}
