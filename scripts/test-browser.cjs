#!/usr/bin/env node
'use strict';

// Requires Playwright + its Chromium browser. Native responses are simulated;
// real encrypted WG/AWG and Windows DPAPI/broker tests live in native-helper.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const vm = require('node:vm');
const http = require('node:http');
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');

const root = path.resolve(__dirname, '..');
const extension = path.join(root, 'omega-target-chromium-extension/build');
const id = 'mdplkgbdfoijjimhpdnoehifogbhhcnb';
const secret = 'TEST-ONLY-PRIVATE-CONFIG-MARKER';

async function main() {
  const profile = await fs.mkdtemp(path.join(os.tmpdir(), 'zeroomega-browser-test-'));
  let context;
  const requests = [];
  const server = http.createServer((request, response) => {
    requests.push(request.headers.host);
    response.end('direct connection works');
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  try {
    context = await chromium.launchPersistentContext(profile, {
      channel: 'chromium', headless: true, locale: 'en-US',
      ignoreDefaultArgs: ['--disable-extensions'],
      args: ['--disable-background-networking',
        '--host-resolver-rules=MAP *.test 127.0.0.1',
        `--disable-extensions-except=${extension}`, `--load-extension=${extension}`],
    });
    const worker = context.serviceWorkers()[0] || await context.waitForEvent('serviceworker');
    assert.match(worker.url(), new RegExp(id));
    const page = await context.newPage();
    const pageErrors = [];
    page.on('pageerror', error => pageErrors.push(error.message));
    await page.goto(`chrome-extension://${id}/options.html`);
    await page.locator('.nav-new-profile a').waitFor();
    const welcome = page.locator('[ng-click=\'$close("skip")\']');
    if (await welcome.isVisible()) await welcome.click();
    await page.locator('.nav-new-profile a').click();
    await page.locator('#profile-new-name').fill('WG browser test');
    await page.locator('input[value="TunnelProfile"]').check();
    await page.locator('.modal-footer button[type="submit"]').click();
    await page.locator('#tunnel-config-text').waitFor();
    await page.locator('[ng-if="tunnelEditor.helperUnavailable"]').waitFor();
    assert.match(await page.locator('body').innerText(), /install\.ps1/);
    console.log('PASS: real extension starts; new profile and missing-helper guidance render.');

    // Exercise real UI -> background RPC -> native client, replacing only the
    // unavailable OS native host with deterministic responses in this test.
    await worker.evaluate(() => {
      let state = 'disconnected';
      chrome.runtime.connectNative = () => {
        const messages = [], disconnects = [];
        const port = {
          onMessage: {addListener: listener => messages.push(listener)},
          onDisconnect: {addListener: listener => disconnects.push(listener)},
          postMessage(request) {
            let result;
            const metadata = {configId: '0123456789abcdef0123456789abcdef',
              protocol: 'wireguard', port: 24123};
            if (request.command === 'version') result = {protocolVersion: 1, version: 'test'};
            else {
              if (request.command === 'connect') state = 'connected';
              if (request.command === 'disconnect' || request.command === 'import') state = 'disconnected';
              result = {...metadata, state};
              // Deliberately over-broad helper response tests the UI whitelist.
              if (request.command === 'import') result.config = request.config;
            }
            setTimeout(() => messages.forEach(listener => listener({id: request.id, ok: true, result})), 0);
          },
          disconnect() { disconnects.forEach(listener => listener()); },
        };
        return port;
      };
    });
    await page.locator('[ng-click="tunnelRefresh()"]').click();
    await page.locator('[ng-if="tunnelEditor.helperUnavailable"]').waitFor({state: 'hidden'});
    await page.locator('#tunnel-config-text').fill(`[Interface]\n# ${secret}\n`);
    await page.locator('[ng-click="tunnelImport()"]').click();
    await page.waitForFunction(() => document.querySelector('#tunnel-config-text').value === '');
    const metadata = await page.evaluate(() => angular.element(document.documentElement)
      .injector().get('$rootScope').options['+WG browser test'].tunnel);
    assert.deepEqual(Object.keys(metadata).sort(), ['configId', 'port', 'protocol']);
    console.log('PASS: import clears pasted text and saves only whitelisted metadata.');

    await page.evaluate(() => {
      const root = angular.element(document.documentElement).injector().get('$rootScope');
      root.$apply(() => {
        root.options['+auto switch'].rules = [{condition: {
          conditionType: 'HostWildcardCondition', pattern: '*.selected.test'},
          profileName: 'WG browser test'}];
        root.options['+auto switch'].defaultProfileName = 'direct';
      });
    });
    await page.locator('[ng-click="applyOptions()"]').click();
    await page.waitForFunction(() => !angular.element(document.documentElement)
      .injector().get('$rootScope').optionsDirty);
    await page.locator('[ng-click="tunnelConnect()"]').click();
    await page.waitForFunction(() => angular.element(document.querySelector('[ng-controller="TunnelProfileCtrl"]'))
      .scope().tunnelEditor.state === 'connected');
    await page.evaluate(() => angular.element(document.documentElement).injector()
      .get('omegaTarget').applyProfile('auto switch'));

    async function route(host) {
      const config = await worker.evaluate(() => chrome.proxy.settings.get({}));
      assert.equal(config.value.mode, 'pac_script');
      assert.equal(config.value.pacScript.mandatory, true);
      const sandbox = {};
      vm.runInNewContext(config.value.pacScript.data, sandbox);
      return sandbox.FindProxyForURL(`https://${host}/`, host);
    }
    assert.equal(await route('www.selected.test'), 'SOCKS5 127.0.0.1:24123');
    assert.equal(await route('unselected.test'), 'DIRECT');
    console.log('PASS: actual Chrome proxy policy routes selected domains, keeps others direct.');

    const screenshotDir = process.env.SCREENSHOT_DIR;
    if (screenshotDir) {
      await fs.mkdir(screenshotDir, {recursive: true});
      for (const [label, width, height] of [['desktop', 1366, 1000], ['narrow', 768, 1000]]) {
        await page.setViewportSize({width, height});
        await page.screenshot({path: path.join(screenshotDir, `tunnel-${label}.png`), fullPage: true});
      }
    }
    await page.locator('[ng-click="tunnelDisconnect()"]').click();
    await page.waitForFunction(() => angular.element(document.querySelector('[ng-controller="TunnelProfileCtrl"]'))
      .scope().tunnelEditor.state === 'disconnected');
    assert.equal(await route('www.selected.test'), 'SOCKS5 127.0.0.1:9');
    assert.equal(await route('unselected.test'), 'DIRECT');
    const networkPage = await context.newPage();
    const port = server.address().port;
    await networkPage.goto(`http://unselected.test:${port}/`);
    assert.equal(await networkPage.locator('body').innerText(), 'direct connection works');
    await assert.rejects(networkPage.goto(`http://www.selected.test:${port}/`),
      /ERR_(SOCKS|PROXY|NO_SUPPORTED_PROXIES)/);
    assert.ok(!requests.some(host => host.startsWith('www.selected.test:')));
    const stored = await worker.evaluate(() => chrome.storage.local.get(null));
    assert.ok(!JSON.stringify(stored).includes(secret));
    const options = await page.evaluate(() => angular.element(document.documentElement)
      .injector().get('$rootScope').options);
    assert.ok(!JSON.stringify(options).includes(secret));
    assert.deepEqual(pageErrors, []);
    console.log('PASS: disconnect blocks selected requests; options/storage contain no imported config.');
  } finally {
    if (context) await context.close();
    await new Promise(resolve => server.close(resolve));
    await fs.rm(profile, {recursive: true, force: true});
  }
}

main().catch(error => { console.error(error); process.exitCode = 1; });
