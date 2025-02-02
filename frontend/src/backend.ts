// Manages the connection from the browser to the Go server.
import { ConnectError, createPromiseClient, PromiseClient, Code, Transport } from "@connectrpc/connect";
import { Mastopoof } from "mastopoof-proto/gen/mastopoof/mastopoof_connect";
import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";
import * as settingspb from "mastopoof-proto/gen/mastopoof/settings/settings_pb";
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
  // Events:
  //  'stream-update' -> StreamUpdateEvent: Fired when last-read is changed.
  //  'login-update' -> LoginUpdateEvent.
  public onEvent: EventTarget;

  // Latest user info obtained from the backend. Might be out of date if an update
  // request is in flight or was not reflected here.
  public userInfo?: pb.UserInfo;

  private client: PromiseClient<typeof Mastopoof>;
  private streamInfo?: pb.StreamInfo;

  constructor(transport: Transport) {
    this.client = createPromiseClient(Mastopoof, transport);
    this.onEvent = new EventTarget();
  }

  private dispatchLoginUpdate(state: LoginState, userInfo: pb.UserInfo | undefined) {
    this.userInfo = userInfo;

    const evt = new LoginUpdateEvent("login-update");
    evt.state = state;
    evt.userInfo = userInfo;
    this.onEvent.dispatchEvent(evt);
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
    this.dispatchLoginUpdate(LoginState.LOADING, undefined);

    try {
      const resp = await this.client.login({});
      if (resp.userInfo) {
        this.dispatchLoginUpdate(LoginState.LOGGED, resp.userInfo);
      } else {
        this.dispatchLoginUpdate(LoginState.NOT_LOGGED, undefined);
      }

    } catch (err) {
      const connectErr = ConnectError.from(err);
      if (connectErr.code === Code.PermissionDenied) {
        this.dispatchLoginUpdate(LoginState.NOT_LOGGED, undefined);
        return null;
      }
      throw err;
    }
  }

  public async logout() {
    this.dispatchLoginUpdate(LoginState.LOADING, undefined);
    await this.client.logout({});
    this.dispatchLoginUpdate(LoginState.NOT_LOGGED, undefined);
  }

  public async authorize(serverAddr: string, inviteCode?: string): Promise<pb.AuthorizeResponse> {
    const resp = await this.client.authorize({ serverAddr: serverAddr, inviteCode: inviteCode });
    return resp;
  }

  public async token(serverAddr: string, authCode: string) {
    const resp = await this.client.token({ serverAddr: serverAddr, authCode: authCode });
    if (!resp.userInfo) {
      throw "oops";
    }
    this.dispatchLoginUpdate(LoginState.LOGGED, resp.userInfo);
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

  public async updateSettings(settings: settingspb.Settings): Promise<pb.UpdateSettingsResponse> {
    return await this.client.updateSettings({ settings: settings });
  }
}