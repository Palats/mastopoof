import { LitElement, css, html } from 'lit'
import { customElement } from 'lit/decorators.js'
import * as common from "./common";

@customElement('mast-settings')
export class MastSettings extends LitElement {
  render() {
    return html`
      <mast-main-view selectedView="settings">
        <span slot="header">Settings</span>
        <div slot="list">
          In the future, some settings.
        </div>
        <div slot="footer" class="centered">Voila voila.</div>
      </mast-main-view>
    `;
  }
  static styles = [common.sharedCSS, css`
  `];
}

declare global {
  interface HTMLElementTagNameMap {
    'mast-settings': MastSettings
  }
}