// Runs in the page after vendor/Readability.js and returns the article as
// XHTML with its images. Only the tags and attributes below survive, so no
// script, style, form, frame, SVG or event handler reaches the EPUB.
(async () => {
  // Kept with their attributes from ATTRS; any other element keeps only its
  // text, except DROP, which goes with everything inside it.
  const KEEP = new Set(
    (
      "p h1 h2 h3 h4 h5 h6 a img figure figcaption blockquote pre code em strong i b u s sub sup small mark " +
      "abbr cite q time del ins kbd samp var br hr ul ol li dl dt dd table thead tbody tfoot tr th td caption"
    ).split(" "),
  );
  const DROP = new Set("script style noscript template iframe frame frameset object embed svg math canvas video audio form select textarea button input head".split(" "));
  const ATTRS = { a: ["href"], img: ["src", "alt"], td: ["colspan", "rowspan"], th: ["colspan", "rowspan"], "*": ["id", "lang", "dir", "title"] };
  const MAX_IMAGES = 60;
  const MAX_IMAGE_BYTES = 8e6;
  const MAX_TOTAL_BYTES = 40e6;
  const IMAGE_TYPES = new Set(["image/png", "image/jpeg", "image/gif", "image/webp"]);

  const article = new Readability(document.cloneNode(true)).parse();
  if (!article?.content) return { error: "ink-drop found no article on this page." };
  const body = new DOMParser().parseFromString(article.content, "text/html").body;

  const sources = [];
  clean(body);

  // Images are fetched as this page would fetch them, without cookies: a
  // server that does not allow it leaves the image out.
  let total = 0;
  const images = [];
  for (const url of sources) {
    const img = await fetchImage(url, MAX_TOTAL_BYTES - total);
    total += img?.size ?? 0;
    images.push(img && { type: img.type, data: await base64(img) });
  }

  const xml = new XMLSerializer();
  return {
    title: article.title || document.title || location.hostname,
    byline: article.byline || "",
    site: article.siteName || location.hostname,
    lang: article.lang || document.documentElement.lang || "en",
    excerpt: article.excerpt || "",
    url: location.href,
    words: (article.textContent.match(/\S+/g) || []).length,
    xhtml: [...body.childNodes].map((n) => xml.serializeToString(n)).join(""),
    images,
  };

  function clean(parent) {
    for (const node of [...parent.childNodes]) {
      if (node.nodeType === Node.TEXT_NODE) continue;
      if (node.nodeType !== Node.ELEMENT_NODE) {
        node.remove();
        continue;
      }
      const tag = node.localName;
      if (DROP.has(tag) || node.namespaceURI !== "http://www.w3.org/1999/xhtml") {
        node.remove();
        continue;
      }
      clean(node);
      if (!KEEP.has(tag)) {
        node.replaceWith(...node.childNodes);
        continue;
      }
      const srcset = node.getAttribute("srcset");
      const allowed = [...(ATTRS[tag] ?? []), ...ATTRS["*"]];
      for (const { name } of [...node.attributes]) if (!allowed.includes(name)) node.removeAttribute(name);
      if (tag === "a") {
        const href = node.getAttribute("href") ?? "";
        if (!/^(https?:|mailto:|#)/i.test(href)) node.removeAttribute("href");
      }
      if (tag === "img") {
        const src = largest(srcset) || node.getAttribute("src") || "";
        if (!/^https?:/i.test(src) || sources.length >= MAX_IMAGES) {
          node.remove();
          continue;
        }
        node.setAttribute("src", `ink-img-${sources.length}`);
        node.setAttribute("alt", node.getAttribute("alt") ?? "");
        sources.push(src);
      }
    }
  }

  async function fetchImage(url, room) {
    try {
      const res = await fetch(url, { credentials: "omit", referrerPolicy: "no-referrer" });
      const blob = await res.blob();
      const type = blob.type.split(";")[0];
      if (!res.ok || !IMAGE_TYPES.has(type) || blob.size > Math.min(MAX_IMAGE_BYTES, room)) return null;
      return blob;
    } catch {
      return null;
    }
  }

  function base64(blob) {
    return new Promise((resolve, reject) => {
      const r = new FileReader();
      r.onload = () => resolve(String(r.result).split(",", 2)[1]);
      r.onerror = () => reject(r.error);
      r.readAsDataURL(blob);
    });
  }

  // largest returns the widest candidate of a srcset, which Readability has
  // already made absolute.
  function largest(srcset) {
    let best = "",
      width = 0;
    for (const part of (srcset || "").split(/,\s+/)) {
      const [url, size = "1x"] = part.trim().split(/\s+/);
      const w = parseFloat(size) * (size.endsWith("x") ? 1000 : 1);
      if (url && w > width) [best, width] = [url, w];
    }
    return best;
  }
})();
