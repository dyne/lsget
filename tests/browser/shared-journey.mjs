import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import { join } from "node:path";

const parent = "/release-journey";
const nested = parent + "/shared";
const folder = parent + "/preview";
const preview = folder + "/preview.svg";
const archiveName = "release.zip";
const archive = folder + "/" + archiveName;
const portal = "/shared-link/";
const readmeHeading = "Shared release notes";
const touchCases = [[320, 640, 16, true], [390, 844, 16, true], [844, 390, 16, true], [390, 844, 16, false], [390, 844, 32, true]];
const caseLabel = (width, height, fontSize, scripts) => `${width}x${height}-${fontSize / 16 * 100}${scripts ? "" : "-nojs"}`;
// A stored ZIP containing note.txt with a fixed timestamp and release text.
const archiveBytes = Buffer.from("UEsDBBQAAAAAAAAAdV0E8FYcFQAAABUAAAAIAAAAbm90ZS50eHRMb2NhbCBzaGFyZWQgcmVsZWFzZQpQSwECFAMUAAAAAAAAAHVdBPBWHBUAAAAVAAAACAAAAAAAAAAAAAAAgAEAAAAAbm90ZS50eHRQSwUGAAAAAAEAAQA2AAAAOwAAAAAA", "base64");

const fileLink = path => `.file-row[href=${JSON.stringify(path)}]`;
const terminalFile = path => `.clickable-file[data-file=${JSON.stringify(path)}]`;
const filesAt = path => `location.pathname === ${JSON.stringify(path)} && document.querySelector("main h1")?.textContent === ${JSON.stringify("Index of " + path)}`;
const terminalAt = path => `location.pathname === ${JSON.stringify(path)} && document.querySelector("#command-prompt")?.textContent === ${JSON.stringify("guest@browser:" + path + "$ ")}`;
const readmeReady = `document.querySelector("#readme h1")?.textContent === ${JSON.stringify(readmeHeading)}`;

export async function createSharedJourneyFixture(site) {
  for (const path of [nested, folder, portal]) await mkdir(join(site, path), { recursive: true });
  const links = touchCases.map(values => {
    const label = caseLabel(...values);
    return `<p><a data-viewport="${label}" href="${nested}?campaign=release&amp;case=${label}">Shared directory (${label})</a></p>`;
  });
  await writeFile(join(site, portal, "index.html"), `<!doctype html><meta name="viewport" content="width=device-width, initial-scale=1"><title>Shared link source</title>${links.join("\n")}`);
  await writeFile(join(site, nested, "note.txt"), "Start in a shared nested directory.\n");
  await writeFile(join(site, folder, "README.md"), `# ${readmeHeading}\n\nA Markdown note shipped with the archive.\n`);
  await writeFile(join(site, preview), '<svg id="shared-preview" xmlns="http://www.w3.org/2000/svg" width="96" height="48"><rect width="96" height="48" fill="blue"/></svg>');
  await writeFile(join(site, archive), archiveBytes);
  for (let row = 1; row <= 18; row++) {
    await writeFile(join(site, folder, "item-" + String(row).padStart(2, "0") + ".txt"), "A listing row before the preview.\n");
  }
}

async function browserBack(page) {
  const { currentIndex, entries } = await page.send("Page.getNavigationHistory");
  assert.ok(currentIndex > 0);
  await page.send("Page.navigateToHistoryEntry", { entryId: entries[currentIndex - 1].id });
}

async function filePosition(page, selector) {
  return page.evaluate(selector => {
    const item = document.querySelector(selector).getBoundingClientRect();
    const header = document.querySelector("header").getBoundingClientRect();
    return { scrollY, top: item.top, visible: item.top >= header.bottom && item.bottom <= innerHeight,
      fontSize: parseFloat(getComputedStyle(document.documentElement).fontSize) };
  }, selector);
}

async function assertRestoredPosition(page, selector, record, until) {
  await until(async () => {
    const after = await filePosition(page, selector);
    record.after = after;
    return after.visible && after.fontSize === record.before.fontSize &&
      Math.abs(after.scrollY - record.before.scrollY) <= 4 && Math.abs(after.top - record.before.top) <= 4;
  }, "Files scroll and item position restored");
}

