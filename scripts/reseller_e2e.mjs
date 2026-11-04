import { spawn, execFileSync } from "node:child_process";
import { createServer } from "node:http";
import { createServer as createSMTP } from "node:net";
import { createHmac, randomBytes } from "node:crypto";
import { mkdtemp, mkdir, writeFile, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { pathToFileURL, fileURLToPath } from "node:url";
import { DatabaseSync } from "node:sqlite";
import assert from "node:assert/strict";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const work = await mkdtemp(join(tmpdir(), "reseller-e2e-"));
const output = process.env.RESELLER_E2E_OUTPUT || join(work, "evidence");
await mkdir(output, { recursive: true });
const library = process.env.RESELLER_PLAYWRIGHT_MODULE;
if (!library)
  throw new Error("Set RESELLER_PLAYWRIGHT_MODULE to an installed playwright-core module");
const { chromium } = await import(pathToFileURL(library));
const results = [],
  mail = [],
  upstreamCalls = [],
  jobs = new Map();
const processes = [];
const versions = {
  node: process.version,
  go: execFileSync("go", ["version"], { encoding: "utf8" }).trim(),
  bun: execFileSync("bun", ["--version"], { encoding: "utf8" }).trim(),
};
let browser,
  gateway,
  failure,
  gatewayOutput = "",
  releaseHeld,
  completed = false;
const report = async () =>
  writeFile(
    join(output, "summary.json"),
    JSON.stringify(
      {
        complete: completed && !failure && !results.some((item) => item.status === "failed"),
        work,
        cases: results,
        upstream_requests: upstreamCalls.length,
        delivered_mail: mail.length,
        versions,
      },
      null,
      2,
    ),
  );
async function step(name, fn) {
  try {
    await fn();
    results.push({ name, status: "passed" });
    console.log("PASS " + name);
  } catch (error) {
    results.push({ name, status: "failed", error: error.message });
    throw error;
  } finally {
    await report();
  }
}
const listen = (server) =>
  new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve(server.address().port)));
const provider = createServer(async (req, res) => {
  let body = "";
  for await (const chunk of req) body += chunk;
  const input = body ? JSON.parse(body) : {};
  if (input.messages?.some((message) => message.content === "synthetic-failure")) {
    res.writeHead(500, { "Content-Type": "application/json" });
    res.end(
      JSON.stringify({ error: { message: "synthetic provider failure", type: "server_error" } }),
    );
    return;
  }
  upstreamCalls.push({ path: req.url, model: input.model, stream: Boolean(input.stream) });
  if (input.messages?.some((message) => message.content === "synthetic-held"))
    await new Promise((resolve) => {
      releaseHeld = resolve;
    });
  if (req.url === "/task/jobs" && req.method === "POST") {
    const id = "synthetic-job-" + (jobs.size + 1),
      status = input.case.endsWith("failure") ? "FAILURE" : "SUCCESS";
    jobs.set(id, { id, status });
    res.setHeader("Content-Type", "application/json");
    res.end(
      JSON.stringify({
        id,
        status: input.case.startsWith("immediate") ? status : "NOT_START",
        immediate: input.case.startsWith("immediate"),
      }),
    );
    return;
  }
  if (req.url.startsWith("/task/jobs/") && req.method === "GET") {
    const result = jobs.get(req.url.split("/").at(-1));
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify(result));
    return;
  }
  if (req.url === "/v1/models") {
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify({ object: "list", data: [{ id: "reseller-demo", object: "model" }] }));
    return;
  }
  if (req.url === "/v1/chat/completions") {
    const common = { id: "e2e-chat", model: input.model, created: Math.floor(Date.now() / 1000) };
    if (input.stream) {
      res.setHeader("Content-Type", "text/event-stream");
      for (const event of [
        {
          ...common,
          object: "chat.completion.chunk",
          choices: [
            {
              index: 0,
              delta: { role: "assistant", content: "Synthetic E2E response" },
              finish_reason: null,
            },
          ],
        },
        {
          ...common,
          object: "chat.completion.chunk",
          choices: [{ index: 0, delta: {}, finish_reason: "stop" }],
          usage: { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 },
        },
      ])
        res.write("data: " + JSON.stringify(event) + "\n\n");
      res.end("data: [DONE]\n\n");
      return;
    }
    res.setHeader("Content-Type", "application/json");
    res.end(
      JSON.stringify({
        ...common,
        object: "chat.completion",
        choices: [
          {
            index: 0,
            message: { role: "assistant", content: "Synthetic E2E response" },
            finish_reason: "stop",
          },
        ],
        usage: { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 },
      }),
    );
    return;
  }
  res.writeHead(404);
  res.end(JSON.stringify({ error: { message: "unknown synthetic upstream endpoint" } }));
});
const smtp = createSMTP((socket) => {
  socket.setEncoding("utf8");
  socket.write("220 e2e.invalid\r\n");
  let buffer = "",
    data = false,
    message = "",
    recipient = "";
  socket.on("data", (chunk) => {
    buffer += chunk;
    while (buffer.includes("\n")) {
      const i = buffer.indexOf("\n");
      const line = buffer.slice(0, i).replace(/\r$/, "");
      buffer = buffer.slice(i + 1);
      if (data) {
        if (line === ".") {
          mail.push({ recipient, message });
          data = false;
          message = "";
          socket.write("250 queued\r\n");
        } else message += line + "\n";
      } else if (/^EHLO|^HELO/.test(line)) socket.write("250-e2e.invalid\r\n250 PIPELINING\r\n");
      else if (/^RCPT TO:/i.test(line)) {
        recipient = line.slice(8).replace(/[<>]/g, "").trim();
        socket.write("250 ok\r\n");
      } else if (line === "DATA") {
        data = true;
        socket.write("354 end with dot\r\n");
      } else if (line === "QUIT") {
        socket.end("221 bye\r\n");
      } else socket.write("250 ok\r\n");
    }
  });
});
const portProbe = createServer();
const port = await listen(portProbe);
await new Promise((r) => portProbe.close(r));
const origin = `http://localhost:${port}`,
  apiOrigin = `http://127.0.0.1:${port}`,
  httpOrigin = `http://reseller-e2e.test:${port}`;
