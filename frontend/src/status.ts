import { LitElement, css, html, nothing, TemplateResult } from 'lit'
import { customElement, state, property } from 'lit/decorators.js'
import { unsafeHTML } from 'lit/directives/unsafe-html.js';
import { classMap } from 'lit/directives/class-map.js';
import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";
import dayjs from 'dayjs';

import * as common from "./common";
import * as mastodon from "./mastodon";

// StatusItem represents the state in the UI of a given status.
export interface StatusItem {
  // Position in the stream in the backend.
  position: bigint;
  // status, if loaded.
  status: mastodon.Status;
  // The account where this status was obtained from.
  account: pb.Account;
  statusstate: pb.StatusState;
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
  const s = item.status.reblog ? item.status.reblog : item.status;
  return `${item.account.serverAddr}/@${s.account.acct}/${item.status.id}`;
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
    common.backend.setLastRead(this.stid, this.item?.position - 1n);
  }

  render() {
    if (!this.item) {
      return html`
        <div class="status">oops.</div>`
    }

    var filteredAr: string[] = [];
    for (const filter of this.item.statusstate.filterstate ?? []) {
      if (filter.matched == true) {
        filteredAr.push(filter.desc);
      }
    }
    const filtered = filteredAr.join(", ");

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
        attachments.push(html`<span>Unsupported attachment type: ${ma.type}. <a href=${ma.url}>Direct link</a>.</span>`);
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

    const card: TemplateResult[] = [];
    if (s.card && s.media_attachments.length === 0) {
      card.push(html`
        <a href="${s.card.url}" target="_blank" class="previewcard-link">
          <div class="previewcard-container">
            <div class="previewcard-image">
              <img src="${s.card.image ?? ''}"/>
            </div>
            <div class="previewcard-meta">
              <div class="previewcard-provider">${s.card.provider_name}</div>
              <div class="previewcard-title">${s.card.title}</div>
            </div>
          </div>
        </a>
      `)
    }

    // Main created time is the time of the status or of the reblog content
    // if the status is a reblog.
    const createdTime = dayjs(s.created_at);
    const createdTimeLabel = `${common.displayTimezone}: ${createdTime.tz(common.displayTimezone).format()}\nSource: ${s.created_at}`;

    // Reblog time is the time of the main status, as in those case, the real content is in the reblog.
    const reblogTime = dayjs(this.item.status.created_at);
    const reblogTimeLabel = `${common.displayTimezone}: ${reblogTime.tz(common.displayTimezone).format()}\nSource: ${this.item.status.created_at}`;

    const openTarget = localStatusURL(this.item);

    const contentHtml = html`
      ${!!reblog ? html`
        <div class="reblog">
          <span class="centered">
            <img class="avatar" src=${account.avatar}></img>
            Reblog by ${expandEmojis(account.display_name, account.emojis)} &lt;${qualifiedAccount(account)}&gt;
          </span>
          <span class="timestamp" title="${reblogTimeLabel}">${reblogTime.fromNow()}</span>
        </div>
      ` : nothing}
    <div class="content">
      ${expandEmojis(s.content, s.emojis)}
    </div>
    <div class="poll">
      ${poll}
    </div>
    <div class="previewcard">
      ${card}
    </div>
    <div class="attachments">
      ${attachments}
    </div>
    <div class="tools">
      <div>
        <button disabled><span class="material-symbols-outlined" title="Favorite">star</span></button>
        <span class="count">${s.favourites_count}</span>
        <button disabled><span class="material-symbols-outlined" title="Boost">repeat</span></button>
        <span class="count">${s.reblogs_count}</span>
        <button disabled><span class="material-symbols-outlined" title="Reply...">reply</span></button>
        <span class="count">${s.replies_count}</span>
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
    ${this.showRaw ? html`
      <pre class="rawcontent">${JSON.stringify(this.item.status, null, "  ")}</pre>` : nothing}
    </div>
    `;

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
              <a href=${s.url ?? ""} target="_blank"><span class="material-symbols-outlined"
                                                           title="Open status on original server">travel_explore</span></a>
            </span>
        </div>
        ${!!filtered ? html`<div class="filtered">filtered by ${filtered}</div>` : contentHtml}
      </div>`;
  }

  static styles = [common.sharedCSS, css`
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

      .filtered {
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

      .previewcard-link {
        color: inherit;
        text-decoration: inherit;
      }

      .previewcard-meta {
        align-self: center
      }

      .previewcard-provider {
        font-style: italic;
      }

      .previewcard-title {
        font-weight: bold;
        font-size: 1.2rem;
      }


      .previewcard-image > img {
        max-width: 150px;
        max-height: 150px;
      }

      .previewcard-container {
        border-style: solid;
        border-width: 1px;
        display:grid;
        grid-template-columns: 150px 1fr;
        column-gap: 10px;
        background-color: var(--color-grey-150);
      }
    `];
}

declare global {
  interface HTMLElementTagNameMap {
    'mast-status': MastStatus
  }
}
