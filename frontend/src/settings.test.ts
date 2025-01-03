import { expect } from '@esm-bundle/chai';
import { html, render } from 'lit';
import * as testlib from './testlib';
import './settings';

before(() => {
  testlib.setMastopoofConfig();
});

it('basic element construction', async () => {
  const server = new testlib.TestServer();
  server.setAsBackend();
  await server.defaultLogin();

  await render(html`<mast-settings></mast-settings>`, document.body);
  const elt = document.body.querySelector("mast-settings");
  expect(elt!.shadowRoot!.innerHTML).to.contain("Number of statuses");
});