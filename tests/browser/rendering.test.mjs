// Run with LSGET_BINARY=/absolute/path/to/lsget node --test tests/browser/rendering.test.mjs.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { test } from "node:test";
import { setTimeout as delay } from "node:timers/promises";

async function until(probe, description) {
  const deadline = Date.now() + 8_000;
  while (Date.now() < deadline) {
    const result = await probe();
    if (result) return result;
    await delay(50);
  }
  assert.fail("Timed out waiting for " + description);
}

function start(cleanups, command, args, cwd) {
  const child = spawn(command, args, { cwd, stdio: ["ignore", "ignore", "pipe"] });
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
  const profile = join(scratch, "chrome");
  const running = start(cleanups, process.env.CHROME_BIN || "google-chrome", [
    "--headless", "--no-first-run", "--disable-background-networking",
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
      expression, returnByValue: true, userGesture: true,
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

async function startServer(cleanups, binary, site, cwd, mode, template = "") {
  const address = await testAddress();
  const origin = "http://" + address;
  const running = start(cleanups, binary, [
    "-dir", site, "-addr", address, "-pid=", "-logfile=", "-baseurl=", "-sitemap=0",
    "-shell-csp", mode, "-dev-template", template,
  ], cwd);
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
  });
  const site = join(scratch, "served");
  const { file, punctuation } = await fixture(site);
  const workA = join(scratch, "work-a");
  const workB = join(scratch, "work-b");
  await mkdir(workA);
  await mkdir(workB);
  await writeFile(join(workA, "index.html"), "<title>Local A</title>");
  await writeFile(join(workB, "index.html"), "<title>Local B</title>");
  const mode = process.env.LSGET_TEST_CSP || "enforce";
  const origin = await startServer(cleanups, binary, site, workA, mode);

  const page = await browserPage(cleanups, scratch);
  function command(input) {
    return page.evaluate((input) => {
      const ghost = document.getElementById("ghost");
      ghost.value = input;
      ghost.dispatchEvent(new Event("input", { bubbles: true }));
      ghost.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    }, input);
  }

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
    await page.wait('document.querySelector(".input-before").textContent === "pwd"');
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", windowsVirtualKeyCode: 13 });
    await page.wait('Array.from(document.querySelectorAll("#transcript .out")).some(node => node.textContent === "/rst")');
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", key: "ArrowUp", windowsVirtualKeyCode: 38 });
    await page.wait('document.querySelector(".input-before").textContent === "pwd"');
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", key: "u", modifiers: 2, windowsVirtualKeyCode: 85 });
    await command("cd /docs");
    await page.wait('location.pathname === "/docs" && document.querySelector("#readme h1")?.textContent === "Markdown fixture"');
    await page.evaluate("history.back()");
    await page.wait('location.pathname === "/rst" && document.querySelector("#readme h1")?.textContent === "RST fixture"');
    await page.evaluate("history.forward()");
    await page.wait('location.pathname === "/docs" && document.querySelector("#readme h1")?.textContent === "Markdown fixture"');
    t.diagnostic("Typed pwd, recalled with ArrowUp, changed directory, and restored README on browser Back and Forward.");
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

  await t.test("Disabling page JavaScript reaches the plain file listing", async () => {
    await page.send("Emulation.setScriptExecutionDisabled", { value: true });
    try {
      await page.open(origin + "/docs");
      await page.wait('location.search === "?nojs=1" && document.querySelector("h1")?.textContent === "Index of /docs"');
      assert.equal(await page.evaluate('document.querySelector("a[href=\\"/docs/sample.svg\\"]")?.textContent'), "sample.svg");
    } finally {
      await page.send("Emulation.setScriptExecutionDisabled", { value: false });
    }
    t.diagnostic("The browser followed the noscript refresh and displayed the file listing.");
  });

  await t.test("Embedded shell, explicit override, and publishing use their intended responses", async () => {
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
    t.diagnostic("Two default launches served the embedded shell. An explicit template won only on the shell route. Published HTML and the no-JS listing had no shell CSP.");
  });

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
