import * as terminalView from "./terminal-view.js";

const layout = document.querySelector(".layout");
const terminal = layout.querySelector(".terminal");
const form = document.getElementById("command-form");
const input = document.getElementById("command");
const completeButton = document.getElementById("complete-command");
const transcript = document.getElementById("transcript");
const completions = document.getElementById("completions");
const completionHint = document.getElementById("completion-hint");
const completionOptions = document.getElementById("completion-options");
const panel = document.getElementById("readme");
const readmeToggle = document.getElementById("readme-toggle");
const compactReader = window.matchMedia(
  "(max-width: 768px), (orientation: landscape) and (max-height: 450px) and (any-pointer: coarse)",
);
const touchScreen = window.matchMedia("(any-pointer: coarse)");
const directoryError = document.getElementById("directory-error");
const directoryErrorMessage = document.getElementById("directory-error-message");
const retryDirectory = document.getElementById("retry-directory");
const prompt = document.getElementById("command-prompt");
const status = document.getElementById("terminal-status");

const maxTranscriptEntries = 100;
const maxTranscriptCharacters = 512 * 1024;
const maxHistoryEntries = 100;
const maxHistoryCommandLength = 8192;
const commandHistory = [];
let historyIndex = 0;
let transcriptCharacters = 0;
let cwd = layout.dataset.initialPath || "/";
let directoryRequest;
let commandRequest;
let completionRequest;
let suggestions = [];
let selectedSuggestion = -1;
let composing = false;
let readmeOpen = false;
let readmeClosing = false;
let readmeRequest;
let readmeLoaded = false;

function focusInput() {
  input.focus({ preventScroll: true });
}

function focusNavigationResult(event) {
  const touchActivation = event.pointerType === "touch" || event.pointerType === "pen" ||
    (!event.pointerType && touchScreen.matches);
  if (touchActivation) {
    transcript.focus({ preventScroll: true });
  } else {
    focusInput();
  }
}

function scrollTranscript() {
  requestAnimationFrame(() => { transcript.scrollTop = transcript.scrollHeight; });
}

function announce(message) {
  status.textContent = message;
}

function appendTranscript(render) {
  const entry = document.createElement("div");
  render(entry);
  if (!entry.hasChildNodes()) return;
  transcript.append(entry);
  transcriptCharacters += entry.textContent.length;
  while (transcript.children.length > maxTranscriptEntries ||
         transcriptCharacters > maxTranscriptCharacters) {
    const oldest = transcript.firstElementChild;
    transcriptCharacters -= oldest.textContent.length;
    oldest.remove();
  }
  scrollTranscript();
}

function appendMessage(text) {
  appendTranscript(entry => entry.append(terminalView.ansiLine(text)));
  announce(text);
}

function setInput(text, position = text.length) {
  input.value = text;
  input.setSelectionRange(position, position);
}

