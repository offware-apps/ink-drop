// Opening the popup saves the current tab, as a read-later button does.
const $ = (id) => document.getElementById(id);

$("library").addEventListener("click", () => {
  api.tabs.create({ url: api.runtime.getURL("library.html") });
  window.close();
});

$("send").addEventListener("click", async () => {
  $("send").disabled = true;
  show("", [spinner(), "Sending to tablet…"]);
  try {
    show("good", "✓ Sent to tablet", await sendToTablet());
  } catch (err) {
    show("bad", err.message);
  }
  $("send").disabled = false;
});

function spinner() {
  return Object.assign(document.createElement("span"), { className: "spinner" });
}

// show sets the status line to text, a string or a list of nodes and strings.
function show(kind, text, detail = "") {
  const status = $("status");
  status.className = `status ${kind}`;
  status.replaceChildren(...[].concat(text));
  $("detail").textContent = detail;
}

const [tab] = await api.tabs.query({ active: true, currentWindow: true });
$("title").textContent = tab?.title || "This page";
$("site").textContent = /^https?:/.test(tab?.url ?? "") ? new URL(tab.url).hostname : "";

if (!/^https?:/.test(tab?.url ?? "")) {
  show("bad", "Only web pages can be saved.");
} else {
  // The background script keeps working if this popup closes first.
  const res = await api.runtime.sendMessage({ type: "save", tabId: tab.id });
  if (res?.item) {
    show("good", "✓ Saved for offline reading", `${res.item.minutes} min read`);
  } else {
    show("bad", res?.error || "Saving failed.");
  }
}
// Sending waits for the save, so no half-written book is copied.
$("send").disabled = false;
