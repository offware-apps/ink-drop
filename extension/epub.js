// Builds an EPUB 3 from what extract.js returned, as the bytes of a ZIP file.

const IMAGE_TYPES = { "image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp" };

const escapeXML = (s) =>
  String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&apos;" })[c]);

// buildEpub returns the EPUB for article a, whose images[i] is null when that
// image could not be fetched, else { type, bytes }.
function buildEpub(a) {
  const saved = a.saved ?? new Date();
  const modified = saved.toISOString().replace(/\.\d+Z$/, "Z");
  const files = [];
  const items = [];
  let body = a.xhtml;
  a.images.forEach((img, i) => {
    const placeholder = `src="ink-img-${i}"`;
    if (!img || !IMAGE_TYPES[img.type]) {
      body = body.replace(new RegExp(`<img\\b[^>]*${placeholder}[^>]*/>`, "g"), "");
      return;
    }
    const href = `images/img-${i}.${IMAGE_TYPES[img.type]}`;
    body = body.replaceAll(placeholder, `src="${href}"`);
    files.push([`OEBPS/${href}`, img.bytes]);
    items.push(`<item id="img${i}" href="${href}" media-type="${img.type}"/>`);
  });

  const title = escapeXML(a.title);
  const lang = escapeXML(a.lang || "en");
  const source = escapeXML(a.url);
  const day = saved.toISOString().slice(0, 10);
  const chapter = `<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="${lang}" lang="${lang}">
<head><title>${title}</title><link rel="stylesheet" href="style.css"/></head>
<body>
<h1>${title}</h1>
<p class="source">${a.byline ? `${escapeXML(a.byline)} · ` : ""}<a href="${source}">${escapeXML(a.site)}</a> · ${day}</p>
${body}
</body>
</html>`;

  return zip([
    ["mimetype", "application/epub+zip"],
    [
      "META-INF/container.xml",
      `<?xml version="1.0" encoding="utf-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
<rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`,
    ],
    [
      "OEBPS/content.opf",
      `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id" xml:lang="${lang}">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
<dc:identifier id="id">${source}</dc:identifier>
<dc:title>${title}</dc:title>
<dc:language>${lang}</dc:language>
${a.byline ? `<dc:creator>${escapeXML(a.byline)}</dc:creator>` : ""}
<dc:publisher>${escapeXML(a.site)}</dc:publisher>
<dc:source>${source}</dc:source>
<meta property="dcterms:modified">${modified}</meta>
</metadata>
<manifest>
<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
<item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>
<item id="css" href="style.css" media-type="text/css"/>
${items.join("\n")}
</manifest>
<spine><itemref idref="chapter"/></spine>
</package>`,
    ],
    [
      "OEBPS/nav.xhtml",
      `<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="${lang}">
<head><title>${title}</title></head>
<body><nav epub:type="toc"><ol><li><a href="chapter.xhtml">${title}</a></li></ol></nav></body>
</html>`,
    ],
    [
      "OEBPS/style.css",
      `body { font-family: serif; line-height: 1.5; margin: 0 1em; }
h1 { font-size: 1.5em; line-height: 1.2; margin: 1em 0 .3em; }
.source { color: #555; font-size: .85em; margin-bottom: 2em; }
img { max-width: 100%; height: auto; }
figure { margin: 1em 0; }
figcaption { font-size: .85em; color: #555; }
pre { white-space: pre-wrap; font-size: .85em; }
blockquote { margin: 1em 0; padding-left: 1em; border-left: 3px solid #999; }
`,
    ],
    ["OEBPS/chapter.xhtml", chapter],
    ...files,
  ]);
}

// zip returns entries stored uncompressed, which EPUB requires of mimetype,
// the first entry, and which images gain nothing from.
function zip(entries) {
  const enc = new TextEncoder();
  const now = new Date();
  const time = (now.getHours() << 11) | (now.getMinutes() << 5) | (now.getSeconds() >> 1);
  const date = ((now.getFullYear() - 1980) << 9) | ((now.getMonth() + 1) << 5) | now.getDate();
  const parts = [];
  const central = [];
  let offset = 0;
  for (const [name, data] of entries) {
    const n = enc.encode(name);
    const d = typeof data === "string" ? enc.encode(data) : data;
    const crc = crc32(d);
    const local = new DataView(new ArrayBuffer(30));
    local.setUint32(0, 0x04034b50, true);
    local.setUint16(4, 20, true);
    local.setUint16(10, time, true);
    local.setUint16(12, date, true);
    local.setUint32(14, crc, true);
    local.setUint32(18, d.length, true);
    local.setUint32(22, d.length, true);
    local.setUint16(26, n.length, true);
    parts.push(new Uint8Array(local.buffer), n, d);
    const dir = new DataView(new ArrayBuffer(46));
    dir.setUint32(0, 0x02014b50, true);
    dir.setUint16(4, 20, true);
    dir.setUint16(6, 20, true);
    dir.setUint16(12, time, true);
    dir.setUint16(14, date, true);
    dir.setUint32(16, crc, true);
    dir.setUint32(20, d.length, true);
    dir.setUint32(24, d.length, true);
    dir.setUint16(28, n.length, true);
    dir.setUint32(42, offset, true);
    central.push(new Uint8Array(dir.buffer), n);
    offset += 30 + n.length + d.length;
  }
  const size = central.reduce((s, c) => s + c.length, 0);
  const end = new DataView(new ArrayBuffer(22));
  end.setUint32(0, 0x06054b50, true);
  end.setUint16(8, entries.length, true);
  end.setUint16(10, entries.length, true);
  end.setUint32(12, size, true);
  end.setUint32(16, offset, true);
  const all = [...parts, ...central, new Uint8Array(end.buffer)];
  const out = new Uint8Array(all.reduce((s, c) => s + c.length, 0));
  let at = 0;
  for (const c of all) {
    out.set(c, at);
    at += c.length;
  }
  return out;
}

const CRC_TABLE = Array.from({ length: 256 }, (_, n) => {
  for (let k = 0; k < 8; k++) n = n & 1 ? 0xedb88320 ^ (n >>> 1) : n >>> 1;
  return n >>> 0;
});

function crc32(bytes) {
  let c = 0xffffffff;
  for (const b of bytes) c = CRC_TABLE[(c ^ b) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}
