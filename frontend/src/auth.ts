import { LitElement, css, html } from 'lit'
import { customElement, state } from 'lit/decorators.js'
import { Ref, createRef, ref } from 'lit/directives/ref.js';

import * as common from "./common";


function cleanServerAddr(addr: string): string {
  if (addr === "") {
    return "";
  }
  if (addr.includes("://")) {
    return addr;
  }
  return "https://" + addr;
}

// Login screen
@customElement('mast-login')
export class MastLogin extends LitElement {

  @state() private authURI: string = "";
  // Server address as used to get the authURI.
  @state() private serverAddr: string = "";
  // Current user input for mastodon server, cleaned up.
  @state() inputServerAddr: string = "";

  private serverAddrRef: Ref<HTMLInputElement> = createRef();
  private authCodeRef: Ref<HTMLInputElement> = createRef();
  private inviteCodeRef: Ref<HTMLInputElement> = createRef();

  firstUpdated() {
    this.refreshInputServerAddr();
  }

  async startLogin() {
    const serverAddr = this.serverAddrRef.value?.value;
    if (!serverAddr) {
      return;
    }
    const inviteCode = this.inviteCodeRef.value?.value;
    try {
      this.authURI = await common.backend.authorize(serverAddr, inviteCode);
    } catch (e) {
      console.error("failed authorize:", e);
      return;
    }
    this.refreshInputServerAddr();
    this.serverAddr = this.inputServerAddr;
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

  refreshInputServerAddr() {
    const rawInput = this.serverAddrRef.value?.value ?? "";
    this.inputServerAddr = cleanServerAddr(rawInput);
  }

  render() {
    if (!this.authURI) {
      return html`
        <div class="middlepane">
          <div>
            <div>
              <label>
                Mastodon server address
                <input id="server-addr" type="url" ${ref(this.serverAddrRef)} value="mastodon.social" @input=${this.refreshInputServerAddr} required autofocus></input>
              </label>
              <br>
              Using: ${this.inputServerAddr}
            </div>
            <div>
              <label>Invite code
                <input id="invite-code" type="text" ${ref(this.inviteCodeRef)} value=""></input>
              </label>
            </div>
          </div>
          <div>
            <button id="do-auth" @click=${this.startLogin}>Auth</button>
          </div>
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
  `];
}

declare global {
  interface HTMLElementTagNameMap {
    'mast-login': MastLogin
  }
}
