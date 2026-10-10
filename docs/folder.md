# The folder contract

Every ink-drop stage reads and writes plain folders, and the folder is the only
interface between them. A tool that keeps the three rules below is a stage, in
any language, and a tool that reads a folder is a sink: Syncthing, an OPDS
server, a tablet's own library.

## The rules

1. An item is one document, `<name>.epub`, `<name>.pdf` or `<name>.html`,
   with one JSON sidecar at `.ink/<name>.<ext>.json` in the same folder. The
   hidden `.ink/` keeps sidecars out of a reader's book list.
2. The sidecar is written before the document, and each file lands by rename
   from a temporary file. A sink therefore never sees half a document, nor a
   document without its sidecar.
3. A stage reads item paths on stdin, one per line, writes its output items,
   and prints each output path on stdout. Stages chain with a pipe and run from
   cron or `xargs` unchanged.

```sh
echo https://example.org/post | ink-fetch | ink-readable | ink-epub -to ~/Books
```

## The sidecar

```json
{
  "schema": 1,
  "file": "post.epub",
  "title": "A post",
  "source": "https://example.org/post",
  "fetched": "2026-10-10T09:00:00Z",
  "stage": "ink-epub"
}
```

A reader of a sidecar rejects any `schema` it does not know, and any `file`
other than the document's own name. A sidecar is data: it is parsed, never
evaluated. [`folder.Put`](../folder/folder.go#L35-L52) and
[`folder.Get`](../folder/folder.go#L55-L71) implement both rules for Go
stages.