function quoteOperand(path) {
  return /[\s"'\\]/.test(path) ? '"' + path.replace(/["\\]/g, "\\$&") + '"' : path;
}

function rememberCommand(command) {
  if (command && command.length <= maxHistoryCommandLength) {
    commandHistory.push(command);
    if (commandHistory.length > maxHistoryEntries) commandHistory.shift();
  }
  historyIndex = commandHistory.length;
}

function setDirectory(path) {
  if (path !== cwd) {
    readmeRequest?.abort();
    readmeRequest = undefined;
    readmeLoaded = false;
    panel.replaceChildren();
    panel.scrollTop = 0;
  }
  cwd = path;
  prompt.textContent = "guest@browser:" + path + "$ ";
  document.getElementById("files-view")?.setAttribute("href", directoryURL(path, "files"));
  document.getElementById("terminal-view")?.setAttribute("href", directoryURL(path, "terminal"));
}

function directoryURL(path, view = new URLSearchParams(window.location.search).get("view")) {
  const url = new URL(terminalView.pathURL(path), window.location.origin);
  url.search = window.location.search;
  url.searchParams.delete("file");
  url.searchParams.delete("nojs");
  if (view === "files" || view === "terminal") url.searchParams.set("view", view);
  else url.searchParams.delete("view");
  return url.pathname + url.search;
}

function clearCompletion() {
  completionRequest?.abort();
  completionRequest = undefined;
  suggestions = [];
  selectedSuggestion = -1;
  input.removeAttribute("aria-activedescendant");
  input.setAttribute("aria-expanded", "false");
  completions.hidden = true;
  completionHint.textContent = "";
  completionOptions.hidden = true;
  completionOptions.replaceChildren();
}

function showCompletion(message, announcement = message) {
  completionHint.textContent = message;
  completions.hidden = false;
  announce(announcement);
  scrollTranscript();
}

function highlightSuggestion(index) {
  completionOptions.children[selectedSuggestion]?.setAttribute("aria-selected", "false");
  selectedSuggestion = index;
  const option = completionOptions.children[index];
  option.setAttribute("aria-selected", "true");
  input.setAttribute("aria-activedescendant", option.id);
  option.scrollIntoView({ block: "nearest", inline: "nearest" });
}

function acceptSuggestion(index) {
  const suggestion = suggestions[index];
  setInput(suggestion.command);
  clearCompletion();
  focusInput();
  announce("Completed " + suggestion.name + ".");
}

function updateReadme(open = false) {
  const wasOpen = readmeOpen;
  const previousFocus = document.activeElement;
  readmeOpen = compactReader.matches && open;
  panel.classList.toggle("show", readmeOpen);
  panel.inert = compactReader.matches && !readmeOpen;
  terminal.inert = readmeOpen;
  readmeToggle.setAttribute("aria-expanded", String(readmeOpen));
  readmeToggle.setAttribute("aria-label", readmeOpen ? "Close README" : "Show README");
  readmeToggle.textContent = readmeOpen ? "Close README" : "README";
  if (readmeOpen && !wasOpen) {
    panel.focus({ preventScroll: true });
  } else if (panel.inert && (wasOpen || panel.contains(previousFocus))) {
    readmeToggle.focus({ preventScroll: true });
  } else if (!compactReader.matches && previousFocus === readmeToggle) {
    panel.focus({ preventScroll: true });
  }
}

function closeReadme() {
  // Close and Escape consume the same entry as Back. Ignore repeated dismissal
  // while the history traversal is still pending.
  if (readmeClosing) return;
  readmeClosing = true;
  updateReadme();
  window.history.back();
}

function toggleReadme() {
  if (!compactReader.matches || readmeClosing) return;
  if (readmeOpen) {
    closeReadme();
    return;
  }
  if (window.history.state?.readme !== true) {
    window.history.pushState({ cwd, readme: true }, "");
  }
  updateReadme(true);
  void loadReadme();
}

async function requestJSON(endpoint, directory, request, body) {
  const query = new URLSearchParams({ cwd: directory });
  if (endpoint === "exec") query.set("readme", "0");
  const response = await fetch("/api/" + endpoint + "?" + query, {
    signal: request.signal,
    cache: "no-store",
    ...(body && {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }),
  });
  if (!response.ok) throw new Error("HTTP " + response.status);
  return response.json();
}

async function listDirectory(directory, request) {
  const { output, listing } = await requestJSON("exec", directory, request, { input: "ls" });
  if (request.signal.aborted) return;
  appendTranscript(entry => {
    terminalView.appendCommand(entry, prompt.textContent, "ls");
    if (output) entry.append(terminalView.renderOutput(output, listing));
  });
}

async function loadDirectory() {
  directoryRequest?.abort();
  const request = new AbortController();
  directoryRequest = request;
  directoryError.hidden = true;
  try {
    await listDirectory(cwd, request);
  } catch (error) {
    if (request.signal.aborted) return;
    directoryErrorMessage.textContent = "Could not load this directory (" + error.message + ").";
    directoryError.hidden = false;
    announce(directoryErrorMessage.textContent);
  } finally {
    if (directoryRequest === request) directoryRequest = undefined;
  }
}

function readmeNotice(text) {
  const notice = document.createElement("p");
  notice.className = "readme-notice";
  notice.setAttribute("role", "status");
  notice.textContent = text;
  panel.replaceChildren(notice);
}

async function loadReadme() {
  if (readmeLoaded || readmeRequest) return;
  const request = new AbortController();
  readmeRequest = request;
  readmeNotice("Loading README…");
  try {
    const response = await requestJSON("config", cwd, request);
    if (request.signal.aborted) return;
    await terminalView.readme(panel, response, request.signal);
    if (!request.signal.aborted) readmeLoaded = true;
  } catch {
    if (request.signal.aborted) return;
    readmeNotice("Could not load the README.");
    const retry = document.createElement("button");
    retry.type = "button";
    retry.className = "terminal-control";
    retry.textContent = "Retry README";
    retry.addEventListener("click", () => {
      panel.focus({ preventScroll: true });
      void loadReadme();
    });
    panel.append(retry);
  } finally {
    if (readmeRequest === request) readmeRequest = undefined;
  }
}

function loadVisibleReadme() {
  if (!compactReader.matches || readmeOpen) void loadReadme();
}

async function copyURL(url, request) {
  try {
    if (!navigator.clipboard) throw new Error("Clipboard unavailable");
    await navigator.clipboard.writeText(url);
    if (!request?.signal.aborted) appendMessage("URL copied to clipboard.");
  } catch {
    if (!request?.signal.aborted) appendMessage("Could not copy the URL. Select it above to copy it manually.");
  }
}

function appendShareControls(entry, url) {
  const controls = document.createElement("div");
  controls.className = "share-controls";
  const field = document.createElement("input");
  field.className = "terminal-input";
  field.readOnly = true;
  field.value = url;
  field.setAttribute("aria-label", "Shareable URL");
  field.addEventListener("focus", () => field.select());
  const button = document.createElement("button");
  button.type = "button";
  button.className = "terminal-control";
  button.textContent = "Copy URL";
  button.addEventListener("click", () => { void copyURL(url); });
  controls.append("Shareable URL:", field, button);
  entry.append(controls);
}

async function runCommand(command, { listAfterCd = false, literalPath = false } = {}) {
  commandRequest?.abort();
  clearCompletion();
  appendTranscript(entry => terminalView.appendCommand(entry, prompt.textContent, command));
  if (!command.trim()) return;
  const request = new AbortController();
  commandRequest = request;
  try {
    const directory = cwd;
    const response = await requestJSON("exec", directory, request, {
      input: command,
      ...(literalPath && { literalPath }),
    });
    if (request.signal.aborted) return;
    const { output, listing, help, image, download, clipboard, redirect } = response;
    const link = terminalView.downloadLink(download);
    appendTranscript(entry => {
      if (output && !clipboard) entry.append(terminalView.renderOutput(output, listing));
      if (help) terminalView.appendHelp(entry);
      if (image) terminalView.appendImage(entry, image);
      if (link) entry.append(link);
      if (clipboard) appendShareControls(entry, clipboard);
    });
    if (link) {
      announce("Download link available in terminal output.");
      link.click();
    } else if (!clipboard && (output || help || image)) {
      announce(output && output.length <= 160 && !output.includes("\x1b") ? output : "Command output is available.");
    }
    if (clipboard) await copyURL(clipboard, request);
    if (request.signal.aborted) return;
    const destination = typeof redirect === "string" && redirect.startsWith("/")
      ? terminalView.sameOriginURL(terminalView.pathURL(redirect))
      : null;
    if (destination) {
      if (window.history.state?.readme === true) {
        updateReadme();
        window.location.replace(destination);
      } else {
        window.location.assign(destination);
      }
      return;
    }
    if (typeof response.cwd === "string") {
      const changedDirectory = response.cwd !== cwd;
      if (changedDirectory) {
        directoryRequest?.abort();
        directoryError.hidden = true;
        clearCompletion();
        updateReadme();
        setDirectory(response.cwd);
        // A command may finish while the reader is open. Replace its entry so
        // directory Back still lands on the previous folder with the reader closed.
        if (window.history.state?.readme === true) {
          window.history.replaceState({ cwd }, "", directoryURL(cwd));
        } else {
          window.history.pushState({ cwd }, "", directoryURL(cwd));
        }
        announce("Directory " + cwd + ".");
      }
      if (listAfterCd) void loadDirectory();
      if (changedDirectory) loadVisibleReadme();
    }
  } catch (error) {
    if (!request.signal.aborted) {
      appendMessage("Could not run the command (" + error.message + "). Try again.");
    }
  } finally {
    if (commandRequest === request) commandRequest = undefined;
  }
}

async function completeInput() {
  clearCompletion();
  const match = input.value.match(/^\s*(\S+)(?:\s+(.*))?$/);
  const command = match?.[1];
  const argument = match?.[2] || "";
  const operand = [...argument.matchAll(/"(?:\\.|[^"\\])*"?|'[^']*'?|\S+/g)]
    .findLast(token => !token[0].startsWith("-"));
  let pathArg = operand?.[0] || "";
  if (pathArg.startsWith('"')) {
    pathArg = pathArg.slice(1).replace(/\\(.)|("$)/g, (_, escaped) => escaped || "");
  } else if (pathArg.startsWith("'")) {
    pathArg = pathArg.slice(1).replace(/'$/, "");
  }
  const dirsOnly = command === "cd";
  const textOnly = command === "cat";
  const filesOnly = textOnly || ["wget", "get", "rget", "download", "url", "share", "sum", "checksum"].includes(command);
  if (!(dirsOnly || filesOnly || command === "ls" || command === "dir")) {
    showCompletion("Type ls, cd, cat, or another command with a path to complete.");
    return;
  }

  const request = new AbortController();
  completionRequest = request;
  try {
    const { items, truncated } = await requestJSON("complete", cwd, request, {
      path: pathArg, dirsOnly, filesOnly, textOnly,
    });
    if (request.signal.aborted) return;
    if (!items?.length) {
      showCompletion("No matching paths.");
      return;
    }
    const dirPart = pathArg.slice(0, pathArg.lastIndexOf("/") + 1);
    const flags = operand ? argument.slice(0, operand.index) : argument.trimEnd();
    const prefix = command + (flags ? " " + flags.trimEnd() : "") + " ";
    suggestions = items.map(item => {
      const name = item.name + (item.dir ? "/" : "");
      return { name, command: prefix + quoteOperand(dirPart + name) };
    });
    if (suggestions.length === 1 && !truncated) {
      acceptSuggestion(0);
      return;
    }

    const options = document.createDocumentFragment();
    for (const [index, suggestion] of suggestions.entries()) {
      const option = document.createElement("li");
      option.id = "completion-option-" + index;
      option.setAttribute("role", "option");
      option.setAttribute("aria-selected", "false");
      option.textContent = suggestion.name;
      option.dataset.index = index;
      options.append(option);
    }
    completionOptions.replaceChildren(options);
    completionOptions.hidden = false;
    input.setAttribute("aria-expanded", "true");
    const count = (truncated ? "Showing " : "") + suggestions.length + " paths.";
    if (touchScreen.matches) {
      showCompletion(count + " Tap a path.", count + " Tap a path, or use the up or down arrow, then Enter.");
    } else {
      showCompletion(count + " ↑/↓ then Enter", count + " Use the up or down arrow, then Enter.");
    }
  } catch (error) {
    if (request.signal.aborted) return;
    showCompletion(error.message === "HTTP 503"
      ? "Completion busy. Try Complete again."
      : "Could not complete the path. Try Complete again.");
  } finally {
    if (completionRequest === request) completionRequest = undefined;
  }
}

input.addEventListener("input", clearCompletion);
input.addEventListener("blur", clearCompletion);
input.addEventListener("compositionstart", () => { composing = true; });
input.addEventListener("compositionend", () => { composing = false; });

completeButton.addEventListener("click", () => {
  focusInput();
  void completeInput();
});

completionOptions.addEventListener("mousedown", event => event.preventDefault());
completionOptions.addEventListener("click", event => {
  const option = event.target.closest("[role=option]");
  if (option) acceptSuggestion(Number(option.dataset.index));
});

form.addEventListener("submit", event => {
  event.preventDefault();
  // A composing Enter can also trigger an implicit form submission.
  if (composing) return;
  const command = input.value;
  rememberCommand(command);
  setInput("");
  void runCommand(command);
});

input.addEventListener("keydown", event => {
  if (event.isComposing || composing) return;
  const key = event.key;
  if (key === "Enter") {
    if (selectedSuggestion >= 0) {
      acceptSuggestion(selectedSuggestion);
    } else {
      form.requestSubmit();
    }
  } else if (key === "Escape" && (completionRequest || !completions.hidden)) {
    clearCompletion();
    announce("Path suggestions closed.");
  } else if (key === "ArrowUp" || key === "ArrowDown") {
    if (event.shiftKey || event.altKey || event.ctrlKey || event.metaKey) return;
    const step = key === "ArrowUp" ? -1 : 1;
    if (!completionOptions.hidden) {
      const initial = step > 0 ? 0 : suggestions.length - 1;
      const index = selectedSuggestion < 0 ? initial
        : (selectedSuggestion + step + suggestions.length) % suggestions.length;
      highlightSuggestion(index);
    } else {
      const next = historyIndex + step;
      if (next >= 0 && next <= commandHistory.length) {
        historyIndex = next;
        setInput(commandHistory[historyIndex] || "");
      }
    }
  } else if (event.ctrlKey && key === "u") {
    setInput("");
    clearCompletion();
  } else if (event.ctrlKey && key === "w") {
    const prefix = input.value.slice(0, input.selectionStart).replace(/\S*\s*$/, "");
    input.setRangeText("", prefix.length, input.selectionEnd, "end");
    clearCompletion();
  } else if (event.ctrlKey && key === "k") {
    setInput(input.value.slice(0, input.selectionStart));
    clearCompletion();
  } else if (event.ctrlKey && key === "l") {
    transcript.replaceChildren();
    transcriptCharacters = 0;
    announce("Terminal output cleared.");
  } else if (event.ctrlKey && key === "e") {
    input.setSelectionRange(input.value.length, input.value.length);
  } else {
    return;
  }
  event.preventDefault();
});

transcript.addEventListener("click", event => {
  if (window.getSelection()?.type === "Range") return;
  const item = event.target.closest(".clickable-file");
  if (!item) return;
  focusNavigationResult(event);
  const { cmd, file } = item.dataset;
  const command = cmd + " " + quoteOperand(file);
  rememberCommand(command);
  void runCommand(command, { listAfterCd: cmd === "cd", literalPath: cmd === "get" });
  event.preventDefault();
});

readmeToggle.addEventListener("click", toggleReadme);
compactReader.addEventListener("change", () => {
  if (!compactReader.matches && readmeOpen) {
    closeReadme();
  } else {
    updateReadme();
  }
  loadVisibleReadme();
});
layout.addEventListener("keydown", event => {
  if (event.key === "Escape" && readmeOpen) {
    closeReadme();
    event.preventDefault();
  }
});
retryDirectory.addEventListener("click", event => {
  focusNavigationResult(event);
  void loadDirectory();
  loadVisibleReadme();
});

window.addEventListener("popstate", event => {
  const path = event.state?.cwd || decodeURIComponent(window.location.pathname);
  const sameDirectory = path === cwd;
  readmeClosing = false;
  updateReadme(sameDirectory && event.state?.readme === true);
  if (sameDirectory) {
    loadVisibleReadme();
    return;
  }
  commandRequest?.abort();
  clearCompletion();
  setDirectory(path);
  announce("Directory " + cwd + ".");
  void loadDirectory();
  loadVisibleReadme();
});

setDirectory(cwd);
updateReadme();
window.history.replaceState({ cwd }, "", terminalView.pathURL(cwd) + window.location.search + window.location.hash);
appendTranscript(terminalView.appendHelp);
if (!touchScreen.matches) focusInput();
void loadDirectory();
loadVisibleReadme();
