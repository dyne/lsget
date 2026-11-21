const htmlNamespace = "http://www.w3.org/1999/xhtml";
const documentTags = new Set([
  "a", "b", "blockquote", "br", "code", "dd", "del", "div", "dl", "dt",
  "em", "h1", "h2", "h3", "h4", "h5", "h6", "hr", "i", "img", "input",
  "kbd", "li", "ol", "p", "pre", "s", "span", "strong", "sub", "sup",
  "table", "tbody", "td", "th", "thead", "tr", "ul",
]);
const ansiColors = new Map([
  ["31", "#e78284"], ["32", "#a6d189"], ["33", "#e5c890"], ["34", "#8caaee"],
  ["35", "#f4b8e4"], ["36", "#81c8be"], ["37", "#c6d0f5"], ["90", "#737994"],
  ["92", "#a6d189"], ["93", "#e5c890"], ["96", "#99d1db"],
]);

function safeURL(value, base, allowMail = false) {
  if (typeof value !== "string" || !value.trim()) return null;
  try {
    const url = new URL(value, base);
    if (url.protocol === "http:" || url.protocol === "https:" ||
        (allowMail && url.protocol === "mailto:")) return url;
  } catch {}
  return null;
}

function pathURL(path) {
  return path.split("/").map(encodeURIComponent).join("/");
}

function sameOriginURL(value) {
  const url = safeURL(value, window.location.href);
  return url?.origin === window.location.origin ? url.href : null;
}

function documentLink(value, cwd) {
  const base = new URL(pathURL(cwd), window.location.origin);
  if (!value?.trim().startsWith("#") && !base.pathname.endsWith("/")) base.pathname += "/";
  return safeURL(value, base, true);
}

// Parse Markdown's HTML in an inert template, then create fresh nodes.
// Source nodes and attributes never move into the live document.
function renderMarkdown(parent, content, cwd) {
  const source = document.createElement("template");
  source.innerHTML = marked.parse(content);
  const anchors = new Map();
  const fragmentLinks = [];

  function copyChildren(target, from) {
    for (const child of from.childNodes) {
      if (child.nodeType === Node.TEXT_NODE) {
        target.append(child.data);
        continue;
      }
      if (child.nodeType !== Node.ELEMENT_NODE || child.namespaceURI !== htmlNamespace) continue;
      const tag = child.localName;
      if (!documentTags.has(tag)) {
        if (tag !== "script" && tag !== "style") copyChildren(target, child);
        continue;
      }
      const node = document.createElement(tag);

      if (tag === "a") {
        const href = child.getAttribute("href");
        const url = documentLink(href, cwd);
        if (url) {
          node.href = url.href;
          if (href.trim().startsWith("#")) fragmentLinks.push({ node, url });
        }
      } else if (tag === "img") {
        const directory = pathURL(cwd).replace(/\/$/, "") + "/";
        const url = safeURL(child.getAttribute("src"), new URL(directory, window.location.origin));
        const alt = child.getAttribute("alt") || "";
        if (!url) {
          target.append(alt);
          continue;
        }
        node.alt = alt;
        node.referrerPolicy = "no-referrer";
        node.src = url.href;
        for (const dimension of ["height", "width"]) {
          const size = child.getAttribute(dimension);
          if (/^[1-9]\d{0,3}$/.test(size)) node.setAttribute(dimension, size);
        }
      } else if (tag === "input") {
        if (child.getAttribute("type") !== "checkbox" || !child.disabled) continue;
        node.type = "checkbox";
        node.checked = child.checked;
        node.disabled = true;
      } else if (tag === "ol" && /^-?\d{1,9}$/.test(child.getAttribute("start"))) {
        node.start = child.start;
      } else if (tag === "td" || tag === "th") {
        const align = child.getAttribute("align");
        if (["left", "center", "right"].includes(align)) node.setAttribute("align", align);
      }
      if ((tag === "a" || tag === "img") && child.hasAttribute("title")) node.title = child.title;
      // Use generated IDs so document targets cannot shadow shell elements.
      const names = [child.getAttribute("id"), tag === "a" ? child.getAttribute("name") : null];
      for (const name of names) {
        if (name && !anchors.has(name)) {
          if (!node.id) node.id = "readme-anchor-" + anchors.size;
          anchors.set(name, node.id);
        }
      }
      copyChildren(node, child);
      target.append(node);
    }
  }

  copyChildren(parent, source.content);
  for (const { node, url } of fragmentLinks) {
    let name = url.hash.slice(1);
    try { name = decodeURIComponent(name); } catch {}
    if (anchors.has(name)) {
      url.hash = anchors.get(name);
      node.href = url.href;
    }
  }
}

