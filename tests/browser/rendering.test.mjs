// Run with LSGET_BINARY=/absolute/path/to/lsget node --test tests/browser/rendering.test.mjs.
// LSGET_PREVIOUS_SHELL_BINARY checks cache compatibility with the prior release.
// LSGET_PLAN_BASELINE_BINARY adds the throttled first-listing comparison.
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
import { compareFirstListing, createSharedJourneyFixture, verifySharedJourney } from "./shared-journey.mjs";

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

async function browserPage(cleanups, scratch, fontSize) {
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
  if (fontSize) {
    const defaults = join(profile, "Default");
    await mkdir(defaults, { recursive: true });
    // A profile preference survives a browser history traversal across SVG and HTML documents.
    await writeFile(join(defaults, "Preferences"), JSON.stringify({ webkit: { webprefs: {
      default_font_size: fontSize, default_fixed_font_size: fontSize,
    } } }));
  }
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
  const network = [];
  socket.addEventListener("message", (event) => {
    const response = JSON.parse(event.data);
    if (response.method === "Log.entryAdded" && response.params.entry.source === "security") {
      policyMessages.push(response.params.entry.text);
    }
    const params = response.params;
    switch (response.method) {
      case "Network.requestWillBeSent":
        network.push({ event: "request", id: params.requestId, time: params.timestamp, type: params.type, url: params.request.url });
        break;
      case "Network.responseReceived":
        network.push({ event: "response", id: params.requestId, time: params.timestamp, status: params.response.status, url: params.response.url });
        break;
      case "Network.loadingFinished":
        network.push({ event: "complete", id: params.requestId, time: params.timestamp, bytes: params.encodedDataLength });
        break;
      case "Network.loadingFailed":
        network.push({ event: "failed", id: params.requestId, time: params.timestamp, reason: params.errorText });
        break;
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
    network,
    wait(expression) {
      return until(() => evaluate(expression), expression);
    },
  };
}

function browserControls(page) {
  async function key(key, windowsVirtualKeyCode, modifiers = 0) {
    const options = { key, code: key, windowsVirtualKeyCode, modifiers };
    const text = key === "Enter" ? { text: "\r", unmodifiedText: "\r" } : {};
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", ...options, ...text });
    await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...options });
  }

  async function tap(selector) {
    const point = await page.evaluate(selector => {
      const { left, top, width, height } = document.querySelector(selector).getBoundingClientRect();
      return { x: left + width / 2, y: top + height / 2 };
    }, selector);
    await page.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [point] });
    await page.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  }

  async function center(selector) {
    await page.evaluate(selector => document.querySelector(selector).scrollIntoView({ block: "center" }), selector);
  }

  async function capture(label) {
    if (!process.env.LSGET_SCREENSHOTS) return;
    const { data } = await page.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
    await writeFile(join(process.env.LSGET_SCREENSHOTS, label + ".png"), Buffer.from(data, "base64"));
  }

  return { key, tap, center, capture };
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
  const touchPath = "/touch-navigation";
  const touchChild = touchPath + "/entry-40-folder";
  await mkdir(join(root, touchChild), { recursive: true });
  await writeFile(join(root, touchChild, "child-only.txt"), "Child file opened.\n");
  // Files lists folders first, so the target folder needs preceding folders to exercise scrolling.
  for (let i = 1; i <= 24; i++) {
    await mkdir(join(root, touchPath, "directory-" + i));
  }
  for (let i = 1; i <= 29; i++) {
    await writeFile(join(root, touchPath, "entry-" + String(i).padStart(2, "0") + ".txt"), "Mobile navigation entry " + i + "\n");
  }
  return { file, punctuation, touchPath, touchChild };
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
  const { file, punctuation, touchPath, touchChild } = await fixture(site);
  await createSharedJourneyFixture(site);
  const workA = join(scratch, "work-a");
  const workB = join(scratch, "work-b");
  await mkdir(workA);
  await mkdir(workB);
  await writeFile(join(workB, "index.html"), "<title>Local B</title>");
  const mode = process.env.LSGET_TEST_CSP || "enforce";
  const origin = await startServer(cleanups, binary, site, workA, mode);

  const page = await browserPage(cleanups, scratch);
  const { key, tap, center, capture } = browserControls(page);

  function readingColors(selectors, outlineBackdrop = null) {
    return page.evaluate((selectors, outlineBackdrop) => {
      const rgba = color => {
        const [red, green, blue, alpha = 1] = color.match(/[\d.]+/gu).map(Number);
        return [red, green, blue, alpha];
      };
      const over = ([r, g, b, alpha], under) => {
        const opacity = alpha + under[3] * (1 - alpha);
        const rgb = [r, g, b].map((value, i) =>
          opacity ? (value * alpha + under[i] * under[3] * (1 - alpha)) / opacity : 0);
        return [...rgb, opacity];
      };
      const luminance = color => color.slice(0, 3).map(channel => {
        const v = channel / 255;
        return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
      }).reduce((total, channel, i) => total + channel * [0.2126, 0.7152, 0.0722][i], 0);
      const contrast = (first, second) => {
        const values = [luminance(first), luminance(second)].sort((a, b) => b - a);
        return (values[0] + 0.05) / (values[1] + 0.05);
      };

      return selectors.flatMap(selector =>
        Array.from(document.querySelectorAll(selector), element => {
          const style = getComputedStyle(element);
          const layers = [];
          for (let parent = element; parent; parent = parent.parentElement) layers.push(getComputedStyle(parent));
          // Compare a painted glyph with the same point without the glyph. Each
          // ancestor's opacity applies to the group before its parent is painted.
          const paint = color => {
            let pixel = rgba(color);
            for (const layer of layers) {
              pixel = over(pixel, rgba(layer.backgroundColor));
              pixel[3] *= Number(layer.opacity);
            }
            return over(pixel, [255, 255, 255, 1]);
          };
          const foreground = paint(style.color);
          const background = paint("rgba(0, 0, 0, 0)");
          const ringBackground = outlineBackdrop
            ? rgba(getComputedStyle(document.querySelector(outlineBackdrop)).backgroundColor)
            : background;
          const { x, y, width, height } = element.getBoundingClientRect();
          return {
            selector, text: element.textContent.trim(), color: style.color, foreground, background,
            layers: layers.map(layer => [layer.backgroundColor, Number(layer.opacity)]),
            ratio: contrast(foreground, background),
            size: parseFloat(style.fontSize), weight: Number(style.fontWeight),
            decoration: style.textDecorationLine, thickness: style.textDecorationThickness,
            outline: style.outlineStyle, outlineWidth: parseFloat(style.outlineWidth),
            outlineRatio: contrast(paint(style.outlineColor), ringBackground),
            visible: element.checkVisibility({ visibilityProperty: true, opacityProperty: true }),
            rect: { x, y, width, height },
          };
        }));
    }, selectors, outlineBackdrop);
  }

  function holdNavigationResponses(initiallyHeld = []) {
    return page.send("Page.addScriptToEvaluateOnNewDocument", {
      source: "(" + function (initiallyHeld) {
        const fetchPage = window.fetch.bind(window);
        const holds = new Map();
        const armed = new Set(initiallyHeld);
        const failures = new Set();
        const requests = [];
        const settled = new Set();
        window.navigationFixture = {
          arm(key) { armed.add(key); },
          fail(key) { failures.add(key); },
          release(key) { holds.get(key)?.(); },
          releaseAll() { holds.forEach(release => release()); armed.clear(); failures.clear(); },
          holds, requests, settled,
        };
        window.fetch = async (url, options) => {
          const { pathname, searchParams } = new URL(url, location.origin);
          const data = options?.body && JSON.parse(options.body);
          const key = pathname === "/api/config" ? "config " + searchParams.get("cwd")
            : pathname === "/api/complete" ? "completion" : data?.input;
          requests.push(pathname);
          if (failures.delete(key)) return new Response("Fixture unavailable", { status: 503 });
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
      }.toString() + ")(" + JSON.stringify(initiallyHeld) + ")",
    });
  }

  async function releaseNavigationResponses(instrumentation) {
    try {
      await page.evaluate(async () => {
        window.navigationFixture?.releaseAll();
        await new Promise(requestAnimationFrame);
        if (window.navigationFixture?.holds.size) throw new Error("Held browser response did not settle");
      });
    } finally {
      await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier: instrumentation.identifier });
    }
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

  await t.test("Files, README and terminal help text keep readable contrast", async () => {
    const directory = join(site, "legibility");
    await mkdir(directory);
    await writeFile(join(directory, "note.txt"), "A shared note.\n");
    await writeFile(join(directory, "README.md"), [
      "# Shared notes", "", "[Read the note](note.txt)", "",
      "## Getting started", "### Notes", "#### More details", "##### Examples", "###### References", "",
      "> An important note.", "", "Use `cat`, **bold** and *emphasis*.", "",
      "| [*Documentation*](note.txt) |", "| --- |", "| Contents |", "", "- [x] Read the note",
    ].join("\n"));
    const captures = [];
    const failures = [];
    const link = "#readme p > a";
    const help = ["version", "note", "heading", "description"].map(role => ".help-" + role);

    async function measure(label, selectors, state = "rest") {
      const colors = await readingColors(selectors);
      for (const selector of selectors) {
        assert.ok(colors.some(color => color.selector === selector), label + " missing " + selector);
      }
      for (const color of colors) {
        const { selector, text, visible, size, weight, ratio } = color;
        assert.ok(visible, label + " hidden " + selector);
        const threshold = size >= 24 || (size >= 18.66 && weight >= 700) ? 3 : 4.5;
        if (ratio < threshold) failures.push([label, state, selector, text, ratio, threshold]);
        if (state === "focus-visible" &&
            (color.outline !== "solid" || color.outlineWidth < 2 || color.outlineRatio < 3)) {
          failures.push([label, state, "focus outline", color.outline, color.outlineWidth, color.outlineRatio]);
        }
      }
      const geometry = await page.evaluate(() => ({
        width: innerWidth, height: innerHeight, content: document.documentElement.scrollWidth,
        rootFontSize: getComputedStyle(document.documentElement).fontSize,
      }));
      captures.push({ label, state, geometry, colors });
      return colors[0];
    }

    await page.send("Page.enable");
    await page.send("DOM.enable");
    await page.send("CSS.enable");
    await page.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-reduced-motion", value: "reduce" }] });
    try {
      for (const [width, height] of [[390, 844], [1280, 800]]) {
        const mobile = width === 390;
        await page.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile });
        await page.send("Emulation.setTouchEmulationEnabled", { enabled: mobile });
        for (const size of [16, 32]) {
          await page.send("Page.setFontSizes", { fontSizes: { standard: size, fixed: size } });
          const format = width + "x" + height + "-" + (size / 16 * 100);
          const files = "reading-files-" + format;
          const terminal = "reading-help-" + format;
          const reader = "reading-readme-" + format;

          await page.open(origin + "/legibility?view=files&size=" + size);
          await page.wait('document.querySelector(".file-row") && location.search.endsWith(' + JSON.stringify("size=" + size) + ')');
          await page.evaluate('document.querySelector(".file-actions").open = true');
          await measure(files, [".up", ".view-link", "h1", ".file-row", ".file-meta", ".file-actions[open] a", ".file-actions summary"]);
          await capture(files);
          await key("Tab", 9);
          await measure(files, [".up:focus-visible"], "focus-visible");

          await page.open(origin + "/legibility?view=terminal&size=" + size);
          await page.wait('document.querySelector("#transcript .clickable-file")');
          await page.evaluate(async () => {
            await new Promise(requestAnimationFrame);
            document.getElementById("transcript").scrollTop = 0;
          });
          await measure(terminal, [...help.map(selector => "#transcript " + selector), "#run-command", "#complete-command"]);
          await capture(terminal);
          await command("help");
          await page.wait('document.querySelectorAll("#transcript .help-version").length === 2');
          await measure(terminal, help.map(selector => "#transcript .out " + selector), "command-output");
          if (mobile) {
            await tap("#readme-toggle");
            await page.wait('!document.getElementById("readme").inert');
          }
          await page.wait('document.querySelector("#readme h1")?.textContent === "Shared notes"');
          await measure(reader, [
            "#readme a", "#readme h1", "#readme h2", "#readme h3", "#readme h4", "#readme h5", "#readme h6",
            "#readme blockquote", "#readme code", "#readme strong", "#readme em", "#readme th",
          ]);
          await capture(reader);
          const checkbox = await page.evaluate(() => {
            const task = document.querySelector("#readme input[type=checkbox]");
            return { disabled: task.matches(":disabled"), checked: task.checked, paint: getComputedStyle(task).appearance };
          });
          assert.deepEqual(checkbox, { disabled: true, checked: true, paint: "auto" });
          captures.push({ label: reader, task: checkbox });

          const resting = await measure(reader, [link]);
          const { root } = await page.send("DOM.getDocument");
          const { nodeId } = await page.send("DOM.querySelector", { nodeId: root.nodeId, selector: link });
          try {
            for (const state of ["hover", "active"]) {
              await page.send("CSS.forcePseudoState", { nodeId, forcedPseudoClasses: [state] });
              const measured = await measure(reader, [link], state);
              if (state === "active" && measured.color === resting.color && measured.thickness === resting.thickness) {
                failures.push([reader, state, "link state matches rest"]);
              }
            }
          } finally {
            await page.send("CSS.forcePseudoState", { nodeId, forcedPseudoClasses: [] });
          }
          await page.evaluate('document.getElementById("readme").focus({ preventScroll: true })');
          await key("Tab", 9);
          await measure(reader, [link + ":focus-visible"], "focus-visible");
          await capture(reader + "-focus");
          if (!resting.decoration.includes("underline")) failures.push([reader, "rest", "link must be underlined"]);
          if (mobile) {
            await key("Escape", 27);
            await page.wait('!history.state?.readme && document.getElementById("readme").inert');
          }
          for (const { label, geometry } of captures.filter(entry => entry.label.endsWith(format) && entry.geometry)) {
            assert.equal(geometry.width, width, label);
            assert.equal(geometry.height, height, label);
            assert.equal(geometry.rootFontSize, size + "px", label);
          }
        }
      }
      if (process.env.LSGET_SCREENSHOTS) {
        await writeFile(join(process.env.LSGET_SCREENSHOTS, "reading-contrast.json"), JSON.stringify({ captures, failures }, null, 2));
      }
      assert.deepEqual(failures.slice(0, 3), [], failures.length + " reading contrast or state failures");
    } finally {
      await page.send("CSS.disable");
      await page.send("DOM.disable");
      await page.send("Emulation.setEmulatedMedia", { features: [] });
      await page.send("Emulation.setTouchEmulationEnabled", { enabled: false });
      await page.send("Page.setFontSizes", { fontSizes: { standard: 16, fixed: 16 } });
      await page.send("Emulation.clearDeviceMetricsOverride");
    }
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

  await t.test("Enter during an active composition does not submit the command", async () => {
    await page.open(origin + "/rst?composition-fixture=1");
    await page.wait('location.search === "?composition-fixture=1" && document.activeElement?.id === "command" && document.querySelector("#transcript .clickable-file")');
    await page.evaluate(() => {
      const field = document.getElementById("command");
      window.composingEnter = [];
      field.addEventListener("keydown", event => {
        if (event.key === "Enter") window.composingEnter.push(event.isComposing);
      });
    });
    const before = await page.evaluate('document.getElementById("transcript").childElementCount');
    await page.send("Input.insertText", { text: "pwd" });
    await page.send("Input.imeSetComposition", { text: "仮", selectionStart: 1, selectionEnd: 1 });
    await key("Enter", 13);
    assert.deepEqual(await page.evaluate("window.composingEnter"), [true]);
    assert.equal(await page.evaluate('document.getElementById("command").value'), "pwd仮");
    assert.equal(await page.evaluate('document.getElementById("transcript").childElementCount'), before);
    await page.send("Input.imeSetComposition", { text: "", selectionStart: 0, selectionEnd: 0 });
    await key("Enter", 13);
    await page.wait('document.getElementById("terminal-status").textContent === "/rst"');
    assert.deepEqual(await page.evaluate("window.composingEnter"), [true, false]);
    t.diagnostic("Chrome reported isComposing on Enter; the preedit and transcript stayed intact. Enter after composition ended ran pwd.");
  });

  await t.test("Deep links, raced navigation, and completion keep the latest directory and input", async () => {
    await page.send("Page.enable");
    const instrumentation = await holdNavigationResponses();
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
      assert.deepEqual(await page.evaluate("navigationFixture.requests"), ["/api/exec", "/api/config"]);
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

      const filePath = touchPath + "/entry-27.txt";
      const childFilePath = touchChild + "/child-only.txt";
      const entry = path => '.clickable-file[data-file=' + JSON.stringify(path) + ']';
      const transcriptFocus = 'document.activeElement?.id === "transcript"';
      const heldCommand = "cat " + filePath;
      await page.send("Emulation.setTouchEmulationEnabled", { enabled: true });
      for (const [width, height] of [[320, 640], [390, 844]]) {
        const viewport = width + "x" + height;
        await page.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: true });
        await page.open(origin + touchPath + "?view=terminal&test=" + viewport);
        await page.wait('location.search.endsWith(' + JSON.stringify(viewport) + ') && document.querySelector(' + JSON.stringify(entry(filePath)) + ')');
        assert.equal(await page.evaluate('document.activeElement?.id === "command"'), false, viewport + " first load");
        await page.evaluate(key => navigationFixture.arm(key), heldCommand);
        await center(entry(filePath));
        await tap(entry(filePath));
        await page.wait('navigationFixture.holds.has(' + JSON.stringify(heldCommand) + ')');
        assert.equal(await page.evaluate(transcriptFocus), true, viewport + " pending file");
        await center(entry(touchChild));
        await tap(entry(touchChild));
        await page.wait('location.pathname === ' + JSON.stringify(touchChild) + ' && document.querySelector(' + JSON.stringify(entry(childFilePath)) + ')');
        await page.evaluate(async key => {
          navigationFixture.release(key);
          await new Promise(requestAnimationFrame);
        }, heldCommand);
        assert.deepEqual(await page.evaluate(() => ({
          path: location.pathname,
          view: new URLSearchParams(location.search).get("view"),
          prompt: document.getElementById("command-prompt").textContent,
          focused: document.activeElement.id,
          staleOutput: document.getElementById("transcript").textContent.includes("Mobile navigation entry 27"),
          held: navigationFixture.holds.size,
          settled: [...navigationFixture.settled],
        })), {
          path: touchChild, view: "terminal", prompt: "guest@browser:" + touchChild + "$ ",
          focused: "transcript", staleOutput: false, held: 0, settled: [heldCommand],
        });
        await center(entry(childFilePath));
        await tap(entry(childFilePath));
        await page.wait('document.getElementById("terminal-status").textContent.trim() === "Child file opened."');
        assert.equal(await page.evaluate(transcriptFocus), true, viewport + " completed file");
        await capture("terminal-touch-result-" + viewport);
        await page.evaluate("history.back()");
        await page.wait('location.pathname === ' + JSON.stringify(touchPath));
        assert.equal(await page.evaluate(transcriptFocus), true, viewport + " Terminal Back");

        await tap("#command");
        await page.send("Input.insertText", { text: "pwd" });
        await key("Enter", 13);
        await page.wait('document.getElementById("terminal-status").textContent === ' + JSON.stringify(touchPath));
        assert.equal(await page.evaluate('document.activeElement?.id'), "command");
        t.diagnostic(viewport + " touch activation and Back keep output focus, held response settled without stale output, and deliberate typing holds command focus.");
      }
    } finally {
      try {
        await releaseNavigationResponses(instrumentation);
      } finally {
        await page.send("Emulation.setTouchEmulationEnabled", { enabled: false });
        await page.send("Emulation.clearDeviceMetricsOverride");
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
      await page.wait('location.pathname === "/docs" && !document.querySelector("#directory-error").hidden && document.querySelector("#readme button")');
      assert.deepEqual(await page.evaluate(() => [
        document.querySelector("#command-prompt").textContent,
        document.querySelector("#readme [role=status]").textContent,
        document.activeElement?.id,
      ]), ["guest@browser:/docs$ ", "Could not load the README.", "command"]);
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

  await t.test("Terminal listings open text and download archives and uncolored files", async () => {
    const directory = "/click-files";
    const folder = join(site, directory);
    const downloads = join(scratch, "clicked-downloads");
    await mkdir(join(folder, "child"), { recursive: true });
    await mkdir(downloads);
    const texts = new Map([
      ["note.txt", "Text file opened by click."],
      ["notes.MD", "Markdown file opened by click."],
      ["settings.json", '{"opened":"by click"}'],
    ]);
    const binaries = new Map([
      ["dynebolic-git.tar.bz2", Buffer.from([0x42, 0x5a, 0x68, 0x00])],
      ["package.deb", Buffer.from([0x21, 0x3c, 0x61, 0xff])],
      ["live [x].iso", Buffer.from([0x00, 0x01, 0x02, 0x03])],
    ]);
    for (const [name, body] of [...texts, ...binaries]) await writeFile(join(folder, name), body);
    await writeFile(join(folder, "live x.iso"), "Glob match with different bytes");
    const nested = Buffer.from("Nested package only");
    await writeFile(join(folder, "child", "package.deb"), nested);

    await page.send("Browser.setDownloadBehavior", { behavior: "allow", downloadPath: downloads });
    await page.open(origin + directory + "?view=terminal&click-files");
    async function clickEntry(path) {
      const selector = ".clickable-file[data-file=" + JSON.stringify(path) + "]";
      await page.wait("document.querySelector(" + JSON.stringify(selector) + ") !== null");
      await center(selector);
      const point = await page.evaluate(selector => {
        const { left, top, width, height } = document.querySelector(selector).getBoundingClientRect();
        return { x: left + width / 2, y: top + height / 2 };
      }, selector);
      await page.send("Input.dispatchMouseEvent", { type: "mousePressed", button: "left", clickCount: 1, ...point });
      await page.send("Input.dispatchMouseEvent", { type: "mouseReleased", button: "left", clickCount: 1, ...point });
    }
    async function checkDownload(name, expected) {
      const body = await until(async () => {
        try {
          return await readFile(join(downloads, name));
        } catch (error) {
          if (error.code !== "ENOENT") throw error;
        }
      }, "downloaded " + name);
      assert.deepEqual(body, expected);
      await rm(join(downloads, name));
    }

    for (const [name, body] of texts) {
      await clickEntry(directory + "/" + name);
      await page.wait('document.querySelector("#terminal-status").textContent === ' + JSON.stringify(body));
    }
    for (const [name, body] of binaries) {
      await clickEntry(directory + "/" + name);
      await checkDownload(name, body);
    }
    await command("tree");
    await clickEntry(directory + "/child/package.deb");
    await checkDownload("package.deb", nested);
    await clickEntry(directory + "/child");
    await page.wait('location.pathname === "/click-files/child"');
  });

  await t.test("Keyboard completion, focus, live status and transfer controls", async () => {
    await page.open(origin + "/docs?keyboard-fixture=1");
    await page.send("Page.bringToFront");
    await page.wait('location.search === "?keyboard-fixture=1" && document.activeElement?.id === "command" && document.querySelector("#readme h1")?.textContent === "Markdown fixture"');
    const focus = () => page.evaluate(() => document.activeElement.id);
    await expectSpeech("Terminal command");
    await key("Tab", 9);
    assert.equal(await focus(), "complete-command");
    await key("Tab", 9);
    assert.equal(await focus(), "run-command");
    await key("Tab", 9, 8);
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
      const run = nodes.find(node => !node.ignored && node.role?.value === "button" && node.name?.value === "Run");
      assert.equal(input.name.value, "Terminal command");
      assert.ok(run, "Run is exposed as a named button");
      assert.equal(input.properties.find(property => property.name === "expanded").value.value, true);
      assert.equal(input.properties.find(property => property.name === "activedescendant").value.relatedNodes[0].idref, "completion-option-0");
      assert.equal(live.properties.find(property => property.name === "live").value.value, "polite");
      if (process.env.LSGET_SCREENSHOTS) {
        const evidence = { input, run, live, listbox: role("listbox"), option: role("option") };
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
    try {
      for (const width of [360, 1280]) {
        const mobile = width === 360;
        await page.send("Emulation.setDeviceMetricsOverride", { width, height: 800, deviceScaleFactor: 1, mobile });
        await page.send("Emulation.setTouchEmulationEnabled", { enabled: mobile });
        for (const percent of [100, 200]) {
          const size = 16 * percent / 100;
          const token = width + "x800-" + percent;
          await page.send("Page.setFontSizes", { fontSizes: { standard: size, fixed: size } });
          const search = (mobile ? "?view=terminal&" : "?") + "matrix=" + token;
          await page.open(origin + "/docs" + search);
          await page.wait('location.search === ' + JSON.stringify(search) + ' && document.querySelector("#transcript .clickable-file")');
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
            const run = document.querySelector("#run-command");
            const inViewport = ({ left, top, right, bottom }) => left >= -1 && top >= -1 && right <= innerWidth + 1 && bottom <= innerHeight + 1;
            return {
              width: innerWidth, height: innerHeight, size: parseFloat(getComputedStyle(command).fontSize),
              below: panel.getBoundingClientRect().top >= command.getBoundingClientRect().bottom,
              visible: [command, complete, run, panel, option].every(node => inViewport(node.getBoundingClientRect())),
              wrap: document.documentElement.scrollWidth === innerWidth,
              reducedMotion: getComputedStyle(document.querySelector("#readme")).transitionDuration === "0s",
              textKeyboard: command.inputMode === "text" && command.enterKeyHint === "go",
              touchSize: complete.getBoundingClientRect().height >= 44,
            };
          });
          if (!geometry.visible) t.diagnostic(JSON.stringify(await page.evaluate(() => Object.fromEntries(["command", "complete-command", "run-command", "completions", "completion-option-0"].map(id => { const r = document.getElementById(id).getBoundingClientRect(); return [id, { top: r.top, bottom: r.bottom, left: r.left, right: r.right }]; })))));
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
            assert.equal(await page.evaluate('document.getElementById("completions").hidden'), true);
            await key("Tab", 9);
            await key("Enter", 13);
            await page.wait('document.getElementById("command").getAttribute("aria-expanded") === "true"');
            await key("ArrowDown", 40);
            await key("ArrowDown", 40);
            await key("ArrowUp", 38);
            await key("Enter", 13);
            assert.equal(await page.evaluate('document.getElementById("command").value'), 'cat "read me two.txt"');
            await key("Enter", 13);
            await page.wait('document.getElementById("terminal-status").textContent === "Second path suggestion opened."');
            await key("ArrowUp", 38);
            assert.equal(await page.evaluate('document.getElementById("command").value'), 'cat "read me two.txt"');
            await key("ArrowDown", 40);
            await key("Tab", 9);
            await key("Tab", 9);
            await key("Tab", 9);
          }
          await page.wait('document.querySelector("#readme h1")?.textContent === "Markdown fixture"');
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
            assert.equal(await page.evaluate("document.activeElement.id"), "readme-toggle");
            assert.equal(await page.evaluate('document.querySelector("#readme-toggle").getAttribute("aria-expanded")'), "false");
            await page.wait('!history.state?.readme');
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

  await t.test("Run and quoted path suggestions work by touch in portrait and short landscape", async () => {
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: true });
    try {
      for (const [width, height] of [[320, 640], [390, 844], [844, 390]]) {
        const viewport = width + "x" + height;
        await page.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: true });
        const search = "?view=terminal&touch-command=" + viewport;
        await page.open(origin + "/docs" + search);
        await page.wait('location.search === ' + JSON.stringify(search) +
          ' && document.querySelector("#transcript .clickable-file")');
        await page.evaluate(() => {
          window.submitTaps = [];
          const run = document.getElementById("run-command");
          run.addEventListener("click", event => window.submitTaps.push([event.isTrusted, event.pointerType]));
        });
        await tap("#command");
        await page.send("Input.insertText", { text: "pwd" });
        await capture("command-form-" + viewport);
        await tap("#run-command");
        await page.wait('document.getElementById("terminal-status").textContent === "/docs"');

        await tap("#command");
        await page.send("Input.insertText", { text: "cat read" });
        await tap("#complete-command");
        await page.wait('document.getElementById("command").getAttribute("aria-expanded") === "true"');
        const geometry = await page.evaluate(() => {
          const targets = ["command", "complete-command", "run-command", "completion-option-0"];
          return {
            viewport: [innerWidth, innerHeight],
            hint: document.getElementById("completion-hint").textContent,
            targets: Object.fromEntries(targets.map(id => {
              const target = document.getElementById(id);
              const { top, left, bottom, right, height, width } = target.getBoundingClientRect();
              const hit = document.elementFromPoint(left + width / 2, top + height / 2);
              return [id, { width, height, reachable: top >= 0 && left >= 0 && bottom <= innerHeight &&
                right <= innerWidth && target.contains(hit) }];
            })),
          };
        });
        t.diagnostic(viewport + " touch command geometry " + JSON.stringify(geometry));
        assert.deepEqual(geometry.viewport, [width, height]);
        assert.match(geometry.hint, /Tap a path/);
        for (const [label, target] of Object.entries(geometry.targets)) {
          assert.ok(target.reachable && target.width >= 44 && target.height >= 44, viewport + " " + label);
        }
        await capture("command-suggestions-" + viewport);
        await tap("#completion-option-0");
        assert.equal(await page.evaluate('document.getElementById("command").value'), 'cat "read me two.txt"');
        await tap("#run-command");
        await page.wait('document.getElementById("terminal-status").textContent === "Second path suggestion opened."');
        assert.deepEqual(await page.evaluate("window.submitTaps"), [[true, "touch"], [true, "touch"]]);
      }
    } finally {
      await page.send("Emulation.setTouchEmulationEnabled", { enabled: false });
      await page.send("Emulation.clearDeviceMetricsOverride");
    }
  });

  await t.test("README and Markdown wait for compact reader intent without delaying browsing", async () => {
    const opening = 'document.querySelector("#readme [role=status]")?.textContent === "Loading README…"';
    const closed = '!history.state?.readme && document.querySelector("#readme").inert';
    const markdown = 'document.querySelector("#readme h1")?.textContent === "Markdown fixture"';
    const toggle = "#readme-toggle";
    const downloadPath = join(scratch, "lazy-reader-downloads");
    const phases = [];
    const requests = () => page.network.filter(event => event.event === "request").map(event => new URL(event.url));
    const configs = path => requests().filter(url => url.pathname === "/api/config" && url.searchParams.get("cwd") === path);
    const parsers = () => requests().filter(url => url.pathname.endsWith("/marked.min.js"));
    const mark = label => phases.push({ label, endEvent: page.network.length });

    await mkdir(downloadPath);
    await page.send("Page.enable");
    const instrumentation = await holdNavigationResponses(["config /docs"]);
    await page.send("Network.enable");
    await page.send("Network.setCacheDisabled", { cacheDisabled: true });
    await page.send("Browser.setDownloadBehavior", { behavior: "allow", downloadPath });
    await page.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: true });
    page.network.length = 0;
    try {
      await page.open(origin + "/docs?view=files&lazy-reader=files");
      await page.wait('location.search.endsWith("lazy-reader=files") && document.querySelector(".file-row")');
      assert.ok(!requests().some(url => url.pathname.startsWith("/api/")));
      assert.equal(parsers().length, 0);
      mark("files-no-readme-or-parser");

      await page.open(origin + "/docs?view=terminal&lazy-reader=compact");
      await page.wait('location.search.endsWith("lazy-reader=compact") && document.querySelector("#transcript .clickable-file")');
      assert.equal(configs("/docs").length, 0);
      assert.equal(parsers().length, 0);
      assert.equal(await page.evaluate("typeof globalThis.marked"), "undefined");
      assert.equal(await page.evaluate(closed), true);
      await capture("lazy-reader-390-before");
      mark("compact-listing-before-open");

      await tap(toggle);
      await page.wait(opening + ' && navigationFixture.holds.has("config /docs")');
      assert.equal(configs("/docs").length, 1);
      assert.equal(parsers().length, 0);
      mark("compact-readme-response-held");
      await tap(toggle);
      await page.wait(closed);
      await command("get read-me-one.txt");
      await until(async () => (await readdir(downloadPath)).includes("read-me-one.txt"), "completed download during pending README");
      assert.equal(await readFile(join(downloadPath, "read-me-one.txt"), "utf8"), "First path suggestion opened.");
      mark("download-while-readme-held");

      await tap(toggle);
      await page.wait(opening);
      assert.equal(await page.evaluate('navigationFixture.requests.filter(path => path === "/api/config").length'), 1);
      await page.evaluate('navigationFixture.release("config /docs")');
      await page.wait(markdown + ' && navigationFixture.holds.size === 0 && document.querySelector("#readme").getBoundingClientRect().top <= 1');
      assert.equal(parsers().length, 1);
      assert.equal(parsers()[0].pathname, await page.evaluate(() => new URL("./marked.min.js", document.getElementById("terminal-script").src).pathname));
      await capture("lazy-reader-390-after");
      mark("compact-opened-with-versioned-parser");

      await tap(toggle);
      await page.wait(closed);
      await command("cd /rst");
      await page.wait('location.pathname === "/rst" && document.querySelector("#command-prompt").textContent === "guest@browser:/rst$ "');
      assert.equal(configs("/rst").length, 0);
      assert.equal(await page.evaluate('document.querySelector("#readme h1")'), null);
      assert.ok(requests().some(url => url.pathname === "/api/exec" && url.searchParams.get("readme") === "0"));
      mark("compact-changed-directory-without-preview");

      await page.evaluate('navigationFixture.fail("config /rst")');
      await tap(toggle);
      await page.wait('document.querySelector("#readme [role=status]")?.textContent === "Could not load the README."' +
        ' && document.querySelector("#readme").getBoundingClientRect().top <= 1');
      assert.equal(await page.evaluate('document.querySelector("#directory-error").hidden'), true);
      await key("Tab", 9);
      const [retryFocus] = await readingColors(["#readme button:focus-visible"], "#readme");
      assert.ok(retryFocus?.visible && retryFocus.outline === "solid" && retryFocus.outlineWidth >= 2 && retryFocus.outlineRatio >= 3);
      assert.equal(await page.evaluate('getComputedStyle(document.activeElement).outlineOffset'), "2px");
      phases.push({ label: "keyboard-retry-focus", endEvent: page.network.length, retryFocus });
      await capture("lazy-reader-390-retry-focus");
      await tap("#readme button");
      await page.wait('document.querySelector("#readme h1")?.textContent === "RST fixture"');
      assert.equal(await page.evaluate('document.activeElement.id'), "readme");
      assert.equal(parsers().length, 1);
      assert.equal(await page.evaluate('navigationFixture.requests.filter(path => path === "/api/config").length'), 3);
      mark("compact-api-failure-and-retry");

      await tap(toggle);
      await page.wait(closed);
      await page.send("Emulation.setTouchEmulationEnabled", { enabled: false });
      await page.send("Emulation.setDeviceMetricsOverride", { width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
      await page.open(origin + "/docs?view=terminal&lazy-reader=desktop");
      await page.wait('location.search.endsWith("lazy-reader=desktop") && navigationFixture.holds.has("config /docs")');
      await page.wait(opening + ' && document.querySelector("#transcript .clickable-file")');
      assert.deepEqual(await page.evaluate("navigationFixture.requests"), ["/api/exec", "/api/config"]);
      assert.equal(parsers().length, 1);
      mark("desktop-listing-before-held-readme");
      await page.evaluate('navigationFixture.release("config /docs")');
      await page.wait(markdown + ' && navigationFixture.holds.size === 0');
      assert.equal(parsers().length, 2);
      mark("desktop-readme-rendered");
      t.diagnostic("Files and compact Terminal started with no README or parser. Compact Download completed while README was held, reopening reused the request, and API retry rendered the new directory. Desktop listed while README was held.");
    } finally {
      try {
        await releaseNavigationResponses(instrumentation);
        if (process.env.LSGET_SCREENSHOTS) {
          await writeFile(join(process.env.LSGET_SCREENSHOTS, "lazy-reader-network.json"), JSON.stringify({ mode, phases, events: page.network }, null, 2));
        }
      } finally {
        await page.send("Browser.setDownloadBehavior", { behavior: "default" });
        await page.send("Emulation.setTouchEmulationEnabled", { enabled: false });
        await page.send("Emulation.clearDeviceMetricsOverride");
        await page.send("Network.setCacheDisabled", { cacheDisabled: false });
        await page.send("Network.disable");
        await page.send("Page.disable");
      }
    }
  });

  await t.test("Compact README history closes before leaving the folder and keeps focus available", async () => {
    const parent = "/reader-navigation";
    const child = parent + "/chapter";
    const published = parent + "/published";
    await mkdir(join(site, child), { recursive: true });
    await mkdir(join(site, published), { recursive: true });
    await writeFile(join(site, parent, "README.md"), "# Reader parent\n\nParent documentation.\n");
    await writeFile(join(site, child, "README.md"), "# Reader chapter\n\nChapter documentation.\n");
    await writeFile(join(site, published, "index.html"), "<h1>Published reader fixture</h1>");
    const toggle = "#readme-toggle";
    const childLink = '.clickable-file[data-file=' + JSON.stringify(child) + ']';
    const inFolder = path => 'location.pathname === ' + JSON.stringify(path) +
      ' && document.querySelector("#command-prompt")?.textContent === ' + JSON.stringify("guest@browser:" + path + "$ ");
    const blocking = 'document.querySelector(".terminal").inert';
    const readmeVisible = 'document.querySelector("#readme").classList.contains("show")';
    const toggleFocused = 'document.activeElement?.id === "readme-toggle"';
    const entryClosed = '!history.state?.readme';
    const viewport = (width, height, mobile = true) =>
      page.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile });
    const go = delta => page.evaluate(delta => history.go(delta), delta);

    async function startAtParent(token) {
      const search = "?view=terminal&reader-fixture=" + token;
      await page.open(origin + parent + search);
      await page.wait('location.search === ' + JSON.stringify(search) + ' && ' + inFolder(parent));
      await page.wait('document.querySelector(' + JSON.stringify(childLink) + ')');
    }

    async function enterChild(token) {
      await startAtParent(token);
      await center(childLink);
      await tap(childLink);
      await page.wait(inFolder(child) + ' && ' + entryClosed);
    }

    async function readmeOpened(path) {
      await page.wait(inFolder(path) + ' && ' + blocking + ' && ' + readmeVisible +
        ' && document.querySelector("#readme").getBoundingClientRect().top <= 1' +
        ' && document.querySelector("#readme h1")?.textContent === ' + JSON.stringify(path === parent ? "Reader parent" : "Reader chapter"));
      assert.deepEqual(await page.evaluate(() => {
        const button = document.getElementById("readme-toggle");
        const { width, height } = button.getBoundingClientRect();
        return { label: button.getAttribute("aria-label"), expanded: button.getAttribute("aria-expanded"),
          focus: document.activeElement.id, actionSize: width >= 44 && height >= 44 };
      }), { label: "Close README", expanded: "true", focus: "readme", actionSize: true });
    }

    async function readmeClosed(path, { restoreFocus = true, baseEntry = true } = {}) {
      await page.wait(inFolder(path) + ' && !(' + blocking + ') && !(' + readmeVisible + ')' +
        (baseEntry ? ' && ' + entryClosed : ''));
      assert.deepEqual(await page.evaluate(() => {
        const button = document.getElementById("readme-toggle");
        return { label: button.getAttribute("aria-label"), expanded: button.getAttribute("aria-expanded"),
          panelInert: document.getElementById("readme").inert, focusedInert: Boolean(document.activeElement.closest("[inert]")) };
      }), { label: "Show README", expanded: "false", panelInert: true, focusedInert: false });
      if (restoreFocus) assert.equal(await page.evaluate(toggleFocused), true);
    }

    await page.send("Page.enable");
    const instrumentation = await holdNavigationResponses();
    try {
      await page.send("Emulation.setTouchEmulationEnabled", { enabled: true });
      for (const [width, height] of [[390, 844], [844, 390]]) {
        const size = width + "x" + height;
        await viewport(width, height);
        for (const preference of ["no-preference", "reduce"]) {
          await page.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-reduced-motion", value: preference }] });
          for (const dismissal of ["back", "close", "escape"]) {
            await enterChild(size + "-" + preference + "-" + dismissal);
            const childURL = await page.evaluate("location.href");
            await tap(toggle);
            await readmeOpened(child);
            assert.equal(await page.evaluate("location.href"), childURL);
            if (preference === "reduce") {
              assert.equal(await page.evaluate('getComputedStyle(document.getElementById("readme")).transitionDuration'), "0s");
            }
            const takeCapture = preference === "no-preference" && dismissal === "back";
            if (takeCapture) await capture("reader-open-" + size);
            if (dismissal === "back") await go(-1);
            else if (dismissal === "close") await tap(toggle);
            else await key("Escape", 27);
            await readmeClosed(child);
            if (takeCapture) await capture("reader-closed-" + size);
            await go(-1);
            await readmeClosed(parent);
          }
          t.diagnostic(size + " " + preference + ": Back, Close and Escape returned focus to README; next Back reached the parent.");
        }
      }

      await viewport(390, 844);
      await enterChild("rotate");
      await tap(toggle);
      await readmeOpened(child);
      await viewport(844, 390);
      await page.wait("innerWidth === 844 && innerHeight === 390");
      await readmeOpened(child);
      await capture("reader-rotated-844x390");
      await go(-1);
      await readmeClosed(child);

      await go(1);
      await readmeOpened(child);
      await viewport(1280, 800, false);
      await page.wait("innerWidth === 1280 && " + entryClosed + ' && !(' + blocking + ')');
      assert.equal(await page.evaluate('document.querySelector("#readme").contains(document.activeElement)'), true);
      await go(-1);
      await page.wait(inFolder(parent));
      t.diagnostic("Portrait-to-landscape rotation kept the reader and focus; widening to desktop consumed the overlay entry.");

      await viewport(390, 844);
      await startAtParent("stale-config");
      await page.evaluate(path => navigationFixture.arm("config " + path), parent);
      await tap(toggle);
      await page.wait('location.pathname === ' + JSON.stringify(parent) +
        ' && navigationFixture.holds.has(' + JSON.stringify("config " + parent) + ')');
      await go(-1);
      await readmeClosed(parent);
      await center(childLink);
      await tap(childLink);
      await page.wait(inFolder(child) + ' && ' + entryClosed);
      await readmeClosed(child, { restoreFocus: false });
      assert.equal(await page.evaluate('document.activeElement.id'), "transcript");
      await tap(toggle);
      await readmeOpened(child);
      await page.evaluate(async path => {
        navigationFixture.release("config " + path);
        await new Promise(requestAnimationFrame);
      }, parent);
      assert.deepEqual(await page.evaluate("Array.from(navigationFixture.settled)"), ["config " + parent]);
      await readmeOpened(child);
      await go(-2);
      await readmeClosed(parent);
      await go(2);
      await readmeClosed(child, { baseEntry: false });
      t.diagnostic("A real stale parent README response settled without replacing the newer chapter. A cross-folder jump to an overlay entry kept the reader closed.");

      for (const destination of [child, published]) {
        await startAtParent("pending-" + destination.split("/").at(-1));
        const pending = "cd " + destination;
        await page.evaluate(pending => navigationFixture.arm(pending), pending);
        await command(pending);
        await page.wait('navigationFixture.holds.has(' + JSON.stringify(pending) + ')');
        await tap(toggle);
        await readmeOpened(parent);
        const release = await page.evaluate(async pending => {
          navigationFixture.release(pending);
          await Promise.resolve();
          return { held: navigationFixture.holds.size, settled: [...navigationFixture.settled] };
        }, pending);
        assert.deepEqual(release, { held: 0, settled: [pending] });
        if (destination === child) {
          await readmeClosed(child);
        } else {
          await page.wait('document.querySelector("h1")?.textContent === "Published reader fixture"');
        }
        await go(-1);
        await readmeClosed(parent, { restoreFocus: destination === child });
      }
      t.diagnostic("Pending cd commands consumed the old overlay entry when reaching a folder or a published index. Back reached the plain parent view.");

      await enterChild("repeated-dismissal");
      await tap(toggle);
      await readmeOpened(child);
      await page.evaluate(() => {
        const button = document.getElementById("readme-toggle");
        button.click();
        button.click();
      });
      await readmeClosed(child);
      await go(-1);
      await readmeClosed(parent);
      t.diagnostic("Two immediate Close activations consumed one reader entry.");
    } finally {
      try {
        await releaseNavigationResponses(instrumentation);
      } finally {
        await page.send("Emulation.setEmulatedMedia", { features: [] });
        await page.send("Emulation.setTouchEmulationEnabled", { enabled: false });
        await page.send("Emulation.clearDeviceMetricsOverride");
        await page.send("Page.disable");
      }
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

  await t.test("Files starts at the top and Back restores the visible row after a file or child folder", async () => {
    const listIs = path => 'location.pathname === ' + JSON.stringify(path) +
      ' && document.querySelector("h1")?.textContent === ' + JSON.stringify("Index of " + path);
    const position = selector => page.evaluate(selector => {
      const { top, bottom } = document.querySelector(selector).getBoundingClientRect();
      return {
        scroll: scrollY, top,
        visible: top >= document.querySelector("header").getBoundingClientRect().bottom && bottom <= innerHeight,
      };
    }, selector);
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: true });
    try {
      for (const [width, height] of [[320, 640], [390, 844]]) {
        const viewport = width + "x" + height;
        await page.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: true });
        await page.open(origin + touchPath + "?view=files&test=" + viewport);
        await page.wait(listIs(touchPath) + ' && location.search.endsWith(' + JSON.stringify(viewport) + ')');
        const initial = await page.evaluate(() => {
          const visible = selector => {
            const { top, bottom } = document.querySelector(selector).getBoundingClientRect();
            return top >= 0 && bottom <= innerHeight;
          };
          return { scroll: scrollY, rows: document.querySelectorAll(".file-row").length, up: visible(".up"), first: visible(".file-row") };
        });
        assert.deepEqual(initial, { scroll: 0, rows: 54, up: true, first: true });
        await capture("files-entry-" + viewport);
        for (const [label, destination] of [["file", touchPath + "/entry-27.txt"], ["child", touchChild]]) {
          const selector = label === "file" ? '.file-row[href=' + JSON.stringify(destination) + ']'
            : '.file-row[href^=' + JSON.stringify(destination + "?") + ']';
          await center(selector);
          const before = await position(selector);
          assert.ok(before.scroll > 0 && before.visible, viewport + " before " + label);
          await capture("files-" + label + "-before-" + viewport);
          await tap(selector);
          if (label === "file") {
            await page.wait('location.pathname === ' + JSON.stringify(destination) + ' && document.body.textContent.includes("Mobile navigation entry 27")');
          } else {
            await page.wait(listIs(destination));
            assert.deepEqual(await page.evaluate(() => {
              const query = new URLSearchParams(location.search);
              const up = document.querySelector(".up").getBoundingClientRect();
              return { scroll: scrollY, up: up.top >= 0 && up.bottom <= innerHeight, view: query.get("view"), test: query.get("test") };
            }), { scroll: 0, up: true, view: "files", test: viewport });
          }
          await page.evaluate("history.back()");
          await page.wait(listIs(touchPath));
          const after = await until(async () => {
            const current = await position(selector);
            return current.visible && Math.abs(current.scroll - before.scroll) <= 1 &&
              Math.abs(current.top - before.top) <= 1 && current;
          }, viewport + " native scroll restoration after " + label);
          assert.equal(await page.evaluate('document.activeElement?.id === "command"'), false);
          await capture("files-" + label + "-back-" + viewport);
          t.diagnostic(viewport + " " + label + " " + JSON.stringify({ initial, before, after }));
        }
      }
    } finally {
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

  await t.test("Keyboard and accessibility navigation at 200 percent text", async () => {
    const folder = "/keyboard";
    const chapter = folder + "/chapter";
    const name = "Résumé & field-report with a long name.bin";
    const file = Buffer.from([0, 3, 128, 255, 10]);
    const headings = ["Keyboard guide", "Read a note", "More details"];
    await mkdir(join(site, chapter), { recursive: true });
    await writeFile(join(site, folder, name), file);
    await writeFile(join(site, chapter, "README.md"), [
      "# " + headings[0], "", "## " + headings[1], "", "[Parent folder](../)", "", "### " + headings[2],
    ].join("\n"));

    const evidence = [];
    const failures = [];
    async function snapshot(label, stage) {
      const { nodes } = await page.send("Accessibility.getFullAXTree");
      const index = new Map(nodes.map(node => [node.nodeId, node]));
      const ordered = [];
      function visit(node) {
        if (!node) return;
        if (!node.ignored) {
          const states = Object.fromEntries((node.properties || []).filter(property =>
            ["current", "expanded", "focused", "level", "readonly"].includes(property.name))
            .map(property => [property.name, property.value.value]));
          ordered.push({ role: node.role?.value, name: node.name?.value, states });
        }
        for (const id of node.childIds || []) visit(index.get(id));
      }
      visit(nodes[0]);
      evidence.push({ label, stage, accessibility: ordered });
      return ordered;
    }

    function expectOrder(tree, targets) {
      let position = -1;
      return targets.map(([role, name]) => {
        const next = tree.findIndex((node, index) => index > position && node.role === role && node.name === name);
        assert.ok(next > position, role + " " + name + " is missing or out of reading order: " +
          JSON.stringify(tree.filter(node => targets.some(([, name]) => node.name === name))));
        position = next;
        return tree[position];
      });
    }

    async function focus(label, selector, reverse = false) {
      await key("Tab", 9, reverse ? 8 : 0);
      const measured = await page.evaluate(selector => {
        const node = document.activeElement;
        const style = getComputedStyle(node);
        const { top, bottom, left, right } = node.getBoundingClientRect();
        const coveringHeader = document.querySelector(".files") && !node.closest("header")
          ? document.querySelector("header").getBoundingClientRect().bottom : 0;
        const center = document.elementFromPoint((left + right) / 2, (top + bottom) / 2);
        const range = document.createRange();
        range.selectNodeContents(node);
        return {
          expected: node.matches(selector), tag: node.tagName, id: node.id, text: node.textContent.trim().slice(0, 100),
          visible: node.contains(center), clip: { top, bottom, left, right, coveringHeader },
          viewport: { width: innerWidth, height: innerHeight, content: document.documentElement.scrollWidth },
          rootFontSize: getComputedStyle(document.documentElement).fontSize,
          textRects: range.getClientRects().length,
          focusVisible: [...document.querySelectorAll(":focus-visible")].map(element => element === node),
          outline: { style: style.outlineStyle, width: parseFloat(style.outlineWidth), color: style.outlineColor, offset: parseFloat(style.outlineOffset) },
          boxShadow: style.boxShadow,
        };
      }, selector);
      evidence.push({ label, selector, direction: reverse ? "reverse" : "forward", focus: measured });
      const { visible, clip, viewport, rootFontSize, focusVisible, outline } = measured;
      assert.ok(measured.expected, label + " Tab expected " + selector + ": " + JSON.stringify(measured));
      assert.deepEqual(focusVisible, [true], label + " " + selector);
      assert.equal(rootFontSize, "32px", label);
      assert.equal(outline.style, "solid", label + " " + selector);
      assert.ok(outline.width >= 2, label + " " + selector);
      assert.ok(viewport.content <= viewport.width, label + " " + JSON.stringify(viewport));
      if (!visible || clip.top < clip.coveringHeader - 1 || clip.bottom > viewport.height + 1 ||
          clip.left < -1 || clip.right > viewport.width + 1) {
        failures.push({ label, selector, reason: "occluded or clipped keyboard focus", measured });
      }
      return measured;
    }

    async function view(label, selected) {
      const state = await page.evaluate(() => Array.from(document.querySelectorAll(".view-link, .view-switch a"), anchor =>
        [anchor.textContent, anchor.getAttribute("aria-current")]));
      assert.deepEqual(state, [["Files", selected === "files" ? "page" : null], ["Terminal", selected === "terminal" ? "page" : null]]);
      evidence.push({ label, selectedView: selected, currentLinks: state });
    }

    async function readerToggleFocus(label, backdrop) {
      await focus(label, "#readme-toggle");
      const [ring] = await readingColors(["#readme-toggle"], backdrop);
      evidence.push({ label, backdrop, toggleFocusContrast: ring.outlineRatio });
      if (ring.outlineRatio < 3) failures.push({ label, selector: "#readme-toggle", reason: "focus contrast", ratio: ring.outlineRatio });
    }

    async function back() {
      const { currentIndex, entries } = await page.send("Page.getNavigationHistory");
      assert.ok(currentIndex > 0);
      await page.send("Page.navigateToHistoryEntry", { entryId: entries[currentIndex - 1].id });
    }

    const folderPath = "[href^=" + JSON.stringify(chapter + "?") + "]";
    const fileLink = ".file-row:not(" + folderPath + ")";
    const folderLink = ".file-row" + folderPath;
    await page.send("Page.enable");
    await page.send("Accessibility.enable");
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: true });
    await page.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-reduced-motion", value: "reduce" }] });
    try {
      for (const [width, height] of [[320, 640], [390, 844], [844, 390]]) {
        const label = width + "x" + height + "-200";
        const downloads = join(scratch, "keyboard-download-" + width);
        await mkdir(downloads);
        await page.send("Browser.setDownloadBehavior", { behavior: "allow", downloadPath: downloads });
        await page.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: true });
        await page.send("Page.setFontSizes", { fontSizes: { standard: 32, fixed: 32 } });
        await page.open(origin + folder + "?view=files&keyboard=" + width);
        await page.wait('document.querySelector("h1")?.textContent === "Index of /keyboard" && location.search.endsWith(' + JSON.stringify(String(width)) + ')');
        assert.equal(await page.evaluate("document.scripts.length"), 0);
        await view(label, "files");
        const closed = await snapshot(label, "files-closed");
        const [, , , heading, , , action] = expectOrder(closed, [
          ["link", "Up to parent folder"], ["link", "Files"], ["link", "Terminal"], ["heading", "Index of /keyboard"],
          ["link", "chapter/"], ["link", name], ["DisclosureTriangle", "Actions for " + name],
        ]);
        assert.equal(heading.states.level, 1);
        assert.equal(action.states.expanded, false);
        assert.ok(!closed.some(node => node.name === "Download " + name || node.name === "Share URL for " + name));
        for (const selector of [".up", "#files-view", "#terminal-view", ".breadcrumbs a", folderLink]) {
          await focus(label, selector);
        }
        const filename = await focus(label, fileLink);
        if (width < 400) assert.ok(filename.textRects > 1, label + " filename should wrap");
        await focus(label, "summary");
        await key("Enter", 13);
        assert.equal(await page.evaluate('document.querySelector("details").open'), true);
        const opened = await snapshot(label, "files-expanded");
        const [expanded, , share] = expectOrder(opened, [
          ["DisclosureTriangle", "Actions for " + name], ["link", "Download " + name], ["textbox", "Share URL for " + name],
        ]);
        assert.equal(expanded.states.expanded, true);
        assert.equal(share.states.readonly, true);
        await focus(label, ".download");
        await capture("keyboard-download-" + label);
        await focus(label, ".share-url");
        await capture("keyboard-share-" + label);
        await focus(label, ".download", true);
        if (height < 450) await capture("keyboard-reverse-download-" + label);
        await key("Enter", 13);
        const bytes = await until(async () => {
          const names = await readdir(downloads);
          if (names.length !== 1 || names[0].endsWith(".crdownload")) return;
          return readFile(join(downloads, names[0]));
        }, label + " keyboard download");
        assert.deepEqual(bytes, file);
        evidence.push({ label, downloaded: bytes.toString("hex") });
        await focus(label, "summary", true);
        if (height < 450) await capture("keyboard-reverse-summary-" + label);
        for (const selector of [fileLink, folderLink]) await focus(label, selector, true);
        await key("Enter", 13);
        await page.wait('document.querySelector("h1")?.textContent === "Index of /keyboard/chapter"');
        assert.equal(await page.evaluate('new URL(location.href).searchParams.get("view")'), "files");
        for (const selector of [".up", "#files-view", "#terminal-view"]) await focus(label, selector);
        await key("Enter", 13);
        await page.wait('document.querySelector("#transcript .clickable-file")');
        await view(label, "terminal");
        const terminal = await snapshot(label, "terminal");
        const terminalOrder = expectOrder(terminal, [
          ["link", "Files"], ["link", "Terminal"], ["combobox", "Terminal command"], ["button", "Run"], ["button", "Show README"],
        ]);
        assert.equal(terminalOrder.at(-1).states.expanded, false);
        assert.ok(!terminal.some(node => node.role === "heading" && node.name === headings[0]));
        for (const selector of ["#files-view", "#terminal-view", "#transcript", "#command"]) {
          await focus(label, selector);
        }
        await page.send("Input.insertText", { text: "pwd" });
        await focus(label, "#complete-command");
        await focus(label, "#run-command");
        await capture("keyboard-run-" + label);
        await key("Enter", 13);
        await page.wait('document.getElementById("terminal-status").textContent.trim() === "/keyboard/chapter"');
        evidence.push({ label, run: "pwd", workingDirectory: chapter });
        await readerToggleFocus(label, "body");
        await key("Enter", 13);
        const openedReader = 'history.state?.readme === true && document.activeElement.id === "readme"';
        await page.wait(openedReader + ' && document.querySelector("#readme h1")?.textContent === "Keyboard guide"');
        const readerGeometry = await page.evaluate(() => {
          const panel = document.getElementById("readme");
          const text = document.createRange();
          text.selectNodeContents(panel.querySelector("h1"));
          return {
            content: panel.scrollWidth, width: panel.clientWidth,
            headingLines: text.getClientRects().length,
          };
        });
        evidence.push({ label, readerGeometry });
        if (readerGeometry.content > readerGeometry.width) {
          failures.push({ label, selector: "#readme", reason: "reader content overflows horizontally", measured: { clip: readerGeometry } });
        }
        await capture("keyboard-reader-heading-" + label);
        const reader = await snapshot(label, "reader-expanded");
        const [h1, h2, , h3, toggle] = expectOrder(reader, [
          ["heading", headings[0]], ["heading", headings[1]], ["link", "Parent folder"], ["heading", headings[2]], ["button", "Close README"],
        ]);
        assert.deepEqual([h1.states.level, h2.states.level, h3.states.level], [1, 2, 3]);
        assert.equal(toggle.states.expanded, true);
        assert.ok(!reader.some(node => node.role === "button" && node.name === "Run"));
        await focus(label, "#readme a");
        await capture("keyboard-reader-" + label);
        await readerToggleFocus(label, "#readme");
        await capture("keyboard-reader-close-" + label);
        const dismissed = '!history.state?.readme && document.activeElement.id === "readme-toggle" && document.getElementById("readme").inert';
        await key("Escape", 27);
        await page.wait(dismissed);
        await key("Enter", 13);
        await page.wait(openedReader);
        await back();
        await page.wait(dismissed);
        await back();
        await page.wait('document.querySelector("h1")?.textContent === "Index of /keyboard/chapter"');
        assert.equal(await page.evaluate("location.pathname"), chapter);
        assert.equal(await page.evaluate('new URL(location.href).searchParams.get("view")'), "files");
        evidence.push({ label, escapeAndBackRestoreTrigger: true, secondBackReturnsFiles: true });
      }
      assert.deepEqual(failures.map(({ label, selector, reason, measured, ratio }) =>
        [label, selector, reason, measured?.clip ?? ratio]), []);
    } finally {
      if (process.env.LSGET_SCREENSHOTS) {
        await writeFile(join(process.env.LSGET_SCREENSHOTS, "keyboard-navigation.json"), JSON.stringify({ evidence, failures }, null, 2));
      }
      await page.send("Browser.setDownloadBehavior", { behavior: "default" });
      await page.send("Accessibility.disable");
      await page.send("Emulation.setEmulatedMedia", { features: [] });
      await page.send("Emulation.setTouchEmulationEnabled", { enabled: false });
      await page.send("Page.setFontSizes", { fontSizes: { standard: 16, fixed: 16 } });
      await page.send("Emulation.clearDeviceMetricsOverride");
      await page.send("Page.disable");
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

  await t.test("Cold directory entry selects Files before requesting Terminal assets", async () => {
    const publicBase = "https://public.example/releases%20here";
    const directoryServer = () => startServer(cleanups, binary, site, workA, mode, "", ["-baseurl", publicBase]);
    const address = await directoryServer();
    const visible = view => view === "terminal"
      ? `document.querySelector("#command-prompt")?.textContent === ${JSON.stringify("guest@browser:" + touchChild + "$ ")}`
      : `document.querySelector("main h1")?.textContent === ${JSON.stringify("Index of " + touchChild)}`;
    const shellScripts = ["terminal-view.js", "terminal.js"];
    const icon = await readFile(new URL("../../internal/lsget/assets/favicon.png", import.meta.url));
    const query = "keep=two%26words&keep=second&flag=&file=view-selected.txt";
    const traces = [];

    async function viewport(width, height, touch) {
      await page.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: touch });
      await page.send("Emulation.setTouchEmulationEnabled", { enabled: touch });
    }

    async function verify(label, view, expectedSearch, navigate, hash = "") {
      await page.send("Network.clearBrowserCache");
      page.network.length = 0;
      const trace = { label, view };
      traces.push(trace);
      try {
        await navigate();
        await page.wait(visible(view) + ' && document.readyState === "complete"');
        Object.assign(trace, await page.evaluate(() => ({
          search: location.search, hash: location.hash, viewport: [innerWidth, innerHeight],
          coarse: matchMedia("(any-pointer: coarse)").matches, canonical: document.querySelector('link[rel="canonical"]').href,
          icon: document.querySelector('link[rel="icon"]')?.href,
        })));
        await capture("entry-" + label);
      } finally {
        trace.events = [...page.network];
      }
      assert.equal(trace.search, expectedSearch);
      assert.equal(trace.hash, hash);
      assert.equal(trace.canonical, publicBase + touchChild);
      const requests = trace.events.filter(event => event.event === "request");
      const bundle = requests.filter(event => new URL(event.url).pathname.startsWith("/assets/lsget-shell/"));
      if (view === "terminal") {
        assert.deepEqual(bundle.filter(event => event.type === "Script").map(event => new URL(event.url).pathname.split("/").at(-1)).sort(), shellScripts);
        assert.match(trace.icon, /\/assets\/lsget-shell\/[a-f0-9]{64}\/favicon\.png$/u);
      } else {
        assert.deepEqual(bundle, []);
        assert.equal(trace.icon, "data:image/png;base64," + icon.toString("base64"));
        assert.ok(!requests.some(event => /^\/(api\/|assets\/js\/|favicon\.ico$)/u.test(new URL(event.url).pathname)), label);
      }
      t.diagnostic(`${label}: ${trace.viewport.join("x")}, coarse=${trace.coarse}, ${view}, ${requests.length} cold requests, ${bundle.length} shell assets`);
      return trace;
    }

    await page.send("Network.enable");
    await page.send("Network.setCacheDisabled", { cacheDisabled: true });
    try {
      for (const [width, height, touch] of [[320, 640, true], [390, 844, true], [844, 390, true], [1280, 800, false], [390, 844, false]]) {
        await viewport(width, height, touch);
        // A fresh origin avoids serving the same Terminal URL from the current history entry.
        const entry = touch ? address : await directoryServer();
        const observed = await verify(`${width}x${height}-${touch ? "touch" : "pointer"}`, touch ? "files" : "terminal",
          touch ? "?flag=&keep=two%26words&keep=second&view=files" : "?" + query,
          () => page.open(entry + touchChild + "?" + query + "#shared"), "#shared");
        assert.deepEqual(observed.viewport, [width, height]);
        assert.equal(observed.coarse, touch);
      }

      await viewport(390, 844, true);
      await verify("explicit-terminal", "terminal", "?view=terminal&" + query,
        () => page.open(address + touchChild + "?view=terminal&" + query));
      await verify("explicit-nojs", "files", "?view=terminal&nojs=1&" + query,
        () => page.open(address + touchChild + "?view=terminal&nojs=1&" + query));
      await page.send("Emulation.setScriptExecutionDisabled", { value: true });
      await verify("disabled-scripts", "files", "?flag=&keep=two%26words&keep=second&nojs=1",
        () => page.open(address + touchChild + "?" + query));
      await page.send("Emulation.setScriptExecutionDisabled", { value: false });

      await page.open(address + "/docs/guide/");
      await page.wait('document.querySelector("h1")?.textContent === "Published guide"');
      const before = await page.send("Page.getNavigationHistory");
      await page.evaluate(href => {
        const link = document.createElement("a");
        link.href = href;
        link.textContent = "Shared directory";
        document.body.append(link);
        link.focus();
      }, address + touchChild + "?" + query + "#shared");
      const shared = await verify("nested-shared-link", "files", "?flag=&keep=two%26words&keep=second&view=files",
        () => key("Enter", 13), "#shared");
      const after = await page.send("Page.getNavigationHistory");
      // Chrome can evict the oldest entry when navigation history is full.
      shared.history = { source: before.entries[before.currentIndex], previous: after.entries[after.currentIndex - 1] };
      assert.equal(shared.history.previous?.id, shared.history.source.id);
      await page.evaluate("history.back()");
      await page.wait('location.pathname === "/docs/guide/" && document.querySelector("h1")?.textContent === "Published guide"');
      shared.history.afterBack = await page.evaluate("location.href");

      await viewport(1280, 800, false);
      await verify("explicit-files", "files", "?view=files&" + query,
        () => page.open(address + touchChild + "?view=files&" + query));
    } finally {
      if (process.env.LSGET_SCREENSHOTS) {
        await writeFile(join(process.env.LSGET_SCREENSHOTS, "entry-network.json"), JSON.stringify({ mode, traces }, null, 2));
      }
      await page.send("Emulation.setScriptExecutionDisabled", { value: false });
      await page.send("Emulation.setTouchEmulationEnabled", { enabled: false });
      await page.send("Emulation.clearDeviceMetricsOverride");
      await page.send("Network.setCacheDisabled", { cacheDisabled: false });
      await page.send("Network.disable");
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
        assert.match(reportOnly, /script-src 'nonce-[^']+';/u);
      } else {
        assert.equal(reportOnly, null);
        assert.match(enforced, /script-src 'nonce-[^']+';/u);
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
    await page.wait(`document.title === ${JSON.stringify(title)} && document.querySelector('link[rel="icon"]')`);
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

  await t.test("Shared directory links complete Files, README and archive journeys", async t => {
    await verifySharedJourney(t, { page, origin, scratch, mode, capture, tap, center, key, until });
  });

  await t.test("The shared directory journey works at 200 percent text", async t => {
    const largePage = await browserPage(cleanups, join(scratch, "large-text-browser"), 32);
    await verifySharedJourney(t, { page: largePage, origin, scratch, mode, ...browserControls(largePage), until, largeText: true });
  });

  await t.test("The nonce-only shell policy covers the native controls and lazy parser", async () => {
    const policyMessages = () => page.policyMessages.filter(message => /Content Security Policy/u.test(message));
    await page.open(origin + "/docs?policy-fixture");
    await page.wait('location.search === "?policy-fixture" && document.querySelector("#readme h1")?.textContent === "Markdown fixture"');
    assert.deepEqual(policyMessages(), []);
    await page.evaluate(() => {
      const script = document.createElement("script");
      script.textContent = "window.policyFixtureRan = true";
      document.head.append(script);
    });
    await until(() => policyMessages().length > 0, "nonce-free script policy message");
    assert.equal(await page.evaluate("window.policyFixtureRan === true"), mode === "report-only");
    assert.ok(policyMessages().every(message => /inline/iu.test(message) && /report.only/iu.test(message) === (mode === "report-only")));
    t.diagnostic(`${mode}: native shell and lazy parser ran with no policy violations; a nonce-free inline script generated the expected policy report.`);
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

  if (process.env.LSGET_PLAN_BASELINE_BINARY) {
    await t.test("Mobile first listing transfers less than the whole-plan baseline", async t => {
      const previous = await startServer(cleanups, resolve(process.env.LSGET_PLAN_BASELINE_BINARY), site, workA, mode);
      const current = await startServer(cleanups, binary, site, workA, mode);
      await compareFirstListing(t, { page, previous, current, mode, capture, until });
    });
  }
});
