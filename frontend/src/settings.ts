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

  @state() loadingBarUsers = 0;

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
    this.checkBoxRef.value!.checked = false;
    this.updateSettings();
  }

  changeCheckbox() {
    if (!this.defaultSettings || !this.currentSettings) {
      throw new Error("missing settings");
    }
    if (this.checkBoxRef.value?.checked) {
      this.inputRef.value!.value = this.defaultSettings.defaultListCount.toString();
    }
    this.updateSettings();
  }

  updateSettings() {
    if (!this.defaultSettings || !this.currentSettings) {
      throw new Error("missing settings");
    }

    if (this.checkBoxRef.value?.checked) {
      this.currentSettings.defaultListCount = BigInt(0);
    } else {
      this.currentSettings.defaultListCount = BigInt(this.inputRef.value!.value);
    }
    this.requestUpdate();
  }

  async save() {
    if (!this.defaultSettings || !this.currentSettings) {
      throw new Error("missing settings");
    }

    this.loadingBarUsers++;
    console.log("settings", this.currentSettings);
    await common.backend.updateSettings(this.currentSettings);
    this.loadingBarUsers--;
  }

  render() {
    if (!this.defaultSettings || !this.currentSettings) {
      throw new Error("missing settings");
    }

    const isDefault = this.currentSettings.defaultListCount == BigInt(0);
    const actualValue = isDefault ? this.defaultSettings.defaultListCount : this.currentSettings.defaultListCount;

    return html`
      <mast-main-view .loadingBarUsers=${this.loadingBarUsers} selectedView="settings">
        <span slot="header">Settings</span>
        <div slot="list" class="list">
          <div>
            Number of statuses to fetch when clicking "Get more statuses"
            <div class="inputs">
              <span>
                Default: ${this.defaultSettings.defaultListCount}
              </span>

              <span>
                <label for="s-default-list-count-default">Override</label>
                <input type="checkbox" id="s-default-list-count-default" ?checked=${isDefault} @change=${this.changeCheckbox} ${ref(this.checkBoxRef)}></input>
                <input type="number" id="s-default-list-count" value=${actualValue.toString()} @change=${this.changeInput} ${ref(this.inputRef)}></input>
              </span>
            </div>
          </div>

          <div>
            Another setting
          </div>
        </div>
        <div slot="footer" class="centered">
          <button @click=${this.save}>Save</button>
        </div>
      </mast-main-view>
    `;
  }
  static styles = [common.sharedCSS, css`
    .list {
      display: flex;
      flex-direction: vertical;
    }

    .list > * {
      margin: 4px;
      margin-bottom: 6px;
      border-width: 1px;
      border-style: solid;
      border-color: var(--color-grey-300);
    }

    .inputs {
      display: flex;
      justify-content: flex-end;
      align-items: center;
      gap: 8px;
    }

    .inputs > * + * {
      border-left: solid 2px var(--color-grey-999);
      padding-left: 8px;
    }

    input {
      margin-left: 2px;
      margin-right: 2px;
    }
  `];
}

declare global {
  interface HTMLElementTagNameMap {
    'mast-settings': MastSettings
  }
}