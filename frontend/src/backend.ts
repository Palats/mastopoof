// Manages the connection from the browser to the Go server.
import { ConnectError, createPromiseClient, PromiseClient, Code } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { Mastopoof } from "mastopoof-proto/gen/mastopoof/mastopoof_connect";
import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";
import * as protobuf from "@bufbuild/protobuf";


// Return a random value around [ref-delta, ref+delta[, where
// delta = ref * deltaRatio.
function fuzzy(ref: number, deltaRatio: number): number {
    return ref + (2 * Math.random() - 1) * (ref * deltaRatio);
}

// Event type when info about the stream/pool is updated.
export class StreamUpdateEvent extends Event {
    prev: StreamInfo = {};
    curr: StreamInfo = {};
}

export class LoginUpdateEvent extends Event {
    state: LoginState = LoginState.LOADING;
    userInfo?: pb.UserInfo;
}

export interface StreamInfo {
    // The last-read status position, from this frontend perspective - i.e.,
    // including local updates which might not have been propagated to the
    // server.
    lastRead?: number;
    // Highest position in the stream.
    lastPosition?: number;
    // Number of status in the pool but not in the stream.
    remaining?: number;
}

export enum LoginState {
    // Currently trying to log-in
    LOADING = 1,
    // Successful login.
    LOGGED = 2,
    // Unsuccessful login.
    NOT_LOGGED = 3,
}

export class Backend {
    private streamInfo: StreamInfo = {};

    // Events:
    //  'stream-update': Fired when last-read is changed. Gives a StreamUpdateEvent event.
    public onEvent: EventTarget;

    private lastReadDirty = false;
    private lastReadQueued = false;

    private client: PromiseClient<typeof Mastopoof>;

    constructor() {
        const transport = createConnectTransport({
            baseUrl: "/_rpc/",
        });

        this.client = createPromiseClient(Mastopoof, transport);
        this.onEvent = new EventTarget();
    }

    // Update the last-read position on the server. It will be rate limited,
    // so not all call might be immediately effective. It will always use the last value.
    public setLastRead(position: number) {
        const old = { ... this.streamInfo };
        this.streamInfo.lastRead = position;
        this.lastReadDirty = true;
        if (!this.lastReadQueued) {
            this.lastReadQueued = true;
            requestIdleCallback(() => this.updateLastRead(), { timeout: fuzzy(1000, 0.1) });
        }
        const evt = new StreamUpdateEvent("stream-update");
        evt.prev = old;
        evt.curr = { ... this.streamInfo };
        this.onEvent.dispatchEvent(evt);
    }

    // Internal method used in rate-limiting updates of last-read marker.
    async updateLastRead() {
        if (!this.lastReadDirty) {
            this.lastReadQueued = false;
            return;
        }

        console.log("last read to", this.streamInfo.lastRead);
        // this.lastRead is always not-undefined at this point, as the method is
        // only called after `setLastRead` which does not allow for it.
        const promise = this.client.setRead({ lastRead: BigInt(this.streamInfo.lastRead!) });
        this.lastReadDirty = false;
        await promise;

        setTimeout(() => this.updateLastRead(), 1000);
    }

    public async fetch(request: protobuf.PartialMessage<pb.FetchRequest>) {
        const resp = await this.client.fetch(request);

        const old = { ... this.streamInfo }
        this.streamInfo.lastRead = Number(resp.lastRead);
        this.streamInfo.lastPosition = Number(resp.lastPosition);
        this.streamInfo.remaining = Number(resp.remainingPool);
        const evt = new StreamUpdateEvent("stream-update");
        evt.prev = old;
        evt.curr = { ... this.streamInfo };
        this.onEvent.dispatchEvent(evt);

        return resp;
    }

    public async login(request: protobuf.PartialMessage<pb.LoginRequest>) {
        const evt = new LoginUpdateEvent("login-update");
        evt.state = LoginState.LOADING;
        this.onEvent.dispatchEvent(evt);

        try {
            const resp = await this.client.login(request);
            const evt = new LoginUpdateEvent("login-update");
            evt.state = LoginState.LOGGED;
            evt.userInfo = resp.userInfo;
            this.onEvent.dispatchEvent(evt);
        } catch (err) {
            const connectErr = ConnectError.from(err);
            if (connectErr.code === Code.PermissionDenied) {
                const evt = new LoginUpdateEvent("login-update");
                evt.state = LoginState.NOT_LOGGED;
                this.onEvent.dispatchEvent(evt);
                return null;
            }
            throw err;
        }
    }

    public async logout() {
        let evt = new LoginUpdateEvent("login-update");
        evt.state = LoginState.LOADING;
        this.onEvent.dispatchEvent(evt);
        await this.client.logout({});
        evt = new LoginUpdateEvent("login-update");
        evt.state = LoginState.NOT_LOGGED;
        this.onEvent.dispatchEvent(evt);
    }

    public async authorize(serverAddr: string): Promise<string> {
        const resp = await this.client.authorize({ serverAddr: serverAddr });
        return resp.authorizeAddr;
    }

    public async token(serverAddr: string, authCode: string): Promise<pb.UserInfo> {
        const resp = await this.client.token({ serverAddr: serverAddr, authCode: authCode });
        if (!resp.userInfo) {
            throw "oops";
        }
        let evt = new LoginUpdateEvent("login-update");
        evt.state = LoginState.LOGGED;
        this.onEvent.dispatchEvent(evt);
        return resp.userInfo;
    }
}