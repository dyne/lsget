// Run with LSGET_BINARY=/absolute/path/to/lsget node --test tests/browser/rendering.test.mjs.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, mkdir, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import { createServer as createHTTPServer } from "node:http";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { test } from "node:test";
import { setTimeout as delay } from "node:timers/promises";
import { verifyShellUpgrade } from "./shell-upgrade.mjs";

async function until(probe, description) {
  const deadline = Date.now() + 8_000;
  while (Date.now() < deadline) {
    const result = await probe();
    if (result) return result;
    await delay(50);
  }
  assert.fail("Timed out waiting for " + description);
}

function start(cleanups, command, args, cwd, env = process.env) {
  const child = spawn(command, args, { cwd, env, stdio: ["ignore", "ignore", "pipe"] });
  const stopped = new Promise((resolve) => child.once("close", resolve));
  let startError;
  child.once("error", (error) => { startError = error; });
  let diagnostics = "";
  child.stderr.setEncoding("utf8").on("data", (text) => {
    diagnostics = (diagnostics + text).slice(-1_500);
  });
  cleanups.push(async () => {
    child.kill();
    const force = setTimeout(() => child.kill("SIGKILL"), 2_000);
    try {
      await stopped;
    } finally {
      clearTimeout(force);
    }
  });
  return () => {
    if (startError) throw startError;
    assert.equal(child.exitCode, null, command + " exited early: " + diagnostics);
  };
}

async function browserPage(cleanups, scratch) {
  const deniedRequests = [];
  const proxy = createHTTPServer((request, response) => {
    deniedRequests.push(request.url);
    response.writeHead(403, { "Content-Type": "text/plain" });
    response.end("Outside test listeners blocked");
  });
  proxy.on("connect", (request, socket) => {
    deniedRequests.push(request.url);
    socket.end("HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n");
  });
  proxy.listen(0, "127.0.0.1");
  await once(proxy, "listening");
  cleanups.push(() => new Promise(resolve => {
    proxy.close(resolve);
    proxy.closeAllConnections();
  }));
  const profile = join(scratch, "chrome");
  const running = start(cleanups, process.env.CHROME_BIN || "google-chrome", [
    ...(process.env.LSGET_TEST_HEADFUL
      ? ["--ozone-platform=x11", "--force-renderer-accessibility", "--window-size=1280,900"] : ["--headless"]),
    "--no-first-run", "--disable-background-networking",
    "--proxy-server=http://127.0.0.1:" + proxy.address().port,
    "--proxy-bypass-list=127.0.0.1",
    "--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1",
    "--user-data-dir=" + profile, "--remote-debugging-port=0", "about:blank",
  ], scratch);
  const endpoint = await until(async () => {
    running();
    try {
      return (await readFile(join(profile, "DevToolsActivePort"), "utf8")).split("\n")[0];
    } catch (error) {
      if (error.code !== "ENOENT") throw error;
    }
  }, "Chrome DevTools endpoint");
  const targets = await fetch("http://127.0.0.1:" + endpoint + "/json/list", {
    signal: AbortSignal.timeout(3_000),
  }).then((r) => r.json());
  const url = targets.find((target) => target.type === "page")?.webSocketDebuggerUrl;
  assert.ok(url, "Expected a page in Chrome");
  const socket = new WebSocket(url);
  await once(socket, "open", { signal: AbortSignal.timeout(3_000) });
  cleanups.push(() => socket.close());

  let id = 0;
  const pending = new Map();
  const policyMessages = [];
  socket.addEventListener("message", (event) => {
    const response = JSON.parse(event.data);
    if (response.method === "Log.entryAdded" && response.params.entry.source === "security") {
      policyMessages.push(response.params.entry.text);
    }
    pending.get(response.id)?.(response);
  });

  function send(method, params = {}) {
    return new Promise((resolve, reject) => {
      const key = ++id;
      const timeout = setTimeout(() => {
        pending.delete(key);
        reject(new Error("No Chrome response to " + method));
      }, 3_000);
      pending.set(key, ({ result, error }) => {
        clearTimeout(timeout);
        pending.delete(key);
        if (error) reject(new Error(method + ": " + error.message));
        else resolve(result);
      });
      socket.send(JSON.stringify({ id: key, method, params }));
    });
  }

  async function evaluate(expression, ...args) {
    if (typeof expression === "function") {
      expression = "(" + expression.toString() + ")(" + args.map((arg) => JSON.stringify(arg)).join(",") + ")";
    }
    const { result, exceptionDetails } = await send("Runtime.evaluate", {
      expression, awaitPromise: true, returnByValue: true, userGesture: true,
    });
    assert.ok(!exceptionDetails, result?.description || exceptionDetails?.text);
    return result.value;
  }

  await send("Log.enable");
  return {
    async open(url) {
      const result = await send("Page.navigate", { url });
      assert.equal(result.errorText, undefined);
    },
    evaluate,
    send,
    deniedRequests,
    policyMessages,
    wait(expression) {
      return until(() => evaluate(expression), expression);
    },
  };
}

async function testAddress() {
  const socket = createServer();
  socket.listen({ host: "127.0.0.1", port: 0 });
  await once(socket, "listening");
  const address = "127.0.0.1:" + socket.address().port;
  await new Promise((resolve, reject) => socket.close((error) => error ? reject(error) : resolve()));
  return address;
}

async function startServer(cleanups, binary, site, cwd, mode, template = "", args = []) {
  const address = await testAddress();
  const origin = "http://" + address;
  const running = start(cleanups, binary, [
    "-dir", site, "-addr", address, "-pid=", "-logfile=", "-baseurl=", "-sitemap=0",
    "-shell-csp", mode, "-dev-template", template, ...args,
  ], cwd, { ...process.env, LSGET_SITE_TITLE: "", LSGET_SITE_DESCRIPTION: "", LSGET_PREVIEW_IMAGE: "" });
  await until(() => {
    running();
    return fetch(origin, { signal: AbortSignal.timeout(500) }).then((r) => r.ok, () => false);
  }, "lsget listener");
  return origin;
}

async function fixture(root) {
  const punctuation = 'A < B & "quoted"';
  const file = "notes & 'quote'.txt";
  await mkdir(join(root, "docs", "guide"), { recursive: true });
  await mkdir(join(root, "rst"));
  await mkdir(join(root, "empty"));
  await mkdir(join(root, "below"));
  await mkdir(join(root, "above"));
  await writeFile(join(root, "docs", "README.md"), [
    "# Markdown fixture", "", "1. **First**", "2. *Second*", "",
    "- Unordered", "", "- [x] Task", "", "\x60" + punctuation + "\x60", "",
    "[Local guide](guide/)", "[HTTPS](https://example.com/guide)", "[Email](mailto:docs@example.com)",
    "[Unsupported](ftp://example.com/readme)", "[Fragment](#section)", "",
    "![Preview](sample.svg)", "", '<span class="decoration" data-note="demo">Plain span.</span>',
    "", "<details><summary>Harmless content.</summary></details>",
    "", '<h2 id="section">Fragment target</h2>',
  ].join("\n"));
  await writeFile(join(root, "docs", "guide", "index.html"), "<h1>Published guide</h1>");
  await writeFile(join(root, "docs", "sample.svg"), '<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10" fill="blue"/></svg>');
  await writeFile(join(root, "docs", "read me two.txt"), "Second path suggestion opened.");
  await writeFile(join(root, "docs", "read-me-one.txt"), "First path suggestion opened.");
  await writeFile(join(root, "rst", "README.rst"), [
    "RST fixture", "===========", "", "**Bold** and *italic*.", "",
    "\x60Relative & quoted <../docs/guide/>\x60_",
    "\x60Unsupported RST <ftp://example.com/readme>\x60_", "",
    "Punctuation::", "", "    " + punctuation,
  ].join("\n"));
  await writeFile(join(root, "below", "README.txt"), "x".repeat(256 * 1024 - 1));
  await writeFile(join(root, "above", "README.txt"), "y".repeat(256 * 1024) + "!");
  await writeFile(join(root, file), "\x1b[34m" + punctuation + "\x1b[0m \x1b[5munsupported style\x1b[0m\n");
  return { file, punctuation };
}

