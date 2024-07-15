import { expect } from '@esm-bundle/chai';
import { html, render } from 'lit';
import './auth';
import * as common from "./common";
import { createRouterTransport } from '@connectrpc/connect';
import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";
import { Mastopoof } from "mastopoof-proto/gen/mastopoof/mastopoof_connect";
import { Backend } from "./backend";
import { waitFor } from '@testing-library/dom'


it('basic element construction test', async () => {
  // const elt = document.createElement('mast-login');
  // document.body.appendChild(elt);
  // await expect($(elem)).toHaveText('Hello, WebdriverIO!')
  // elem.remove();

  await render(html`<mast-login></mast-login>`, document.body);
  const elt = document.body.querySelector("mast-login");
  expect(elt!.shadowRoot!.innerHTML).to.contain("Mastodon server");
});

it('calls authorize', async () => {
  const mockTransport = createRouterTransport(({ service }) => {
    service(Mastopoof, {
      authorize: (req: pb.AuthorizeRequest) => {
        console.log("authorize:", req);
        return new pb.AuthorizeResponse({
          authorizeAddr: "https://authaddr",
        });
      },
    });
  });
  common.setBackend(new Backend(mockTransport));

  await render(html`<mast-login></mast-login>`, document.body);
  const elt = document.body.querySelector("mast-login")!;

  elt.shadowRoot!.querySelector("#server-addr")!.setAttribute("value", "https://fakeserver");

  const button = elt.shadowRoot!.querySelector("#do-auth")! as HTMLButtonElement;
  button.click();
  await waitFor(() => {
    expect(elt!.shadowRoot!.innerHTML).to.contain("Mastodon Auth");
  })
});