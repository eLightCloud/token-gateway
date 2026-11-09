// Local SMTP -> production send APIs -> captured MIME -> Chromium desktop/mobile.
// Does not use real mail credentials or change any gateway database/settings.
import { spawn } from 'node:child_process';
import { createServer } from 'node:net';
import { mkdtemp, mkdir, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';
import { DatabaseSync } from 'node:sqlite';
import { createServer as createHTTPServer } from 'node:http';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const output = process.env.NOTIFICATIONS_E2E_OUTPUT || await mkdtemp(join(tmpdir(), 'notifications-e2e-'));
await mkdir(output, { recursive: true });
assert(process.env.NOTIFICATIONS_PLAYWRIGHT_MODULE, 'Set NOTIFICATIONS_PLAYWRIGHT_MODULE to installed playwright-core');
assert(process.env.NOTIFICATIONS_CHROMIUM_PATH, 'Set NOTIFICATIONS_CHROMIUM_PATH to Chromium executable');
const { chromium } = await import(pathToFileURL(process.env.NOTIFICATIONS_PLAYWRIGHT_MODULE));
const messages = [], cases = [], sockets = new Set();
const work = await mkdtemp(join(tmpdir(), 'notifications-e2e-app-'));
async function run(command, args, options = {}) {
  const child = spawn(command, args, { cwd: root, ...options, stdio: ['ignore', 'pipe', 'pipe'] });
  let stdout = '', stderr = '';
  child.stdout.on('data', chunk => { stdout += chunk; });
  child.stderr.on('data', chunk => { stderr += chunk; });
  const code = await new Promise((resolve, reject) => { child.on('error', reject); child.on('exit', resolve); });
  assert.equal(code, 0, stderr);
  return stdout;
}
const smtp = createServer(socket => {
  sockets.add(socket);
  socket.on('close', () => sockets.delete(socket));
  socket.setTimeout(15000, () => socket.destroy());
  socket.write('220 localhost isolated E2E SMTP\r\n');
  let buffer = '', data = false, message = '';
  socket.on('data', chunk => {
    buffer += chunk.toString();
    for (;;) {
      const end = buffer.indexOf('\r\n');
      if (end < 0) break;
      const line = buffer.slice(0, end);
      buffer = buffer.slice(end + 2);
      if (data) {
        if (line !== '.') { message += line.replace(/^\.\./, '.') + '\r\n'; continue; }
        messages.push(message); message = ''; data = false;
        socket.write('250 accepted\r\n');
      } else if (/^(EHLO|HELO|MAIL FROM|RCPT TO)/i.test(line)) {
        if (/^RCPT TO/i.test(line)) assert(['RCPT TO:<recipient@e2e.invalid>', 'RCPT TO:<verification@e2e.invalid>', 'RCPT TO:<admin@e2e.invalid>'].includes(line));
        socket.write('250 OK\r\n');
      } else if (line === 'DATA') {
        data = true; socket.write('354 send data\r\n');
      } else if (line === 'QUIT') { socket.end('221 bye\r\n'); }
      else socket.write('500 unsupported\r\n');
    }
  });
});
await new Promise(resolve => smtp.listen(0, '127.0.0.1', resolve));
const provider = createHTTPServer(async (req, res) => {
  let body = '';
  for await (const chunk of req) body += chunk;
  const input = JSON.parse(body);
  res.setHeader('Content-Type', 'application/json');
  if (input.messages?.[0]?.content === 'synthetic-usage-error') {
    res.writeHead(400);
    res.end(JSON.stringify({ error: { code: 'context_length_exceeded', type: 'invalid_request_error', message: 'Synthetic context limit' } }));
    return;
  }
  res.end(JSON.stringify({ id: 'synthetic-chat', object: 'chat.completion', model: input.model, choices: [{ index: 0, message: { role: 'assistant', content: 'Synthetic response' }, finish_reason: 'stop' }], usage: { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 } }));
});
await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve));
let browser, gateway, complete = false, gatewayLog = '';
try {
  const smtpAddress = `127.0.0.1:${smtp.address().port}`;
  const stdout = await run('go', ['run', './scripts/notifications_e2e_fixture.go'], { env: { ...process.env, NOTIFICATIONS_E2E_SMTP: smtpAddress } });
  const kinds = stdout.trim().split(/\s+/);
  assert.equal(kinds.length, 14);
  assert.equal(new Set(kinds).size, 14);
  assert.equal(messages.length, 14);
  browser = await chromium.launch({ executablePath: process.env.NOTIFICATIONS_CHROMIUM_PATH, headless: true });
  for (const [i, mime] of messages.entries()) {
    const kind = kinds[i];
    const split = mime.indexOf('\r\n\r\n');
    const headers = mime.slice(0, split);
    assert.match(headers, /Content-Type: text\/html/i);
    const payload = mime.slice(split + 4);
    const html = /Content-Transfer-Encoding: base64/i.test(headers) ? Buffer.from(payload.replace(/\s/g, ''), 'base64').toString() : payload;
    assert(html.includes(`data-email-template="${kind}"`));
    assert.equal((html.match(/<!doctype html>/g) || []).length, 1);
    assert(!html.includes('tracking.invalid') && !html.includes('window.injected'));
    await writeFile(join(output, `${kind}.eml`), mime);
    await writeFile(join(output, `${kind}.html`), html);
    for (const [device, width] of [['desktop', 800], ['mobile', 375]]) {
      const page = await browser.newPage({ viewport: { width, height: 1000 } });
      const requests = [];
      await page.route('**/*', route => { requests.push(route.request().url()); return route.abort(); });
      await page.setContent(html, { waitUntil: 'load' });
      assert.equal(await page.locator('body').getAttribute('data-email-template'), kind);
      assert.equal(await page.locator('h1').count(), 1);
      assert.equal(await page.locator('script,img,iframe').count(), 0);
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${kind} overflows ${device}`);
      assert.equal(requests.length, 0);
      if (kind === 'verification-code') assert.match(await page.locator('strong').innerText(), /482731/);
      if (kind === 'password-recovery') assert.equal(await page.locator('a').getAttribute('href'), 'https://gateway.example/account-activation#token=synthetic-token&purpose=security_password_reset');
      await page.screenshot({ path: join(output, `${kind}-${device}.png`), fullPage: true });
      await page.close();
      cases.push({ kind, device, status: 'passed' });
    }
    console.log(`PASS ${kind}: SMTP, desktop, mobile`);
  }
  // Exercise normal public routes in a real gateway process, with disposable DB.
  const reserve = createServer();
  await new Promise(resolve => reserve.listen(0, '127.0.0.1', resolve));
  const port = reserve.address().port;
  await new Promise(resolve => reserve.close(resolve));
  const env = { ...process.env, PORT: String(port), SQL_DSN: '', LOG_SQL_DSN: '', REDIS_CONN_STRING: '', SQLITE_PATH: join(work, 'main.db'), SQL_MAX_OPEN_CONNS: '1', SQL_MAX_IDLE_CONNS: '1', SESSION_SECRET: randomBytes(32).toString('hex'), MFA_STORAGE_KEY: 'isolated-notifications-e2e-key', SESSION_COOKIE_SECURE: 'false', SECURITY_COMPLETION_ENFORCED: 'true', GIN_MODE: 'release', NOTIFICATIONS_E2E_SMTP: smtpAddress, NOTIFICATIONS_E2E_UPSTREAM: `http://127.0.0.1:${provider.address().port}` };
  const binary = join(work, 'gateway'), fixture = join(work, 'fixture');
  await run('go', ['build', '-o', binary, '.']);
  await run('go', ['build', '-o', fixture, './scripts/notifications_e2e_fixture.go']);
  await run(fixture, [], { cwd: work, env: { ...env, NOTIFICATIONS_E2E_MODE: 'seed' } });
  gateway = spawn(binary, [], { cwd: work, env, stdio: ['ignore', 'pipe', 'pipe'] });
  gateway.stdout.on('data', chunk => { gatewayLog += chunk; });
  gateway.stderr.on('data', chunk => { gatewayLog += chunk; });
  const origin = `http://127.0.0.1:${port}`;
  await assert.doesNotReject(async () => {
    for (let attempt = 0; attempt < 100; attempt++) {
      try { if ((await fetch(origin + '/api/status')).ok) return; } catch {}
      if (gateway.exitCode !== null) throw new Error('Gateway exited: ' + gatewayLog);
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    throw new Error('Gateway did not become ready');
  });
  const db = new DatabaseSync(env.SQLITE_PATH);
  const before = db.prepare('SELECT quota, used_quota, password, auth_version FROM users WHERE username = ?').get('notifications-e2e');
  for (const [route, kind] of [['/api/verification?email=verification@e2e.invalid', 'verification-code'], ['/api/reset_password?email=recipient@e2e.invalid', 'password-recovery']]) {
    const count = messages.length;
    const response = await fetch(origin + route);
    assert.equal(response.status, 200);
    assert.equal((await response.json()).success, true);
    for (let attempt = 0; messages.length === count && attempt < 100; attempt++) await new Promise(resolve => setTimeout(resolve, 50));
    assert.equal(messages.length, count + 1);
    const mime = messages.at(-1);
    assert(mime.includes(`data-email-template="${kind}"`));
    if (kind === 'verification-code') assert.match(mime, /<strong>[a-zA-Z0-9]{6}<\/strong>/);
    else {
      const recovery = mime.match(/href="([^"]*account-activation#[^"]*)"/);
      assert(recovery, 'Recovery link must retain its fragment');
      const link = new URL(recovery[1].replaceAll('&amp;', '&'));
      const fragment = new URLSearchParams(link.hash.slice(1));
      assert.equal(fragment.get('purpose'), 'security_password_reset');
      assert(fragment.get('token')?.length > 20);
      assert.equal(link.searchParams.has('token'), false);
    }
    await writeFile(join(output, `gateway-${kind}.eml`), mime);
    cases.push({ route, kind, status: 'passed' });
    console.log(`PASS gateway route ${kind}`);
  }
  assert.deepEqual(db.prepare('SELECT quota, used_quota, password, auth_version FROM users WHERE username = ?').get('notifications-e2e'), before);
  const account = db.prepare('SELECT id, quota, used_quota FROM users WHERE username = ?').get('notifications-e2e');
  for (const [content, expected] of [['synthetic-success', 200], ['synthetic-usage-error', 400]]) {
    const response = await fetch(origin + '/v1/chat/completions', { method: 'POST', headers: { Authorization: 'Bearer ' + 'n'.repeat(48), 'Content-Type': 'application/json' }, body: JSON.stringify({ model: 'notifications-demo', messages: [{ role: 'user', content }] }) });
    const reply = await response.json();
    assert.equal(response.status, expected, JSON.stringify(reply));
    cases.push({ name: `Real relay ${content}`, status: 'passed' });
  }
  // Wait for the normal bounded background flush, rather than inserting counters.
  for (let attempt = 0; attempt < 100; attempt++) {
    const observed = db.prepare('SELECT SUM(requests) AS requests, SUM(failures) AS failures FROM operations_counters WHERE user_id = ?').get(account.id);
    if (observed.requests === 2 && observed.failures === 1) break;
    await new Promise(resolve => setTimeout(resolve, 250));
  }
  const observed = db.prepare('SELECT SUM(requests) AS requests, SUM(failures) AS failures FROM operations_counters WHERE user_id = ?').get(account.id);
  assert.equal(observed.requests, 2);
  assert.equal(observed.failures, 1);
  const consumed = db.prepare('SELECT SUM(quota) AS quota FROM logs WHERE user_id = ? AND type = 2').get(account.id).quota;
  assert(consumed > 0);
  const settled = db.prepare('SELECT quota, used_quota FROM users WHERE id = ?').get(account.id);
  assert.equal(account.quota - settled.quota, consumed);
  assert.equal(settled.used_quota - account.used_quota, consumed);
  cases.push({ name: 'Real consumption settled and final errors persisted by normal observer', status: 'passed' });
  await new Promise(resolve => { gateway.once('exit', resolve); gateway.kill('SIGTERM'); });
  gateway = undefined;
  // Move only the actual captured facts into the previous reporting day. Clock
  // and host resource boundaries are controlled in the Go integration fixture;
  // no production clock override or test endpoint is introduced.
  const localDate = new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit' }).format(new Date());
  const end = Date.parse(localDate + 'T00:00:00+08:00'), start = end - 86400000;
  const day = new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit' }).format(new Date(start));
  db.prepare('UPDATE logs SET created_at = ? WHERE user_id = ? AND type = 2').run(start / 1000 + 1, account.id);
  db.prepare('UPDATE operations_counters SET day = ? WHERE user_id = ?').run(day, account.id);
  db.exec('DELETE FROM operations_deliveries; DELETE FROM operations_accounts; DELETE FROM operations_days;');
  db.close();
  cases.push({ name: 'Notification delivery leaves account balance, usage and credentials unchanged', status: 'passed' });
  const mailCount = messages.length;
  const dailyOutput = await run('go', ['test', './service', '-run', '^TestOperationsGatewayDailyE2E$', '-count=1', '-v'], { env: { ...env, OPS_E2E_SQLITE: env.SQLITE_PATH, OPS_E2E_ORIGIN: origin } });
  await writeFile(join(output, 'business-daily.log'), dailyOutput);
  assert.equal(messages.length, mailCount + 2, 'One account summary and one administrator report, with no replay');
  const accountMail = messages.slice(mailCount).find(message => message.includes('data-email-template="account-summary"'));
  const adminMail = messages.slice(mailCount).find(message => message.includes('data-email-template="operations-report"'));
  assert(accountMail && adminMail);
  assert.match(accountMail, /Observed requests: 2/);
  assert.match(accountMail, /Observed final failures: 1/);
  assert.match(adminMail, /Affected accounts: 1/);
  const logLink = accountMail.match(/href="([^"]*usage-logs\/common\?[^"]*)"/);
  assert(logLink);
  const url = new URL(logLink[1].replaceAll('&amp;', '&'));
  assert.equal(Number(url.searchParams.get('startTime')), start);
  assert.equal(Number(url.searchParams.get('endTime')), end - 1);
  await writeFile(join(output, 'business-account-summary.eml'), accountMail);
  await writeFile(join(output, 'business-operations-report.eml'), adminMail);
  cases.push({ name: 'Real relay facts -> daily aggregation/selection -> SMTP; silent account skipped, no mutation or replay, correct log date', status: 'passed' });
  complete = true;
} finally {
  if (gateway && gateway.exitCode === null) {
    gateway.kill('SIGTERM');
    await new Promise(resolve => gateway.once('exit', resolve));
  }
  await writeFile(join(output, 'gateway.log'), gatewayLog);
  await browser?.close();
  for (const socket of sockets) socket.destroy();
  await new Promise(resolve => smtp.close(resolve));
  await new Promise(resolve => provider.close(resolve));
  await writeFile(join(output, 'summary.json'), JSON.stringify({ complete, scope: 'Templates/SMTP/browser rendering, real gateway security mail and relay consumption/error traffic, then production daily pipeline with reporting clock/resource boundaries controlled in Go test. Live scheduler timing and resource sensors are not claimed as E2E coverage.', delivered_mail: messages.length, cases }, null, 2));
  console.log(`Evidence: ${output}`);
}