const password = "Synthetic-" + randomBytes(16).toString("hex");
const env = {
  ...process.env,
  SQL_DSN: "",
  SQL_MAX_OPEN_CONNS: "1",
  SQL_MAX_IDLE_CONNS: "1",
  LOG_SQL_DSN: "",
  REDIS_CONN_STRING: "",
  SQLITE_PATH: join(work, "main.db"),
  RESELLER_E2E_FIXTURE: "synthetic-only",
  RESELLER_E2E_PASSWORD: password,
  RESELLER_E2E_MANIFEST: join(work, "fixture.json"),
  RESELLER_E2E_ORIGIN: origin,
  MFA_STORAGE_KEY: "isolated-e2e-storage-key-20261104",
  SESSION_SECRET: randomBytes(32).toString("hex"),
  SESSION_COOKIE_SECURE: "false",
  SESSION_COOKIE_TRUSTED_URL: "",
  PASSWORD_LOGIN_ENCRYPTION_ENABLED: "false",
  SECURITY_COMPLETION_ENFORCED: "true",
  GLOBAL_WEB_RATE_LIMIT: "100000",
  GLOBAL_API_RATE_LIMIT: "100000",
  CRITICAL_RATE_LIMIT: "1000",
  GIN_MODE: "release",
};
const binary = join(work, "gateway"),
  seed = join(work, "seed");
