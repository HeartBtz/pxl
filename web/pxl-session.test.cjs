'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const {execFileSync} = require('node:child_process');
const {pathToFileURL} = require('node:url');

function load(fetch) {
  const values = new Map([['pxl_token', 'fixture'], ['pxl_refresh', 'fixture']]);
  const context = {window: {}, fetch, localStorage: {
    setItem: (k, v) => values.set(k, v),
    removeItem: k => values.delete(k)
  }};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'static/pxl-session.js'), 'utf8'), context);
  return {api: context.window.PXLSession, values};
}

test('HTML escaping covers both text and quoted attribute contexts', () => {
  const {api, values} = load(() => {throw new Error('unexpected fetch');});
  assert.equal(api.escapeHTML('"\'><&'), '&quot;&#39;&gt;&lt;&amp;');
  assert.equal(api.escapeHTML(0), '0');
  assert.equal(api.escapeHTML(null), '');
  assert.equal(values.has('pxl_token'), false);
  assert.equal(values.has('pxl_refresh'), false);
});

test('concurrent refresh requests consume one cookie once', async () => {
  let calls = 0;
  const {api} = load(async () => {calls++; await new Promise(r => setTimeout(r, 5)); return {ok: true, json: async () => ({user: {username: 'fixture'}})};});
  assert.deepEqual(await Promise.all([api.refresh(), api.refresh(), api.refresh()]), [true, true, true]);
  assert.equal(calls, 1);
});

test('logout failures are not reported as success', async () => {
  const {api} = load(async () => ({ok: false, status: 500}));
  await assert.rejects(api.logout(), /Logout failed/);
});

test('Chromium preserves attribute values without creating injected DOM', {skip: !process.env.PXL_AUDIT_CHROMIUM}, () => {
  const output = execFileSync(process.env.PXL_AUDIT_CHROMIUM, [
    '--headless', '--no-sandbox', '--disable-gpu', '--disable-background-networking',
    '--no-first-run', '--allow-file-access-from-files', '--dump-dom',
    pathToFileURL(path.join(__dirname, 'audit-browser.html')).href
  ], {encoding: 'utf8', timeout: 30000, stdio: ['ignore', 'pipe', 'ignore']});
  assert.equal(output.includes('data-audit="PASS"'), true, 'isolated DOM fixture failed');
});
