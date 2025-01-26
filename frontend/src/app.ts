import "@material-symbols/font-400/outlined.css";

import { LitElement, css, html } from 'lit'
import { customElement, state } from 'lit/decorators.js'

import { LoginUpdateEvent, LoginState } from "./backend";
import * as common from "./common";
import * as mainview from "./mainview";
import "./auth";
import "./stream";
import "./search";
import "./settings";

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
    let targetView = new URL(document.location.toString()).searchParams.get("v");
    if (!targetView) {
      // When the view is not specified in the URL, we want to fallback to stream by default.
      targetView = "stream";
    }
    // TODO: verify it is a valid value.
    this.currentView = targetView as mainview.viewName;
  }

  render() {
    if (!this.lastLoginUpdate || this.lastLoginUpdate.state === LoginState.LOADING) {
      return html`Loading...`;
    }
    if (this.lastLoginUpdate.state === LoginState.NOT_LOGGED) {
      return html`<mast-login></mast-login>`;
    }
    if (!this.lastLoginUpdate?.userInfo) {
      throw new Error("missing login information");
    }

    switch (this.currentView) {
      case "stream":
        const userInfo = this.lastLoginUpdate.userInfo;
        return html`<mast-stream .userInfo=${userInfo} .stid=${userInfo.defaultStid}></mast-stream>`;
      case "search":
        return html`<mast-search></mast-search>`;
      case "settings":
        return html`<mast-settings></mast-settings>`;
      default:
        const exhaustive: never = this.currentView;
        return html`Invalid view ${exhaustive}`;
    }
  }

  static styles = [common.sharedCSS, css``];
}

declare global {
  interface HTMLElementTagNameMap {
    'app-root': AppRoot
  }
}