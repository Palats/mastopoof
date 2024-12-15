import { LitElement, css, html, nothing } from 'lit'
import { customElement, state, property } from 'lit/decorators.js'
import { classMap } from 'lit/directives/class-map.js';

import * as common from "./common";

import "./time";

export type viewName = "stream" | "search" | "settings";

export class ChangeViewEvent extends CustomEvent<viewName> { }

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

  @property() selectedView: viewName = "stream";

  @state() private showMenu = false;

  switchView(e: Event, name: viewName) {
    const u = new URL(document.location.toString());
    if (name === "stream") {
      u.searchParams.delete("v");
    } else {
      u.searchParams.set("v", name);
    }
    history.pushState({}, "", u.toString());
    // `pushState` does not trigger a `popstate` event, so force it.
    // It requires `composed` to allow it to cross shadow dom.
    this.dispatchEvent(new PopStateEvent('popstate', { composed: true, bubbles: true }));
    // Avoid reloading the page
    e.preventDefault();
  }

  render() {
    return html`
      <div class="header">
        <div class="headercontent">
          <button class="menubutton" @click=${() => { this.showMenu = !this.showMenu }}>
            ${this.showMenu ? html`
            <span class="material-symbols-outlined" title="Close menu">close</span>
            `: html`
            <span class="material-symbols-outlined" title="Open menu">menu</span>
            `}
          </button>
          <slot name="header"></slot>
        </div>
        ${this.showMenu ? html`
          <div class="menucontent">
            <div class=${classMap({ "menuentry": true, "menuselected": this.selectedView === "stream" })}>
              <a href="?v=stream" @click=${(e: Event) => this.switchView(e, "stream")}>Stream</a>
            </div>
            <div class=${classMap({ "menuentry": true, "menuselected": this.selectedView === "search" })}>
              <a href="?v=search" @click=${(e: Event) => this.switchView(e, "search")}>Search</a>
            </div>
            <div class=${classMap({ "menuentry": true, "menuselected": this.selectedView === "settings" })}>
              <a href="?v=settings" @click=${(e: Event) => this.switchView(e, "settings")}>Settings</a>
            </div>
            <slot name="menu"></slot>
            <div class="menuentry">
              <div><button @click=${() => common.backend.logout()}>Logout</button></div>
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
    }

    .menubutton {
      font-size: 24px;
      margin-right: 10px;
    }

    .menucontent {
      grid-column: 2;
      padding: 0;
      background-color: var(--color-blue-25);
      box-shadow: rgb(0 0 0 / 80%) 0px 16px 12px;
    }

    .menuentry {
      background-color: var(--color-grey-150);
      height: 40px;
      display: flex;
      align-items: center;

      margin-bottom: 2px;
      padding: 0px 0px 0px 8px;
    }

    .menuentry > * {
      flex-grow: 1;
      padding: 8px;
    }

    .menuselected {
      font-weight: bold;
      font-style: italic;

      background-color: var(--color-grey-300);
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
