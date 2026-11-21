import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { once } from "node:events";
import { createServer, request } from "node:http";

const names = ["datastar.js", "marked.min.js", "terminal-view.js", "terminal.js"];
const sha256 = bytes => createHash("sha256").update(bytes).digest("hex");

async function snapshot(origin) {
  const response = await fetch(origin + "/docs");
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("Cache-Control"), "no-store");
  const html = await response.text();
  const entryPaths = [...html.matchAll(/<script\b[^>]*\bsrc="([^"]+)"/gu)].map(match => match[1]);
  assert.equal(entryPaths.length, 3);
  const base = entryPaths[0].replace(/[^/]+$/u, "");
  assert.match(base, /^\/assets\/lsget-shell\/[0-9a-f]{64}\/$/u);
  assert.deepEqual([...entryPaths].sort(), names.filter(name => name !== "terminal-view.js").map(name => base + name));
  const assets = {};
  for (const name of names) {
    const path = base + name;
    const result = await fetch(origin + path);
    const bytes = Buffer.from(await result.arrayBuffer());
    const digest = sha256(bytes);
    assert.equal(result.status, 200, path);
    assert.equal(result.headers.get("Content-Type"), "application/javascript; charset=utf-8", path);
    assert.equal(result.headers.get("Cache-Control"), "public, max-age=31536000, immutable", path);
    assert.equal(result.headers.get("X-Content-Type-Options"), "nosniff", path);
    const etag = result.headers.get("ETag");
    assert.equal(etag, '"' + digest + '"', path);

    const head = await fetch(origin + path, { method: "HEAD" });
    assert.equal(head.status, 200, path);
    assert.equal(head.headers.get("Content-Length"), String(bytes.length), path);
    assert.equal(head.headers.get("ETag"), etag, path);
    assert.equal(await head.text(), "", path);
    const cached = await fetch(origin + path, { headers: { "If-None-Match": etag } });
    assert.equal(cached.status, 304, path);
    assert.equal(cached.headers.get("ETag"), etag, path);
    assert.equal(await cached.text(), "", path);
    assets[name] = { path, digest, etag };
  }
  return { base, html, headers: Object.fromEntries(response.headers), entryPaths, assets };
}

export async function verifyShellUpgrade(t, cleanups, page, previous, current) {
  const before = await snapshot(previous);
  const after = await snapshot(current);
  assert.notEqual(before.base, after.base);
  assert.notEqual(before.assets["terminal.js"].digest, after.assets["terminal.js"].digest);

  // Cross-release requests must fail even if an unchanged file has an ETag
  // that would normally satisfy the supplied conditional request.
  for (const [origin, stale] of [[current, before], [previous, after]]) {
    for (const { path, etag } of Object.values(stale.assets)) {
      const result = await fetch(origin + path, { headers: { "If-None-Match": etag } });
      assert.equal(result.status, 404, path);
      assert.equal(result.headers.get("Cache-Control"), "no-store", path);
      assert.equal(result.headers.get("ETag"), null, path);
      assert.match(await result.text(), /404 page not found/u);
    }
  }

  let upstream = new URL(previous);
  const responses = [];
  const relay = createServer((client, response) => {
    if (client.url === "/docs?replay-stale-shell") {
      response.writeHead(200, before.headers);
      response.end(before.html);
      return;
    }
    const forwarded = request({
      hostname: upstream.hostname,
      port: upstream.port,
      path: client.url,
      method: client.method,
      headers: client.headers,
    }, result => {
      responses.push({ path: client.url, status: result.statusCode });
      response.writeHead(result.statusCode, result.headers);
      result.pipe(response);
    });
    forwarded.on("error", () => {
      response.writeHead(502);
      response.end();
    });
    client.pipe(forwarded);
  });
  relay.listen(0, "127.0.0.1");
  await once(relay, "listening");
  cleanups.push(() => new Promise(resolve => {
    relay.close(resolve);
    relay.closeAllConnections();
  }));
  const address = "http://127.0.0.1:" + relay.address().port;
  const appReady = search => "location.search === " + JSON.stringify(search) +
    ' && document.querySelector("#readme h1")?.textContent === "Markdown fixture"';
  const assetResponses = () => responses.filter(row => row.path.startsWith("/assets/lsget-shell/"));
  const paths = snapshot => names.map(name => snapshot.assets[name].path);
  const assertAssets = (expected, status) => {
    assert.deepEqual(assetResponses().map(row => row.path).sort(), [...expected].sort());
    assert.ok(assetResponses().every(row => row.status === status));
  };

  await page.open(address + "/docs?prime-old-assets");
  await page.wait(appReady("?prime-old-assets"));
  assertAssets(paths(before), 200);
  responses.length = 0;
  await page.open(address + "/docs?warm-old-assets");
  await page.wait(appReady("?warm-old-assets"));
  assertAssets([], 200);

  // Keep the same origin, Chrome profile, and warm cache while replacing the
  // upstream binary. The new shell must request every new URL.
  upstream = new URL(current);
  responses.length = 0;
  await page.open(address + "/docs?upgraded-shell");
  await page.wait(appReady("?upgraded-shell"));
  assertAssets(paths(after), 200);

  // A stale shell with an absent cache must fail closed. A fresh navigation
  // at the same origin recovers the current shell.
  await page.send("Network.enable");
  await page.send("Network.setCacheDisabled", { cacheDisabled: true });
  try {
    responses.length = 0;
    await page.open(address + "/docs?replay-stale-shell");
    await page.wait('location.search === "?replay-stale-shell" && document.readyState === "complete"');
    assertAssets(before.entryPaths, 404);
    assert.equal(await page.evaluate('document.querySelector("#command-prompt").textContent'), "");

    responses.length = 0;
    await page.open(address + "/docs?refreshed-shell");
    await page.wait(appReady("?refreshed-shell"));
    assertAssets(paths(after), 200);
  } finally {
    await page.send("Network.setCacheDisabled", { cacheDisabled: false });
    await page.send("Network.disable");
  }
  t.diagnostic(JSON.stringify({
    previous: before.assets, current: after.assets,
    observed: ["old-cache-hit", "upgraded-shell", "stale-shell-assets-rejected", "refreshed-shell"],
  }));
}
