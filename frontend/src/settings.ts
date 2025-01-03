import { LitElement, css, html } from 'lit'
import { customElement, state } from 'lit/decorators.js'
import { LoginUpdateEvent } from './backend';
import * as common from "./common";
import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";
import { createRef, ref, Ref } from 'lit/directives/ref.js';
import { ifDefined } from 'lit/directives/if-defined.js';

@customElement('mast-settings')
export class MastSettings extends LitElement {
  // Currently known settings, incl. modification made through the UI.
  @state() private currentSettings: pb.Settings = new pb.Settings();

  @state() loadingBarUsers = 0;

  private defaultSettings?: pb.Settings;

  private listCountInputRef: Ref<HTMLInputElement> = createRef();
  private listCountCheckBoxRef: Ref<HTMLInputElement> = createRef();

  connectedCallback(): void {
    super.connectedCallback();

    this.userInfoUpdate(common.backend.userInfo);

    common.backend.onEvent.addEventListener("login-update", ((evt: LoginUpdateEvent) => {
      this.userInfoUpdate(evt.userInfo);
    }) as EventListener);
  }

  userInfoUpdate(userInfo?: pb.UserInfo) {
    // TODO: this can update the values while the user is editing, which
    // is a terrible experience.
    this.currentSettings = userInfo?.settings ?? this.currentSettings;
    this.defaultSettings = userInfo?.defaultSettings;
  }

  // Update currentSettings with the content of the UI.
  updateCurrentSettings() {
    if (!this.defaultSettings) {
      throw new Error("missing default settings");
    }

    this.currentSettings.listCount = new pb.SettingInt64({
      value: BigInt(this.listCountInputRef.value?.value || this.defaultSettings.listCount!.value),
      override: this.listCountCheckBoxRef.value?.checked || false,
    });
    this.requestUpdate();
  }

  async save() {
    if (!this.defaultSettings) {
      throw new Error("missing settings");
    }
    this.updateCurrentSettings();

    this.loadingBarUsers++;
    await common.backend.updateSettings(this.currentSettings);
    this.loadingBarUsers--;
  }

  render() {
    if (!this.defaultSettings) {
      throw new Error("missing settings");
    }

    return html`
      <mast-main-view .loadingBarUsers=${this.loadingBarUsers} selectedView="settings">
        <span slot="header">Settings</span>
        <div slot="list" class="list">
          <div>
            Number of statuses to fetch when clicking "Get more statuses"
            <div class="inputs">
              <span>
                Default: ${this.defaultSettings.listCount!.value}
              </span>
              <span>
                <label for="s-default-list-count-default">Override</label>
                <input
                  type="checkbox"
                  id="s-default-list-count-default"
                  ?checked=${this.currentSettings?.listCount?.override}
                  @change=${this.updateCurrentSettings}
                  ${ref(this.listCountCheckBoxRef)}>
                </input>
                <input
                  type="number"
                  id="s-default-list-count"
                  value=${ifDefined(this.currentSettings?.listCount?.value.toString())}
                  @change=${this.updateCurrentSettings}
                  ${ref(this.listCountInputRef)}>
                </input>
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

      display: flex;
      align-items: center;
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