test("Terminal and README rendering in Chrome", { timeout: 45_000 }, async (t) => {
  assert.ok(process.env.LSGET_BINARY, "Set LSGET_BINARY to the built lsget executable");
  const binary = resolve(process.env.LSGET_BINARY);
  const scratch = await mkdtemp(join(tmpdir(), "lsget-render-browser-"));
  const cleanups = [];
  t.after(async () => {
    try {
      for (const cleanup of cleanups.reverse()) await cleanup();
    } finally {
      await rm(scratch, { recursive: true, force: true, maxRetries: 3, retryDelay: 100 });
    }
    t.diagnostic("Owned browser and server processes settled, local listeners closed, and scratch removed.");
  });
  const site = join(scratch, "served");
  const { file, punctuation } = await fixture(site);
  const workA = join(scratch, "work-a");
  const workB = join(scratch, "work-b");
  await mkdir(workA);
  await mkdir(workB);
  await writeFile(join(workB, "index.html"), "<title>Local B</title>");
  const mode = process.env.LSGET_TEST_CSP || "enforce";
  const origin = await startServer(cleanups, binary, site, workA, mode);

  const page = await browserPage(cleanups, scratch);
  async function key(key, windowsVirtualKeyCode, modifiers = 0) {
    const options = { key, code: key, windowsVirtualKeyCode, modifiers };
    const text = key === "Enter" ? { text: "\r", unmodifiedText: "\r" } : {};
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", ...options, ...text });
    await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...options });
  }

  async function speechOffset() {
    return process.env.LSGET_ORCA_LOG ? (await stat(process.env.LSGET_ORCA_LOG)).size : 0;
  }

  async function expectSpeech(utterance, offset = 0) {
    if (!process.env.LSGET_ORCA_LOG) return;
    await until(async () => {
      const log = (await readFile(process.env.LSGET_ORCA_LOG)).subarray(offset).toString("utf8");
      return log.split("\n").some(line => line.includes("SPEECH OUTPUT:") && line.includes(utterance));
    }, "Orca speech output containing " + utterance);
  }

  function command(input) {
    return page.evaluate((input) => {
      const field = document.getElementById("command");
      field.value = input;
      field.dispatchEvent(new Event("input", { bubbles: true }));
      field.form.requestSubmit();
    }, input);
  }

  await t.test("Chrome can reach only the local test listeners", async () => {
    const outside = "http://outside.lsget-test.invalid/network-probe";
    await page.open(outside);
    await page.wait('document.body?.textContent === "Outside test listeners blocked"');
    assert.ok(page.deniedRequests.includes(outside));
  });

  await t.test("Markdown renders only supported elements and navigation targets", async () => {
    await page.open(origin + "/docs");
    await page.wait('document.querySelector("#readme h1")?.textContent === "Markdown fixture"');
    const dom = await page.evaluate(() => {
      const panel = document.getElementById("readme");
      return {
        ordered: Array.from(panel.querySelectorAll("ol li"), (item) => item.textContent),
        strong: panel.querySelector("strong")?.textContent,
        italic: panel.querySelector("em")?.textContent,
        bullet: Array.from(panel.querySelectorAll("ul li"), (item) => item.textContent.trim()),
        checked: panel.querySelector("input")?.checked && panel.querySelector("input").disabled,
        code: panel.querySelector("code")?.textContent,
        urls: Object.fromEntries(Array.from(panel.querySelectorAll("a"), (link) => [link.textContent, link.getAttribute("href")])),
        image: panel.querySelector("img")?.src,
        fragmentTarget: panel.querySelector("h2")?.id,
        attributes: panel.querySelector("span")?.getAttributeNames(),
        unsupported: panel.querySelectorAll("details, summary").length,
        retainedText: panel.textContent.includes("Harmless content."),
      };
    });
    assert.deepEqual(dom, {
      ordered: ["First", "Second"], strong: "First", italic: "Second",
      bullet: ["Unordered", "Task"], checked: true, code: punctuation,
      urls: {
        "Local guide": origin + "/docs/guide/", HTTPS: "https://example.com/guide",
        Email: "mailto:docs@example.com", Unsupported: null, Fragment: origin + "/docs#readme-anchor-0",
      },
      image: origin + "/docs/sample.svg", fragmentTarget: "readme-anchor-0",
      attributes: [], unsupported: 0, retainedText: true,
    });
    t.diagnostic(JSON.stringify(dom));
    await page.evaluate(() => {
      Array.from(document.querySelectorAll("#readme a")).find((a) => a.textContent === "Fragment").click();
    });
    await page.wait('location.hash === "#readme-anchor-0" && document.querySelector(":target")?.textContent === "Fragment target"');
    await page.evaluate(() => {
      Array.from(document.querySelectorAll("#readme a")).find((a) => a.textContent === "Local guide").click();
    });
    await page.wait('document.querySelector("h1")?.textContent === "Published guide"');
    assert.equal(await page.evaluate("location.pathname"), "/docs/guide/");
  });

  await t.test("RST preserves text and supported destinations", async () => {
    await page.open(origin + "/rst");
    await page.wait('document.querySelector("#readme h1")?.textContent === "RST fixture"');
    const dom = await page.evaluate(() => {
      const panel = document.getElementById("readme");
      return {
        strong: panel.querySelector("strong")?.textContent,
        italic: panel.querySelector("em")?.textContent,
        code: panel.querySelector("code")?.textContent,
        urls: Object.fromEntries(Array.from(panel.querySelectorAll("a"), (link) => [link.textContent, link.getAttribute("href")])),
      };
    });
    assert.deepEqual(dom, {
      strong: "Bold", italic: "italic", code: punctuation,
      urls: { "Relative & quoted": origin + "/docs/guide/", "Unsupported RST": null },
    });
    t.diagnostic(JSON.stringify(dom));
  });

  await t.test("README byte cap is visible on initial load and directory change", async () => {
    await page.open(origin + "/below");
    await page.wait('document.querySelector("#readme pre")?.textContent.length === 262143');
    assert.equal(await page.evaluate('document.querySelector("#readme .readme-notice")'), null);
    await command("cd /above");
    await page.wait('document.querySelector("#readme .readme-notice")?.textContent === "README preview truncated."');
    assert.equal(await page.evaluate('document.querySelector("#readme pre").textContent.length'), 262144);
    assert.equal(await page.evaluate('document.querySelector("#readme pre").textContent.endsWith("!")'), false);
    await command("cd /empty");
    await page.wait('location.pathname === "/empty" && document.querySelector("#readme").childNodes.length === 0');
    t.diagnostic("Below: 262143 bytes, no notice. Above: 262144 preview bytes, notice. Empty directory clears preview.");
  });

  await t.test("Terminal filenames and supported ANSI remain literal text", async () => {
    await page.open(origin + "/");
    const item = "#transcript .clickable-file";
    await page.wait('Array.from(document.querySelectorAll(".clickable-file")).some(item => item.dataset.file === ' + JSON.stringify("/" + file) + ')');
    const found = await page.evaluate((selector, name) => {
      const link = Array.from(document.querySelectorAll(selector)).find((node) => node.dataset.file === "/" + name);
      const result = { text: link.textContent, children: link.children.length };
      link.click();
      return result;
    }, item, file);
    assert.deepEqual(found, { text: "/" + file, children: 0 });
    await page.wait('Array.from(document.querySelectorAll("#transcript .out")).some(line => line.textContent.includes("unsupported style"))');
    const dom = await page.evaluate(() => {
      const line = Array.from(document.querySelectorAll("#transcript .out")).find((node) => node.textContent.includes("unsupported style"));
      return { text: line.textContent, color: line.firstChild.style.color, styled: line.children.length };
    });
    assert.deepEqual(dom, { text: punctuation + " unsupported style\n", color: "rgb(140, 170, 238)", styled: 1 });
    t.diagnostic(JSON.stringify(dom));
  });

  await t.test("Help and image previews render through terminal controls", async () => {
    assert.equal(await page.evaluate('document.querySelector("#transcript strong")?.textContent'), "help");
    await command("help");
    await page.wait('document.querySelectorAll("#transcript .line strong").length > 15');
    await command("cat /docs/sample.svg");
    await page.wait('document.querySelector("#transcript .terminal-image img")?.naturalWidth === 10');
    const preview = await page.evaluate(() => {
      const node = document.querySelector("#transcript .terminal-image");
      return {
        src: node.querySelector("img").src,
        alt: node.querySelector("img").alt,
        href: node.querySelector("a").href,
        caption: node.querySelector("div").textContent,
      };
    });
    assert.equal(preview.src, origin + "/docs/sample.svg");
    assert.equal(preview.href, preview.src);
    assert.equal(preview.alt, "sample.svg");
    assert.match(preview.caption, /^📷 sample\.svg \(/u);
    t.diagnostic(JSON.stringify(preview));
  });

  await t.test("Typing, command recall, and browser history work under the shell policy", async () => {
    await page.open(origin + "/rst");
    await page.wait('document.querySelector("#readme h1")?.textContent === "RST fixture"');
    await page.send("Input.insertText", { text: "pwd" });
    await page.wait('document.querySelector("#command").value === "pwd"');
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", windowsVirtualKeyCode: 13 });
    await page.wait('Array.from(document.querySelectorAll("#transcript .out")).some(node => node.textContent === "/rst")');
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", key: "ArrowUp", windowsVirtualKeyCode: 38 });
    await page.wait('document.querySelector("#command").value === "pwd"');
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", key: "u", modifiers: 2, windowsVirtualKeyCode: 85 });
    await command("cd /docs");
    await page.wait('location.pathname === "/docs" && document.querySelector("#readme h1")?.textContent === "Markdown fixture"');
    await page.evaluate("history.back()");
    await page.wait('location.pathname === "/rst" && document.querySelector("#readme h1")?.textContent === "RST fixture"');
    await page.evaluate("history.forward()");
    await page.wait('location.pathname === "/docs" && document.querySelector("#readme h1")?.textContent === "Markdown fixture"');
    t.diagnostic("Typed pwd, recalled with ArrowUp, changed directory, and restored README on browser Back and Forward.");
  });

  await t.test("Deep links, raced navigation, and completion keep the latest directory and input", async () => {
    await page.send("Page.enable");
    const instrumentation = await page.send("Page.addScriptToEvaluateOnNewDocument", {
      source: "(" + function () {
        const fetchPage = window.fetch.bind(window);
        const holds = new Map();
        const armed = new Set();
        const requests = [];
        const settled = new Set();
        window.navigationFixture = {
          arm(key) { armed.add(key); },
          release(key) { holds.get(key)?.(); },
          releaseAll() { holds.forEach(release => release()); armed.clear(); },
          holds, requests, settled,
        };
        window.fetch = async (url, options) => {
          const endpoint = new URL(url, location.origin).pathname;
          const data = options?.body && JSON.parse(options.body);
          const key = endpoint === "/api/complete" ? "completion" : data?.input;
          requests.push(endpoint);
          const response = await fetchPage(url, options);
          if (armed.delete(key)) {
            // Buffer the response so aborting the original network request
            // cannot prevent late data reaching the application.
            const late = new Response(await response.text(), { status: response.status, headers: response.headers });
            await new Promise(resolve => holds.set(key, resolve));
            holds.delete(key);
            settled.add(key);
            return late;
          }
          return response;
        };
      }.toString() + ")()",
    });
    const directoryIs = (path, readme) => page.wait([
      'location.pathname === ' + JSON.stringify(path),
      'document.querySelector("#command-prompt")?.textContent === ' + JSON.stringify("guest@browser:" + path + "$ "),
      'document.querySelector("#readme h1")?.textContent === ' + JSON.stringify(readme),
      'document.activeElement?.id === "command"',
    ].join(" && "));

    try {
      // The preceding history test finishes at /docs. Wait for the distinct
      // deep link and its document-start instrumentation before checking it.
      await page.open(origin + "/docs?navigation-fixture=1");
      await page.wait('location.search === "?navigation-fixture=1" && !!window.navigationFixture');
      await directoryIs("/docs", "Markdown fixture");
      await page.wait('document.querySelector("#transcript .clickable-file")');
      assert.deepEqual(await page.evaluate("navigationFixture.requests"), ["/api/config", "/api/exec"]);
      const historyLength = await page.evaluate("history.length");

      await page.evaluate('navigationFixture.arm("cd /rst")');
      await command("cd /rst");
      await page.wait('navigationFixture.holds.has("cd /rst")');
      await command("cd /below");
      const below = 'location.pathname === "/below" && document.querySelector("#command-prompt").textContent === "guest@browser:/below$ " && document.querySelector("#readme pre")?.textContent.length === 262143';
      await page.wait(below);
      await page.evaluate(async () => {
        window.navigationFixture.release("cd /rst");
        await new Promise(requestAnimationFrame);
      });
      assert.equal(await page.evaluate(below), true);
      assert.equal(await page.evaluate("history.length"), historyLength + 1);
      await page.evaluate("history.back()");
      await directoryIs("/docs", "Markdown fixture");
      await page.evaluate("history.forward()");
      await page.wait(below);
      assert.equal(await page.evaluate("history.length"), historyLength + 1);
      await page.evaluate("history.back()");
      await directoryIs("/docs", "Markdown fixture");
      await page.wait('document.querySelector("#transcript .clickable-file")');
      await command("cat README.md");
      await page.wait('Array.from(document.querySelectorAll("#transcript .out")).some(line => line.textContent.startsWith("# Markdown fixture"))');

      await page.evaluate('navigationFixture.arm("completion")');
      await page.send("Input.insertText", { text: "cat RE" });
      await page.evaluate('document.querySelector("#complete-command").click()');
      await page.wait('navigationFixture.holds.has("completion")');
      await page.evaluate(() => {
        window.savedTranscript = [...document.querySelector("#transcript").children];
      });
      await page.send("Input.insertText", { text: "X" });
      await page.evaluate(async () => {
        window.navigationFixture.release("completion");
        await new Promise(requestAnimationFrame);
      });
      assert.deepEqual(await page.evaluate(() => ({
        input: document.querySelector("#command").value,
        suggestionsClosed: document.querySelector("#completions").hidden &&
          document.querySelector("#completion-options").children.length === 0,
        sameScrollback: window.savedTranscript.every((node, index) => node === document.querySelector("#transcript").children[index]),
        held: window.navigationFixture.holds.size,
        settled: [...window.navigationFixture.settled],
      })), {
        input: "cat REX", suggestionsClosed: true, sameScrollback: true, held: 0,
        settled: ["cd /rst", "completion"],
      });
      await page.send("Input.dispatchKeyEvent", { type: "keyDown", key: "u", modifiers: 2, windowsVirtualKeyCode: 85 });
      await page.send("Input.insertText", { text: "cat RE" });
      await page.evaluate('document.querySelector("#complete-command").click()');
      await page.wait('document.querySelector("#command").value === "cat README.md"');
      t.diagnostic("Two real API responses held and released. URL, prompt, README, history length, focused input and scrollback identity verified.");
    } finally {
      try {
        await page.evaluate(async () => {
          window.navigationFixture?.releaseAll();
          await new Promise(requestAnimationFrame);
          if (window.navigationFixture?.holds.size) throw new Error("Held browser response did not settle");
        });
      } finally {
        await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier: instrumentation.identifier });
        await page.send("Page.disable");
      }
    }
  });

  await t.test("Offline Back and clipboard denial leave clear recovery paths", async () => {
    await page.open(origin + "/docs");
    await page.wait('document.querySelector("#readme h1")?.textContent === "Markdown fixture"');
    await command("cd /rst");
    await page.wait('location.pathname === "/rst" && document.querySelector("#readme h1")?.textContent === "RST fixture"');

    await page.send("Network.enable");
    const connection = { latency: 0, downloadThroughput: -1, uploadThroughput: -1 };
    try {
      await page.send("Network.emulateNetworkConditions", { offline: true, ...connection });
      await page.evaluate("history.back()");
      await page.wait('location.pathname === "/docs" && !document.querySelector("#directory-error").hidden');
      assert.deepEqual(await page.evaluate(() => [
        document.querySelector("#command-prompt").textContent,
        document.querySelector("#readme").textContent,
        document.activeElement?.id,
      ]), ["guest@browser:/docs$ ", "", "command"]);
      await command("pwd");
      await page.wait('document.querySelector("#transcript").textContent.includes("Could not run the command")');
      await expectSpeech("Could not run the command");
    } finally {
      await page.send("Network.emulateNetworkConditions", { offline: false, ...connection });
      await page.send("Network.disable");
    }
    await page.evaluate('document.querySelector("#retry-directory").click()');
    await page.wait('document.querySelector("#directory-error").hidden && document.querySelector("#readme h1")?.textContent === "Markdown fixture"');
    assert.equal(await page.evaluate("document.activeElement?.id"), "command");
    await command("pwd");
    await page.wait('Array.from(document.querySelectorAll("#transcript .out")).some(line => line.textContent === "/docs")');

    await page.evaluate(() => {
      Object.defineProperty(navigator, "clipboard", {
        configurable: true,
        value: { writeText: async () => { throw new Error("Permission denied"); } },
      });
    });
    try {
      await command("share README.md");
      await page.wait('document.querySelector("#transcript").textContent.includes("Select it above to copy it manually.")');
      const output = await page.evaluate('document.querySelector("#transcript").textContent');
      assert.equal(await page.evaluate('document.querySelector(".share-controls input").value'), origin + "/docs/README.md");
      assert.ok(!output.includes("URL copied to clipboard"));
      await page.evaluate(() => {
        navigator.clipboard.writeText = async (url) => { window.copiedURL = url; };
      });
      await command("share README.md");
      await page.wait('document.querySelector("#transcript").textContent.includes("URL copied to clipboard.")');
      assert.equal(await page.evaluate("window.copiedURL"), origin + "/docs/README.md");
    } finally {
      await page.evaluate("delete navigator.clipboard");
    }
    t.diagnostic("Offline Back retained URL, prompt and focus, and retry restored README. Clipboard denial left the URL visible and a successful write received that URL.");
  });

  await t.test("A terminal download saves the served bytes", async () => {
    const downloads = join(scratch, "downloads");
    await mkdir(downloads);
    await page.send("Browser.setDownloadBehavior", { behavior: "allow", downloadPath: downloads });
    await command('get "/' + file + '"');
    const body = await until(async () => {
      try {
        return await readFile(join(downloads, file), "utf8");
      } catch (error) {
        if (error.code !== "ENOENT") throw error;
      }
    }, "completed browser download");
    assert.equal(body, "\x1b[34m" + punctuation + "\x1b[0m \x1b[5munsupported style\x1b[0m\n");
    t.diagnostic("Chrome saved the exact file bytes from the terminal download command.");
  });

  await t.test("Keyboard completion, focus, live status and transfer controls", async () => {
    await page.open(origin + "/docs?keyboard-fixture=1");
    await page.send("Page.bringToFront");
    await page.wait('location.search === "?keyboard-fixture=1" && document.activeElement?.id === "command" && document.querySelector("#readme h1")?.textContent === "Markdown fixture"');
    const focus = () => page.evaluate(() => document.activeElement.id);
    await expectSpeech("Terminal command");
    await key("Tab", 9);
    assert.equal(await focus(), "complete-command");
    await key("Tab", 9, 8);
    assert.equal(await focus(), "command");
    await key("Tab", 9, 8);
    assert.equal(await focus(), "transcript");
    await key("Tab", 9);
    assert.equal(await focus(), "command");
    assert.deepEqual(await page.evaluate(() => {
      const input = document.activeElement;
      const outline = getComputedStyle(input);
      const box = input.getBoundingClientRect();
      return [outline.outlineStyle, parseFloat(outline.outlineWidth) >= 2, box.width > 40];
    }), ["solid", true, true]);

    async function showSuggestions() {
      await key("Tab", 9);
      assert.equal(await focus(), "complete-command");
      await key("Enter", 13);
      await page.wait('document.activeElement?.id === "command" && document.querySelector("#command").getAttribute("aria-expanded") === "true"');
    }

    await page.send("Input.insertText", { text: "cat read" });
    await showSuggestions();
    assert.match(await page.evaluate('document.querySelector("#terminal-status").textContent'), /^2 paths/);
    await expectSpeech("2 paths");
    await key("ArrowDown", 40);
    assert.equal(await page.evaluate('document.querySelector("[aria-selected=true]").textContent'), "read me two.txt");
    await expectSpeech("read me two.txt");

    await page.send("Accessibility.enable");
    try {
      const { nodes } = await page.send("Accessibility.getFullAXTree");
      const role = value => nodes.find(node => !node.ignored && node.role?.value === value);
      const input = role("combobox");
      const live = role("status");
      assert.equal(input.name.value, "Terminal command");
      assert.equal(input.properties.find(property => property.name === "expanded").value.value, true);
      assert.equal(input.properties.find(property => property.name === "activedescendant").value.relatedNodes[0].idref, "completion-option-0");
      assert.equal(live.properties.find(property => property.name === "live").value.value, "polite");
      if (process.env.LSGET_SCREENSHOTS) {
        const evidence = { input, live, listbox: role("listbox"), option: role("option") };
        await writeFile(join(process.env.LSGET_SCREENSHOTS, "terminal-accessibility.json"), JSON.stringify(evidence, null, 2));
      }
    } finally {
      await page.send("Accessibility.disable");
    }

    await key("Escape", 27);
    assert.equal(await focus(), "command");
    assert.equal(await page.evaluate('document.querySelector("#completions").hidden'), true);
    await expectSpeech("Path suggestions closed");
    const beforeReopen = await speechOffset();
    await showSuggestions();
    await expectSpeech("2 paths", beforeReopen);
    await key("ArrowDown", 40);
    await key("ArrowDown", 40);
    assert.equal(await page.evaluate('document.querySelector("[aria-selected=true]").textContent'), "read-me-one.txt");
    await key("ArrowUp", 38);
    await key("Enter", 13);
    assert.equal(await focus(), "command");
    assert.equal(await page.evaluate('document.querySelector("#command").value'), 'cat "read me two.txt"');
    await key("Enter", 13);
    await page.wait('document.querySelector("#terminal-status").textContent === "Second path suggestion opened."');

    await page.evaluate(() => {
      window.keyboardCopies = [];
      Object.defineProperty(navigator, "clipboard", {
        configurable: true,
        value: { writeText: async url => window.keyboardCopies.push(url) },
      });
    });
    try {
      await page.send("Input.insertText", { text: "share README.md" });
      await key("Enter", 13);
      await page.wait("window.keyboardCopies.length === 1");
      await key("Tab", 9, 8);
      assert.equal(await page.evaluate("document.activeElement.textContent"), "Copy URL");
      await key("Tab", 9, 8);
      assert.equal(await page.evaluate('document.activeElement.getAttribute("aria-label")'), "Shareable URL");
      assert.equal(await page.evaluate("document.activeElement.selectionEnd"), (origin + "/docs/README.md").length);
      await key("Tab", 9);
      await key("Enter", 13);
      await page.wait("window.keyboardCopies.length === 2");
      assert.deepEqual(await page.evaluate("window.keyboardCopies"), [origin + "/docs/README.md", origin + "/docs/README.md"]);
      await key("Tab", 9);
      assert.equal(await focus(), "command");
    } finally {
      await page.evaluate("delete navigator.clipboard");
    }

    await page.send("Input.insertText", { text: "get README.md" });
    await key("Enter", 13);
    await page.wait('document.querySelector("#transcript a[download]")');
    await page.evaluate(() => {
      document.querySelector("#transcript a[download]").addEventListener("click", event => {
        window.keyboardDownload = event.isTrusted;
      }, { once: true });
    });
    await key("Tab", 9, 8);
    assert.equal(await page.evaluate("document.activeElement.textContent"), "Download file");
    await key("Enter", 13);
    await page.wait("window.keyboardDownload === true");
    t.diagnostic("Tab/Shift-Tab, combobox selection and Escape, AX live status, and keyboard copy and download passed.");
  });

  await t.test("Touch, reduced motion and the desktop and mobile text-size matrix", async () => {
    await page.send("Page.enable");
    await page.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-reduced-motion", value: "reduce" }] });
    async function capture(label) {
      if (!process.env.LSGET_SCREENSHOTS) return;
      const { data } = await page.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
      await writeFile(join(process.env.LSGET_SCREENSHOTS, label + ".png"), Buffer.from(data, "base64"));
    }
    async function tap(selector) {
      const point = await page.evaluate(selector => {
        const { left, top, width, height } = document.querySelector(selector).getBoundingClientRect();
        return { x: left + width / 2, y: top + height / 2 };
      }, selector);
      await page.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [point] });
      await page.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    }
    try {
      for (const width of [360, 1280]) {
        const mobile = width === 360;
        await page.send("Emulation.setDeviceMetricsOverride", { width, height: 800, deviceScaleFactor: 1, mobile });
        await page.send("Emulation.setTouchEmulationEnabled", { enabled: mobile });
        for (const percent of [100, 200]) {
          const size = 16 * percent / 100;
          const token = width + "x800-" + percent;
          await page.send("Page.setFontSizes", { fontSizes: { standard: size, fixed: size } });
          await page.open(origin + "/docs?matrix=" + token);
          await page.wait('location.search === ' + JSON.stringify("?matrix=" + token) + ' && document.querySelector("#readme h1")?.textContent === "Markdown fixture"');
          if (mobile) await tap("#command");
          assert.equal(await page.evaluate("document.activeElement.id"), "command");
          await page.send("Input.insertText", { text: "cat read" });
          if (mobile) {
            await tap("#complete-command");
          } else {
            await key("Tab", 9);
            await key("Enter", 13);
          }
          await page.wait('document.querySelector("#command").getAttribute("aria-expanded") === "true"');
          await key("ArrowDown", 40);
          const geometry = await page.evaluate(() => {
            const command = document.querySelector("#command");
            const panel = document.querySelector("#completions");
            const option = document.querySelector("[aria-selected=true]");
            const complete = document.querySelector("#complete-command");
            const inViewport = ({ left, top, right, bottom }) => left >= -1 && top >= -1 && right <= innerWidth + 1 && bottom <= innerHeight + 1;
            return {
              width: innerWidth, height: innerHeight, size: parseFloat(getComputedStyle(command).fontSize),
              below: panel.getBoundingClientRect().top >= command.getBoundingClientRect().bottom,
              visible: [command, complete, panel, option].every(node => inViewport(node.getBoundingClientRect())),
              wrap: document.documentElement.scrollWidth === innerWidth,
              reducedMotion: getComputedStyle(document.querySelector("#readme")).transitionDuration === "0s",
              textKeyboard: command.inputMode === "text" && command.enterKeyHint === "go",
              touchSize: complete.getBoundingClientRect().height >= 44,
            };
          });
          if (!geometry.visible) t.diagnostic(JSON.stringify(await page.evaluate(() => Object.fromEntries(["command", "complete-command", "completions", "completion-option-0"].map(id => { const r = document.getElementById(id).getBoundingClientRect(); return [id, { top: r.top, bottom: r.bottom, left: r.left, right: r.right }]; })))));
          assert.deepEqual(geometry, {
            width, height: 800, size, below: true, visible: true, wrap: true,
            reducedMotion: true, textKeyboard: true, touchSize: mobile || size === 32,
          });
          await capture("terminal-" + token);
          if (mobile) {
            await tap("#completion-option-0");
            assert.equal(await page.evaluate('document.querySelector("#command").value'), 'cat "read me two.txt"');
            assert.equal(await page.evaluate("document.activeElement.id"), "command");
            await key("Enter", 13);
            await page.wait('document.querySelector("#terminal-status").textContent === "Second path suggestion opened."');
            const measured = await page.evaluate(() => {
              const button = document.querySelector("#readme-toggle");
              const box = button.getBoundingClientRect();
              return box.width >= 44 && box.height >= 44;
            });
            assert.equal(measured, true);
            await tap("#readme-toggle");
            await page.wait('!document.querySelector("#readme").inert && document.querySelector("#readme-toggle").getAttribute("aria-expanded") === "true"');
          } else {
            await key("Escape", 27);
            await key("Tab", 9);
            await key("Tab", 9);
          }
          assert.equal(await page.evaluate("document.activeElement.id"), "readme");
          const documentMetrics = await page.evaluate(() => {
            const readme = document.querySelector("#readme");
            const heading = readme.querySelector("h1");
            const box = readme.getBoundingClientRect();
            return {
              heading: heading.textContent, visible: box.top >= -1 && box.bottom <= innerHeight + 1 && box.width > 250,
              fitsHorizontally: readme.scrollWidth === readme.clientWidth,
            };
          });
          assert.deepEqual(documentMetrics, { heading: "Markdown fixture", visible: true, fitsHorizontally: true });
          await capture("readme-" + token);
          if (mobile) {
            await key("Escape", 27);
            assert.equal(await page.evaluate("document.activeElement.id"), "command");
            assert.equal(await page.evaluate('document.querySelector("#readme-toggle").getAttribute("aria-expanded")'), "false");
          }
          t.diagnostic(token + " command, completion, README and reduced-motion geometry verified");
        }
      }
    } finally {
      await page.send("Emulation.setTouchEmulationEnabled", { enabled: false });
      await page.send("Emulation.clearDeviceMetricsOverride");
      await page.send("Emulation.setEmulatedMedia", { features: [] });
      await page.send("Page.setFontSizes", { fontSizes: { standard: 16, fixed: 16 } });
      await page.send("Page.disable");
    }
  });

  await t.test("Disabling page JavaScript reaches the plain file listing", async () => {
    await page.send("Emulation.setScriptExecutionDisabled", { value: true });
    try {
      await page.open(origin + "/docs");
      await page.wait('location.search === "?nojs=1" && document.querySelector("h1")?.textContent === "Index of /docs"');
      assert.equal(await page.evaluate('document.querySelector("a[href=\\"/docs/sample.svg\\"]")?.textContent'), "sample.svg");
      await page.open(origin + "/rst");
      await page.wait('location.search === "?nojs=1" && document.querySelector("h1")?.textContent === "Index of /rst"');
      await page.evaluate("history.back()");
      await page.wait('location.pathname === "/docs" && location.search === "?nojs=1" && document.querySelector("h1")?.textContent === "Index of /docs"');
    } finally {
      await page.send("Emulation.setScriptExecutionDisabled", { value: false });
    }
    t.diagnostic("The browser followed the noscript refresh for both plain listings and Back restored the first listing.");
  });

  await t.test("Files rows reflow with JavaScript disabled and enlarged text", async () => {
    const longName = "A long shared filename with <angle> & quoted text that wraps on a phone.txt";
    await writeFile(join(site, "docs", longName), "Literal filename fixture\n");
    const measurements = [];
    await page.send("Emulation.setScriptExecutionDisabled", { value: true });
    try {
      for (const width of [320, 390]) {
        await page.send("Emulation.setDeviceMetricsOverride", { width, height: width === 320 ? 640 : 844, deviceScaleFactor: 1, mobile: true });
        for (const fontSize of [16, 32]) {
          await page.send("Page.setFontSizes", { fontSizes: { standard: fontSize, fixed: fontSize } });
          await page.open(origin + "/docs?view=files");
          await page.wait('document.querySelector(".files") && document.querySelector("h1")?.textContent === "Index of /docs"');
          const geometry = await page.evaluate(() => {
            const rect = element => {
              const { x, y, width, height } = element.getBoundingClientRect();
              return { x, y, width, height };
            };
            return {
              viewport: innerWidth, content: document.documentElement.scrollWidth,
              rows: Array.from(document.querySelectorAll(".file-row"), rect),
              up: rect(document.querySelector(".up")),
              names: Array.from(document.querySelectorAll(".file-row"), a => a.textContent),
              size: getComputedStyle(document.documentElement).fontSize,
              scripts: document.scripts.length,
            };
          });
          assert.equal(geometry.viewport, width);
          assert.ok(geometry.content <= width, JSON.stringify(geometry));
          assert.ok(geometry.rows.length >= 3);
          assert.ok(geometry.rows.every(row => row.height >= 44 && row.x >= 0 && row.x + row.width <= width));
          assert.ok(geometry.up.y >= 0 && geometry.up.y + geometry.up.height < 150, JSON.stringify(geometry));
          assert.ok(geometry.names.includes(longName));
          assert.equal(geometry.size, fontSize + "px");
          assert.equal(geometry.scripts, 0);
          await key("Tab", 9);
          assert.equal(await page.evaluate("document.activeElement.className"), "up");
          await key("Tab", 9);
          assert.equal(await page.evaluate("document.activeElement.textContent"), "Files");
          await key("Tab", 9);
          assert.equal(await page.evaluate("document.activeElement.textContent"), "Terminal");
          await key("Tab", 9);
          assert.equal(await page.evaluate("document.activeElement.textContent"), "Shared files");
          await key("Tab", 9);
          assert.equal(await page.evaluate("document.activeElement.className"), "file-row");
          const label = `files-${width}-${fontSize}`;
          measurements.push({ label, ...geometry });
          if (process.env.LSGET_SCREENSHOTS) {
            const { data } = await page.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
            await writeFile(join(process.env.LSGET_SCREENSHOTS, label + ".png"), Buffer.from(data, "base64"));
          }
          await page.evaluate("window.scrollTo(0, document.body.scrollHeight)");
          assert.equal(await page.evaluate("document.querySelector('.up').getBoundingClientRect().top >= 0"), true);
          await page.evaluate("document.querySelector('.up').focus()");
          await key("Enter", 13);
          await page.wait('location.pathname === "/" && location.search === "?view=files"');
        }
      }
      const deepPath = "/" + Array.from({ length: 12 }, (_, i) => "deep-shared-folder-" + i).join("/");
      await mkdir(join(site, deepPath), { recursive: true });
      await writeFile(join(site, deepPath, "reachable.txt"), "Deep shared file\n");
      await page.send("Emulation.setDeviceMetricsOverride", { width: 320, height: 640, deviceScaleFactor: 1, mobile: true });
      await page.send("Page.setFontSizes", { fontSizes: { standard: 32, fixed: 32 } });
      await page.open(origin + deepPath + "?view=files");
      await page.wait('document.querySelector(".file-row")?.textContent === "reachable.txt"');
      await page.evaluate("document.querySelector('.file-row').scrollIntoView({block:'center'})");
      const deep = await page.evaluate(() => {
        const row = document.querySelector('.file-row').getBoundingClientRect();
        const header = document.querySelector('header').getBoundingClientRect();
        return { rowTop: row.top, rowBottom: row.bottom, headerBottom: header.bottom, height: innerHeight, width: innerWidth, content: document.documentElement.scrollWidth };
      });
      assert.ok(deep.rowTop >= deep.headerBottom && deep.rowBottom <= deep.height, JSON.stringify(deep));
      assert.equal(deep.content, deep.width);
      measurements.push({ label: "files-deep-320-32", ...deep });
      if (process.env.LSGET_SCREENSHOTS) {
        const { data } = await page.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
        await writeFile(join(process.env.LSGET_SCREENSHOTS, "files-deep-320-32.png"), Buffer.from(data, "base64"));
      }
      if (process.env.LSGET_SCREENSHOTS) {
        await writeFile(join(process.env.LSGET_SCREENSHOTS, "files-geometry.json"), JSON.stringify(measurements, null, 2));
      }
      t.diagnostic(JSON.stringify(measurements.map(({ label, viewport, content, size, rows }) => ({ label, viewport, content, size, rows: rows?.length }))));
    } finally {
      await page.send("Emulation.setScriptExecutionDisabled", { value: false });
      await page.send("Page.setFontSizes", { fontSizes: { standard: 16, fixed: 16 } });
      await page.send("Emulation.clearDeviceMetricsOverride");
      await rm(join(site, "docs", longName));
    }
  });

  await t.test("Files actions support touch, keyboard, downloads and selectable share URLs without scripts", async () => {
    const name = 'résumé # <&".bin';
    const bytes = Buffer.from([0, 1, 2, 3, 255, 13, 10]);
    await mkdir(join(site, "actions", "folder"), { recursive: true });
    await writeFile(join(site, "actions", name), bytes);
    const downloads = join(scratch, "files-downloads");
    await mkdir(downloads);
    await page.send("Browser.setDownloadBehavior", { behavior: "allow", downloadPath: downloads });
    await page.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: true });
    await page.send("Emulation.setScriptExecutionDisabled", { value: true });
    try {
      await page.open(origin + "/actions?view=files");
      await page.wait('document.querySelector("h1")?.textContent === "Index of /actions"');
      const point = await page.evaluate(() => {
        const { x, y, width, height } = document.querySelector("summary").getBoundingClientRect();
        return { x: x + width / 2, y: y + height / 2 };
      });
      await page.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [point] });
      await page.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
      await page.wait('document.querySelector("details").open');
      await page.evaluate('document.querySelector("summary").focus()');
      await key("Enter", 13);
      assert.equal(await page.evaluate('document.querySelector("details").open'), false);
      await key("Enter", 13);
      await key("Tab", 9);
      assert.equal(await page.evaluate("document.activeElement.className"), "download");
      const focus = await page.evaluate("getComputedStyle(document.activeElement).outlineStyle");
      assert.equal(focus, "solid");
      await key("Tab", 9);
      assert.equal(await page.evaluate("document.activeElement.className"), "share-url");
      await key("a", 65, 2);
      const share = await page.evaluate(() => {
        const field = document.activeElement;
        return { value: field.value, readOnly: field.readOnly, selected: field.value.slice(field.selectionStart, field.selectionEnd), width: document.documentElement.scrollWidth };
      });
      assert.equal(share.value, origin + "/actions/" + encodeURIComponent(name).replaceAll("%26", "&"));
      assert.equal(share.readOnly, true);
      assert.equal(share.selected, share.value);
      assert.equal(share.width, 390);
      const { nodes } = await page.send("Accessibility.getFullAXTree");
      for (const accessibleName of ["Actions for " + name, "Download " + name, "Share URL for " + name]) {
        assert.ok(nodes.some(node => node.name?.value === accessibleName), accessibleName);
      }
      if (process.env.LSGET_SCREENSHOTS) {
        const { data } = await page.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
        await writeFile(join(process.env.LSGET_SCREENSHOTS, "files-actions-390.png"), Buffer.from(data, "base64"));
        await writeFile(join(process.env.LSGET_SCREENSHOTS, "files-actions.json"), JSON.stringify({ name, share, focus }, null, 2));
      }
      await page.evaluate('document.querySelector(".download").focus()');
      await key("Enter", 13);
      let savedName;
      const saved = await until(async () => {
        const entries = await readdir(downloads);
        // Chrome may replace platform-sensitive characters in the suggested name.
        if (entries.length !== 1 || entries[0].endsWith(".crdownload")) return;
        savedName = entries[0];
        return readFile(join(downloads, savedName));
      }, "Files attachment download");
      assert.deepEqual(saved, bytes);
      if (process.env.LSGET_SCREENSHOTS) {
        await writeFile(join(process.env.LSGET_SCREENSHOTS, "files-actions.json"), JSON.stringify({ name, savedName, share, focus, downloadedBytes: saved.length }, null, 2));
      }
      t.diagnostic("Native details opened by touch/Enter, Download focus and AX names verified, readonly share URL selected, exact binary bytes downloaded without page scripts; Chrome saved as " + savedName);
    } finally {
      await page.send("Emulation.setScriptExecutionDisabled", { value: false });
      await page.send("Emulation.setTouchEmulationEnabled", { enabled: false });
      await page.send("Emulation.clearDeviceMetricsOverride");
    }
  });

  await t.test("Files reverse Tab keeps action controls below persistent Up", async () => {
    await mkdir(join(site, "reverse-focus"));
    for (let i = 0; i < 20; i++) await writeFile(join(site, "reverse-focus", `file-${String(i).padStart(2, "0")}.txt`), "Focus fixture\n");
    await page.send("Emulation.setScriptExecutionDisabled", { value: true });
    const measurements = [];
    try {
      for (const [width, fontSize] of [[390, 16], [320, 32]]) {
        await page.send("Emulation.setDeviceMetricsOverride", { width, height: 844, deviceScaleFactor: 1, mobile: true });
        await page.send("Page.setFontSizes", { fontSizes: { standard: fontSize, fixed: fontSize } });
        await page.open(origin + "/reverse-focus?view=files");
        await page.wait('document.querySelector("h1")?.textContent === "Index of /reverse-focus"');
        await page.evaluate(() => {
          const details = document.querySelectorAll("details")[10];
          details.open = true;
          const input = details.querySelector("input");
          input.focus();
          window.scrollTo(0, input.getBoundingClientRect().top + scrollY - 8);
        });
        for (const expected of ["download", "", "file-row"]) {
          await key("Tab", 9, 8);
          const focus = await page.evaluate(() => {
            const current = document.activeElement;
            const box = current.getBoundingClientRect();
            return { tag: current.tagName, className: current.className, top: box.top, bottom: box.bottom, headerBottom: document.querySelector("header").getBoundingClientRect().bottom, height: innerHeight };
          });
          measurements.push({ width, fontSize, ...focus });
          assert.equal(focus.className, expected);
          assert.ok(focus.top >= focus.headerBottom && focus.bottom <= focus.height, JSON.stringify(focus));
        }
      }
      if (process.env.LSGET_SCREENSHOTS) await writeFile(join(process.env.LSGET_SCREENSHOTS, "files-reverse-focus.json"), JSON.stringify(measurements, null, 2));
    } finally {
      await page.send("Emulation.setScriptExecutionDisabled", { value: false });
      await page.send("Page.setFontSizes", { fontSizes: { standard: 16, fixed: 16 } });
      await page.send("Emulation.clearDeviceMetricsOverride");
    }
  });

  await t.test("Files and Terminal share explicit nested-directory links and history", async () => {
    const parent = "/views # & <folder>";
    const nested = parent + "/nested";
    const escaped = path => path.split("/").map(encodeURIComponent).join("/");
    await mkdir(join(site, nested, "child"), { recursive: true });
    await writeFile(join(site, nested, "child", "note.txt"), "Shared view fixture\n");
    await writeFile(join(site, "view-selected.txt"), "Reserved selector fixture\n");
    const captures = [];
    async function current(path, view) {
      await page.wait(`decodeURIComponent(location.pathname) === ${JSON.stringify(path)} && new URLSearchParams(location.search).get('view') === ${JSON.stringify(view)}`);
      await page.wait(view === "terminal" ? `document.querySelector('#command-prompt')?.textContent === ${JSON.stringify("guest@browser:" + path + "$ ")}` : `document.querySelector('h1')?.textContent === ${JSON.stringify("Index of " + path)}`);
      assert.equal(await page.evaluate("new URLSearchParams(location.search).get('file')"), null);
      assert.equal(await page.evaluate("new URLSearchParams(location.search).get('keep')"), "two&words");
    }
    async function activate(selector) {
      await page.evaluate(selector => document.querySelector(selector).focus(), selector);
      await key("Enter", 13);
    }
    async function capture(label) {
      const links = await page.evaluate(() => Object.fromEntries(["files-view", "terminal-view"].map(id => [id, document.getElementById(id).getAttribute("href")])));
      captures.push({ label, links });
      if (process.env.LSGET_SCREENSHOTS) {
        const { data } = await page.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
        await writeFile(join(process.env.LSGET_SCREENSHOTS, label + ".png"), Buffer.from(data, "base64"));
      }
    }
    await page.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
    try {
      await page.open(origin + escaped(nested) + "?view=files&keep=two%26words&file=view-selected.txt");
      await page.wait('document.querySelector(".files")');
      await capture("views-files-390");
      await activate("#terminal-view");
      await current(nested, "terminal");
      await capture("views-terminal-390");
      await command("cd child");
      await current(nested + "/child", "terminal");
      assert.ok((await page.evaluate("document.getElementById('files-view').href")).includes("/child?"));
      await page.evaluate("history.back()");
      await current(nested, "terminal");
      await command("cd child");
      await current(nested + "/child", "terminal");
      await command("cd ..");
      await current(nested, "terminal");
      await activate("#files-view");
      await current(nested, "files");
      await activate(".file-row");
      await current(nested + "/child", "files");
      await activate(".up");
      await current(nested, "files");
      await activate(".up");
      await current(parent, "files");
      await page.evaluate("history.back()");
      await current(nested, "files");
      await page.send("Emulation.setScriptExecutionDisabled", { value: true });
      await activate("#terminal-view");
      await page.wait("new URLSearchParams(location.search).get('nojs') === '1' && document.querySelector('.files')");
      assert.equal(await page.evaluate("new URLSearchParams(location.search).get('keep')"), "two&words");
      assert.equal(await page.evaluate("new URLSearchParams(location.search).get('view')"), null);
      await page.send("Emulation.setScriptExecutionDisabled", { value: false });
      await page.send("Emulation.setDeviceMetricsOverride", { width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
      await activate("#terminal-view");
      await current(nested, "terminal");
      await capture("views-terminal-1280");
      if (process.env.LSGET_SCREENSHOTS) await writeFile(join(process.env.LSGET_SCREENSHOTS, "view-links.json"), JSON.stringify(captures, null, 2));
    } finally {
      await page.send("Emulation.setScriptExecutionDisabled", { value: false });
      await page.send("Emulation.clearDeviceMetricsOverride");
    }
  });

  await t.test("Embedded shell, explicit override, and publishing use their intended responses", async () => {
    assert.deepEqual(await readdir(workA), []);
    const second = await startServer(cleanups, binary, site, workB, mode);
    for (const url of [origin, second]) {
      const response = await fetch(url);
      assert.match(await response.text(), /<title>📂 lsget<\/title>/u);
      const enforced = response.headers.get("Content-Security-Policy");
      const reportOnly = response.headers.get("Content-Security-Policy-Report-Only");
      if (mode === "report-only") {
        assert.equal(enforced, null);
        assert.doesNotMatch(reportOnly, /'unsafe-eval'/u);
        assert.match(reportOnly, /script-src 'nonce-/u);
      } else {
        assert.equal(reportOnly, null);
        assert.match(enforced, /script-src 'nonce-[^']+' 'unsafe-eval'/u);
        assert.doesNotMatch(enforced, /'unsafe-inline'/u);
      }
    }
    const override = await startServer(cleanups, binary, site, workB, mode, "index.html");
    await page.open(override);
    await page.wait('document.title === "Local B"');
    await page.open(override + "/docs/guide/");
    await page.wait('document.querySelector("h1")?.textContent === "Published guide"');
    for (const url of [override, override + "/docs/guide/", origin + "/docs?nojs=1"]) {
      const response = await fetch(url);
      assert.equal(response.status, 200);
      assert.equal(response.headers.get("Content-Security-Policy"), null);
      assert.equal(response.headers.get("Content-Security-Policy-Report-Only"), null);
    }
    t.diagnostic("The first binary served the embedded shell from an empty working directory. The default and explicit override in the second working directory preserved publishing and no-JS handling.");
  });

  await t.test("Initial metadata and bundled favicon match the browser", async () => {
    let previewRequests = 0;
    const preview = createHTTPServer((_, response) => {
      previewRequests++;
      response.writeHead(500).end();
    });
    preview.listen(0, "127.0.0.1");
    await once(preview, "listening");
    cleanups.push(() => new Promise((resolve) => {
      preview.close(resolve);
      preview.closeAllConnections();
    }));
    const previewURL = `http://127.0.0.1:${preview.address().port}/cover.png?v=2&w=640`;
    const publicBase = "https://public.example/releases%20here";
    const title = 'Café & <em> "Releases"';
    const description = 'Browse crédit & "checksums".';
    const custom = await startServer(cleanups, binary, site, workA, mode, "", [
      "-baseurl", publicBase, "-site-title", title, "-site-description", description, "-preview-image", previewURL,
    ]);
    const path = "/docs?from=preview";
    const crawler = await fetch(custom + path, { headers: { "User-Agent": "lsget-metadata-fixture-crawler" } });
    assert.equal(crawler.status, 200);
    const initial = await crawler.text();
    assert.match(initial, /<title>Café &amp; &lt;em&gt; &#34;Releases&#34;<\/title>/u);
    assert.ok(initial.includes(`<link rel="canonical" href="${publicBase}/docs" />`));
    assert.ok(initial.includes(`<meta property="og:image" content="${previewURL.replace("&", "&amp;")}" />`));

    await page.open(custom + path);
    await page.wait(`document.title === ${JSON.stringify(title)}`);
    const data = await page.evaluate(() => ({
      title: document.title,
      ogTitle: document.querySelector('meta[property="og:title"]').content,
      ogDescription: document.querySelector('meta[property="og:description"]').content,
      description: document.querySelector('meta[name="description"]').content,
      canonical: document.querySelector('link[rel="canonical"]').href,
      ogURL: document.querySelector('meta[property="og:url"]').content,
      ogImage: document.querySelector('meta[property="og:image"]').content,
      favicon: document.querySelector('link[rel="icon"]').getAttribute("href"),
    }));
    assert.match(data.favicon, /^\/assets\/lsget-shell\/[a-f0-9]{64}\/favicon\.png$/u);
    assert.deepEqual(data, { title, ogTitle: title, ogDescription: description, description,
      canonical: publicBase + "/docs", ogURL: publicBase + "/docs", ogImage: previewURL, favicon: data.favicon });
    const icon = await fetch(custom + data.favicon);
    assert.equal(icon.status, 200);
    assert.equal(icon.headers.get("Content-Type"), "image/png");
    assert.deepEqual(Buffer.from(await icon.arrayBuffer()), await readFile(new URL("../../internal/lsget/assets/favicon.png", import.meta.url)));
    assert.equal(previewRequests, 0, "The metadata response must not fetch the preview image");
    assert.deepEqual(await readdir(workA), []);
  });

  if (process.env.LSGET_PREVIOUS_SHELL_BINARY) {
    await t.test("A cached shell and modules cannot mix between binary releases", async t => {
      const previous = await startServer(cleanups, resolve(process.env.LSGET_PREVIOUS_SHELL_BINARY), site, workA, mode);
      await verifyShellUpgrade(t, cleanups, page, previous, origin);
      for (const path of ["/docs/guide/", "/docs/guide/?nojs=1",
        "/" + encodeURIComponent(file), "/?file=" + encodeURIComponent(file),
        "/assets/js/marked.min.js", "/assets/js/datastar.js"]) {
        const [before, after] = await Promise.all([fetch(previous + path), fetch(origin + path)]);
        assert.equal(before.status, 200, path);
        assert.equal(after.status, before.status, path);
        for (const header of ["Content-Type", "Content-Disposition", "Content-Length", "Cache-Control", "Content-Security-Policy"]) {
          assert.equal(after.headers.get(header), before.headers.get(header), `${path} ${header}`);
        }
        assert.deepEqual(await after.arrayBuffer(), await before.arrayBuffer(), path);
      }
      const plain = await fetch(origin + "/docs?nojs=1");
      assert.equal(plain.status, 200);
      assert.match(await plain.text(), /class="files"/u);
      t.diagnostic("Six published, download, and stable vendor responses matched the previous binary byte for byte; the intentionally updated no-JS listing retained its successful response.");
    });
  }

  await t.test("Chrome reports the tighter Datastar policy during the trial", async () => {
    const messages = [...new Set(page.policyMessages.filter((message) => /Content Security Policy/u.test(message)))];
    if (mode === "report-only") {
      assert.ok(messages.length > 0, "Expected Chrome to report Datastar's Function compilation");
      assert.ok(messages.every((message) => /evaluation|evaluate|eval/u.test(message)), messages.join("\n"));
      assert.ok(messages.every((message) => /report.only/u.test(message)), messages.join("\n"));
    } else {
      assert.deepEqual(messages, []);
    }
    t.diagnostic(JSON.stringify({ mode, policyMessages: messages.map((line) => line.replace(/'nonce-[^']+'/gu, "'nonce-REDACTED'")) }));
  });
});
