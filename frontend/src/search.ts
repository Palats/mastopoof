import { LitElement, css, html } from 'lit'
import { customElement, state } from 'lit/decorators.js'
import { Ref, createRef, ref } from 'lit/directives/ref.js';
import * as mastodon from "./mastodon";

import * as statuslib from "./status";
import * as common from "./common";


@customElement('mast-search')
export class MastSearch extends LitElement {
  @state() private items: statuslib.StatusItem[] = [];

  private searchBoxRef: Ref<HTMLInputElement> = createRef();

  async doSearch() {
    let value = this.searchBoxRef.value?.value.trim();
    if (!value) {
      return;
    }
    console.log("searching for", value);
    const resp = await common.backend.search(value);
    this.items = [];
    for (const item of resp.items) {
      const position = item.position;
      const status = JSON.parse(item.status!.content) as mastodon.Status;
      this.items.push({
        position: position,
        status: status,
        account: item.account!,
      });
    }

  }

  render() {
    return html`
      <mast-main-view>
        <span slot="header">Search</span>
        <div slot="list">
          <div class="search-form">
            <label for="search-box">Status ID</label>
            <input type="text" id="search-box" ${ref(this.searchBoxRef)} value="" required autofocus></input>
            <button @click=${() => this.doSearch()}>Search</button>
          </div>

          ${this.items.map(item => html`
            <mast-status .item=${item as any}></mast-status>
          `)}
        </div>
        <div slot="footer">No search.</div>
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