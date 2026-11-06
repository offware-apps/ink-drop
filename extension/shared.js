// What the popup, the library page and the background script share. Firefox
// names the extension API `browser`, Chromium `chrome`.
const api = globalThis.browser ?? globalThis.chrome;

// folder is created under the browser's download folder.
const DEFAULTS = { folder: "ink-drop", items: [] };

// sendToTablet asks the ink-send helper to run the user's send command, and
// returns the last line it printed. The helper takes no input from here.
async function sendToTablet() {
  let res;
  try {
    res = await api.runtime.sendNativeMessage("ink_drop", {});
  } catch {
    throw new Error("The ink-send helper is not installed. Run: ink-send -install");
  }
  if (!res?.ok) throw new Error(res?.output || "Sending failed.");
  // The command's last line, past any "path: " prefix such as adb prints.
  return (res.output.split("\n").filter(Boolean).pop() || "Sent.").split(": ").pop();
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
