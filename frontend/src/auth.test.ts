import { expect } from '@esm-bundle/chai';
import { html, render } from 'lit';
import './auth';
import * as common from "./common";
import { Code, ConnectError, createRouterTransport } from '@connectrpc/connect';
import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";
import { Mastopoof } from "mastopoof-proto/gen/mastopoof/mastopoof_connect";
import { Backend } from "./backend";
import { waitFor } from '@testing-library/dom'
import { Message } from '@bufbuild/protobuf';


it('basic element construction test', async () => {
  // const elt = document.createElement('mast-login');
  // document.body.appendChild(elt);
  // await expect($(elem)).toHaveText('Hello, WebdriverIO!')
  // elem.remove();

  await render(html`<mast-login></mast-login>`, document.body);
  const elt = document.body.querySelector("mast-login");
  expect(elt!.shadowRoot!.innerHTML).to.contain("Mastodon server");
});

type RPCReqInfo<ReqT, RespT> = {
  req: ReqT;
  respond: (resp: RespT) => void;
  fail: (err: Error) => void;
}

class RPCIntercept<ReqT extends Message, RespT extends Message> {
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

class TestServer {
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

  setAsBackend() {
    common.setBackend(this.backend);
  }
}

it('calls authorize', async () => {
  const server = new TestServer();
  server.setAsBackend();

  await render(html`<mast-login></mast-login>`, document.body);
  const root = document.body.querySelector("mast-login")!;
  const elt = root.shadowRoot!;

  // Set the target server and invite code.
  // First we start with an invalid invite code and change it afterward.
  elt.querySelector("#server-addr")!.setAttribute("value", "https://fakeserver1");
  elt.querySelector("#invite-code")!.setAttribute("value", "invalid invite");

  // Ask for authentication.
  const button = elt.querySelector("#do-auth")! as HTMLButtonElement;
  button.click();

  // Mimick a failed code response.
  // The RPC returns a Permission Denied error.
  const authReq1 = await server.authorize.expect();
  expect(authReq1.req.serverAddr).to.eq("https://fakeserver1");
  authReq1.fail(new ConnectError("nopnop", Code.PermissionDenied));

  // Try again with correct invite code.
  elt.querySelector("#invite-code")!.setAttribute("value", "invite1");
  // Ask for authentication again.
  button.click();

  const auth2Req = await server.authorize.expect();
  expect(auth2Req.req.serverAddr).to.eq("https://fakeserver1");
  auth2Req.respond(new pb.AuthorizeResponse({
    authorizeAddr: "https://authaddr",
  }));

  // UI should now display how to get to the Mastodon auth.
  // Maybe it will be redirect at some point, but not for now.
  await waitFor(() => {
    expect(elt.innerHTML).to.contain("https://fakeserver1");
  });

  // At this point, user will go to the Mastodon oauth flow
  // and be redirected to a backend URL - no visible from the UI.
});