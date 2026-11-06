// Saves a tab as an EPUB in the download folder and adds it to the library
// list. Firefox loads shared.js and epub.js from the manifest, Chromium here.
if (typeof importScripts === "function") importScripts("shared.js", "epub.js");

api.runtime.onMessage.addListener((msg, sender, respond) => {
  if (sender.id !== api.runtime.id || msg?.type !== "save" || !Number.isInteger(msg.tabId)) return false;
  save(msg.tabId).then(
    (item) => respond({ item }),
    (err) => respond({ error: err.message || String(err) }),
  );
  return true;
});

async function save(tabId) {
  const [run] = await api.scripting.executeScript({ target: { tabId }, files: ["vendor/Readability.js", "extract.js"] });
  const a = run?.result;
  if (!a) throw new Error("ink-drop cannot read this page.");
  if (a.error) throw new Error(a.error);
  const images = a.images.map((img) => img && { type: img.type, bytes: decode(img.data) });
  const saved = new Date();
  const book = buildEpub({ ...a, images, saved });
  const { folder, items } = await api.storage.local.get(DEFAULTS);
  const downloadId = await download(book, `${folder}/${slug(a.title)}.epub`);
  const item = {
    downloadId,
    title: a.title,
    url: a.url,
    site: new URL(a.url).hostname,
    excerpt: a.excerpt.slice(0, 280),
    minutes: Math.max(1, Math.round(a.words / 230)),
    saved: saved.getTime(),
  };
  await api.storage.local.set({ items: [item, ...items.filter((i) => i.url !== a.url)] });
  return item;
}

function decode(b64) {
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return bytes;
}

// download saves bytes as filename under the download folder. A service worker
// has no URL.createObjectURL, so Chromium gets a data: URL instead.
async function download(bytes, filename) {
  let url;
  if (typeof URL.createObjectURL === "function") {
    url = URL.createObjectURL(new Blob([bytes], { type: "application/epub+zip" }));
  } else {
    let bin = "";
    for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
    url = `data:application/epub+zip;base64,${btoa(bin)}`;
  }
  try {
    return await api.downloads.download({ url, filename, conflictAction: "uniquify", saveAs: false });
  } finally {
    if (url.startsWith("blob:")) setTimeout(() => URL.revokeObjectURL(url), 60_000);
  }
}
