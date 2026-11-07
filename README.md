# ink-drop

Web pages, PDFs, feeds and your own Markdown, made into books in one folder that
any e-reader reads offline.

ink-drop is a set of small command-line stages. Each one does a single job and
hands its result to the next through a plain folder, whose shape is
[the folder contract](docs/folder.md). The finished folder is the library. A
[browser extension](extension/) lists it, and a tick puts a book in the folder
your sync tool sends to your reader, Syncthing for one. Reading needs no
network.

> The French Wikipedia article on
> [electronic paper](https://fr.wikipedia.org/wiki/Papier_%C3%A9lectronique),
> on the left as the web serves it and on the right as the EPUB the three
> stages made from it. Both are headless Chromium at 600×800, the EPUB page in
> greyscale with a serif stylesheet standing in for an e-ink reader's. Text
> [CC BY-SA 4.0](https://creativecommons.org/licenses/by-sa/4.0/), photo
> [in the public domain](https://commons.wikimedia.org/wiki/File:E-ink-display-detail.jpg).

![The Wikipedia page with its menus, tabs and language picker, beside the EPUB holding only the title, the source link, the photo and the article text](media/before-after.png)

Each stage reads one line at a time on stdin, a URL for `ink-fetch` and
`ink-feed` and an item path for the others, and prints the paths it wrote.
`-to` sets the folder it writes to: `ink-fetch`, `ink-feed` and `ink-readable`
default to `ink-drop/<stage>` under the user cache folder, and `ink-epub` needs
it.

## TL;DR

```sh
# 1. Build every stage
go build -o bin/ ./cmd/...
# 2. Turn one article into an EPUB in the library
echo https://example.org/post | bin/ink-fetch | bin/ink-readable | bin/ink-epub -to ~/Books
# 3. Turn the new posts of one site's feed into an EPUB digest
echo https://go.dev/blog/ | bin/ink-feed | bin/ink-epub -to ~/Books
# 4. Install the extension's helper, then load extension/ in Firefox
go install github.com/offware-apps/ink-drop/cmd/ink-send@latest
ink-send -install
```

`ink-feed` takes a feed's address or a site's: given a page, it follows the
first feed the page names in a link tag. Nothing fetches feeds on its own:
[docs/schedule.md](docs/schedule.md) sets up an hourly run that the user
enables.

## Stages

| Stage | Job | State |
| --- | --- | --- |
| [`ink-fetch`](cmd/ink-fetch/main.go) | Downloads each URL into the work folder | Built |
| [`ink-readable`](cmd/ink-readable/main.go) | Strips each HTML item down to its article | Built |
| [`ink-epub`](cmd/ink-epub/main.go) | Packs each HTML item into an EPUB in the library | Built |
| [`ink-feed`](cmd/ink-feed/main.go) | Turns each RSS or Atom feed into one digest item | Built |
| [`ink-mirror`](cmd/ink-mirror/main.go) | Crawls one site into a single HTML item | Not built |

A PDF needs no conversion: `ink-readable` passes it through and `ink-epub`
copies it into the library. `ink-fetch` inlines a page's images, so every
later stage runs offline.

## The browser extension

The [extension](extension/) saves the page you are reading as an EPUB through the
browser's downloads, with nothing else installed. To load it, open
`about:debugging#/runtime/this-firefox` in Firefox, click *Load Temporary
Add-on* and pick `extension/manifest.json`. Firefox keeps a
[temporary add-on](https://extension-workshop.com/documentation/develop/temporary-installation-in-firefox/)
until it restarts.

Its library page lists every book in one place, with a tab per folder, a search,
a sort, a box to tick for each book you want on your reader and a bin to delete
one. Without its helper it lists only the pages the extension saved. The helper
is [`ink-send`](cmd/ink-send/main.go), a small program Firefox starts for each
request: it lists the whole library, sends a ticked book and backs the *Send*
button the popup shows after a save. `ink-send -install` registers it for your
user and writes its settings to `~/.config/ink-drop/folders.json`:

```json
{
  "library": "~/Downloads/ink-drop",
  "sync": "~/Sync/reader",
  "routes": [
    {"path": "~/notes/idea.md"},
    {"path": "~/notes/recipes", "tab": "food"},
    {"path": "~/notes/guide", "title": "My guide", "files": ["intro.md", "setup.md"]},
    {"path": "~/work/reviews", "tab": "reviews", "book": "overview.md", "rounds": "^(\\d+)-"}
  ]
}
```

`library` is the folder the browser saves pages into, `ink-drop` under your
download folder, so nothing ever moves a file into it. A different folder set on
the library page needs `library` set to match.

### The sync folder

`sync` is the one folder your sync tool sends to the reader, a Syncthing folder
for one. Ticking a book, or *Send* in the popup, copies it there, and unticking
removes it. ink-drop never sends anything itself: the sync tool does, when you
let it run.

### Routes

A route offers Markdown already on this machine as books, made only when you tick
one:

- a file is one book;
- a folder offers each of its Markdown files, up to 16 folders deep, as its own
  book;
- `files` makes one book of the Markdown files listed, each inside `path`, in
  that order;
- `book` makes each folder holding a file of that name one book, of the Markdown
  beside it and in the highest numbered folder `rounds` matches, its first
  group read as the number.

`tab` names the library folder and the page's tab, `notes` when left out. A route
is read and never written, and unticking a book deletes the copy that was made. A
file or folder the helper cannot read is left out of the list, and so is a book
over [1 MiB of Markdown](cmd/ink-send/routes.go#L57). A book that takes more
than [20 seconds or 512 MB](cmd/ink-send/routes.go#L447-L448) to make is
refused.

### What the helper cannot do

`ink-send` holds no code that starts a program or opens a connection, and [a
test](cmd/ink-send/main_test.go) fails if the standard library's code for either
ever enters it. Every file it reads or writes, in the library, the sync folder
and the routes, goes through a root that no name or link can leave, and it
opens only plain files. The browser names a library book by its name in the
library, at most one folder deep, or a route book by an id from the helper's own
walk, never a path, and a route holding the home folder or the whole disk is
refused.

## Rules

ink-drop is a tool block under article VIII of the
[Offware constitution](https://github.com/offware-apps/Vision/blob/main/constitution.md):
it runs outside any app and feeds one through plain files.
[constitution.md](constitution.md) checks it against every article.

## License

[AGPL-3.0](LICENSE).
