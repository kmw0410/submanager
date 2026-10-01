// Dependency-free decisions/markup regression tests. This is not a browser layout test.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../web/app.js'), 'utf8');
const nodes = new Map();
const quickButtons = new Map();
const focusCalls = [];
const notices = [];
let activeElement;
function lookup(selector) {
  const quick = selector.match(/^\[data-quick-skip="(\d+)"\]$/);
  return quick ? quickButtons.get(Number(quick[1])) || null : node(selector);
}
function makeNode(selector) {
  let html = '';
  const attributes = new Map();
  return {
    hidden: true, textContent: '', isConnected: true, disabled: false,
    tagName: 'DIV', id: selector.startsWith('#') ? selector.slice(1) : '',
    classList: { add() {}, remove() {}, toggle() {} },
    addEventListener() {}, querySelector: lookup, querySelectorAll: () => [],
    setAttribute(name, value) { attributes.set(name, value); },
    removeAttribute(name) { attributes.delete(name); },
    hasAttribute(name) { return attributes.has(name); },
    getAttribute(name) { return attributes.get(name); },
    focus() { activeElement = this; focusCalls.push(selector); },
    contains(element) { return !!element?.inMain && element.isConnected; },
    append(element) { notices.push(element.textContent); }, remove() {},
    get innerHTML() { return html; },
    set innerHTML(value) {
      html = value;
      if (selector !== '#subscriptionResults') return;
      for (const button of quickButtons.values()) button.isConnected = false;
      quickButtons.clear();
      for (const match of value.matchAll(/<button([^>]*data-quick-skip="(\d+)"[^>]*)>/g)) {
        const id = Number(match[2]);
        const button = makeNode(`[data-quick-skip="${id}"]`);
        button.id = '';
        button.tagName = 'BUTTON'; button.inMain = true;
        button.dataset = { quickSkip: String(id) };
        button.setAttribute('data-quick-skip', String(id));
        button.disabled = /\bdisabled\b/.test(match[1]);
        if (match[1].includes('aria-busy="true"')) button.setAttribute('aria-busy', 'true');
        quickButtons.set(id, button);
      }
    },
  };
}
function node(selector) {
  if (!nodes.has(selector)) nodes.set(selector, makeNode(selector));
  return nodes.get(selector);
}
const baseState = () => ({ user: { Currency: 'KRW' }, stats: { ActiveCount: 0, UpcomingCount: 0, currencies: [], months: [] }, subscriptions: [], currencies: [], services: [], paymentMethods: [] });
class FixedDate extends Date {
  constructor(...args) { super(...(args.length ? args : ['2026-10-01T12:00:00'])); }
}
const context = vm.createContext({
  window: { __INITIAL_STATE__: baseState(), navigator: {}, addEventListener() {}, scrollX: 0, scrollY: 500, scrollTo(x, y) { this.scrollX = x; this.scrollY = y; } },
  document: { querySelector: lookup, querySelectorAll: () => [], addEventListener() {}, documentElement: { dataset: {} },
    get activeElement() { return activeElement; }, createElement: (tag) => makeNode(tag) },
  navigator: {}, matchMedia: () => ({ matches: true, addEventListener() {} }), Date: FixedDate,
  CSS: { escape: (text) => text }, setTimeout() {}, console,
});
vm.runInContext(source.replace(/\}\)\(\);\s*$/, `globalThis.listTest = {
  replaceState, filteredSubscriptions, canQuickSkip, subscriptionList, renderSubscriptionResults, quickSkipSubscription,
  subscriptionDraftErrors, subscriptionBillingPreview,
  setView(view) { currentView = view; },
  isPending(id) { return quickSkipPending.has(id); },
  getFilters() { return [subscriptionQuery, subscriptionCategory, subscriptionStatus, subscriptionSort]; },
  setFilters(query, category, status, sort) { subscriptionQuery = query; subscriptionCategory = category; subscriptionStatus = status; subscriptionSort = sort; }
};})();`), context);
const api = context.listTest;
const subscription = (id, overrides = {}) => ({
  id, ServiceName: `Service ${id}`, Status: 'active', Category: '영상', Memo: '', PaymentMethodName: '카드',
  amount: 100, Currency: 'KRW', BillingCycle: 'monthly', BillingDay: 10, BillingDate: '2026-01-10',
  NextPayment: '2026-10-10', IsTrial: false, Skipped: false, ...overrides,
});
const fixture = [
  subscription(1, { ServiceName: 'Zulu', amount: 300 }),
  subscription(2, { ServiceName: 'Alpha', IsTrial: true, TrialEndsAt: '2026-11-10', BillingDate: '2026-11-10', NextPayment: '2026-11-10' }),
  subscription(3, { ServiceName: 'Skipped', Skipped: true, Category: 'AI', Currency: 'USD', amount: 500 }),
  subscription(4, { ServiceName: 'Cancelled', Status: 'cancelled', CancelledAt: '2026-09-01' }),
  subscription(5, { ServiceName: 'Annual', BillingCycle: 'yearly', BillingDate: '2026-12-10', NextPayment: '2026-12-10', Currency: 'USD', amount: 100 }),
];
api.replaceState({ ...baseState(), subscriptions: fixture });
const ids = () => Array.from(api.filteredSubscriptions(), (item) => item.id);
api.setFilters('', '', 'active', 'name');
assert.deepEqual(ids(), [2, 5, 3, 1], 'active/name sorting');
api.setFilters('', '', 'trial', 'next'); assert.deepEqual(ids(), [2]);
api.setFilters('', '', 'skipped', 'next'); assert.deepEqual(ids(), [3]);
api.setFilters('', '', 'cancelled', 'next'); assert.deepEqual(ids(), [4]);
api.setFilters('ＳＫＩＰＰＥＤ', 'AI', 'active', 'name'); assert.deepEqual(ids(), [3], 'NFKC search and category remain combined');
api.setFilters('', '', 'active', 'amount');
assert.deepEqual(ids(), [1, 2, 3, 5], 'amount order must group currencies before comparing amounts');
api.setFilters('', '', 'active', 'next');
assert.deepEqual(ids(), [1, 2, 3, 5], 'skipped rows sort by actual following payment');
assert.equal(api.canQuickSkip(fixture[0]), true);
assert.equal(api.canQuickSkip(fixture[1]), false, 'future trial has no current occurrence');
assert.equal(api.canQuickSkip(fixture[2]), true, 'existing skip can be restored');
assert.equal(api.canQuickSkip(fixture[3]), false, 'cancelled subscription has no action');
assert.equal(api.canQuickSkip(fixture[4]), false, 'annual subscription outside billing month has no action');
assert.equal(api.canQuickSkip(subscription(6, { BillingDay: 31, BillingDate: '2026-10-31' })), true);
assert.equal(api.canQuickSkip(subscription(7, { BillingDate: '2026-11-10' })), false, 'future start has no current occurrence');
const management = api.subscriptionList(fixture, { management: true });
let buttonDepth = 0;
for (const tag of management.match(/<\/?button\b[^>]*>/g) || []) {
  if (tag.startsWith('</')) buttonDepth--;
  else { assert.equal(buttonDepth, 0, 'quick action must not nest inside an edit button'); buttonDepth++; }
}
assert.equal(buttonDepth, 0);
assert.match(management, /data-quick-skip="1"/);
assert.match(management, /data-quick-skip="3"/);
for (const id of [2, 4, 5]) assert.ok(!management.includes(`data-quick-skip="${id}"`));
assert.ok(!api.subscriptionList(fixture).includes('data-quick-skip'), 'dashboard remains ordinary edit rows');
const editorCurrencies = [{ code: 'KRW', digits: 0, archived: false }, { code: 'USD', digits: 2, archived: false }, { code: 'GBP', digits: 2, archived: true }];
const editorMethods = [{ id: 1, Archived: false }, { id: 2, Archived: true }];
const editorDraft = { serviceName: 'Example', amount: '12.34', currency: 'USD', billingCycle: 'monthly', billingDate: '2026-01-31', paymentMethodId: '2', isTrial: false, trialEndsAt: '' };
assert.deepEqual(Object.keys(api.subscriptionDraftErrors(editorDraft, editorCurrencies, editorMethods, 2)), [], 'retained archived method remains valid');
assert.ok(api.subscriptionDraftErrors(editorDraft, editorCurrencies, editorMethods, null).paymentMethodId, 'archived method cannot be newly assigned');
assert.ok(api.subscriptionDraftErrors({ ...editorDraft, currency: 'GBP' }, editorCurrencies, editorMethods, 2).currency, 'archived currency requires an explicit replacement');
assert.ok(api.subscriptionDraftErrors({ ...editorDraft, billingDate: '2026-02-30' }, editorCurrencies, editorMethods, 2).billingDate, 'invalid calendar date is rejected');
assert.ok(api.subscriptionDraftErrors({ ...editorDraft, isTrial: true, trialEndsAt: '2026-02-01' }, editorCurrencies, editorMethods, 2).billingDate, 'first billing date must follow trial end');
assert.ok(api.subscriptionDraftErrors({ ...editorDraft, amount: '12.345' }, editorCurrencies, editorMethods, 2).amount, 'currency precision is checked');
assert.match(api.subscriptionBillingPreview(editorDraft).rule, /월말로 조정/, 'month-end rule is disclosed');
function deferred() {
  let resolve, reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

async function verifyPendingReplacement(failureStage) {
  api.replaceState({ ...baseState(), subscriptions: fixture });
  api.setView('subscriptions');
  api.setFilters('Zulu', '영상', 'active', 'amount');
  api.renderSubscriptionResults();
  const original = quickButtons.get(1);
  original.focus();
  const mutation = deferred();
  const reload = deferred();
  const reloadStarted = deferred();
  context.fetch = async (url) => {
    if (url.endsWith('/skip')) return mutation.promise;
    if (url === '/api/state') { reloadStarted.resolve(); return reload.promise; }
    throw new Error('Unexpected test route');
  };
  const operation = api.quickSkipSubscription(original);
  assert.equal(original.disabled, true, 'original action must become pending');
  if (failureStage === 'reload') {
    mutation.resolve({ status: 200, ok: true, json: async () => ({}) });
    await reloadStarted.promise;
  }
  // A search/filter update replaces the original while its request is still pending.
  api.renderSubscriptionResults();
  const replacement = quickButtons.get(1);
  assert.notEqual(replacement, original);
  assert.equal(original.isConnected, false);
  assert.equal(replacement.disabled, true, 'replacement must retain in-flight state');
  assert.equal(replacement.hasAttribute('aria-busy'), true);
  // An unrelated modal opened during the request must keep keyboard focus.
  node('#modalBackdrop').hidden = false;
  const modalControl = makeNode('#modalControl');
  modalControl.focus();
  const previousFocusCalls = focusCalls.length;
  notices.length = 0;
  if (failureStage === 'mutation') mutation.reject(new Error('request unavailable'));
  else reload.reject(new Error('reload unavailable'));
  await operation;
  const connected = quickButtons.get(1);
  assert.equal(connected.disabled, false, `${failureStage} failure must enable the connected replacement`);
  assert.equal(connected.hasAttribute('aria-busy'), false, 'connected busy state must be removed');
  assert.equal(api.isPending(1), false, 'request state must be cleared');
  assert.deepEqual(Array.from(api.getFilters()), ['Zulu', '영상', 'active', 'amount']);
  assert.equal(activeElement, modalControl, 'pending cleanup must not steal modal focus');
  assert.equal(focusCalls.length, previousFocusCalls);
  if (failureStage === 'reload') assert.ok(notices.some((notice) => notice.includes('저장했지만')), 'saved mutation must be distinguished from refresh failure');
  node('#modalBackdrop').hidden = true;
}

(async () => {
  await verifyPendingReplacement('mutation');
  await verifyPendingReplacement('reload');
  console.log('Subscription list decisions, markup, and async pending replacement checks passed.');
})().catch((error) => { console.error(error.message); process.exitCode = 1; });
