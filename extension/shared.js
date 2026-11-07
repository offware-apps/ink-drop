// What the popup, the library page and the background script share. Firefox
// names the extension API `browser`, Chromium `chrome`.
const api = globalThis.browser ?? globalThis.chrome;

// folder is created under the browser's download folder.
const DEFAULTS = { folder: "ink-drop", items: [], transmitter: "", sort: "newest" };

// host asks the ink-send helper to list the library, or to send, unsend or delete
// one file, and returns its reply. Without `to`, the helper takes its first
// transmitter.
async function host(action, file, to) {
  let res;
  try {
    res = await api.runtime.sendNativeMessage("ink_drop", { action, file, to });
  } catch {
    throw new Error("The ink-send helper is not installed. Run: ink-send -install");
  }
  if (!res?.ok) throw new Error(res?.error || "ink-send failed.");
  return res;
}

// slug returns s as lowercase ASCII letters and digits joined by hyphens,
// accents dropped, as the ink-drop stages name their books.
function slug(s) {
  return (
    String(s)
      .normalize("NFD")
      .replace(/[\u0300-\u036f]/g, "")
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 80)
      .replace(/-+$/, "") || "item"
  );
}