function run(command, args, options = {}) {
  return execFileSync(command, args, {
    cwd: root,
    env,
    stdio: ["ignore", "pipe", "pipe"],
    ...options,
  });
}
function totp(secret = "JBSWY3DPEHPK3PXP") {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const c of secret) bits += alphabet.indexOf(c).toString(2).padStart(5, "0");
  const key = Buffer.from(bits.match(/.{8}/g).map((x) => parseInt(x, 2)));
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000)));
  const hash = createHmac("sha1", key).update(counter).digest();
  const offset = hash.at(-1) & 15;
  return ((hash.readUInt32BE(offset) & 0x7fffffff) % 1000000).toString().padStart(6, "0");
}
async function eventually(fn, timeout = 20000) {
  const until = Date.now() + timeout;
  let last;
  while (Date.now() < until) {
    try {
      return await fn();
    } catch (error) {
      last = error;
      await new Promise((r) => setTimeout(r, 100));
    }
  }
  throw last;
}
try {
  env.RESELLER_E2E_UPSTREAM = "http://127.0.0.1:" + (await listen(provider));
  env.RESELLER_E2E_SMTP_PORT = String(await listen(smtp));
  console.log("Building isolated gateway and seeding fixture");
  if (!process.env.RESELLER_E2E_SKIP_BUILD)
    run("bun", ["run", "build"], { cwd: join(root, "web") });
  run("go", ["build", "-o", binary, "."]);
  run("go", ["build", "-o", seed, "scripts/reseller_e2e_seed.go"]);
  run(seed, ["--log-dir", join(work, "seed-logs")]);
  const fixture = JSON.parse(await readFile(env.RESELLER_E2E_MANIFEST, "utf8"));
  versions.sqlite = fixture.database_version;
  gateway = spawn(binary, ["--port", String(port), "--log-dir", join(work, "gateway-logs")], {
    cwd: work,
    env,
    stdio: ["ignore", "pipe", "pipe"],
  });
  processes.push(gateway);
  gateway.stdout.on("data", (c) => {
    gatewayOutput += c;
  });
  gateway.stderr.on("data", (c) => {
    gatewayOutput += c;
  });
  await eventually(async () => {
    const response = await fetch(apiOrigin + "/api/status");
    assert.equal(response.status, 200);
  });
  browser = await chromium.launch({
    headless: true,
    executablePath:
      process.env.RESELLER_CHROMIUM_PATH ||
      "/Users/randy/Library/Caches/ms-playwright/chromium_headless_shell-1243/chrome-headless-shell-mac-arm64/chrome-headless-shell",
    args: ["--no-proxy-server", "--host-resolver-rules=MAP reseller-e2e.test 127.0.0.1"],
  });
  versions.chromium = browser.version();
  const db = new DatabaseSync(env.SQLITE_PATH, { readOnly: true });
  db.exec("PRAGMA busy_timeout=10000");
  const sessions = {};
  function observePage(page) {
    page.on("pageerror", (error) => {
      results.push({ name: "browser runtime error", status: "failed", error: error.message });
    });
  }
  async function login(name, base = origin) {
    const context = await browser.newContext({
      locale: "en-US",
      viewport: { width: 1440, height: 1000 },
    });
    await context.addInitScript(() => {
      if (location.protocol === "http:" || location.protocol === "https:") {
        localStorage.setItem("i18nextLng", "en");
      }
    });
    if (base === httpOrigin)
      await context.route(httpOrigin + "/**", async (route) => {
        const url = new URL(route.request().url());
        const response = await route.fetch({
          url: apiOrigin + url.pathname + url.search,
          headers: { ...(await route.request().allHeaders()), host: url.host },
        });
        await route.fulfill({ response });
      });
    context.on("page", observePage);
    const page = await context.newPage();
    page.setDefaultTimeout(15000);
    if (base === origin && fixture.passkeys[name]) {
      const cdp = await context.newCDPSession(page);
      await cdp.send("WebAuthn.enable");
      const { authenticatorId } = await cdp.send("WebAuthn.addVirtualAuthenticator", {
        options: {
          protocol: "ctap2",
          transport: "internal",
          hasResidentKey: true,
          hasUserVerification: true,
          isUserVerified: true,
          automaticPresenceSimulation: true,
        },
      });
      await cdp.send("WebAuthn.addCredential", {
        authenticatorId,
        credential: fixture.passkeys[name],
      });
    }
    await page.goto(base + "/sign-in");
    await page.getByLabel("Username or Email", { exact: true }).fill("e2e-" + name);
    await page.getByLabel("Password", { exact: true }).fill(password);
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
    await page.getByRole("tab", { name: "Authenticator code", exact: true }).click();
    await page.getByRole("textbox", { name: /Authenticator code/ }).fill(totp());
    const responsePromise = page.waitForResponse(
      (r) => r.url().includes("/api/user/login/verify") && r.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Verify", exact: true }).click();
    const response = await responsePromise;
    const result = await response.json();
    assert.equal(result.success, true, JSON.stringify(result));
    assert.ok(result.data.access_token);
    await page.waitForURL((url) => !url.pathname.includes("sign-in"));
    sessions[name] = { context, page, token: result.data.access_token, base };
    await page.screenshot({
      path: join(output, `login-${name}.png`),
      fullPage: true,
      animations: "disabled",
    });
    return sessions[name];
  }
  async function api(name, path, method = "GET", body, headers = {}) {
    const session = sessions[name];
    const response = await fetch(origin + path, {
      method,
      headers: {
        Authorization: "Bearer " + session.token,
        "New-Api-User": String(fixture.users[name]),
        ...(body ? { "Content-Type": "application/json" } : {}),
        ...headers,
      },
      body: body ? JSON.stringify(body) : undefined,
    });
    const text = await response.text();
    let data;
    try {
      data = JSON.parse(text);
    } catch {}
    return { status: response.status, data, text, headers: response.headers };
  }
  await step("real password and MFA login/root", async () => {
    await login("root");
  });
  await step("real HTTP password and MFA login/agent", async () => {
    await login("agent", httpOrigin);
    assert.equal(await sessions.agent.page.evaluate(() => isSecureContext), false);
  });
  await step("real password and MFA login/customer", async () => {
    await login("customer");
  });
  await step("real password and MFA login/ordinary", async () => {
    await login("ordinary");
  });
  await step("real password and MFA login/admin without recovery capability", async () => {
    await login("admin");
  });
  function mailLink(email, purpose) {
    for (const item of [...mail].reverse()) {
      if (item.recipient !== email) continue;
      const href = item.message.match(/href="([^"]+)"/)?.[1]?.replaceAll("&amp;", "&");
      if (href && new URLSearchParams(new URL(href).hash.slice(1)).get("purpose") === purpose)
        return href;
    }
    throw new Error("Expected security mail was not delivered for " + purpose);
  }
  const uid = fixture.users,
    channel = fixture.channel_id;
  const balance = (id) => Number(db.prepare("SELECT quota FROM users WHERE id = ?").get(id).quota);
  const procurement = () =>
    db.prepare("SELECT balance,reserved FROM reseller_accounts WHERE user_id = ?").get(uid.agent);
  async function consume(name, content = "synthetic", stream = false, model = "reseller-demo") {
    const response = await fetch(origin + "/v1/chat/completions", {
      method: "POST",
      headers: {
        Authorization: "Bearer " + fixture.tokens[name],
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ model, stream, messages: [{ role: "user", content }] }),
    });
    return { status: response.status, text: await response.text() };
  }
  async function proof(name, scope, context) {
    const begin = await api(name, "/api/user/passkey/verify/begin", "POST", {
      scope,
      context,
      rp_id: "localhost",
    });
    assert.equal(begin.status, 200, begin.text);
    assert.equal(begin.data.success, true, begin.text);
    let page = sessions[name].page;
    if (sessions[name].base !== origin) {
      const ctx = await browser.newContext({ locale: "en-US" });
      ctx.on("page", observePage);
      page = await ctx.newPage();
      const cdp = await ctx.newCDPSession(page);
      await cdp.send("WebAuthn.enable");
      const { authenticatorId } = await cdp.send("WebAuthn.addVirtualAuthenticator", {
        options: {
          protocol: "ctap2",
          transport: "internal",
          hasResidentKey: true,
          hasUserVerification: true,
          isUserVerified: true,
          automaticPresenceSimulation: true,
        },
      });
      await cdp.send("WebAuthn.addCredential", {
        authenticatorId,
        credential: fixture.passkeys[name],
      });
      await page.goto(origin + "/sign-in");
    }
    const options = begin.data.data.options;
    const credential = await page.evaluate(async (options) => {
      const decode = (value) =>
        Uint8Array.from(atob(value.replaceAll("-", "+").replaceAll("_", "/")), (c) =>
          c.charCodeAt(0),
        ).buffer;
      const encode = (value) =>
        value
          ? btoa(String.fromCharCode(...new Uint8Array(value)))
              .replaceAll("+", "-")
              .replaceAll("/", "_")
              .replace(/=+$/, "")
          : null;
      const publicKey = options.publicKey ?? options;
      publicKey.challenge = decode(publicKey.challenge);
      publicKey.allowCredentials = publicKey.allowCredentials?.map((item) => ({
        ...item,
        id: decode(item.id),
      }));
      const credential = await navigator.credentials.get({ publicKey });
      return {
        id: credential.id,
        rawId: encode(credential.rawId),
        type: credential.type,
        authenticatorAttachment: credential.authenticatorAttachment,
        response: {
          clientDataJSON: encode(credential.response.clientDataJSON),
          authenticatorData: encode(credential.response.authenticatorData),
          signature: encode(credential.response.signature),
          userHandle: encode(credential.response.userHandle),
        },
        clientExtensionResults: credential.getClientExtensionResults(),
      };
    }, options);
    const finish = await api(name, "/api/user/passkey/verify/finish", "POST", {
      flow_token: begin.data.data.flow_token,
      credential,
    });
    assert.equal(finish.status, 200, finish.text);
    assert.equal(finish.data.success, true, finish.text);
    return finish.data.data.proof_token;
  }
  await step("identity domains and ownership restrictions", async () => {
    assert.equal((await api("root", `/api/reseller/admin/agents/${uid.agent}`)).status, 200);
    assert.equal((await api("agent", `/api/reseller/self/customers/${uid.customer}`)).status, 200);
    assert.equal((await api("agent", `/api/reseller/self/customers/${uid.ordinary}`)).status, 404);
    assert.equal((await api("agent", "/api/user/")).status, 403);
    assert.equal((await api("customer", "/api/reseller/self/customers")).status, 403);
    assert.equal((await api("agent", "/api/token/")).status, 403);
  });
  await step("quota allocation is idempotent and does not debit procurement", async () => {
    const beforeB = balance(uid.customer),
      beforeA = procurement();
    const body = {
      operation_id: "e2e-allocation",
      mode: "add",
      quota: 500000,
      reason: "E2E allocation",
    };
    for (let i = 0; i < 2; i++) {
      const result = await api(
        "agent",
        `/api/reseller/self/customers/${uid.customer}/quota-adjustments`,
        "POST",
        body,
      );
      assert.equal(result.status, 200, result.text);
    }
    assert.equal(balance(uid.customer), beforeB + 500000);
    assert.equal(procurement().balance, beforeA.balance);
    const conflict = await api(
      "agent",
      `/api/reseller/self/customers/${uid.customer}/quota-adjustments`,
      "POST",
      { ...body, quota: 500001 },
    );
    assert.equal(conflict.status, 409);
    assert.equal(conflict.data.code, "RESELLER_OPERATION_CONFLICT");
    const page = await api(
      "agent",
      `/api/reseller/self/customers/${uid.customer}/quota-adjustments`,
    );
    assert.equal(page.data.data.items.length, 100);
    assert.ok(page.data.data.next_cursor);
    const next = await api(
      "agent",
      `/api/reseller/self/customers/${uid.customer}/quota-adjustments?after=${page.data.data.next_cursor}`,
    );
    assert.ok(next.data.data.items.length > 0);
    assert.equal(next.data.data.next_cursor, 0);
  });
  await step("configuration conflict returns current revision and metadata history", async () => {
    const path = `/api/reseller/self/customers/${uid.customer}`;
    const result = await api("agent", path + "/config", "PUT", { expected_revision: 0, items: [] });
    assert.equal(result.status, 409, result.text);
    assert.equal(result.data.code, "RESELLER_REVISION_CONFLICT");
    assert.equal(result.data.data.revision, 1);
    const workspace = await api("agent", path);
    assert.equal(workspace.data.data.config.revision, 1);
    assert.equal(workspace.data.data.history[0].revision, 1);
    assert.ok(!Object.hasOwn(workspace.data.data.history[0], "Snapshot"));
  });
  await step("model visibility denies unauthorized routing before reaching upstream", async () => {
    const count = upstreamCalls.length;
    const hidden = await consume("customer", "synthetic", false, "hidden-demo");
    assert.equal(hidden.status, 503, hidden.text);
    assert.equal(JSON.parse(hidden.text).error.code, "model_not_found");
    assert.equal(upstreamCalls.length, count);
    const models = await api("customer", "/api/user/models");
    assert.equal(models.status, 200, models.text);
    assert.ok(models.text.includes("reseller-demo"));
    assert.ok(!models.text.includes("hidden-demo"));
    const prices = await api("customer", "/api/pricing");
    assert.equal(prices.status, 200, prices.text);
    assert.ok(!prices.text.includes("synthetic-upstream-key"));
  });
  await step(
    "real upstream synchronous and streaming calls reconcile customer and procurement",
    async () => {
      await eventually(() => assert.equal(procurement().reserved, 0));
      const b = balance(uid.customer),
        a = Number(procurement().balance);
      const sync = await consume("customer");
      assert.equal(sync.status, 200, sync.text);
      assert.ok(sync.text.includes("Synthetic E2E response"));
      const stream = await consume("customer", "synthetic", true);
      assert.equal(stream.status, 200, stream.text);
      assert.ok(stream.text.includes("[DONE]"));
      assert.equal(balance(uid.customer), b - 180);
      await eventually(() => {
        assert.equal(Number(procurement().balance), a - 160);
        assert.equal(Number(procurement().reserved), 0);
      });
      const state = db
        .prepare(
          "SELECT COUNT(*) AS n FROM reseller_requests WHERE customer_user_id = ? AND state = ?",
        )
        .get(uid.customer, "settled");
      assert.ok(state.n >= 2);
    },
  );
  await step(
    "provider failure restores the full reservation without procurement leakage",
    async () => {
      const b = balance(uid.customer),
        a = Number(procurement().balance);
      const result = await consume("customer", "synthetic-failure");
      assert.equal(result.status, 500, result.text);
      await eventually(() => {
        assert.equal(balance(uid.customer), b);
        assert.equal(Number(procurement().balance), a);
        assert.equal(Number(procurement().reserved), 0);
      });
      assert.ok(
        db
          .prepare(
            "SELECT COUNT(*) AS n FROM reseller_requests WHERE customer_user_id = ? AND state = ?",
          )
          .get(uid.customer, "refunded").n > 0,
      );
    },
  );
  await step("ordinary personal multiplier has no reseller procurement effect", async () => {
    const b = balance(uid.ordinary),
      a = Number(procurement().balance);
    const result = await consume("ordinary");
    assert.equal(result.status, 200, result.text);
    await eventually(() => assert.equal(balance(uid.ordinary), b - 120));
    assert.equal(Number(procurement().balance), a);
  });
  await step("ledger JSON and CSV agree, and customer logs hide channel metadata", async () => {
    const until = Math.floor(Date.now() / 1000) + 1;
    const ledger = await api("agent", `/api/reseller/self/billing?until=${until}`),
      csv = await api("agent", `/api/reseller/self/billing/export?until=${until}`);
    assert.equal(ledger.status, 200, ledger.text);
    assert.equal(csv.status, 200, csv.text);
    assert.ok(csv.headers.get("content-type").includes("text/csv"));
    assert.equal(csv.text.trim().split("\n").length, ledger.data.data.items.length + 1);
    for (const row of ledger.data.data.items) assert.ok(csv.text.includes(row.request_id));
    const logs = await api("customer", "/api/log/self");
    assert.equal(logs.status, 200, logs.text);
    assert.ok(!logs.text.includes("channel_id"));
    assert.ok(!logs.text.includes("synthetic-upstream-key"));
    const all = await api("customer", "/api/log/self/stat"),
      filtered = await api("customer", `/api/log/self/stat?channel=${channel + 100}`);
    assert.deepEqual(filtered.data.data, all.data.data);
    const diagnostics = await api("root", `/api/reseller/admin/agents/${uid.agent}/diagnostics`);
    assert.equal(diagnostics.status, 200, diagnostics.text);
    assert.equal(diagnostics.data.data.ledger.consistent, true);
  });
  for (const scenario of [
    "immediate-success",
    "immediate-failure",
    "async-success",
    "async-failure",
  ])
    await step(
      `native task ${scenario} settles or refunds through the real scheduler and personal ledger`,
      async () => {
        await eventually(() => assert.equal(Number(procurement().reserved), 0));
        const b = balance(uid.customer),
          a = Number(procurement().balance);
        const response = await fetch(origin + "/reseller-e2e/jobs", {
          method: "POST",
          headers: {
            Authorization: "Bearer " + fixture.tokens.customer,
            "Content-Type": "application/json",
          },
          body: JSON.stringify({ model: "reseller-task", case: scenario }),
        });
        const text = await response.text();
        assert.equal(response.status, 200, text);
        const taskID = JSON.parse(text).task_id;
        assert.ok(taskID, text);
        const failure = scenario.endsWith("failure");
        await eventually(() => {
          const task = db.prepare("SELECT status,quota FROM tasks WHERE task_id = ?").get(taskID);
          assert.equal(task.status, failure ? "FAILURE" : "SUCCESS");
          assert.equal(task.quota, failure ? 0 : 90);
          assert.equal(balance(uid.customer), b - (failure ? 0 : 90));
          assert.equal(Number(procurement().balance), a - (failure ? 0 : 80));
          assert.equal(Number(procurement().reserved), 0);
        }, 60000);
        const query = await fetch(origin + "/reseller-e2e/jobs/" + taskID, {
          headers: { Authorization: "Bearer " + fixture.tokens.customer },
        });
        assert.equal(query.status, 200);
        assert.equal((await query.json()).task_id, taskID);
        const foreign = await fetch(origin + "/reseller-e2e/jobs/" + taskID, {
          headers: { Authorization: "Bearer " + fixture.tokens.ordinary },
        });
        assert.equal(foreign.status, 404);
      },
    );
  await step(
    "HTTP browser quota dialog uses accessible radio controls and idempotent receipts",
    async () => {
      const page = sessions.agent.page;
      await page.goto(httpOrigin + `/reseller/customers/${uid.customer}`);
      await page.getByRole("button", { name: "Adjust Quota", exact: true }).click();
      const dialog = page.getByRole("dialog", { name: "Adjust Quota", exact: true });
      await dialog.getByRole("radio", { name: "Subtract", exact: true }).click();
      await dialog.getByLabel("Amount", { exact: true }).fill("0.01");
      await dialog.getByLabel("Reason", { exact: true }).fill("E2E HTTP keyboard allocation");
      assert.equal(
        await dialog
          .getByRole("radio", { name: "Subtract", exact: true })
          .getAttribute("aria-checked"),
        "true",
      );
      const before = balance(uid.customer),
        a = procurement();
      await dialog.getByRole("button", { name: "Confirm", exact: true }).click();
      await dialog.waitFor({ state: "hidden" });
      assert.equal(balance(uid.customer), before - 5000);
      assert.deepEqual(procurement(), a);
      await page.getByRole("tab", { name: "Quota adjustments", exact: true }).click();
      await page.getByText("Synthetic E2E customer allocation", { exact: true }).waitFor();
      await page.getByRole("button", { name: "Next page", exact: true }).click();
      await page.getByText("E2E HTTP keyboard allocation", { exact: true }).waitFor();
      await page.getByRole("button", { name: "Next page", exact: true }).waitFor();
      await eventually(async () =>
        assert.equal(
          await page.getByRole("button", { name: "Next page", exact: true }).isDisabled(),
          true,
        ),
      );
      await page.screenshot({
        path: join(output, "http-quota-pagination.png"),
        fullPage: true,
        animations: "disabled",
      });
    },
  );
  await step(
    "browser 409 keeps draft, cancellation retains it and confirmed reload shows server revision",
    async () => {
      const page = sessions.agent.page,
        path = `/api/reseller/self/customers/${uid.customer}`;
      await page.getByRole("tab", { name: "Model authorization and ratios", exact: true }).click();
      const input = page.getByRole("textbox", {
        name: `Ratio for reseller-demo channel ${channel}`,
        exact: true,
      });
      await input.fill("1.1");
      const original = (await api("agent", path)).data.data.config;
      const changed = await api("agent", path + "/config", "PUT", {
        expected_revision: original.revision,
        items: [{ model: "reseller-demo", channel_id: channel, authorized: true, ratio: "0.95" }],
      });
      assert.equal(changed.status, 200, changed.text);
      await page.getByRole("button", { name: "Save changes", exact: true }).click();
      await page
        .getByText(
          `Configuration changed on the server (revision ${original.revision + 1}). Your draft is preserved.`,
          { exact: true },
        )
        .waitFor();
      assert.equal(await input.inputValue(), "1.1");
      await page
        .getByRole("tabpanel", { name: "Model authorization and ratios", exact: true })
        .getByRole("button", { name: "Reload configuration", exact: true })
        .click();
      let confirm = page.getByRole("alertdialog");
      await confirm.getByRole("button", { name: "Cancel", exact: true }).click();
      await confirm.waitFor({ state: "hidden" });
      assert.equal(await input.inputValue(), "1.1");
      await page
        .getByRole("tabpanel", { name: "Model authorization and ratios", exact: true })
        .getByRole("button", { name: "Reload configuration", exact: true })
        .click();
      confirm = page.getByRole("alertdialog");
      await confirm.getByRole("button", { name: "Reload configuration", exact: true }).click();
      await eventually(async () => assert.equal(await input.inputValue(), "0.95"));
      assert.equal(
        await page
          .getByRole("checkbox", {
            name: `Authorize reseller-demo channel ${channel}`,
            exact: true,
          })
          .isChecked(),
        true,
      );
      await page
        .getByRole("region", { name: "Configuration history", exact: true })
        .getByText(`Revision: ${original.revision + 1}`, { exact: false })
        .waitFor();
      await page.screenshot({
        path: join(output, "configuration-conflict-reloaded.png"),
        fullPage: true,
        animations: "disabled",
      });
      await input.fill("0.9");
      await page.getByRole("button", { name: "Save changes", exact: true }).click();
      await eventually(async () =>
        assert.equal((await api("agent", path)).data.data.config.revision, original.revision + 2),
      );
    },
  );
  await step(
    "mobile customer UI and restricted direct navigation remain bounded by role",
    async () => {
      const page = sessions.customer.page;
      await page.setViewportSize({ width: 390, height: 844 });
      await page.goto(origin + "/wallet");
      await page
        .getByText("reseller-demo: Personal price 0.9× (discount)", { exact: true })
        .first()
        .waitFor();
      assert.ok(!(await page.getByRole("link", { name: "Channel", exact: true }).count()));
      await page.goto(origin + "/reseller/agents");
      await page.waitForURL((url) => url.pathname !== "/reseller/agents");
      await page.screenshot({
        path: join(output, "customer-mobile.png"),
        fullPage: true,
        animations: "disabled",
      });
    },
  );
  await step(
    "revocation blocks new requests while an admitted request settles from its frozen prices",
    async () => {
      const b = balance(uid.customer),
        a = Number(procurement().balance),
        path = `/api/reseller/admin/agents/${uid.agent}`;
      const running = consume("customer", "synthetic-held");
      await eventually(() => assert.equal(typeof releaseHeld, "function"));
      const original = (await api("root", path)).data.data.config;
      try {
        const revoked = await api("root", path + "/config", "PUT", {
          expected_revision: original.revision,
          items: original.items.map((item) => ({ ...item, authorized: false })),
        });
        assert.equal(revoked.status, 200, revoked.text);
        const count = upstreamCalls.length,
          denied = await consume("customer");
        assert.equal(denied.status, 503, denied.text);
        assert.equal(upstreamCalls.length, count);
      } finally {
        releaseHeld();
        releaseHeld = undefined;
      }
      const response = await running;
      assert.equal(response.status, 200, response.text);
      assert.equal(balance(uid.customer), b - 90);
      await eventually(() => {
        assert.equal(Number(procurement().balance), a - 80);
        assert.equal(Number(procurement().reserved), 0);
      });
      const child = (await api("agent", `/api/reseller/self/customers/${uid.customer}`)).data.data
        .config;
      assert.equal(child.items[0].authorized, true);
      assert.equal(child.items[0].ratio, "0.9");
      const restored = await api("root", path + "/config", "PUT", {
        expected_revision: original.revision + 1,
        items: original.items,
      });
      assert.equal(restored.status, 200, restored.text);
      assert.equal((await consume("customer")).status, 200);
    },
  );
  await step(
    "agent and customer restrictions are independent and restoration preserves child restriction",
    async () => {
      const aPath = `/api/reseller/admin/agents/${uid.agent}/enabled`,
        bPath = `/api/reseller/self/customers/${uid.customer}/enabled`;
      const b = balance(uid.customer),
        count = upstreamCalls.length;
      assert.equal((await api("agent", bPath, "PUT", { enabled: false })).status, 200);
      assert.equal((await api("root", aPath, "PUT", { enabled: false })).status, 200);
      assert.equal((await consume("customer")).status, 401);
      assert.equal((await api("root", aPath, "PUT", { enabled: true })).status, 200);
      assert.equal((await consume("customer")).status, 401);
      assert.equal(balance(uid.customer), b);
      assert.equal(upstreamCalls.length, count);
      assert.equal((await api("agent", bPath, "PUT", { enabled: true })).status, 200);
      assert.equal((await consume("customer")).status, 200);
    },
  );
  await step(
    "procurement exhaustion denies new work before upstream and funding restores admission",
    async () => {
      await eventually(() => assert.equal(Number(procurement().reserved), 0));
      const remaining = Number(procurement().balance),
        path = `/api/reseller/admin/agents/${uid.agent}/quota-adjustments`;
      const zero = await api("root", path, "POST", {
        operation_id: "e2e-procurement-zero",
        mode: "subtract",
        quota: remaining,
        reason: "E2E exhaustion boundary",
      });
      assert.equal(zero.status, 200, zero.text);
      const b = balance(uid.customer),
        count = upstreamCalls.length,
        result = await consume("customer");
      assert.equal(result.status, 403, result.text);
      assert.equal(JSON.parse(result.text).error.code, "reseller_procurement_insufficient");
      assert.equal(balance(uid.customer), b);
      assert.equal(upstreamCalls.length, count);
      const restore = await api("root", path, "POST", {
        operation_id: "e2e-procurement-restore",
        mode: "add",
        quota: remaining,
        reason: "E2E restore procurement",
      });
      assert.equal(restore.status, 200, restore.text);
      assert.equal((await consume("customer")).status, 200);
      await eventually(() => assert.equal(Number(procurement().reserved), 0));
    },
  );
  for (const kind of ["agent", "customer"])
    await step(
      `new ${kind} activates by email, chooses password and completes mandatory authenticator enrollment`,
      async () => {
        const name = "new-" + kind,
          owner = kind === "agent" ? "root" : "agent",
          path = kind === "agent" ? "/api/reseller/admin/agents" : "/api/reseller/self/customers";
        const created = await api(owner, path, "POST", {
          username: "e2e-" + name,
          display_name: "E2E new " + kind,
          email: name + "@e2e.invalid",
        });
        assert.equal(created.status, 200, created.text);
        assert.equal(created.data.data.activation_mail_sent, true, created.text);
        uid[name] = created.data.data.user_id;
        const link = mailLink(name + "@e2e.invalid", "account_activation");
        const context = await browser.newContext({ locale: "en-US" });
        await context.addInitScript(() => {
          if (location.protocol === "http:" || location.protocol === "https:") {
            localStorage.setItem("i18nextLng", "en");
          }
        });
        context.on("page", observePage);
        const page = await context.newPage();
        page.setDefaultTimeout(15000);
        await page.goto(link);
        await page.getByLabel("New Password", { exact: true }).fill(password);
        const activationPromise = page.waitForResponse(
          (r) => r.url().endsWith("/api/security/activation") && r.request().method() === "POST",
        );
        await page.getByRole("button", { name: "Activate account", exact: true }).click();
        const activated = await (await activationPromise).json();
        assert.equal(activated.success, true, JSON.stringify(activated));
        assert.ok(!activated.data.access_token);
        await page.goto(origin + "/sign-in");
        await page.getByLabel("Username or Email", { exact: true }).fill("e2e-" + name);
        await page.getByLabel("Password", { exact: true }).fill(password);
        const loginPromise = page.waitForResponse(
          (r) => r.url().includes("/api/user/login?") && r.request().method() === "POST",
        );
        await page.getByRole("button", { name: "Sign in", exact: true }).click();
        const loginResult = await (await loginPromise).json();
        assert.equal(loginResult.success, true, JSON.stringify(loginResult));
        sessions[name] = { page, context, token: loginResult.data.access_token, base: origin };
        await page.waitForURL((url) => url.pathname === "/security-setup");
        const target = kind === "agent" ? "/api/reseller/self/customers" : "/api/token/";
        assert.equal((await api(name, target)).status, 403);
        const verified = await api(name, "/api/verify", "POST", {
          scope: "2fa.setup",
          method: "password",
          password,
        });
        assert.equal(verified.data.success, true, verified.text);
        const setup = await api(
          name,
          "/api/user/2fa/setup",
          "POST",
          {},
          { "X-Security-Proof": verified.data.data.proof_token },
        );
        assert.equal(setup.data.success, true, setup.text);
        const enabled = await api(name, "/api/user/2fa/enable", "POST", {
          flow_token: setup.data.data.flow_token,
          code: totp(setup.data.data.secret),
        });
        assert.equal(enabled.data.success, true, enabled.text);
        sessions[name].token = enabled.data.data.access_token;
        const complete = await api(name, "/api/security/completion");
        assert.equal(complete.data.data.complete, true);
        assert.equal((await api(name, target)).status, 200);
        assert.equal(
          db.prepare("SELECT activation FROM reseller_identities WHERE user_id = ?").get(uid[name])
            .activation,
          "active",
        );
        const replay = await api(name, "/api/user/2fa/enable", "POST", {
          flow_token: setup.data.data.flow_token,
          code: totp(setup.data.data.secret),
        });
        assert.equal(replay.data.success, false);
        await page.screenshot({
          path: join(output, "enrolled-" + kind + ".png"),
          fullPage: true,
          animations: "disabled",
        });
      },
    );
  await step(
    "independent Passkey recovery requires proof, not ordinary admin permission",
    async () => {
      const path = `/api/user/${uid.customer}/reset_passkey`,
        body = {
          incident: "E2E-PASSKEY",
          evidence: "Two independent synthetic identity references",
          confirmed: true,
          revoke_keys: false,
        };
      assert.equal((await api("admin", path, "DELETE", body)).status, 403);
      const missing = await api("root", path, "DELETE", body);
      assert.equal(missing.status, 403);
      assert.equal(missing.data.code, "SECURITY_PROOF_REQUIRED");
      const token = await proof("root", "account.security.passkey_recover", {
        user_id: uid.customer,
        incident: body.incident,
        revoke_keys: false,
      });
      const reset = await api("root", path, "DELETE", body, { "X-Security-Proof": token });
      assert.equal(reset.status, 200, reset.text);
      assert.equal(reset.data.success, true, reset.text);
      assert.equal(
        db
          .prepare(
            "SELECT COUNT(*) AS n FROM passkey_credentials WHERE user_id = ? AND deleted_at IS NULL",
          )
          .get(uid.customer).n,
        0,
      );
      assert.equal(
        db.prepare("SELECT is_enabled FROM two_fas WHERE user_id = ?").get(uid.customer).is_enabled,
        1,
      );
      assert.equal((await api("customer", "/api/user/self")).status, 401);
      assert.equal((await consume("customer")).status, 200);
      const replay = await api("root", path, "DELETE", body, { "X-Security-Proof": token });
      assert.equal(replay.data.code, "SECURITY_PROOF_CONSUMED");
      assert.ok(mail.some((item) => item.recipient === "customer@e2e.invalid"));
    },
  );

  await step(
    "admin email change verifies the new mailbox, notifies both addresses and rejects replay",
    async () => {
      const body = {
        email: "ordinary-new@e2e.invalid",
        incident: "E2E-EMAIL",
        evidence: "Two independent synthetic identity references",
        confirmed: true,
      };
      const token = await proof("root", "account.security.email_change", {
        user_id: uid.ordinary,
        email: body.email,
        incident: body.incident,
      });
      const start = await api(
        "root",
        `/api/security/admin/users/${uid.ordinary}/email-change`,
        "POST",
        body,
        { "X-Security-Proof": token },
      );
      assert.equal(start.status, 200, start.text);
      assert.equal(start.data.success, true, start.text);
      assert.equal(
        db.prepare("SELECT email FROM users WHERE id = ?").get(uid.ordinary).email,
        "ordinary@e2e.invalid",
      );
      const link = mailLink(body.email, "security_email_change");
      const page = await sessions.root.context.newPage();
      await page.goto(link);
      await page.getByRole("button", { name: "Verify Email", exact: true }).click();
      await page
        .getByText("Email verified. Continue with security setup.", { exact: true })
        .waitFor();
      assert.equal(
        db.prepare("SELECT email FROM users WHERE id = ?").get(uid.ordinary).email,
        body.email,
      );
      assert.equal(
        db.prepare("SELECT is_enabled FROM two_fas WHERE user_id = ?").get(uid.ordinary).is_enabled,
        1,
      );
      const input = Object.fromEntries(new URLSearchParams(new URL(link).hash.slice(1)));
      const replay = await api("root", "/api/security/email-change/complete", "POST", {
        user_id: Number(input.user_id),
        token: input.token,
      });
      assert.equal(replay.data.success, false);
      assert.ok(mail.filter((item) => item.recipient === "ordinary@e2e.invalid").length >= 2);
      assert.ok(mail.filter((item) => item.recipient === body.email).length >= 2);
    },
  );
  await step(
    "manual MFA recovery preserves API keys, revokes sessions and keeps enrollment mandatory",
    async () => {
      const body = {
        incident: "E2E-MFA",
        evidence: "Two independent synthetic identity references",
        confirmed: true,
        revoke_keys: false,
      };
      const token = await proof("root", "account.security.recover", {
        user_id: uid.ordinary,
        incident: body.incident,
        revoke_keys: false,
      });
      const reset = await api(
        "root",
        `/api/security/admin/users/${uid.ordinary}/recovery`,
        "POST",
        body,
        { "X-Security-Proof": token },
      );
      assert.equal(reset.status, 200, reset.text);
      assert.equal(reset.data.success, true, reset.text);
      assert.equal((await api("ordinary", "/api/user/self")).status, 401);
      assert.equal(
        db
          .prepare("SELECT COUNT(*) AS n FROM two_fas WHERE user_id = ? AND is_enabled = ?")
          .get(uid.ordinary, 1).n,
        0,
      );
      const previous = balance(uid.ordinary);
      assert.equal((await consume("ordinary")).status, 200);
      await eventually(() => assert.equal(balance(uid.ordinary), previous - 120));
    },
  );
  await step("completed agent can replace its authenticator with a scoped proof", async () => {
    const token = await proof("agent", "2fa.disable");
    const reset = await api(
      "agent",
      "/api/security/factor/replace",
      "POST",
      {},
      { "X-Security-Proof": token },
    );
    assert.equal(reset.status, 200, reset.text);
    assert.equal(reset.data.success, true, reset.text);
    sessions.agent.token = reset.data.data.access_token;
    const completion = await api("agent", "/api/security/completion");
    assert.equal(completion.data.data.complete, false);
    assert.equal((await api("agent", "/api/reseller/self/customers")).status, 403);
    assert.equal((await consume("customer")).status, 200);
  });

  await step(
    "gateway restart retains published balances and idempotent quota receipts",
    async () => {
      await eventually(() =>
        assert.equal(
          db.prepare("SELECT COUNT(*) AS n FROM reseller_events WHERE processed_at = 0").get().n,
          0,
        ),
      );
      for (const context of browser.contexts()) await context.close();
      const b = balance(uid.customer),
        a = procurement(),
        events = db.prepare("SELECT COUNT(*) AS n FROM reseller_events").get().n;
      gateway.kill("SIGTERM");
      await new Promise((resolve) => gateway.once("exit", resolve));
      gateway = spawn(
        binary,
        ["--port", String(port), "--log-dir", join(work, "gateway-restart-logs")],
        { cwd: work, env, stdio: ["ignore", "pipe", "pipe"] },
      );
      processes.push(gateway);
      gateway.stdout.on("data", (c) => {
        gatewayOutput += c;
      });
      gateway.stderr.on("data", (c) => {
        gatewayOutput += c;
      });
      await eventually(async () =>
        assert.equal((await fetch(apiOrigin + "/api/status")).status, 200),
      );
      assert.equal(balance(uid.customer), b);
      assert.deepEqual(procurement(), a);
      const retry = await api(
        "root",
        `/api/reseller/admin/agents/${uid.agent}/quota-adjustments`,
        "POST",
        {
          operation_id: "e2e-procurement-restore",
          mode: "add",
          quota: Number(
            db
              .prepare(
                "SELECT quota FROM reseller_quota_adjustments WHERE operation_id = 'e2e-procurement-restore'",
              )
              .get().quota,
          ),
          reason: "E2E restore procurement",
        },
      );
      assert.equal(retry.status, 200, retry.text);
      assert.deepEqual(procurement(), a);
      assert.equal(db.prepare("SELECT COUNT(*) AS n FROM reseller_events").get().n, events);
      const diagnostic = await api("root", `/api/reseller/admin/agents/${uid.agent}/diagnostics`);
      assert.equal(diagnostic.data.data.ledger.consistent, true);
    },
  );

  // Further domain cases are appended here; each uses production APIs and DB facts.
  await writeFile(join(output, "gateway.log"), gatewayOutput);
  db.close();
  completed = true;
} catch (error) {
  failure = error;
  console.error(error.stack);
  if (browser)
    for (const context of browser.contexts())
      for (const page of context.pages())
        await page
          .screenshot({
            path: join(output, `failure-${browser.contexts().indexOf(context)}.png`),
            fullPage: true,
            animations: "disabled",
          })
          .catch(() => {});
} finally {
  await writeFile(join(output, "gateway.log"), gatewayOutput);
  if (browser) await browser.close();
  for (const process of processes) process.kill("SIGTERM");
  await Promise.all(
    processes.map(
      (process) =>
        new Promise((resolve) => {
          if (process.exitCode !== null) resolve();
          else process.once("exit", resolve);
        }),
    ),
  );
  await new Promise((resolve) => provider.close(resolve));
  await new Promise((resolve) => smtp.close(resolve));
  await report();
}
if (failure || results.some((item) => item.status === "failed")) process.exitCode = 1;
