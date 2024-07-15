import { LitElement, css, html } from 'lit'
import { customElement, state } from 'lit/decorators.js'
import { Ref, createRef, ref } from 'lit/directives/ref.js';

import * as common from "./common";


// Login screen
@customElement('mast-login')
export class MastLogin extends LitElement {

  @state() private authURI: string = "";
  // Server address as used to get the authURI.
  @state() private serverAddr: string = "";

  private serverAddrRef: Ref<HTMLInputElement> = createRef();
  private authCodeRef: Ref<HTMLInputElement> = createRef();
  private inviteCodeRef: Ref<HTMLInputElement> = createRef();

  async startLogin() {
    const serverAddr = this.serverAddrRef.value?.value;
    if (!serverAddr) {
      return;
    }
    const inviteCode = this.inviteCodeRef.value?.value;
    this.authURI = await common.backend.authorize(serverAddr, inviteCode);
    this.serverAddr = serverAddr;
    console.log("authURI", this.authURI);
  }

  async doLogin() {
    const authCode = this.authCodeRef.value?.value;
    if (!authCode) {
      // TODO: surface error
      console.error("invalid authorization code");
      return;
    }
    await common.backend.token(this.serverAddr, authCode);
  }

  render() {
    if (!this.authURI) {
      return html`
        <div>
          <label>
            Mastodon server address (must start with https)
            <input id="server-addr" type="url" ${ref(this.serverAddrRef)} value="https://mastodon.social" required autofocus></input>
            </label>
          <label>Invite code
            <input type="text" ${ref(this.inviteCodeRef)} value=""></input>
          </label>
          <button id="do-auth" @click=${this.startLogin}>Auth</button>
        </div>
      `;
    }

    return html`
      <div>
        <a href=${this.authURI}>Mastodon Auth</a>
      </div>
      <div>
        <label for="auth-code">Authorization code</label>
        <input type="text" id="auth-code" ${ref(this.authCodeRef)} required autofocus></input>
        <button @click=${this.doLogin}>Auth</button>
      </div>
    `;
  }

  static styles = [common.sharedCSS, css``];
}

declare global {
  interface HTMLElementTagNameMap {
    'mast-login': MastLogin
  }
}
