import { LitElement, css, html } from 'lit'
import { customElement, state } from 'lit/decorators.js'
import { Ref, createRef, ref } from 'lit/directives/ref.js';

import * as statuslib from "./status";
import * as common from "./common";
import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";

@customElement('mast-search')
export class MastSearch extends LitElement {
  @state() private items: statuslib.StatusItem[] = [];
  @state() loadingBarUsers = 0;
  @state() searchMsg = "No search.";

  private searchBoxRef: Ref<HTMLInputElement> = createRef();

  async doSearch() {
    let value = this.searchBoxRef.value?.value.trim();
    if (!value) {
      return;
    }
    console.log("searching for", value);
    this.loadingBarUsers++;
    let resp: pb.SearchResponse;
    try {
      resp = await common.backend.search(value);
    } finally {
      this.loadingBarUsers--;
    }

    this.searchMsg = `${resp.items.length} status(es) found.`

    this.items = [];
    for (const item of resp.items) {
      const position = item.position;
      const status = common.parseStatus(item.status!);
      this.items.push({
        position: position,
        status: status,
        account: item.account!,
        statusMeta: item.meta!,    // TODO: check presence
      });
    }

  }

  render() {
    return html`
      <mast-main-view .loadingBarUsers=${this.loadingBarUsers} selectedView="search">
        <span slot="header">Search</span>
        <div slot="list">
          <div class="search-form">
            <form @submit=${(e: Event) => { e.preventDefault(); this.doSearch() }}>
              <label for="search-box">Status ID</label>
              <input type="text" id="search-box" ${ref(this.searchBoxRef)} value="" required autofocus></input>
              <button type="submit">Search</button>
            </form>
          </div>

          ${this.items.map(item => html`
            <mast-status .item=${item as any}></mast-status>
          `)}
        </div>
        <div slot="footer" class="centered">${this.searchMsg}</div>
      </mast-main-view>
    `;
  }
  static styles = [common.sharedCSS, css`
  `];
}

declare global {
  interface HTMLElementTagNameMap {
    'mast-search': MastSearch
  }
}