# Border links

Border links is an opt-in public comparison of a local reference feed and an isolated peer feed. Open `/#/cross-border`; the page reads `GET /api/cross-border`. It compares bounded observations of matching packet content. It does not establish an RF route across a border, packet loss, device misconfiguration or live MQTT connectivity.

## Enable deliberately

1. Provision a separate PostgreSQL telemetry database, migration owner, SELECT-only reader, restricted telemetry writer and ingestor state directory. The PostgreSQL service may be shared; the logical database, roles and ingestion state must be separate. Never add a peer broker to the local installation's `mqttSources`.
2. Initialize the empty peer database using the matching release's `corescope-migrate` and offline owner credentials. Apply the narrow telemetry policy from `docker/postgres-grants.sql` using the **new peer role names**. The supplied file names the standard installation roles; do not execute it unchanged for a peer collector. Check readiness before starting ingestion. See [storage operations](storage.md).
3. Give the peer ingestor only its writer URL in `CORESCOPE_WRITER_DATABASE_URL`. Give the server only the peer reader URL in `CORESCOPE_CROSS_BORDER_PEER_DATABASE_URL`. Keep owner credentials out of runtime services and all credentials out of public config, logs, command arguments and browser responses. A different role, schema or URL spelling for the main database does not provide database isolation.
4. Configure a non-empty, verified `observerIATAWhitelist` on the **peer ingestor**. Supply the explicit public `hashRegions` names needed to recognize the selected feed's named scopes; use verified names, never wildcard keys or credentials. Without matching configured keys, scoped traffic can remain unnamed. Explicitly disable `clientRxCoverage`, `clientRxObservations`, `clientRfSamples` and `clientRegions` there unless a separate collection policy approves those streams. An empty ingestion whitelist accepts every region and is unsuitable for a deliberately limited collector.
5. Set `crossBorder.allowedLocalIata` to the explicit permitted local reference regions. Set `defaultLocalIata` to a subset and choose `defaultPeerIata` from the peer collection. Then set `crossBorder.enabled` to `true` in the server's private configuration and restart only the affected server process through the installation's normal deployment procedure.

There are no built-in country assignments, broker credentials or region selections. `allowedLocalIata: []` permits no local selections. The local reference allowlist and the ingestor whitelist are separate controls: one limits public comparison requests, the other determines which records are collected.

The peer pool is opened lazily. Missing or unavailable peer storage leaves ordinary server startup and other pages independent of the feature. A failed pool initialization is retried after the cache interval.

## Configuration

The complete `crossBorder` block and `_comment_` explanations are in [`config.example.json`](../config.example.json). Defaults are:

| Field | Default | Meaning |
|---|---|---|
| `enabled` | `false` | Explicit feature opt-in |
| `localLabel`, `peerLabel` | `Local reference`, `Peer telemetry` | Public labels; no geographic inference |
| `allowedLocalIata` | `[]` | Local regions authorized for comparison |
| `defaultLocalIata`, `defaultPeerIata` | `[]`, `[]` | Initial selections |
| `defaultWindowMinutes`, `maxWindowMinutes` | `60`, `360` | Short observation window and upper bound |
| `observationLimit` | `5000` | Observation rows per source |
| `metadataLimit` | `500` | Observer/node metadata rows |
| `packetLimit` | `100` | Packet rows returned |
| `observationsPerPacket` | `8` | Observation details retained per packet per source |
| `queryTimeoutSeconds` | `3` | Query deadline |
| `cacheSeconds`, `cacheEntries` | `30`, `4` | Report lifetime and retained result count |
| `maxConnections` | `2` | Peer read pool connections |
| `maxConcurrentBuilds` | `2` | Concurrent report builds |
| `maxSelectedIata` | `16` | Region selection count limit |
| `staleAfterSeconds` | `600` | Stored observation freshness threshold |
| `peerReferenceProfile` | `null` | Optional public radio recommendation |

A reference profile may supply `label`, `url`, `frequencyMHz`, `bandwidthKHz`, `spreadingFactor`, `codingRate` and `hopWidthBytes`. It describes a published recommendation, never the radio settings of a measured device. Keep it absent when no verified public reference is available.

The future customizer milestone is to expose public labels, default regions, windows, limits, freshness and the reference profile with matching validation. Database credentials remain server-only; connection, concurrency and maximum resource ceilings remain operator-controlled.

## Request and response

`GET /api/cross-border` accepts these query parameters. The corresponding page selections are preserved in the URL hash for bookmarks and sharing.

| Parameter | Values |
|---|---|
| `localIata` | Comma-separated permitted local IATA codes; blank uses configured defaults |
| `peerIata` | Comma-separated peer IATA codes; blank uses configured defaults |
| `scope` | Peer cohort only: `all`, `unscoped`, `unknown`, or a named scope; names preserve case |
| `windowMinutes` | `15`, `60`, `180` or `360`, up to the configured maximum |

