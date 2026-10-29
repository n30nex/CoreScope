# Border links

Border links is an opt-in public comparison of a local feed and an isolated peer feed. Open `/#/cross-border`; the visual overview reads `GET /api/cross-border?view=dashboard`. It compares path-width adoption, flood scopes, regional overlap, repeater carrying evidence and reported radio airtime. Packet details remain peer-primary under **Inspect packet and metadata evidence**. None of these views establishes an RF route across a border, packet loss, device misconfiguration or which country caused congestion.

The page refreshes once a minute while visible. Pause live updates when inspecting a snapshot. Overview, Hash widths, Scopes & regions, Radio load and Repeaters use the existing analytics card/tab conventions. Display-only tab and history changes reuse the loaded snapshot. Region, scope, window, live-update, tab and drill-down choices are bookmarkable. The existing Chart.js renderer is reused; no new frontend dependency or build step is required.

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
| `observationLimit` | `5000` | Legacy observation rows per source; dashboard scans use ten times this value, capped at 50,000 |
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

The future customizer milestone is to expose public labels, default regions, windows, limits, freshness, refresh interval and the reference profile with matching validation. Database credentials remain server-only; connection, concurrency and maximum resource ceilings remain operator-controlled.

## Request and response

`GET /api/cross-border` accepts these query parameters. The corresponding page selections are preserved in the URL hash for bookmarks and sharing.

| Parameter | Values |
|---|---|
| `view` | Omit for the legacy peer-first report, or `dashboard` for independent source summaries and charts |
| `localIata` | Comma-separated permitted local IATA codes; blank uses configured defaults |
| `peerIata` | Comma-separated peer IATA codes; blank uses configured defaults |
| `scope` | `all`, `unscoped`, `unknown`, or a named scope; names preserve case. Applies to both dashboard overview samples and to the peer cohort in packet details |
| `windowMinutes` | `15`, `60`, `180` or `360`, up to the configured maximum |

For a synthetic example, `GET /api/cross-border?localIata=AAA&peerIata=BBB&scope=all&windowMinutes=60` requires `AAA` in the local allowlist and suitable peer records for `BBB`. These are placeholders, not deployment recommendations. Invalid filters or local selections outside the allowlist return HTTP 400.

In the legacy report and packet details, the scope filter selects peer observations. Matching local packet hashes remain eligible under the local IATA and time-window constraints even when the local observation's scope is unknown, unscoped or different. Each local observation retains its own scope. Dashboard overview populations are independently selected on both sides and both respect the scope filter. Radio counters describe all reported activity and cannot be filtered by a packet scope.

HTTP 200 may contain `status: "disabled"`, `"selection_required"`, `"unavailable"`, `"partial"` or `"ready"`. Partial reports carry incomplete or stale evidence. Inspect the status and findings before interpreting counts. Disabled and selection-required reports contain typed empty collections. Peer failures, query deadlines and build-capacity limits produce sanitized findings such as `source_unavailable`, `query_timeout` and `busy`; no connection URL is returned. Identical concurrent requests share a build; distinct requests beyond capacity are refused promptly.

Report sections include `filters`, `sources`, `options`, `limits`, `completeness`, `summary`, `peerObservers`, `peerRepeaters`, `packets` and `findings`. Sources expose their label, selected regions, stored observation times and age, sampled row counts and truncation/exclusion flags. `available`, `stale` and `empty` describe a successful database read and its stored records. They are not broker connection states. Observation selection and freshness use the report's upper cutoff before applying row limits. `latestAfterSnapshotAt` reports excluded later timestamps separately; if these are the only records, source status is `after_snapshot`, with no latest in-snapshot observation or age. Such reports are partial. Later timestamps may reflect concurrent ingestion after the cutoff or clock disagreement; they do not establish a clock fault, latency or loss.

## Interpret the evidence

