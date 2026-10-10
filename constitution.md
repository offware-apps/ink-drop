# ink-drop constitution

ink-drop keeps every rule of the
[Offware constitution](https://github.com/offware-apps/Vision/blob/main/constitution.md)
and adds the three below, which never subtract from it.

## I. The folder is the interface

Stages share nothing but [the folder contract](docs/folder.md): no shared
database, no daemon two stages both need, no format only ink-drop reads. Any
tool keeping the contract can replace any stage.

## II. Fetching is the only network use

A stage touches the network only to fetch what the user named: a URL, a feed, a
site. Reading the library, converting and syncing stay offline. A server, where
one is added, is an inbox that runs the same stages, labelled and optional, per
article VI of the Offware constitution.

## III. Generic over one device

Output is EPUB, PDF or HTML any reader opens. A feature for one tablet brand,
its push service or its library format, lives in a stage of its own, never in
the core.

## Constitution check against Offware

| Article | How ink-drop keeps it |
| --- | --- |
| I. Offline first | The library reads with no network; fetching runs once, on the user's command. |
| II. No account | Nothing to sign up for; the stages run on the user's machine. |
| III. Private by default | No telemetry; the only requests are the fetches the user named. |
| IV. One portable file | Each item is its document plus one JSON sidecar, documented and versioned. |
| V. Aggregator | A sidecar records the source URL and fetch time of every item. |
| VI. Network opt-in | Fetching is the explicit action; a server, if added, is off by default. |
| VII. Open | AGPL-3.0, Go standard library first, no proprietary service. |
| VIII. Data is inert | A sidecar is parsed and validated, never evaluated. |
| IX. Fast, accessible | Command-line stages; a web inbox, if added, is keyboard-first. |
| X. E-reader compatible | The output is the e-reader's own formats. |