For a synthetic example, `GET /api/cross-border?localIata=AAA&peerIata=BBB&scope=all&windowMinutes=60` requires `AAA` in the local allowlist and suitable peer records for `BBB`. These are placeholders, not deployment recommendations. Invalid filters or local selections outside the allowlist return HTTP 400.

The scope filter selects peer observations. Matching local packet hashes remain eligible under the local IATA and time-window constraints even when the local observation's scope is unknown, unscoped or different. Each local observation retains its own scope in the report; the peer scope selection does not rewrite local evidence.

HTTP 200 may contain `status: "disabled"`, `"selection_required"`, `"unavailable"`, `"partial"` or `"ready"`. Partial reports carry incomplete or stale evidence. Inspect the status and findings before interpreting counts. Disabled and selection-required reports contain typed empty collections. Peer failures, query deadlines and build-capacity limits produce sanitized findings such as `source_unavailable`, `query_timeout` and `busy`; no connection URL is returned. Identical concurrent requests share a build; distinct requests beyond capacity are refused promptly.

Report sections include `filters`, `sources`, `options`, `limits`, `completeness`, `summary`, `peerObservers`, `peerRepeaters`, `packets` and `findings`. Sources expose their label, selected regions, stored observation times and age, sampled row counts and truncation/exclusion flags. `available`, `stale` and `empty` describe a successful database read and its stored records. They are not broker connection states.

## Interpret the evidence

- `independent` means matching packet content has observations from distinct observer identities after excluding identities present in both feeds. This supports shared observed content, not a proven border-crossing RF path.
- `shared_observer_only` means the overlap is explained only by an observer identity appearing in both feeds. Mirrored or duplicate publication is not independent reception evidence.
- `identity_unverified` means matching content exists but incomplete observer identity evidence prevents establishing independent overlap. `identityUnverifiedPackets` counts these cases separately.
- `not_seen_locally` means no matching local reference observation was found in the selected, bounded sample. A time window, retention, filtering, source outage or row cap can explain missing records; it is not a loss rate.
- `local_unavailable` means local comparison could not be completed; it must not be interpreted as absence of matching local traffic.
- The local reference is queried for candidate packet hashes from the peer sample. `localReferencePackets` is a comparison count, not all local traffic during the window.
- Explicit transmit observations are excluded. Unknown direction remains explicitly unknown. SNR and RSSI describe the listed reception and do not prove an end-to-end path.
- An observed named scope, an explicitly unscoped packet and unknown scope are different states. A node's declared scopes are a separate reported configuration with their own timestamp; absent declarations are unknown, especially when collection of client region answers is disabled.
- Observed hop widths describe packet path evidence. Reported radio hardware is not a measurement of frequency, bandwidth, spreading factor or coding rate. A reference profile must never fill those unknown device settings.
- Metadata, observation, packet and per-packet detail caps are reported explicitly. Check `completeness` and source/row truncation flags before drawing a negative conclusion. In particular, `identity_sample_incomplete` means observer identity coverage is insufficient to establish independent overlap. Empty or missing evidence must stay unknown.

## Resource budget and acceptance

Use a standalone ingestor with no published ports and only its own persistent state mount. Join the existing private database network if sharing PostgreSQL. A conservative initial collector budget is a 192 MiB Go soft memory limit, a 256 MiB container limit, one CPU and rotated logs. Keep its MQTT queue bounded. These are initial limits to validate under the chosen feed, not a guarantee of throughput.

Start with two-day packet retention and seven-day node/observer/metric activity windows. Observer hard-purge must be later than the soft-delete window and still observes reference guards; a seven-day soft-delete policy therefore needs a later purge threshold. Node retention moves stale nodes to inactive metadata, so these settings do not promise a hard seven-day disk bound. Monitor peer database size, ingestion freshness, queue pressure and container memory before widening the selection or retention.

Before enabling public access, prove reader SELECT succeeds while telemetry writes, readiness writes and DDL fail; prove the writer can change telemetry but cannot alter readiness or schema. Confirm neither role inherits access to the main database's tables or persistent DDL. PostgreSQL privileges are additive: a PUBLIC grant cannot be denied for one role. If the existing database exposes data/schema privileges through PUBLIC, stop and review that separately instead of silently changing the main installation's grants.

Validate actual peer observations and local-reference isolation, then exercise the public route in a browser: selected filters, reload/deep link, empty/stale/unavailable states, shared-observer exclusion, truncation warnings and readable mobile tables. Record actual resource use and source freshness. A running collector, ready PostgreSQL or HTTP 200 alone is not proof that independent peer telemetry is arriving.

## Rollback

Set `crossBorder.enabled` to `false`, remove the peer reader environment setting from the server and restart only the affected server process using the approved deployment procedure. Stop only the dedicated peer ingestor. Preserve its database, role definitions, private config and state for review or recovery. Existing local ingestion, PostgreSQL and other receivers remain in service. Deleting a peer database, roles or retained state is a separate explicit cleanup decision.
