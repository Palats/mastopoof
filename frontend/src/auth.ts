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
    this.refreshInputServerAddr();
    const serverAddr = this.inputServerAddr;
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
        <div class="authbox">
          <h1>Sign into Mastodon</h1>

          <label for="server-addr">
            Mastodon server address
          </label>
          <input id="server-addr" type="url" ${ref(this.serverAddrRef)} value="${mastopoofConfig.defServer}" @input=${this.refreshInputServerAddr} required autofocus></input>

          <div class="server-hint" >Using: ${this.inputServerAddr}</div>
          <br>
          <label for="invite-code">
            Mastopoof invite code
          </label>
          <input id="invite-code" type="text" ${ref(this.inviteCodeRef)} value=""></input>
          <br>
          <button id="do-auth" @click=${this.startLogin}>Sign-in</button>
        </div>
      `;
    }

    return html`
      <div class="authbox">
        <div>
          <a href=${this.authURI}>Mastodon Auth</a>
        </div>
        <div>
          <label for="auth-code">Authorization code</label>
          <input type="text" id="auth-code" ${ref(this.authCodeRef)} required autofocus></input>
          <button @click=${this.doLogin}>Auth</button>
        </div>
      </div>
    `;
  }

  static styles = [common.sharedCSS, css`
    :host {
      display: flex;
      box-sizing: border-box;
      min-height: 100%;
      background-color: var(--color-grey-300);

      flex-direction: column;
      align-items: center;
      justify-content: center;
    }

    .authbox {
      min-width: var(--stream-min-width);
      width: 100%;
      max-width: var(--stream-max-width);

      box-sizing: border-box;
      padding: 10px;

      background-color: var(--color-grey-150);

      display: flex;
      flex-direction: column;
      justify-content: center;
      align-items: center;
    }

    .server-hint {
      font-size: 80%;
      font-style: italic;
    }

    button {
      min-height: 35px;
      min-width: 80px;
    }
  `];
}

declare global {
  interface HTMLElementTagNameMap {
    'mast-login': MastLogin
  }
}