async function readArchive(downloadPath, until) {
  const saved = await until(async () => {
    const files = await readdir(downloadPath);
    if (files.includes(archiveName)) return readFile(join(downloadPath, archiveName));
  }, "the shared ZIP to finish downloading");
  assert.deepEqual(saved, archiveBytes);
  return { bytes: saved.length, sha256: createHash("sha256").update(saved).digest("hex") };
}

export async function verifySharedJourney(t, { page, origin, scratch, mode, capture, tap, center, key, until, largeText = false }) {
  const cases = [];
  const action = `li:has(> ${fileLink(archive)})`;
  const shareField = `${action} .share-url`;
  const compactClosed = '!history.state?.readme && document.querySelector("#readme").inert && !document.querySelector(".terminal").inert';

  async function viewport(width, height, touch) {
    await page.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: touch });
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: touch });
  }

  await page.send("Page.enable");
  try {
    for (const [width, height, fontSize, scripts] of touchCases) {
      if (largeText !== (fontSize === 32)) continue;
      const label = caseLabel(width, height, fontSize, scripts);
      const record = { label, interaction: "CDP touch taps, browser history traversal and Ctrl+A selection", scripts, steps: [] };
      cases.push(record);
      await viewport(width, height, true);
      await page.send("Emulation.setScriptExecutionDisabled", { value: !scripts });
      const downloads = join(scratch, "journey-" + label);
      await mkdir(downloads);
      await page.send("Browser.setDownloadBehavior", { behavior: "allow", downloadPath: downloads });

      await page.open(origin + portal);
      await page.wait('document.title === "Shared link source"');
      const sharedLink = `a[data-viewport=${JSON.stringify(label)}]`;
      await center(sharedLink);
      await tap(sharedLink);
      await page.wait(filesAt(nested));
      record.initial = await page.evaluate(() => ({
        search: location.search, size: parseFloat(getComputedStyle(document.documentElement).fontSize),
        viewport: [innerWidth, innerHeight], coarse: matchMedia("(any-pointer: coarse)").matches,
      }));
      const search = `?campaign=release&case=${label}&${scripts ? "view=files" : "nojs=1"}`;
      assert.deepEqual(record.initial, { search, size: fontSize, viewport: [width, height], coarse: true });
      record.steps.push("shared link opened the nested Files view");
      await capture("journey-entry-" + label);

      await tap(".up");
      await page.wait(filesAt(parent));
      await tap('.file-row[href^="' + folder + '?"]');
      await page.wait(filesAt(folder));
      assert.equal(await page.evaluate("location.search"), search);
      const previewLink = fileLink(preview);
      await center(previewLink);
      const beforePreview = await filePosition(page, previewLink);
      assert.ok(beforePreview.visible && beforePreview.scrollY > 0);
      record.previewBack = { before: beforePreview };
      await tap(previewLink);
      await page.wait(`location.pathname === ${JSON.stringify(preview)} && document.documentElement.id === "shared-preview"`);
      await capture("journey-preview-" + label);
      await browserBack(page);
      await page.wait(filesAt(folder));
      await assertRestoredPosition(page, previewLink, record.previewBack, until);
      record.steps.push("Up, child, SVG preview and Back restored the file row");

      await center(`${action} summary`);
      await tap(`${action} summary`);
      await page.wait(`document.querySelector(${JSON.stringify(`${action} details`)})?.open`);
      await center(shareField);
      await tap(shareField);
      await key("a", 65, 2);
      record.share = await page.evaluate(selector => {
        const field = document.querySelector(selector);
        return { value: field.value, readOnly: field.readOnly, focused: field === document.activeElement,
          selected: field.value.slice(field.selectionStart, field.selectionEnd) };
      }, shareField);
      const expectedShare = origin + archive;
      assert.deepEqual(record.share, { value: expectedShare, selected: expectedShare, readOnly: true, focused: true });
      await capture("journey-share-" + label);
      const download = `${action} .download`;
      await center(download);
      await tap(download);
      record.download = await readArchive(downloads, until);
      record.steps.push("archive Share selected and exact ZIP bytes downloaded");

      if (scripts) {
        const switchLink = "#terminal-view";
        await center(switchLink);
        const beforeTerminal = await filePosition(page, switchLink);
        record.terminalBack = { before: beforeTerminal };
        await tap(switchLink);
        await page.wait(terminalAt(folder) + ` && document.querySelector(${JSON.stringify(terminalFile(archive))}) && ` + compactClosed);
        assert.equal(await page.evaluate('document.querySelector("#readme h1")'), null);
        const beforeReadme = await page.evaluate('document.getElementById("transcript").scrollTop');
        await tap("#readme-toggle");
        await page.wait(readmeReady + ' && document.querySelector("#readme").getBoundingClientRect().top <= 1');
        await capture("journey-readme-" + label);
        await browserBack(page);
        await page.wait(compactClosed);
        const afterReadme = await page.evaluate(() => ({ focus: document.activeElement.id, scroll: document.getElementById("transcript").scrollTop }));
        assert.equal(afterReadme.focus, "readme-toggle");
        assert.equal(afterReadme.scroll, beforeReadme);
        record.readerBack = { before: beforeReadme, after: afterReadme };
        await browserBack(page);
        await page.wait(filesAt(folder));
        await assertRestoredPosition(page, switchLink, record.terminalBack, until);
        record.steps.push("explicit Terminal README opened, Back restored Terminal focus and scroll, then Files position");
      }
      t.diagnostic(JSON.stringify(record));
    }

    if (largeText) return;
    const record = { label: "1280x800-desktop", interaction: "CDP text input, Enter, keyboard selection and browser Back", steps: [] };
    cases.push(record);
    await page.send("Emulation.setScriptExecutionDisabled", { value: false });
    await viewport(1280, 800, false);
    const downloads = join(scratch, "journey-desktop-downloads");
    await mkdir(downloads);
    await page.send("Browser.setDownloadBehavior", { behavior: "allow", downloadPath: downloads });
    await page.open(origin + nested + "?campaign=keyboard");
    await page.wait(terminalAt(nested) + ' && document.activeElement.id === "command"');
    record.viewport = await page.evaluate(() => [innerWidth, innerHeight, matchMedia("(any-pointer: coarse)").matches]);
    assert.deepEqual(record.viewport, [1280, 800, false]);
    const type = async command => {
      await page.send("Input.insertText", { text: command });
      await key("Enter", 13);
    };
    await type("cd ..");
    await page.wait(terminalAt(parent));
    await type("cd preview");
    await page.wait(terminalAt(folder) + " && " + readmeReady);
    await type("ls");
    await page.wait(`document.querySelector(${JSON.stringify(terminalFile(archive))})`);
    await type("cat preview.svg");
    await page.wait('document.querySelector(".terminal-image img")?.naturalWidth === 96');
    await type("get release.zip");
    record.download = await readArchive(downloads, until);
    await type("share release.zip");
    await page.wait('document.querySelector(".share-controls input")');
    await page.evaluate('document.querySelector(".share-controls input").focus()');
    await key("a", 65, 2);
    record.share = await page.evaluate(() => {
      const field = document.activeElement;
      return field.value.slice(field.selectionStart, field.selectionEnd);
    });
    assert.equal(record.share, origin + archive);
    await capture("journey-desktop-terminal");
    await browserBack(page);
    await page.wait(terminalAt(parent));
    record.steps.push("typed up, child, listing, SVG preview, exact ZIP download and Share, then Back from desktop README folder");
    t.diagnostic(JSON.stringify(record));
  } finally {
    if (process.env.LSGET_SCREENSHOTS) {
      const file = largeText ? "shared-journey-200.json" : "shared-journey.json";
      await writeFile(join(process.env.LSGET_SCREENSHOTS, file), JSON.stringify({ mode, cases, font: largeText ? "32px Chrome profile" : "16px Chrome default" }, null, 2));
    }
    await page.send("Browser.setDownloadBehavior", { behavior: "default" });
    await page.send("Emulation.setScriptExecutionDisabled", { value: false });
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: false });
    await page.send("Emulation.clearDeviceMetricsOverride");
    await page.send("Page.disable");
  }
}

