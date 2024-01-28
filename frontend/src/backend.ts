// Manages the connection from the browser to the Go server.
import { createPromiseClient, PromiseClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { Mastopoof } from "mastopoof-proto/gen/mastopoof/mastopoof_connect";
import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";
import * as protobuf from "@bufbuild/protobuf";


// Return a random value around [ref-delta, ref+delta[, where
// delta = ref * deltaRatio.
function fuzzy(ref: number, deltaRatio: number): number {
    return ref + (2 * Math.random() - 1) * (ref * deltaRatio);
}

// Event type for last-read events - i.e., when last-read is updated.
export class LastReadEvent extends Event {
    oldPosition?: number;
    newPosition?: number;
}

export class Backend {
    // The last-read status position, from this frontend perspective - i.e.,
    // including local updates which might not have been propagated to the
    // server.
    private lastRead?: number;

    // Fired when last-read is changed. Gives a LastReadEvent event.
    public onLastRead: EventTarget;

    private lastReadDirty = false;
    private lastReadQueued = false;

    // Position of the last status marked as read.
    // From server info.
    private serverLastRead?: number;

    private client: PromiseClient<typeof Mastopoof>;

    constructor() {
        const transport = createConnectTransport({
            baseUrl: "/_rpc/",
        });

        this.client = createPromiseClient(Mastopoof, transport);
        this.onLastRead = new EventTarget();
    }

    // Update the last-read position on the server. It will be rate limited,
    // so not all call might be immediately effective. It will always use the last value.
    public setLastRead(position: number) {
        const old = this.lastRead;
        this.lastRead = position;
        this.lastReadDirty = true;
        if (!this.lastReadQueued) {
            this.lastReadQueued = true;
            requestIdleCallback(() => this.updateLastRead(), { timeout: fuzzy(1000, 0.1) });
        }
        const evt = new LastReadEvent("last-read");
        evt.oldPosition = old;
        evt.newPosition = this.lastRead;
        this.onLastRead.dispatchEvent(evt);
    }

    // Internal method used in rate-limiting updates of last-read marker.
    async updateLastRead() {
        if (!this.lastReadDirty) {
            this.lastReadQueued = false;
            return;
        }

        console.log("last read to", this.lastRead);
        // this.lastRead is always not-undefined at this point, as the method is
        // only called after `setLastRead` which does not allow for it.
        const promise = this.client.setRead({ lastRead: BigInt(this.lastRead!) });
        this.lastReadDirty = false;
        await promise;

        setTimeout(() => this.updateLastRead(), 1000);
    }

    public async fetch(request: protobuf.PartialMessage<pb.FetchRequest>) {
        const resp = await this.client.fetch(request);
        this.serverLastRead = Number(resp.lastRead);
        if (this.lastRead === undefined) {
            this.lastRead = this.serverLastRead;

            const evt = new LastReadEvent("last-read");
            evt.oldPosition = undefined;
            evt.newPosition = this.lastRead;
            this.onLastRead.dispatchEvent(evt);

        }
        return resp;
    }
}