The dashboard adds a typed `dashboard` object with independent `local` and `peer` summaries, width counts, time buckets, scope counts, selected-region counts, a repeater matrix and radio reports. Its `sharedContents` and `independentSharedContents` describe the intersection of those overview samples. `regionPairs` contains independent shared-content counts; cells overlap and must not be summed into a network-wide total.

- **Own-advert width** counts a known repeater or room once, using its latest qualifying self-originated flood advert and full origin key. Conflicting widths at the same latest timestamp remain unknown. This is observed behavior, not a complete configuration audit.
- **Carried widths** count qualifying saved flood records. A repeater can originate a two-byte advert and still carry packets using other widths. All uniquely attributable flood-path hops support carrying; only the last hop identifies the immediately observed transmitter. Direct paths do not establish earlier forwarding.
- Named carrying attribution requires at least two-byte prefixes and a complete bounded known-node collision index. Hidden, non-displayed and non-repeater identities still participate in collision checks. One-byte paths and colliding prefixes remain ambiguous. “Not observed” is not a confirmed block.
- Ordinary unscoped flooding, transport wildcard code `0000`, named scopes and unresolved nonzero scopes are shown separately. Transport wildcard evidence does not establish that ordinary unscoped flooding is enabled.
- **Time-series records are first-stored observer/path evidence, not RF arrival rates.** Ingestion deduplicates repeated observer/path copies and may refresh their raw header without changing the original timestamp. The charts reconstruct retained evidence; they are not an immutable traffic-event history. Missing or capped portions are gaps, not zero traffic. More listeners can produce more records.
- **Radio airtime** uses completed five-minute report buckets. Timestamps are rounded by ingestion, so a slightly future bucket is not automatically a clock fault. Missing baselines, resets, gaps, negative counters and impossible rates do not become zero or a clamped 100%. Source averages use raw counter deltas weighted by valid interval seconds, with the number of reporting radios shown. TX measures reported sending duration; RX estimates successfully parsed reception airtime. These are not national channel-utilization or congestion-cause measurements.

The legacy packet classifications remain:

- `independent` means matching packet content has observations from distinct observer identities after excluding identities present in both feeds. This supports shared observed content, not a proven border-crossing RF path.
- `shared_observer_only` means the same observer ID reported this packet in both samples, with no qualifying independent overlap. Other observations may also exist. This does not establish separate devices or independent delivery.
- `shared_feed_identity` means the packet matches but one side has only identities present in both feeds' metadata, without the same observer ID reporting this packet in both samples. `sharedFeedIdentityPackets` counts these separately from `sharedObserverOnlyPackets`; the page's shared-identity total includes both. Metadata presence alone is not proof of mirrored publication or independent reception.
- `identity_unverified` means matching content exists but incomplete observer identity evidence prevents establishing independent overlap. `identityUnverifiedPackets` counts these cases separately.
- `not_seen_locally` means no matching local reference observation was found in the selected, bounded sample. A time window, retention, filtering, source outage or row cap can explain missing records; it is not a loss rate.
- `local_unavailable` means local comparison could not be completed; it must not be interpreted as absence of matching local traffic.
- In the legacy view, the local reference is queried for candidate packet hashes from the peer sample. `localReferencePackets` remains a peer-content comparison count in either view. Use the independent dashboard summaries for side-by-side source totals.
- Explicit transmit observations are excluded. Unknown direction remains explicitly unknown. SNR and RSSI describe the listed reception and do not prove an end-to-end path.
- An observed named scope, an explicitly unscoped packet and unknown scope are different states. A node's declared scopes are a separate reported configuration with their own timestamp; absent declarations are unknown, especially when collection of client region answers is disabled.
- Observed hop widths describe packet path evidence. Reported radio metadata is the latest broker `radio` string and may describe hardware or a profile; it does not verify per-packet settings. For API compatibility, `latestReportedRadioHardware` retains its legacy name and carries that raw string (bounded to 256 characters), including profile strings such as `910.525,62.5,7,8`. `radioHardwareReportedAt` remains null without a distinct report timestamp. The string is not parsed into inferred compatibility or structured device settings. A reference profile must never fill unknown device settings.
- Metadata, observation, packet and per-packet detail caps are reported explicitly. Check `completeness` and source/row truncation flags before drawing a negative conclusion. In particular, `identity_sample_incomplete` means observer identity coverage is insufficient to establish independent overlap. Empty or missing evidence must stay unknown.
- Privacy checks every sampled observer/observation identity against full observer and linked node names, independently of display caps. Shared-ID checks compare the bounded candidate identities in both directions against each source's full observer table, including unselected regions. Failed checks retain unverified identity status.

