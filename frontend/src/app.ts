import { LitElement, css, html } from 'lit'
import { customElement, state } from 'lit/decorators.js'

import { LoginUpdateEvent, LoginState } from "./backend";
import * as common from "./common";

import "./auth";
import "./stream";


@customElement('app-root')
export class AppRoot extends LitElement {
  @state() private lastLoginUpdate?: LoginUpdateEvent;

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

  static styles = [common.sharedCSS, css``];
}

declare global {
  interface HTMLElementTagNameMap {
    'app-root': AppRoot
  }
}