import './auth';
import * as common from "./common";
import { createRouterTransport } from '@connectrpc/connect';
import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";
import { Mastopoof } from "mastopoof-proto/gen/mastopoof/mastopoof_connect";
import { Backend } from "./backend";
import { Message } from '@bufbuild/protobuf';

export function setMastopoofConfig() {
  globalThis.mastopoofConfig = {
    src: "",
    defServer: "",
    invite: true,
  };
}

export type RPCReqInfo<ReqT, RespT> = {
  req: ReqT;
  respond: (resp: RespT) => void;
  fail: (err: Error) => void;
}

export class RPCIntercept<ReqT extends Message, RespT extends Message> {
  private reqResolve?: (nfo: RPCReqInfo<ReqT, RespT>) => void;

  async expect(): Promise<RPCReqInfo<ReqT, RespT>> {
    return await new Promise<RPCReqInfo<ReqT, RespT>>(resolve => {
      this.reqResolve = resolve;
    });
  }

  async dispatch(req: ReqT) {
    return await new Promise<RespT>((resolve, reject) => {
      if (!this.reqResolve) {
        const msg = `RPC request received, but nothing is expecting it; request=${req.toJsonString()}`;
        console.error(msg);
        throw new Error(msg);
      }
      this.reqResolve({
        req: req,
        respond: resolve,
        fail: (err: Error) => {
          reject(err);
        },
      });
    });
  }
}

// TestServer implements a fake mastopoof backend.
// It allows to verify that the right RPCs are sent and to provide the
// expected answers it would send back.
//
// To verify that a call is made:
//   - Initiate whatever is expected to send the call.
//   - Wait for the call; example:
//        const req1 = testServer.authorize.expect();
//   - Verify the request is as expected; example:
//        expect(req1.req.serverAddr).to.eq("https://fakeserver1");
//   - Send back the answer; example:
//        req1.respond(new pb.AuthorizeResponse({
//          authorizeAddr: "https://authaddr",
//        }));
//   - Or fail it; example:
//        req1.fail(new ConnectError("nopnop", Code.PermissionDenied));
export class TestServer {
  login = new RPCIntercept<pb.LoginRequest, pb.LoginResponse>();
  authorize = new RPCIntercept<pb.AuthorizeRequest, pb.AuthorizeResponse>();

  private backend: Backend;

  constructor() {
    const transport = createRouterTransport(({ service }) => {
      service(Mastopoof, {
        login: req => this.login.dispatch(req),
        authorize: req => this.authorize.dispatch(req),
      });
    });

    this.backend = new Backend(transport);
    common.setBackend(new Backend(transport));
  }

  // Register this test server as the backend for Mastopoof.
  setAsBackend() {
    common.setBackend(this.backend);
  }

  // Trigger a default login request, so some initialization is done.
  async defaultLogin() {
    const login = common.backend.login();
    const reqLogin = await this.login.expect();
    reqLogin.respond(new pb.LoginResponse({
      userInfo: new pb.UserInfo({
        defaultSettings: new pb.Settings({
          listCount: new pb.SettingInt64({ value: BigInt(10) }),
        }),
      }),
    }));
    await login;
  }
}

