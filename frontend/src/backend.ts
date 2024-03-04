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
    prev?: pb.StreamInfo;
    curr?: pb.StreamInfo;
}

export class LoginUpdateEvent extends Event {
    state: LoginState = LoginState.LOADING;
    userInfo?: pb.UserInfo;
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
    private streamInfo?: pb.StreamInfo;

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

    private updateStreamInfo(streamInfo?: pb.StreamInfo) {
        if (!streamInfo) { return; }
        const evt = new StreamUpdateEvent("stream-update");
        evt.prev = this.streamInfo;
        evt.curr = streamInfo;
        this.streamInfo = streamInfo;
        this.onEvent.dispatchEvent(evt);
    }

    // Update the last-read position on the server. It will be rate limited,
    // so not all call might be immediately effective. It will always use the last value.
    public setLastRead(stid: number, position: number) {
        const old = this.streamInfo;
        this.streamInfo = old ? old.clone() : new pb.StreamInfo();

        this.streamInfo.lastRead = BigInt(position);
        this.streamInfo.stid = BigInt(stid);
        this.lastReadDirty = true;
        if (!this.lastReadQueued) {
            this.lastReadQueued = true;
            requestIdleCallback(() => this.updateLastRead(), { timeout: fuzzy(1000, 0.1) });
        }
        const evt = new StreamUpdateEvent("stream-update");
        evt.prev = old;
        evt.curr = this.streamInfo;
        this.onEvent.dispatchEvent(evt);
    }

    // Internal method used in rate-limiting updates of last-read marker.
    async updateLastRead() {
        if (!this.lastReadDirty) {
            this.lastReadQueued = false;
            return;
        }
        if (!this.streamInfo) {
            console.error("missing stream info");
            return;
        }

        console.log("last read to", this.streamInfo.lastRead);
        // this.lastRead is always not-undefined at this point, as the method is
        // only called after `setLastRead` which does not allow for it.
        const promise = this.client.setRead({ stid: BigInt(this.streamInfo.stid!), lastRead: BigInt(this.streamInfo.lastRead!) });
        this.lastReadDirty = false;
        const resp = await promise;
        this.updateStreamInfo(resp.streamInfo);

        setTimeout(() => this.updateLastRead(), 1000);
    }

    public async list(request: protobuf.PartialMessage<pb.ListRequest>) {
        const resp = await this.client.list(request);
        this.updateStreamInfo(resp.streamInfo);
        return resp;
    }

    public async login() {
        const evt = new LoginUpdateEvent("login-update");
        evt.state = LoginState.LOADING;
        this.onEvent.dispatchEvent(evt);

        try {
            const resp = await this.client.login({});
            if (resp.userInfo) {
                const evt = new LoginUpdateEvent("login-update");
                evt.state = LoginState.LOGGED;
                evt.userInfo = resp.userInfo;
                this.onEvent.dispatchEvent(evt);
            } else {
                const evt = new LoginUpdateEvent("login-update");
                evt.state = LoginState.NOT_LOGGED;
                this.onEvent.dispatchEvent(evt);
            }

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

    public async token(serverAddr: string, authCode: string) {
        const resp = await this.client.token({ serverAddr: serverAddr, authCode: authCode });
        if (!resp.userInfo) {
            throw "oops";
        }
        let evt = new LoginUpdateEvent("login-update");
        evt.state = LoginState.LOGGED;
        evt.userInfo = resp.userInfo;
        this.onEvent.dispatchEvent(evt);
    }

    public async fetch(stid: number) {
        const resp = await this.client.fetch({ stid: BigInt(stid) });
        this.updateStreamInfo(resp.streamInfo);
        return resp;
    }
}