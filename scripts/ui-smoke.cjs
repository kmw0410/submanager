/* Run against the disposable test copy:
 * PLAYWRIGHT_MODULE=/path/to/playwright UI_TEST_EMAIL=... UI_TEST_PASSWORD=... node scripts/ui-smoke.cjs
 * The authenticated mutation routes are mocked; application data is not modified.
 */
const assert = require('node:assert/strict');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const baseURL = process.env.UI_TEST_URL || 'http://127.0.0.1:18080';
const email = process.env.UI_TEST_EMAIL;
const password = process.env.UI_TEST_PASSWORD;
if (!email || !password) throw new Error('Set UI_TEST_EMAIL and UI_TEST_PASSWORD for the disposable test account.');

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.CHROME_BIN || '/opt/google/chrome/chrome', headless: true });
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  const errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  const settings = async (tab) => {
    await page.locator('#settingsButton').click();
    if (tab) await page.locator(`[data-tab="${tab}"]`).click();
  };
  const close = () => page.locator('[data-close-modal]').first().click();
  try {
    await page.goto(baseURL);
    await page.locator('#authForm [name=email]').fill(email);
    await page.locator('#authForm [name=password]').fill(password);
    await Promise.all([page.waitForURL(baseURL + '/'), page.locator('#authForm button[type=submit]').click()]);
    await page.locator('#settingsButton').waitFor();

    await settings();
    const savedName = await page.locator('#settingsForm [name=name]').inputValue();
    await page.locator('#settingsForm [name=name]').fill('임시 이름');
    await page.locator('[data-tab=notifications]').click();
    await page.locator('[name=notifyDays]').fill('8');
    await page.locator('[data-tab=channels]').click();
    const telegram = page.locator('[name=telegramEnabled]');
    const initialToggle = await telegram.isChecked();
    const telegramLabel = page.locator('label.integration-toggle').filter({ has: telegram });
    await telegramLabel.click();
    assert.equal(await telegram.isChecked(), !initialToggle);
    await telegramLabel.click();
    assert.equal(await telegram.isChecked(), initialToggle);
    assert.equal(await page.locator('#settingsForm [name=name]').inputValue(), '임시 이름', 'integration toggles must preserve profile drafts');
    assert.equal(await page.locator('[name=notifyDays]').inputValue(), '8', 'integration toggles must preserve notification drafts');
    assert.equal(await page.locator('[name=notifyMonthly]').count(), 0);
    assert.equal(await page.locator('[name=notifyChanges]').count(), 0);
    await close();
    await settings();
    assert.equal(await page.locator('#settingsForm [name=name]').inputValue(), savedName, 'cancelled settings must not mutate persisted state');
    await close();

    let releaseEmail;
    let startedEmail;
    let emailRequests = 0;
    const emailStarted = new Promise((resolve) => { startedEmail = resolve; });
    const emailGate = new Promise((resolve) => { releaseEmail = resolve; });
    await page.route('**/api/account/email', async (route) => {
      emailRequests++;
      startedEmail();
      await emailGate;
      await route.fulfill({ status: 200, json: {} });
    });
    await settings('account');
    await page.locator('#emailChangeForm [name=email]').fill(email);
    await page.locator('#emailChangeForm [name=currentPassword]').fill(password);
    await page.locator('#emailChangeForm button[type=submit]').click();
    await emailStarted;
    await page.waitForFunction(() => document.querySelector('#emailChangeForm button[type=submit]').disabled);
    await page.locator('#emailChangeForm').evaluate((form) => form.requestSubmit());
    assert.equal(emailRequests, 1, 'pending saves must reject duplicate submissions');
    releaseEmail();
    await page.waitForFunction(() => document.querySelector('#emailChangeForm [name=currentPassword]').value === '');
    assert.equal(await page.locator('#emailChangeForm .form-error').textContent(), '');
    await page.unroute('**/api/account/email');

    await page.route('**/api/account/password', (route) => route.fulfill({ status: 200, json: {} }));
    for (const field of ['currentPassword', 'newPassword', 'confirmPassword']) {
      await page.locator(`#passwordChangeForm [name=${field}]`).fill(password);
    }
    await page.locator('#passwordChangeForm button[type=submit]').click();
    await page.waitForFunction(() => document.querySelector('#passwordChangeForm [name=currentPassword]').value === '');
    assert.equal(await page.locator('#passwordChangeForm .form-error').textContent(), '');
    await page.unroute('**/api/account/password');
    await close();

    await page.route('**/api/sessions', (route) => route.fulfill({ status: 200, contentType: 'text/html', body: '<html>unavailable</html>' }));
    await settings('sessions');
    await page.locator('#currentSession .form-error').waitFor();
    assert.match(await page.locator('#currentSession').textContent(), /서버 응답을 읽지 못했어요/);
    await page.unroute('**/api/sessions');
    await close();

    await page.route('**/api/settings', (route) => route.fulfill({ status: 200, json: {} }));
    await page.route('**/api/state', (route) => route.fulfill({ status: 503, json: { error: '대시보드를 다시 불러오지 못했어요.' } }));
    await settings();
    await page.locator('#settingsForm button[type=submit]').click();
    await page.locator('#modalBackdrop').waitFor({ state: 'hidden' });
    await page.getByText('대시보드를 다시 불러오지 못했어요.', { exact: true }).waitFor();
    await page.unroute('**/api/state');
    await page.unroute('**/api/settings');

    // A calendar response started before a state replacement must not repopulate its cache.
    let calendarRequests = 0;
    let staleDate;
    let releaseOld;
    let startedOld;
    const oldStarted = new Promise((resolve) => { startedOld = resolve; });
    const oldGate = new Promise((resolve) => { releaseOld = resolve; });
    await page.route('**/api/upcoming?month=*', async (route) => {
      calendarRequests++;
      const response = await route.fetch();
      if (calendarRequests === 1) {
        startedOld();
        await oldGate;
        const stale = await response.json();
        if (!stale.items.length) {
          stale.items.push({ serviceName: 'STALE_CALENDAR_RESPONSE', scheduledDate: `${stale.period}-01`, currency: 'KRW', amount: 0, paymentMethodName: 'test', billingCycle: 'monthly' });
        }
        stale.items[0].serviceName = 'STALE_CALENDAR_RESPONSE';
        staleDate = stale.items[0].scheduledDate;
        await route.fulfill({ response, json: stale });
      } else {
        await route.fulfill({ response });
      }
    });
    await page.locator('.sidebar [data-view=upcoming]').click();
    await page.locator('[data-upcoming-view=calendar]').click();
    await oldStarted;
    await page.route('**/api/settings', (route) => route.fulfill({ status: 200, json: {} }));
    await settings();
    await page.locator('#settingsForm button[type=submit]').click();
    await page.locator('#modalBackdrop').waitFor({ state: 'hidden' });
    await page.waitForFunction(() => document.querySelector('.calendar-grid'));
    const oldResponse = page.waitForResponse((response) => response.url().includes('/api/upcoming?month='));
    releaseOld();
    // The old route must finish before checking its effect.
    await oldResponse;
    await page.locator('[data-calendar-today]').click();
    if (staleDate) await page.locator(`[data-calendar-date="${staleDate}"]`).click();
    assert.ok(calendarRequests >= 2, 'state replacement must fetch fresh calendar data');
    assert.equal(await page.getByText('STALE_CALENDAR_RESPONSE', { exact: false }).count(), 0);
    await page.unroute('**/api/upcoming?month=*');
    await page.unroute('**/api/settings');
    assert.deepEqual(errors, [], 'browser must not emit uncaught errors');
    console.log('UI behavior checks passed: drafts, account forms, pending saves, malformed JSON, late refresh failure, stale calendar responses.');
  } finally {
    await browser.close();
  }
})().catch((error) => { console.error(error.message); process.exitCode = 1; });
