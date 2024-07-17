import { expect } from '@esm-bundle/chai';
import { html, render } from 'lit';
import './auth';
import * as common from "./common";
import { createRouterTransport } from '@connectrpc/connect';
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
}

class RPCIntercept<ReqT extends Message, RespT extends Message> {
  private reqResolve?: (nfo: RPCReqInfo<ReqT, RespT>) => void;

  async expect(): Promise<RPCReqInfo<ReqT, RespT>> {
    return await new Promise<RPCReqInfo<ReqT, RespT>>(resolve => {
      this.reqResolve = resolve;
    });
  }

  async dispatch(req: ReqT) {
    return await new Promise<RespT>(resolve => {
      if (!this.reqResolve) {
        const msg = `RPC request received, but nothing is expecting it; request=${req.toJsonString()}`;
        console.error(msg);
        throw new Error(msg);
      }
      this.reqResolve({ req: req, respond: resolve });
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

  elt.querySelector("#server-addr")!.setAttribute("value", "https://fakeserver1");
  elt.querySelector("#invite-code")!.setAttribute("value", "invite1");

  const button = elt.querySelector("#do-auth")! as HTMLButtonElement;
  button.click();

  // Check the authorize request
  const authReq = await server.authorize.expect();
  expect(authReq.req.serverAddr).to.eq("https://fakeserver1");
  authReq.respond(new pb.AuthorizeResponse({
    authorizeAddr: "https://authaddr",
  }));

  await waitFor(() => {
    expect(elt.innerHTML).to.contain("Mastodon Auth");
  })
});