// Manages the connection from the browser to the Go server.
import { ConnectError, createPromiseClient, PromiseClient, Code, Transport } from "@connectrpc/connect";
import { Mastopoof } from "mastopoof-proto/gen/mastopoof/mastopoof_connect";
import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";
import * as protobuf from "@bufbuild/protobuf";


// Return a random value around [ref-delta, ref+delta[, where
// delta = ref * deltaRatio.
export function fuzzy(ref: number, deltaRatio: number): number {
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

  private client: PromiseClient<typeof Mastopoof>;

  constructor(transport: Transport) {
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

  // Advance the last-read position on the server if new position is larger.
  public async advanceLastRead(stid: bigint, position: bigint) {
    const resp = await this.client.setRead({ stid: stid, lastRead: position, mode: pb.SetReadRequest_Mode.ADVANCE });
    this.updateStreamInfo(resp.streamInfo);
  }

  // Set last-read to the specified value, even if in the past.
  public async setLastRead(stid: bigint, position: bigint) {
    const resp = await this.client.setRead({ stid: stid, lastRead: position, mode: pb.SetReadRequest_Mode.ABSOLUTE });
    this.updateStreamInfo(resp.streamInfo);
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

  public async authorize(serverAddr: string, inviteCode?: string): Promise<string> {
    const resp = await this.client.authorize({ serverAddr: serverAddr, inviteCode: inviteCode });
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

  // Returns true if finished, false if more statuses are likely available.
  public async fetch(stid: bigint): Promise<boolean> {
    const resp = await this.client.fetch({ stid: stid });
    console.log(`${resp.fetchedCount} statuses fetched (status=${resp.status}).`);
    this.updateStreamInfo(resp.streamInfo);
    return resp.status === pb.FetchResponse_Status.DONE
  }

  public async search(statusID: string): Promise<pb.SearchResponse> {
    return await this.client.search({ statusId: statusID });
  }

  public async setStatus(statusID: string, action: pb.SetStatusRequest_Action): Promise<pb.SetStatusResponse> {
    return await this.client.setStatus({ statusId: statusID, action: action });
  }
}