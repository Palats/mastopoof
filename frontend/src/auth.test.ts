import { expect } from '@esm-bundle/chai';
import { html, render } from 'lit';
import './auth';

it('basic element construction test', async () => {
  // const elt = document.createElement('mast-login');
  // document.body.appendChild(elt);
  // await expect($(elem)).toHaveText('Hello, WebdriverIO!')
  // elem.remove();

  await render(html`<mast-login></mast-login>`, document.body);
  const elt = document.body.querySelector("mast-login");
  expect(elt!.shadowRoot!.innerHTML).to.contain("Mastodon server");
});