function renderRST(parent, content, cwd) {
  const headings = { "=": "h1", "-": "h2", "^": "h3", '"': "h4" };
  function inline(node, text) {
    const pattern = /\x60([^\x60]+) <([^<>\r\n]+)>\x60_|\*\*([^*\r\n]+)\*\*|\*([^*\r\n]+)\*/g;
    let offset = 0;
    for (const match of text.matchAll(pattern)) {
      node.append(text.slice(offset, match.index));
      const [token, label, href, strong, em] = match;
      const part = document.createElement(label ? "a" : strong ? "strong" : "em");
      part.textContent = label ?? strong ?? em;
      if (href) {
        const url = documentLink(href, cwd);
        if (url) part.href = url.href;
      }
      node.append(part);
      offset = match.index + token.length;
    }
    node.append(text.slice(offset));
  }
  for (let block of content.split(/\r?\n(?:[ \t]*\r?\n)+/)) {
    const heading = block.match(/^(.+)\r?\n(=+|-+|\^+|"+)(?:\r?\n|$)/);
    if (heading) {
      const node = document.createElement(headings[heading[2][0]]);
      inline(node, heading[1]);
      parent.append(node);
      block = block.slice(heading[0].length);
    }
    if (!block.trim()) continue;
    const paragraph = document.createElement("p");
    const lines = block.split(/\r?\n/);
    for (const [i, line] of lines.entries()) {
      if (i !== 0) paragraph.append("\n");
      if (line.startsWith("    ")) {
        const code = document.createElement("code");
        code.textContent = line.slice(4);
        paragraph.append(code);
      } else {
        inline(paragraph, line.replace(/::$/, ""));
      }
    }
    parent.append(paragraph);
  }
}

function readme(panel, response) {
  const { readme, docType, cwd = "/", readmeTruncated } = response;
  if (typeof readme !== "string") return;
  const fragment = document.createDocumentFragment();
  if (readmeTruncated) {
    const notice = document.createElement("p");
    notice.className = "readme-notice";
    notice.textContent = "README preview truncated.";
    fragment.append(notice);
  }
  if (readme) {
    if (docType === "markdown") {
      renderMarkdown(fragment, readme, cwd);
    } else if (docType === "rst") {
      renderRST(fragment, readme, cwd);
    } else {
      const text = document.createElement("pre");
      text.className = docType === "nfo" ? "nfo-content" : "text-content";
      text.textContent = readme;
      fragment.append(text);
    }
  }
  panel.replaceChildren(fragment);
}

function line(output = false) {
  const node = document.createElement("div");
  node.className = output ? "line out" : "line";
  return node;
}

function ansiLine(text, clickable) {
  const node = line(true);
  const sequence = /\x1b\[([0-?]*)([ -/]*)([@-~])/g;
  let color = "";
  let bold = false;
  let offset = 0;
  let run = "";
  function flush() {
    if (!run) return;
    if (!color && !bold) {
      node.append(run);
    } else {
      const span = document.createElement("span");
      span.textContent = run;
      if (color) span.style.color = color;
      if (bold) span.style.fontWeight = "400";
      if (clickable && color) {
        const dir = run.endsWith("/");
        span.className = "clickable-file";
        span.dataset.cmd = dir ? "cd" : "cat";
        span.dataset.file = dir && run !== "/" ? run.slice(0, -1) : run;
      }
      node.append(span);
    }
    run = "";
  }

  for (const match of text.matchAll(sequence)) {
    run += text.slice(offset, match.index);
    offset = match.index + match[0].length;
    const [, parameter, intermediate, final] = match;
    if (intermediate || final !== "m") continue;
    if (parameter !== "0" && parameter !== "1" && !ansiColors.has(parameter)) continue;
    flush();
    if (parameter === "0") {
      color = "";
      bold = false;
    } else if (parameter === "1") {
      bold = true;
    } else {
      color = ansiColors.get(parameter);
    }
  }
  run += text.slice(offset);
  flush();
  return node;
}

function appendCommand(transcript, ps1, command) {
  const node = line();
  const prompt = document.createElement("span");
  prompt.className = "ps1";
  prompt.textContent = ps1;
  node.append(prompt, command);
  transcript.append(node);
}

function appendHelp(transcript) {
  const node = line(true);
  node.append(document.getElementById("terminal-help").content.cloneNode(true));
  transcript.append(node);
}

function appendImage(transcript, image) {
  const href = sameOriginURL(image.url);
  if (!href) return;
  const node = line(true);
  node.classList.add("terminal-image");
  const link = document.createElement("a");
  link.href = href;
  link.target = "_blank";
  link.rel = "noopener";
  const picture = document.createElement("img");
  picture.src = href;
  picture.alt = image.name;
  link.append(picture);
  const caption = document.createElement("div");
  caption.textContent = "📷 " + image.name + " (" + image.size + ")";
  node.append(link, caption);
  transcript.append(node);
}

function downloadLink(value) {
  const href = sameOriginURL(value);
  if (!href) return null;
  const link = document.createElement("a");
  link.href = href;
  link.download = "";
  link.textContent = "Download file";
  return link;
}

export { ansiLine, appendCommand, appendHelp, appendImage, downloadLink, pathURL, readme, sameOriginURL };
