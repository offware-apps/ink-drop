// The library: every book in the folder ink-send reads, newest first, with what
// the extension remembers about the pages it saved. Without the helper it shows
// those saved pages alone, matched to their files through the download history.
const $ = (id) => document.getElementById(id);
let items = [];
let helper = false;

function h(tag, props = {}, ...children) {
  const el = Object.assign(document.createElement(tag), props);
  el.append(...children.filter((c) => c != null && c !== false));
  return el;
}

function when(ms) {
  const d = new Date(ms);
  const days = Math.round((new Date().setHours(0, 0, 0, 0) - new Date(ms).setHours(0, 0, 0, 0)) / 864e5);
  if (days === 0) return "Today";
  if (days === 1) return "Yesterday";
  const sameYear = d.getFullYear() === new Date().getFullYear();
  return d.toLocaleDateString(undefined, { day: "numeric", month: "short", year: sameYear ? undefined : "numeric" });
}

async function load() {
  const s = await api.storage.local.get(DEFAULTS);
  $("folder").value = s.folder;
  let res;
  try {
    res = await host("list");
    helper = true;
  } catch (err) {
    helper = false;
    $("status").textContent = err.message;
  }
  if (res) {
    if (res.moved && s.items.some((i) => res.moved[i.file])) {
      s.items.forEach((i) => (i.file = res.moved[i.file] ?? i.file));
      await api.storage.local.set({ items: s.items });
    }
    items = (res.books ?? []).map((b) => fromBook(b, s.items.find((i) => i.file === b.name)));
    $("status").textContent = res.more ? `${res.more} older books not shown` : "";
  } else {
    items = s.items.sort((a, b) => b.saved - a.saved);
    const files = await Promise.all(
      items.map((i) => api.downloads.search({ id: i.downloadId }).then((r) => r[0], () => undefined)),
    );
    items.forEach((it, k) => (it.download = files[k]));
  }
  render();
}

// fromBook is one row: the book ink-send found, and the saved page it came from
// where the extension saved it.
function fromBook(b, page) {
  const url = page?.url || b.source || "";
  return {
    name: b.name,
    sent: b.sent,
    size: b.size,
    url,
    title: page?.title || b.title || b.name,
    excerpt: page?.excerpt || "",
    minutes: page?.minutes,
    saved: page?.saved || b.modified,
    site: page?.site || hostname(url),
  };
}

function hostname(url) {
  try {
    return new URL(url).hostname;
  } catch {
    return "";
  }
}

function megabytes(bytes) {
  return `${Math.max(0.1, bytes / 1e6).toFixed(1)} MB`;
}

function render() {
  const q = $("search").value.trim().toLowerCase();
  const shown = items.filter((i) => !q || `${i.title} ${i.site} ${i.excerpt}`.toLowerCase().includes(q));
  $("count").textContent = items.length ? String(q ? `${shown.length} / ${items.length}` : items.length) : "";
  $("empty").hidden = items.length > 0;
  $("items").replaceChildren(...shown.map(row));
}

// Icon paths, 24×24, drawn as strokes.
const ICONS = {
  folder: "M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z",
  trash: "M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3",
  send: "M12 19V5M5 12l7-7 7 7",
  unsend: "M6 6l12 12M18 6L6 18",
};

function icon(label, path, onClick, disabled = false) {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("aria-hidden", "true");
  const p = document.createElementNS("http://www.w3.org/2000/svg", "path");
  p.setAttribute("d", path);
  svg.append(p);
  const b = h("button", { className: "icon", title: label, disabled }, svg);
  b.setAttribute("aria-label", label);
  b.addEventListener("click", onClick);
  return b;
}

// row is one line per page: the title opens the original page, the icons show
// on hover.
function row(it) {
  const there = helper || (it.download?.exists !== false && it.download?.state !== "interrupted" && it.download);
  const site = it.site.replace(/^www\./, "");
  const open = h("a", { href: "#", className: "title", textContent: it.title, title: it.excerpt || it.title });
  open.addEventListener("click", (e) => {
    e.preventDefault();
    if (/^https?:\/\//.test(it.url)) api.tabs.create({ url: it.url });
  });
  const length = it.minutes ? `${it.minutes} min` : it.size ? megabytes(it.size) : "";
  return h(
    "li",
    { className: there ? "item" : "item gone" },
    h("span", { className: "avatar", textContent: (site || it.title).charAt(0).toUpperCase() }),
    open,
    h("span", {
      className: "sent",
      textContent: it.sent ? "for tablet" : "",
      title: it.sent ? "In the tablet folder. It arrives when sync is on." : "",
    }),
    h("span", { className: "site", textContent: site }),
    h("span", { className: "num", textContent: there ? length : "file missing" }),
    h("span", { className: "num when", textContent: when(it.saved) }),
    helper
      ? h(
          "span",
          { className: "tools" },
          it.sent
            ? icon("Take back from the tablet", ICONS.unsend, () => act("unsend", it))
            : icon("Send to tablet", ICONS.send, () => act("send", it)),
          icon("Remove", ICONS.trash, () => removeItem(it)),
        )
      : h(
          "span",
          { className: "tools" },
          icon("Show in folder", ICONS.folder, () => api.downloads.show(it.downloadId), !there),
          icon("Remove", ICONS.trash, () => removeItem(it)),
        ),
  );
}

// act sends or unsends one book, then shows the library as it now is.
async function act(action, it) {
  try {
    await host(action, it.name);
    await load();
  } catch (err) {
    $("status").textContent = err.message;
  }
}

async function removeItem(it) {
  if (!confirm(`Remove “${it.title}” from your library and delete its file?`)) return;
  const s = await api.storage.local.get(DEFAULTS);
  if (helper) {
    try {
      await host("delete", it.name);
    } catch (err) {
      $("status").textContent = err.message;
      return;
    }
    await api.storage.local.set({ items: s.items.filter((i) => i.file !== it.name) });
  } else {
    if (it.download?.exists) await api.downloads.removeFile(it.downloadId).catch(() => {});
    await api.downloads.erase({ id: it.downloadId }).catch(() => {});
    await api.storage.local.set({ items: s.items.filter((i) => i.downloadId !== it.downloadId) });
  }
  await load();
}

$("search").addEventListener("input", render);

document.addEventListener("keydown", (e) => {
  if (e.key === "/" && document.activeElement?.tagName !== "INPUT") {
    e.preventDefault();
    $("search").focus();
  }
  if (e.key === "Escape" && document.activeElement === $("search")) {
    $("search").value = "";
    render();
  }
});

$("settings").addEventListener("submit", async (e) => {
  e.preventDefault();
  const folder = $("folder").value.trim().replace(/^\/+|\/+$/g, "");
  // The browser refuses a path that climbs out of the download folder.
  if (!folder || folder.split("/").some((part) => !part || part.startsWith("."))) {
    $("folderStatus").textContent = "Use a plain folder name, like ink-drop or Books/ink-drop.";
    return;
  }
  await api.storage.local.set({ folder });
  $("folderStatus").textContent = `New articles go to ${folder}.`;
});

// A page saved from the popup while this tab is open shows up at once.
api.storage.onChanged.addListener((changes) => {
  if (changes.items) load();
});

load();
