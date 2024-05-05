import { LitElement, css, html } from 'lit'
import { customElement } from 'lit/decorators.js'

import * as common from "./common";


@customElement('mast-search')
export class MastSearch extends LitElement {
  render() {
    return html`
      <mast-main-view>
        <span slot="header">Search</span>
        <div slot="list">
          <div class="search-box">
          </div>
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