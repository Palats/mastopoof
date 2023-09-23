import { LitElement, css, html } from 'lit'
import { customElement } from 'lit/decorators.js'

@customElement('app-root')
export class AppRoot extends LitElement {
  render() {
    return html`
      plop
    `
  }

  static styles = css``
}

declare global {
  interface HTMLElementTagNameMap {
    'app-root': AppRoot
  }
}