export async function compareFirstListing(t, { page, previous, current, mode, capture, until }) {
  const profile = { offline: false, latency: 150, downloadThroughput: 200 * 1024, uploadThroughput: 80 * 1024 };
  const viewport = { width: 390, height: 844, deviceScaleFactor: 1, mobile: true };
  const samples = [];
  await page.send("Emulation.setDeviceMetricsOverride", viewport);
  await page.send("Emulation.setTouchEmulationEnabled", { enabled: true });
  await page.send("Network.enable");
  await page.send("Network.setCacheDisabled", { cacheDisabled: true });
  await page.send("Network.emulateNetworkConditions", profile);
  try {
    for (const [label, origin, link] of [["baseline", previous, terminalFile(preview)], ["current", current, fileLink(preview)]]) {
      await page.open("about:blank");
      await page.wait('location.href === "about:blank"');
      await page.send("Network.clearBrowserCache");
      page.network.length = 0;
      const sample = { label, origin, path: folder + "?campaign=first-listing" };
      samples.push(sample);
      try {
        const start = performance.now();
        await page.open(origin + sample.path);
        await page.wait(`${label === "baseline" ? terminalAt(folder) : filesAt(folder)} && document.querySelector(${JSON.stringify(link)}) && document.readyState === "complete"`);
        sample.firstListingMs = Math.round(performance.now() - start);
        sample.boundary = page.network.length;
        const cohort = new Map(page.network.slice(0, sample.boundary)
          .filter(event => event.event === "request" && new URL(event.url).origin === origin)
          .map(event => [event.id, event]));
        const done = events => events.filter(event => event.event === "complete" && cohort.has(event.id));
        const bytes = events => events.reduce((sum, event) => sum + event.bytes, 0);
        const first = done(page.network.slice(0, sample.boundary));
        sample.requestsAtFirstListing = cohort.size;
        sample.completedAtFirstListing = first.length;
        sample.encodedBytesAtFirstListing = bytes(first);
        sample.requests = [...cohort.values()];
        sample.cssViewport = await page.evaluate(() => [innerWidth, innerHeight, devicePixelRatio, matchMedia("(any-pointer: coarse)").matches]);
        assert.deepEqual(sample.cssViewport, [390, 844, 1, true]);
        const completed = await until(() => {
          const events = done(page.network);
          return events.length === cohort.size && events;
        }, "network requests initiated by first listing to complete");
        sample.cohortEncodedBytes = bytes(completed);
        await capture("first-listing-390-" + label);
      } finally {
        sample.events = [...page.network];
      }
    }
    const [baseline, candidate] = samples;
    assert.ok(baseline.requestsAtFirstListing > candidate.requestsAtFirstListing);
    assert.ok(baseline.cohortEncodedBytes > candidate.cohortEncodedBytes);
    assert.equal(candidate.requestsAtFirstListing, 2);
    assert.ok(candidate.requests.every(event => event.type === "Document"));
    t.diagnostic(JSON.stringify(samples.map(({ label, firstListingMs, requestsAtFirstListing, completedAtFirstListing, encodedBytesAtFirstListing, cohortEncodedBytes }) =>
      ({ label, firstListingMs, requestsAtFirstListing, completedAtFirstListing, encodedBytesAtFirstListing, cohortEncodedBytes }))));
  } finally {
    if (process.env.LSGET_SCREENSHOTS) {
      await writeFile(join(process.env.LSGET_SCREENSHOTS, "first-listing-network.json"), JSON.stringify({ mode, profile, viewport, samples,
        method: "HTTP cache disabled and cleared, same local fixture and path. CDP encodedDataLength for completed browser HTTP requests at the first observed listing. Cohort totals include any later completion of HTTP requests already started at that boundary. Timing includes harness observation and is a single paired sample." }, null, 2));
    }
    await page.send("Network.emulateNetworkConditions", { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
    await page.send("Network.setCacheDisabled", { cacheDisabled: false });
    await page.send("Network.disable");
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: false });
    await page.send("Emulation.clearDeviceMetricsOverride");
  }
}
