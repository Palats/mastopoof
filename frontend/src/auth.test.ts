import { expect, test, beforeAll } from 'vitest';
import { page } from '@vitest/browser/context'
import { html, render } from 'lit';
import './auth';
import { Code, ConnectError } from '@connectrpc/connect';
import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";
import * as testlib from './testlib';
import * as protobuf from '@bufbuild/protobuf';

beforeAll(() => {
  testlib.setMastopoofConfig();
});

test('basic element construction test', async () => {
  await render(html`<mast-login></mast-login>`, document.body);
  const elt = document.body.querySelector("mast-login");
  expect(elt!.shadowRoot!.innerHTML).to.contain("Mastodon server");
});

test('calls authorize', async () => {
  const server = new testlib.TestServer();
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
  await auth2Req.respond(protobuf.create(pb.AuthorizeResponseSchema, {
    authorizeAddr: "https://authaddr",
    // This is absolutely needed - otherwise that generates a redirect,
    // which makes the test continue forever.
    outOfBand: true,
  }));

  // UI should now display how to get to the Mastodon auth.
  // Maybe it will be redirect at some point, but not for now.
  await expect.element(page.getByText('Get authorization')).toBeInTheDocument();

  // At this point, user will go to the Mastodon oauth flow
  // and be redirected to a backend URL - no visible from the UI.
});