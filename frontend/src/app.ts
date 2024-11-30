import { LitElement, css, html } from 'lit'
import { customElement, state } from 'lit/decorators.js'

import { LoginUpdateEvent, LoginState } from "./backend";
import * as common from "./common";
import * as mainview from "./mainview";
import "./auth";
import "./stream";
import "./search";

console.log("Config:", globalThis.mastopoofConfig.src);

@customElement('app-root')
export class AppRoot extends LitElement {
  @state() private lastLoginUpdate?: LoginUpdateEvent;
  @state() private currentView: mainview.viewName = "stream";

  constructor() {
    super();

    common.backend.onEvent.addEventListener("login-update", ((evt: LoginUpdateEvent) => {
      if (evt.state === LoginState.LOGGED && this.lastLoginUpdate?.state !== LoginState.LOGGED) {
        console.log("Logged in");
      }
      this.lastLoginUpdate = evt;
    }) as EventListener);

    // Determine if we're already logged in.
    common.backend.login();
  }

  connectedCallback(): void {
    super.connectedCallback();
    // Prevent browser to automatically scroll to random places on load - it
    // does not work well given that the list of elements might have changed.
    if (history.scrollRestoration) {
      history.scrollRestoration = "manual";
    }

    this.updateFromLocation(); // On initial load.
    window.addEventListener('popstate', () => {
      // TODO: remove event listener.
      this.updateFromLocation(); // And when location changes.
    });
  }

  updateFromLocation() {
    const targetView = new URL(document.location.toString()).searchParams.get("v");
    if (targetView === "search") {
      this.currentView = "search";
    } else {
      this.currentView = "stream";
    }
  }

  render() {
    if (!this.lastLoginUpdate || this.lastLoginUpdate.state === LoginState.LOADING) {
      return html`Loading...`;
    }
    if (this.lastLoginUpdate.state === LoginState.NOT_LOGGED) {
      return html`<mast-login></mast-login>`;
    }
    if (this.currentView === "search") {
      return html`<mast-search></mast-search>`;
    };

    if (!this.lastLoginUpdate?.userInfo) {
      throw new Error("missing login information");
    }
    const userInfo = this.lastLoginUpdate.userInfo;
    return html`<mast-stream .userInfo=${userInfo} .stid=${userInfo.defaultStid}></mast-stream>`;
  }

  static styles = [common.sharedCSS, css``];
}

declare global {
  interface HTMLElementTagNameMap {
    'app-root': AppRoot
  }
}