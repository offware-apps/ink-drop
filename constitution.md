# ink-drop constitution

ink-drop is a tool block, per article I of the
[Offware constitution](https://github.com/offware-apps/Vision/blob/main/constitution.md):
command-line stages that run on the user's own machine, outside any app, and
write a folder of plain files that an Offware app reads offline. It keeps every
article below and adds the two rules after the table, which never subtract
from it.

## Constitution check against Offware

| Article | How ink-drop keeps it |
| --- | --- |
| I. Composable blocks | Each stage does one job, and stages meet only at [the folder contract](docs/folder.md): a document plus a JSON sidecar. Any tool keeping the contract replaces any stage. |
| II. Offline first | Converting and reading need no network. Fetching runs outside the app, the case article II names as scheduled outside it. |
| III. Self-reliant, no account | Nothing to sign up for. The stages are programs rather than static files: a browser page reads another site only when that site sends `Access-Control-Allow-Origin`, which Wikipedia and the Go blog do not, so the fetch is a tool and not an app. |
| IV. Private by default | No telemetry; the only requests are the fetches of the URLs the user hands it. |
| V. One portable file | The library is plain documents and documented, versioned JSON sidecars, with no database. |
| VI. Aggregator | Every sidecar records the source URL and the fetch time. |
| VII. Network is opt-in | A stage touches the network only for a URL the user names. |
| VIII. Open and replaceable | AGPL-3.0, with open dependencies only: [go-readability](https://codeberg.org/readeck/go-readability) under MIT and `golang.org/x/net` under BSD. |
| IX. Data is inert | A sidecar is parsed and validated, never evaluated, and every EPUB drops scripts, forms and frames. |
| X. Fast, keyboard-first | Command-line only, driven from the keyboard. |
| XI. E-reader compatible | The output is EPUB and PDF, the formats e-readers open. |

## Fetching is the only network use

A stage touches the network only to fetch what the user named: a URL, a feed, a
site. Converting, reading and syncing the library stay offline.

## Generic over one device

Output is EPUB, PDF or HTML any reader opens. A feature for one tablet brand,
its push service or its library format, lives in a stage of its own, never in
the core.
