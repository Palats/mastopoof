import { LitElement, html } from 'lit'
import { customElement, property } from 'lit/decorators.js'
import { asyncReplace } from 'lit/directives/async-replace.js';
import * as common from "./common";
import dayjs from 'dayjs';

@customElement('time-since')
export class TimeSince extends LitElement {
    @property({ attribute: false })
    unix?: BigInt;

    private fromNow?: AsyncGenerator<string>;

    disconnectedCallback() {
        super.disconnectedCallback();
        this.fromNow?.return("disconnected");
    }

    async* refresh(dt: dayjs.Dayjs) {
        let prev = "";
        while (true) {
            const now = dayjs();
            const s = dt.from(now);
            if (s !== prev) {
                yield s;
                prev = s;
            }
            let delay = 10000;
            if (now.diff(dt, "minute") >= 1) {
                delay = 60000;
            }
            await new Promise((r) => setTimeout(r, delay));
        }
    }

    render() {
        if (!this.unix || this.unix === 0n) {
            return html`<span>never</span>`;
        }
        const dt = dayjs.unix(Number(this.unix));

        this.fromNow?.return("replaced");
        this.fromNow = this.refresh(dt);
        const label = `${common.displayTimezone}: ${dt.tz(common.displayTimezone).format()}\nSource: ${this.unix}`;
        return html`<span title=${label}>${asyncReplace(this.fromNow!)}</span>`;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'time-since': TimeSince
    }
}
