import { LitElement, css, html, nothing, TemplateResult, unsafeCSS } from 'lit'
import { customElement, state, property } from 'lit/decorators.js'
import { unsafeHTML } from 'lit/directives/unsafe-html.js';
import { repeat } from 'lit/directives/repeat.js';
import { Ref, createRef, ref } from 'lit/directives/ref.js';
import { classMap } from 'lit/directives/class-map.js';

import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";
import { Backend, StreamUpdateEvent, LoginUpdateEvent, LoginState } from "./backend";

import normalizeCSSstr from "./normalize.css?inline";
import baseCSSstr from "./base.css?inline";

import * as mastodon from "./mastodon";

import dayjs from 'dayjs';
import relativeTimePlugin from 'dayjs/plugin/relativeTime';
dayjs.extend(relativeTimePlugin);
import utcPlugin from 'dayjs/plugin/utc';
dayjs.extend(utcPlugin);
import timezonePlugin from 'dayjs/plugin/timezone';
dayjs.extend(timezonePlugin);

const displayTimezone = dayjs.tz.guess();
console.log("Display timezone:", displayTimezone);

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
        console.log("Logged in");
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
    const stid = this.lastLoginUpdate.userInfo?.defaultStid;
    if (!stid) {
      throw new Error("missing stid");
    }
    return html`<mast-stream .stid=${stid}></mast-stream>`;
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
  @state() private showMenu = false;
  // Count the number of reasons why the loading bar should be shown.
  // This allows to have multiple things loading, while avoiding having
  // the first one finishing removing the loading bar.
  @state() private loadingBarUsers = 0;

  connectedCallback(): void {
    super.connectedCallback();
    this.observer = new IntersectionObserver(
      (entries: IntersectionObserverEntry[], _: IntersectionObserver) => this.onIntersection(entries), {
      root: null,
      rootMargin: "0px",
      threshold: 0.0,
    });

    backend.onEvent.addEventListener("stream-update", ((evt: StreamUpdateEvent) => {
      if (evt.curr) {
        this.streamInfo = evt.curr;
      }
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
      backend.advanceLastRead(this.stid, disappearedPosition);
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
    const resp = await backend.list({ stid: stid, position: position, direction: pb.ListRequest_Direction.BACKWARD })

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

  // Load newer statuses.
  async loadNext() {
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
      resp = await backend.list({ stid: stid, position: position, direction: pb.ListRequest_Direction.FORWARD })
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

  async fetch() {
    const stid = this.stid;
    if (!stid) {
      throw new Error("missing stream id");
    }
    console.log("Fetching...");
    try {
      this.loadingBarUsers++;
      await backend.fetch(stid);
    } finally {
      this.loadingBarUsers--;
    }
    this.loadNext();
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

    let count = 0n;
    let loadedCount = 0n;
    let loadedMsg = '';
    if (this.items.length === 0) {
      // Initial loading was done, so if items is empty, it means nothing is available.
      count = this.streamInfo.remainingPool;
    } else {
      const lastPosition = this.items[this.items.length - 1].position;
      const lastVisible = this.lastVisiblePosition ?? 0n;
      // We've got:
      //   - visible statuses which are already on stream but not yet on screen/loaded.
      //   - statuses still in pool and not yet sorted in stream.
      loadedCount = lastPosition - lastVisible;
      count = this.streamInfo.remainingPool + loadedCount;
      loadedMsg = loadedCount !== count ? `(${loadedCount} loaded)` : '';
    }

    let remaining = html`Updating...`;
    if (count == 0n) {
      remaining = html`End of stream`;
    } else if (count == 1n) {
      remaining = html`1 remaining status ${loadedMsg}`;
    } else {
      remaining = html`${count} remaining statuses ${loadedMsg}`;
    }

    return html`
      <div class="page">
        <div class="middlepane">
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
            ${this.showMenu ? html`<div class="menucontent">${this.renderMenu()}</div>` : nothing}
          </div>

          <div class="content">
            ${this.renderStreamContent()}
          </div>
          <div class="footer">
            <div class="footercontent">
              <div class=${classMap({ loadingbar: true, hidden: this.loadingBarUsers <= 0 })}></div>
              <div class="centered">${remaining}</div>
            </div>
          </div>
        </div>
      </div>
    `;
  }

  renderMenu(): TemplateResult {
    return html`
      <div>plop</div>
      <div>
        <button @click=${this.fetch}>Fetch now</button>
      </div>
      <div>
        <button @click=${() => backend.logout()}>Logout</button>
      </div>
    `;
  }

  renderStreamContent(): TemplateResult {
    if (!this.streamInfo) {
      throw new Error("should not have been called");
    }

    // This function is called only if the initial loading is done - so if there is no items, it means that
    // the stream was empty at that time, and thus we're at its beginning.
    const isBeginning = this.items.length == 0 || (this.items[0].position === this.streamInfo.firstPosition)

    return html`
      <div class="noanchor contentitem stream-beginning centered">
      ${isBeginning ? html`
        ${this.items.length === 0 ?
          html`<div>No statuses.</div>` :
          html`<div>Beginning of stream.</div>`
        }
      `: html`
        <button @click=${this.loadPrevious}>
          <span class="material-symbols-outlined">arrow_upward</span>
          Load earlier statuses
          <span class="material-symbols-outlined">arrow_upward</span>
        </button>
      `}
      </div>

      ${repeat(this.items, item => item.position, (item, _) => this.renderStatus(item))}

      <div class="noanchor contentitem stream-end">
        <div class="centered">
          <div>
            <button @click=${this.loadNext} ?disabled=${this.streamInfo.remainingPool === 0n}>
              <span class="material-symbols-outlined">arrow_downward</span>
              Load more statuses
              <span class="material-symbols-outlined">arrow_downward</span>
            </button>
          </div>
          <button @click=${this.fetch}>Fetch</button>
        </div>
      </div>
    `;
  }

  renderStatus(item: StatusItem): TemplateResult[] {
    const lastRead = this.streamInfo?.lastRead ?? 0;
    const pos = item.position;
    const content: TemplateResult[] = [];
    content.push(html`<mast-status ?isRead=${pos <= lastRead} class="contentitem" ${ref((elt?: Element) => this.updateStatusRef(item, elt))} .stid=${this.stid} .item=${item as any}></mast-status>`);
    if (item.position == lastRead) {
      content.push(html`<div class="lastread contentitem centered">The bookmark</div>`);
    }
    return content;
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

      background-color: var(--color-grey-300);
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
      min-height: 50px;
      background-color: var(--color-grey-300);

      display: flex;
      flex-direction: column;
    }

    /* Header content is separated from header styling. This way, the header
    element can cover everything behind (to pretend it is not there) and let
    options for styling, beyond a basic all encompassing box.
    */
    .headercontent {
      background-color: var(--color-blue-25);
      padding: 8px;

      min-height: 50px;

      border-bottom-style: double;
      border-bottom-width: 3px;

      display: flex;
      align-items: center;
      justify-content: space-between;
    }

    .menucontent {
      padding: 8px;
      background-color: var(--color-blue-25);
      box-shadow: rgb(0 0 0 / 80%) 0px 16px 12px;
    }

    .footer {
      position: sticky;
      bottom: 0;
      z-index: 2;
      box-sizing: border-box;
      min-height: 30px;
      background-color: var(--color-grey-300);

      display: grid;
      grid-template-rows: 1fr;

      border-top-style: double;
      border-top-width: 3px;
    }

    .footercontent {
      background-color: var(--color-blue-25);
      padding: 5px;
      box-sizing: border-box;
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

function localStatusURL(item: StatusItem): string {
  return `${item.account.serverAddr}/@${item.status.account.acct}/${item.status.id}`;
}

/**
 * Expands messages to resolve custom emojis to their images.
 * TODO: add a flag to disable HTML interpretation when needed (for instance for people names)
 * @param msg content in which to resolve emojis, interpreted as HTML
 * @param emojis emoji mapping
 */
function expandEmojis(msg: string, emojis?: mastodon.CustomEmoji[]): TemplateResult {
  if (!emojis || emojis.length === 0) {
    return html`${unsafeHTML(msg)}`;
  }

  const perCode = new Map<string, mastodon.CustomEmoji>();
  for (const emoji of emojis) {
    perCode.set(emoji.shortcode, emoji);
  }

  // TODO escape emoji short codes for regexs
  const emojiregex = new RegExp(`:(${Array.from(perCode.keys()).join('|')}):`, 'g');
  const doc = (new DOMParser).parseFromString(msg, "text/html");
  const treeWalker = doc.createTreeWalker(doc.body, NodeFilter.SHOW_TEXT);
  var textNodes = [];
  while (treeWalker.nextNode()) {
    textNodes.push(treeWalker.currentNode);
  }
  for (const node of textNodes) {
    const parent = node.parentNode;
    if (!parent) {
      // can't happen because body is at least parent, so let's soothe TS with that
      continue;
    }
    const txt = node.textContent || '';
    const matches = txt.matchAll(emojiregex) || [];
    var prevMatchEnd = 0;
    for (const match of matches) {
      const code = match[1];
      const emoji = perCode.get(code);
      if (emoji) {
        const img = doc.createElement('img');
        img.setAttribute('class', 'emoji');
        img.setAttribute('src', emoji.url);
        img.setAttribute('alt', `emoji ${emoji.shortcode}`);
        img.setAttribute('title', `emoji ${emoji.shortcode}`);
        parent.insertBefore(doc.createTextNode(txt.substring(prevMatchEnd, match.index)), node)
        parent.insertBefore(img, node);
        prevMatchEnd = match.index + match[0].length;
      }
    }
    parent.insertBefore(doc.createTextNode(txt.substring(prevMatchEnd)), node);
    parent.removeChild(node);
  }
  const result = doc.body.innerHTML;

  return html`${unsafeHTML(result)}`;
}

@customElement('mast-status')
export class MastStatus extends LitElement {
  @property({ attribute: false })
  item?: StatusItem;

  @property({ attribute: false })
  stid?: bigint;

  @property({ type: Boolean })
  isRead: boolean = false;

  @state()
  private accessor showRaw = false;

  markUnread() {
    if (!this.item) {
      console.error("missing connection");
      return;
    }
    if (!this.stid) {
      throw new Error("missing stream id");
    }
    // Not sure if doing computation on "position" is fine, but... well.
    backend.setLastRead(this.stid, this.item?.position - 1n);
  }

  render() {
    if (!this.item) {
      return html`<div class="status">oops.</div>`
    }

    // This actual status - i.e., the reblogged one when it is a reblogged, or
    // the basic one.
    const s = this.item.status.reblog ?? this.item.status;
    const reblog = this.item.status.reblog;
    const account = this.item.status.account;

    const attachments: TemplateResult[] = [];
    for (const ma of (s.media_attachments ?? [])) {
      if (ma.type === "image") {
        // TODO: preview_url is probably wrong?
        attachments.push(html`
          <img src=${ma.preview_url} alt=${ma.description || ""}></img>
        `);
      } else if (ma.type === "gifv") {
        attachments.push(html`
          <video controls muted src=${ma.url} alt=${ma.description || nothing}></video>
        `);
      } else if (ma.type === "video") {
        attachments.push(html`
          <video controls src=${ma.url} alt=${ma.description || nothing}></video>
        `);
      } else {
        attachments.push(html`unsupported attachment type ${ma.type}`);
      }
    }

    const poll: TemplateResult[] = [];
    if (s.poll) {
      for (const option of s.poll.options) {
        poll.push(html`
            <div class="poll-option"><input type="radio" disabled>${option.title}</input></div>
        `);
      }
    }

    // Main created time is the time of the status or of the reblog content
    // if the status is a reblog.
    const createdTime = dayjs(s.created_at);
    const createdTimeLabel = `${displayTimezone}: ${createdTime.tz(displayTimezone).format()}\nSource: ${s.created_at}`;

    // Reblog time is the time of the main status, as in those case, the real content is in the reblog.
    const reblogTime = dayjs(this.item.status.created_at);
    const reblogTimeLabel = `${displayTimezone}: ${reblogTime.tz(displayTimezone).format()}\nSource: ${this.item.status.created_at}`;

    const openTarget = localStatusURL(this.item);

    return html`
      <div class="status ${classMap({ read: this.isRead, unread: !this.isRead })}">
        <div class="account">
          <span class="centered">
            <img class="avatar" src=${s.account.avatar}></img>
            ${expandEmojis(s.account.display_name, s.account.emojis)} &lt;${qualifiedAccount(s.account)}&gt;
          </span>
          <span>
            <span class="timestamp" title="${createdTimeLabel}">${createdTime.fromNow()}</span>
            <a href=${openTarget} target="_blank"><span class="material-symbols-outlined" title="Open status">open_in_new</span></a>
            <a href=${s.url ?? ""} target="_blank"><span class="material-symbols-outlined" title="Open status on original server">travel_explore</span></a>
          </span>
        </div>
        ${!!reblog ? html`
          <div class="reblog">
            <span class="centered">
              <img class="avatar" src=${account.avatar}></img>
              Reblog by ${expandEmojis(account.display_name, account.emojis)} &lt;${qualifiedAccount(account)}&gt;
            </span>
            <span class="timestamp" title="${reblogTimeLabel}">${reblogTime.fromNow()}</span>
          </div>
        `: nothing}
        <div class="content">
          ${expandEmojis(s.content, s.emojis)}
        </div>
        <div class="poll">
          ${poll}
        </div>
        <div class="attachments">
          ${attachments}
        </div>
        <div class="tools">
          <div>
            <button disabled><span class="material-symbols-outlined" title="Favorite">star</span></button><span class="count">${s.favourites_count}</span>
            <button disabled><span class="material-symbols-outlined" title="Boost">repeat</span></button><span class="count">${s.reblogs_count}</span>
            <button disabled><span class="material-symbols-outlined" title="Reply...">reply</span></button><span class="count">${s.replies_count}</span>
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
      border-radius: 4px;
      border-width: 1px;
      padding: 0;
      background-color: var(--color-grey-000);

      overflow: hidden;
      display: flex;
      flex-direction: column;
    }

    .read {
      border-color: var( --color-blue-300);
    }

    .unread {
      border-color: var(--color-grey-999);
    }

    .rawcontent {
      white-space: pre-wrap;
      word-break: break-all;
    }

    .account {
      display: flex;
      align-items: center;
      padding: 3px;
      justify-content: space-between;
      background-color: var(--color-blue-100);
    }

    .reblog {
      display: flex;
      align-items: center;
      padding: 2px;
      font-size: 0.8rem;
      font-style: italic;
      justify-content: space-between;
      background-color: var(--color-blue-50);
    }

    .avatar {
      width: auto;
      padding-right: 3px;
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
      padding: 4px;
    }

    .attachments {
      align-items: center;
      display: flex;
      flex-direction: column;
    }

    .attachments img {
      max-width: 100%;
      max-height: 400px;
    }

    .attachments video {
      display: block;
      max-width: 100%;
      max-height: 400px;
    }

    .tools {
      display: flex;
      align-items: center;
      justify-content: space-between;
      padding: 2px;
      margin-top: 2px;
      background-color: var(--color-blue-400);
      color: var(--color-grey-000);
    }

    .count {
      font-size: 0.8rem;
      margin-left: 2px;
      margin-right: 4px;
    }

    .timestamp {
      font-size: 0.8rem;
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
  private inviteCodeRef: Ref<HTMLInputElement> = createRef();

  async startLogin() {
    const serverAddr = this.serverAddrRef.value?.value;
    if (!serverAddr) {
      return;
    }
    const inviteCode = this.inviteCodeRef.value?.value;
    this.authURI = await backend.authorize(serverAddr, inviteCode);
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
    await backend.token(this.serverAddr, authCode);
  }

  render() {
    if (!this.authURI) {
      return html`
        <div>
          <label for="server-addr">Mastodon server address (must start with https)</label>
          <input type="url" id="server-addr" ${ref(this.serverAddrRef)} value="https://mastodon.social" required autofocus></input>
          <label for="invite-code">Invite code</label>
          <input type="text" id="invite-code" ${ref(this.inviteCodeRef)} value=""></input>

          <button @click=${this.startLogin}>Auth</button>
        </div>
      `;
    }

    return html`
      <div>
        <a href=${this.authURI}>Mastodon Auth</a>
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
