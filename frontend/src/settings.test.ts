import { expect } from '@esm-bundle/chai';
import { html, render } from 'lit';
import * as testlib from './testlib';
import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";
import './settings';

before(() => {
  testlib.setMastopoofConfig();
});

it('basic element construction', async () => {
  const server = new testlib.TestServer();
  server.setAsBackend();

  await render(html`<mast-settings></mast-settings>`, document.body);
  const elt = document.body.querySelector("mast-settings")!.shadowRoot!;
  expect(elt.innerHTML).to.contain("Number of statuses");
});

it('saves override', async () => {
  const server = new testlib.TestServer();
  server.setAsBackend();

  // Render the component.
  await render(html`<mast-settings></mast-settings>`, document.body);

  // Change some setting content.
  const elt = document.body.querySelector("mast-settings")!.shadowRoot!;
  const input = elt.querySelector("#s-list-count-input")! as HTMLInputElement;
  input.setAttribute("value", "12");
  const override = elt.querySelector("#s-list-count-override")! as HTMLInputElement;
  override.checked = true;

  // Trigger the update of the settings.
  const saveButton = elt.querySelector("#save")! as HTMLButtonElement;
  saveButton.click();

  // And verify that the right UpdateSettings is called.
  const req1 = await server.updateSettings.expect();
  expect(req1.req.settings?.listCount?.value).to.eq(BigInt(12));
  expect(req1.req.settings?.listCount?.override).to.eq(true);
  req1.respond(new pb.UpdateSettingsResponse({}));
});