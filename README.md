# ink-drop

Web pages, PDFs and feeds, fetched into one folder that any e-reader reads
offline.

ink-drop is a set of small command-line stages. Each one does a single job and
hands its result to the next through a plain folder, whose shape is
[the folder contract](docs/folder.md). The finished folder is the library:
sync it to a Boox, a Kobo or any tablet with whatever already syncs there,
Syncthing, WebDAV or a USB cable. Reading needs no network.

No stage is built yet. The [`folder`](folder/folder.go) package they share is,
and the commands below are the target.

## TL;DR

```sh
# 1. Build every stage
go build -o bin/ ./cmd/...
# 2. Turn one article into an EPUB in the library
echo https://example.org/post | bin/ink-fetch | bin/ink-readable | bin/ink-epub -to ~/Books
```

## Stages

| Stage | Job | State |
| --- | --- | --- |
| [`ink-fetch`](cmd/ink-fetch/main.go) | Downloads each URL into the work folder | Not built |
| [`ink-readable`](cmd/ink-readable/main.go) | Strips each HTML item down to its article | Not built |
| [`ink-epub`](cmd/ink-epub/main.go) | Packs each HTML item into an EPUB in the library | Not built |
| [`ink-feed`](cmd/ink-feed/main.go) | Turns each RSS or Atom feed into one digest item | Not built |
| [`ink-mirror`](cmd/ink-mirror/main.go) | Crawls one site into a single HTML item | Not built |

A PDF needs no conversion: `ink-fetch` drops it in the work folder and a copy
moves it to the library.

## Rules

ink-drop keeps the [Offware constitution](https://github.com/offware-apps/Vision/blob/main/constitution.md),
plus its own in [constitution.md](constitution.md).

## License

[AGPL-3.0](LICENSE).