## Resource budget and acceptance

The dashboard shares the existing three-second build deadline, two-build limit, two-connection peer pool and four-entry cache. Each source scan has a hard 50,000-record ceiling. Node identities, observer identities, hidden-identity checks and metric reads are bounded; display metadata is capped at 500 rows. Region overlap is limited to 16 by 16 cells, scope summaries preserve omitted categories in Other, and per-repeater scope lists are bounded. Applicable caps are explicit; incomplete identities cannot manufacture a unique carrying attribution. The page paginates repeater rows and keeps packet details at the existing limits.

For PostgreSQL dashboard reads, a complete observer selection of at most 500 entries supplies bound numeric row IDs to the time-range query. This lets the database estimate the selected population instead of estimating a transformed IATA expression. The captured population also governs freshness and radio reads, so an IATA metadata update cannot silently mix cohorts mid-build. Partial or legacy metadata retains the IATA query. These row IDs remain private and do not enter the API response.

Regression checks cover independent local populations, own-advert versus carrying widths, all-hop attribution, collisions including hidden candidates, first-record timestamp semantics, scope wildcard separation, sample gaps, weighted airtime/reset handling, privacy, and cancellation. Browser checks use the actual chart renderer and exercise light/dark/mobile layouts, live-refresh teardown, filters and drill-downs. Measure the actual database queries and native host before enabling a larger resource budget; allocation benchmarks are not resident-memory measurements.

Use a standalone ingestor with no published ports and only its own persistent state mount. Join the existing private database network if sharing PostgreSQL. A conservative initial collector budget is a 192 MiB Go soft memory limit, a 256 MiB container limit, one CPU and rotated logs. Keep its MQTT queue bounded. These are initial limits to validate under the chosen feed, not a guarantee of throughput.

Start with two-day packet retention and seven-day node/observer/metric activity windows. Observer hard-purge must be later than the soft-delete window and still observes reference guards; a seven-day soft-delete policy therefore needs a later purge threshold. Node retention moves stale nodes to inactive metadata, so these settings do not promise a hard seven-day disk bound. Monitor peer database size, ingestion freshness, queue pressure and container memory before widening the selection or retention.

Before enabling public access, prove reader SELECT succeeds while telemetry writes, readiness writes and DDL fail; prove the writer can change telemetry but cannot alter readiness or schema. Confirm neither role inherits access to the main database's tables or persistent DDL. PostgreSQL privileges are additive: a PUBLIC grant cannot be denied for one role. If the existing database exposes data/schema privileges through PUBLIC, stop and review that separately instead of silently changing the main installation's grants.

Validate actual peer observations and local-reference isolation, then exercise the public route in a browser: selected filters, reload/deep link, empty/stale/unavailable states, shared-observer exclusion, truncation warnings and readable mobile tables. Record actual resource use and source freshness. A running collector, ready PostgreSQL or HTTP 200 alone is not proof that independent peer telemetry is arriving.

## Rollback

Set `crossBorder.enabled` to `false`, remove the peer reader environment setting from the server and restart only the affected server process using the approved deployment procedure. Stop only the dedicated peer ingestor. Preserve its database, role definitions, private config and state for review or recovery. Existing local ingestion, PostgreSQL and other receivers remain in service. Deleting a peer database, roles or retained state is a separate explicit cleanup decision.
