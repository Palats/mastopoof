import { LitElement, css, html } from 'lit'
import { customElement, state } from 'lit/decorators.js'
import { LoginUpdateEvent } from './backend';
import * as common from "./common";
import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";
import { createRef, ref, Ref } from 'lit/directives/ref.js';

@customElement('mast-settings')
export class MastSettings extends LitElement {
  // Currently known settings, incl. modification made through the UI.
  @state() private currentSettings?: pb.Settings;

  private defaultSettings?: pb.Settings;

  private inputRef: Ref<HTMLInputElement> = createRef();
  private checkBoxRef: Ref<HTMLInputElement> = createRef();

  connectedCallback(): void {
    super.connectedCallback();

    this.currentSettings = common.backend.userInfo?.settings;
    this.defaultSettings = common.backend.userInfo?.defaultSettings;

    common.backend.onEvent.addEventListener("login-update", ((evt: LoginUpdateEvent) => {
      this.currentSettings = evt.userInfo?.settings;
    }) as EventListener);
  }

  changeInput() {
    if (!this.defaultSettings || !this.currentSettings) {
      throw new Error("missing settings");
    }

    if (this.checkBoxRef.value) {
      this.checkBoxRef.value.checked = false;
    }
  }

  changeCheckbox() {
    if (!this.defaultSettings || !this.currentSettings) {
      throw new Error("missing settings");
    }
    const checked = this.checkBoxRef.value?.checked;
    if (checked) {
      this.currentSettings.defaultListCount = this.defaultSettings?.defaultListCount;
      this.inputRef.value!.value = this.currentSettings.defaultListCount.toString();
    }
  }

  render() {
    if (!this.defaultSettings || !this.currentSettings) {
      throw new Error("missing settings");
    }

    const isDefault = this.currentSettings.defaultListCount === BigInt(0);
    const actualValue = isDefault ? this.defaultSettings.defaultListCount : this.currentSettings.defaultListCount;

    return html`
      <mast-main-view selectedView="settings">
        <span slot="header">Settings</span>
        <div slot="list">
          <div>
          <label for="s-default-list-count">Number of statuses to fetch when clicking "Get more statuses"</label>
          <input type="number" id="s-default-list-count" value=${actualValue.toString()} @change=${this.changeInput} ${ref(this.inputRef)}></input>
          <label for="s-default-list-count-default">Use default</label>
          <input type="checkbox" id="s-default-list-count-default" ?checked=${isDefault} @change=${this.changeCheckbox} ${ref(this.checkBoxRef)}></input>
          </div>
        </div>
        <div slot="footer" class="centered">
          <button>Save</button>
        </div>
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