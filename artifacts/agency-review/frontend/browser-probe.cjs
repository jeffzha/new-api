const { JSDOM } = require('../../../web/node_modules/jsdom');
const { writeFileSync } = require('node:fs');
const { join } = require('node:path');
const base = process.env.AGENCY_AUDIT_URL;
const actor = process.env.AGENCY_AUDIT_ACTOR;
const calls = [];
const checks = [];
let active = 0;
let idleWaiters = [];

function check(name, passed, details) {
  checks.push({ name, status: passed ? 'PASS' : 'FAIL', details });
}

(async () => {
  const html = await (await fetch(base + '/agency/')).text();
  const dom = new JSDOM(html, {
    url: base + '/agency/',
    beforeParse(window) {
      window.document.cookie = 'agency_csrf=' + process.env.AGENCY_AUDIT_CSRF + '; Path=/agency';
      window.alert = (message) => calls.push({ alert: message.replace(/临时密码：[\s\S]*/, '临时密码：[REDACTED]') });
      window.fetch = async (path, options = {}) => {
        active++;
        const url = new URL(path, base);
        const headers = new Headers(options.headers);
        headers.set('Cookie', 'agency_session=' + process.env.AGENCY_AUDIT_SESSION + '; agency_csrf=' + process.env.AGENCY_AUDIT_CSRF);
        headers.set('Origin', base);
        const response = await fetch(url, { ...options, headers });
        const text = await response.text();
        calls.push({ method: options.method || 'GET', path: url.pathname + url.search, status: response.status, body: options.body, response: JSON.parse(text) });
        active--;
        if (active === 0) {
          for (const resolve of idleWaiters) resolve();
          idleWaiters = [];
        }
        return { ok: response.ok, status: response.status, json: async () => JSON.parse(text) };
      };
    },
  });
  const document = dom.window.document;
  // Bun's vm.runInContext cannot use jsdom 29's global Proxy. Execute the
  // unchanged served script with the browser globals supplied explicitly.
  new Function('window', 'document', 'fetch', 'FormData', 'location', 'crypto', 'alert', document.querySelector('script').textContent)(
    dom.window, document, dom.window.fetch, dom.window.FormData, dom.window.location, globalThis.crypto, dom.window.alert,
  );
  // Wait for actual network and DOM work to finish; no timing success assertion.
  async function settled() {
    for (let i = 0; i < 100; i++) {
      if (active > 0) await new Promise(resolve => idleWaiters.push(resolve));
      await new Promise(setImmediate);
      if (active === 0) {
        await new Promise(setImmediate);
        if (active === 0) return;
      }
    }
    throw new Error('Requests did not settle');
  }
  async function click(selector) {
    const element = document.querySelector(selector);
    if (!element) return false;
    element.click();
    await settled();
    return true;
  }
  async function submit(selector, fields) {
    const form = document.querySelector(selector);
    for (const [name, value] of Object.entries(fields)) form.elements.namedItem(name).value = value;
    form.dispatchEvent(new dom.window.Event('submit', { bubbles: true, cancelable: true }));
    await settled();
  }
  await settled();
  check('The server serves the agency UI', document.title === '代理商中心', { title: document.title });
  if (actor === 'first-login') {
    check('First login presents required password-change form', !!document.querySelector('#content input[type=password]'), { content: document.querySelector('#content').textContent, calls });
  } else if (actor === 'root') {
    check('Root overview loads agency and customer totals', !document.querySelector('#content .error'), document.querySelector('#content').textContent);
    await click('[data-tab=customers]');
    check('Root customer tab can list the bound customer', document.querySelector('#content').textContent.includes('audit-customer'), calls.at(-1));
    await click('[data-tab=ledger]');
    check('Root commission ledger has a usable agency context', !document.querySelector('#content .error'), calls.at(-1));
    await click('[data-tab=agencies]');
    check('Root agency list loads', document.querySelector('#content').textContent.includes('Audit agency'), calls.at(-1));
    await click('[data-action=new-agency]');
    await submit('#agency-form', { display_name: 'New audit agency', operator_username: 'new-audit-operator' });
    check('Root create agency form succeeds with verification flow', calls.at(-1).status === 201, calls.at(-1));
    await click('[data-tab=agencies]');
    await click('tr[data-row-id]');
    check('Root selected agency pricing provides editable controls and publish action', !!document.querySelector('#content input'), document.querySelector('#content').textContent);
    check('Root can explicitly enter and leave agency management', calls.some(c => /\/enter$/.test(c.path || '')), calls.filter(c => /\/enter$/.test(c.path || '')));
  } else {
    check('Operator overview loads', !document.querySelector('#content .error'), document.querySelector('#content').textContent);
    await click('[data-tab=customers]');
    check('Operator customer list loads', document.querySelector('#content').textContent.includes('audit-customer'), calls.at(-1));
    document.querySelector('#customer-filter').value = 'nonexistent-customer';
    document.querySelector('#customer-filter').dispatchEvent(new dom.window.Event('input', { bubbles: true }));
    check('Customer filter changes visible results', !document.querySelector('#content').textContent.includes('audit-customer'), document.querySelector('#content').textContent);
    for (const tab of ['usage', 'topups']) {
      await click('[data-tab=' + tab + ']');
      const before = calls.length;
      await click('tr[data-row-id]');
      check('Selecting customer loads ' + tab + ' detail', calls.slice(before).some(c => new RegExp('/customers/\\d+/' + tab).test(c.path || '')), calls.slice(before));
    }
    await click('[data-tab=pricing]');
    check('Operator can edit preview and publish sales pricing', !!document.querySelector('#content input'), document.querySelector('#content').textContent);
    await click('[data-tab=accounts]');
    await click('[data-action=new-account]');
    await submit('#account-form', { account_type: 'bank', account_name: 'Synthetic test account', account_no: '000000001234', bank_name: 'Synthetic bank' });
    check('Operator account creation completes password verification', calls.at(-1).status === 201, calls.at(-1));
    await click('[data-tab=withdrawals]');
    await click('[data-action=new-withdrawal]');
    await submit('#withdrawal-form', { amount_micros: '1000000', account_id: '1' });
    check('Operator withdrawal obtains a verification proof before submission', calls.some(c => c.path === '/agency/api/v1/auth/verify'), calls.at(-1));
  }
  const result = { actor, scope: 'Real served HTML and real Gin API; synthetic local SQLite; jsdom, not visual browser QA', checks, calls, passed: checks.filter(c => c.status === 'PASS').length, failed: checks.filter(c => c.status === 'FAIL').length };
  writeFileSync(join(__dirname, actor + '-results.json'), JSON.stringify(result, null, 2));
  console.log(JSON.stringify({ actor, passed: result.passed, failed: result.failed, checks }, null, 2));
  dom.window.close();
  process.exitCode = result.failed ? 1 : 0;
})().catch(error => { console.error(error); process.exitCode = 2; });
