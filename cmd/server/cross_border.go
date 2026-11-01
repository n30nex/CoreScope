package main

// CrossBorderConfig is opt-in. The peer reader URL is supplied only through
// CORESCOPE_CROSS_BORDER_PEER_DATABASE_URL and is never part of public config.
type CrossBorderConfig struct {
	Enabled               bool                         `json:"enabled"`
	LocalLabel            string                       `json:"localLabel"`
	PeerLabel             string                       `json:"peerLabel"`
	AllowedLocalIATA      []string                     `json:"allowedLocalIata"`
	DefaultLocalIATA      []string                     `json:"defaultLocalIata"`
	DefaultPeerIATA       []string                     `json:"defaultPeerIata"`
	DefaultWindowMinutes  int                          `json:"defaultWindowMinutes"`
	MaxWindowMinutes      int                          `json:"maxWindowMinutes"`
	ObservationLimit      int                          `json:"observationLimit"`
	MetadataLimit         int                          `json:"metadataLimit"`
	PacketLimit           int                          `json:"packetLimit"`
	ObservationsPerPacket int                          `json:"observationsPerPacket"`
	QueryTimeoutSeconds   int                          `json:"queryTimeoutSeconds"`
	CacheSeconds          int                          `json:"cacheSeconds"`
	CacheEntries          int                          `json:"cacheEntries"`
	MaxConnections        int                          `json:"maxConnections"`
	MaxConcurrentBuilds   int                          `json:"maxConcurrentBuilds"`
	MaxSelectedIATA       int                          `json:"maxSelectedIata"`
	StaleAfterSeconds     int                          `json:"staleAfterSeconds"`
	PeerReferenceProfile  *CrossBorderReferenceProfile `json:"peerReferenceProfile,omitempty"`
}

// ReferenceProfile is operator-supplied community guidance, never a measured
// device setting and never taken from the analytics airtime estimation preset.
type CrossBorderReferenceProfile struct {
	Label           string  `json:"label"`
	URL             string  `json:"url"`
	FrequencyMHz    float64 `json:"frequencyMHz"`
	BandwidthKHz    float64 `json:"bandwidthKHz"`
	SpreadingFactor int     `json:"spreadingFactor"`
	CodingRate      int     `json:"codingRate"`
	HopWidthBytes   int     `json:"hopWidthBytes"`
}

type CrossBorderFilters struct {
	View          string   `json:"view,omitempty"`
	LocalIATA     []string `json:"localIata"`
	PeerIATA      []string `json:"peerIata"`
	Scope         string   `json:"scope"`
	WindowMinutes int      `json:"windowMinutes"`
}

type CrossBorderSource struct {
	Label                        string                       `json:"label"`
	Status                       string                       `json:"status"`
	SelectionIATA                []string                     `json:"selectionIata"`
	OldestStoredObservationAt    *string                      `json:"oldestStoredObservationAt"`
	LatestObservationAt          *string                      `json:"latestObservationAt"`
	LatestAfterSnapshotAt        *string                      `json:"latestAfterSnapshotAt"`
	AgeSeconds                   *float64                     `json:"ageSeconds"`
	RowsRead                     int                          `json:"rowsRead"`
	ExcludedTXObservations       int                          `json:"excludedTxObservations"`
	ExcludedInvalidObservations  int                          `json:"excludedInvalidObservations"`
	UnknownDirectionObservations int                          `json:"unknownDirectionObservations"`
	ObservationsTruncated        bool                         `json:"observationsTruncated"`
	ObserversTruncated           bool                         `json:"observersTruncated"`
	NodesTruncated               bool                         `json:"nodesTruncated"`
	ReferenceProfile             *CrossBorderReferenceProfile `json:"referenceProfile"`
}

type CrossBorderSources struct {
	Local CrossBorderSource `json:"local"`
	Peer  CrossBorderSource `json:"peer"`
}

type CrossBorderRegion struct {
	IATA      string `json:"iata"`
	Label     string `json:"label"`
	Observers int    `json:"observers"`
}

type CrossBorderScopeOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type CrossBorderOptions struct {
	LocalRegions  []CrossBorderRegion      `json:"localRegions"`
	PeerRegions   []CrossBorderRegion      `json:"peerRegions"`
	Scopes        []CrossBorderScopeOption `json:"scopes"`
	WindowMinutes []int                    `json:"windowMinutes"`
}

type CrossBorderLimits struct {
	ObservationRows       int `json:"observationRows"`
	MetadataRows          int `json:"metadataRows"`
	PacketRows            int `json:"packetRows"`
	ObservationsPerPacket int `json:"observationsPerPacket"`
	CacheSeconds          int `json:"cacheSeconds"`
}

type CrossBorderCompleteness struct {
	Complete               bool `json:"complete"`
	PacketsTruncated       bool `json:"packetsTruncated"`
	PeerRepeatersTruncated bool `json:"peerRepeatersTruncated"`
}

type CrossBorderSummary struct {
	PeerPackets               int `json:"peerPackets"`
	LocalReferencePackets     int `json:"localReferencePackets"`
	IndependentSharedPackets  int `json:"independentSharedPackets"`
	SharedObserverOnlyPackets int `json:"sharedObserverOnlyPackets"`
	SharedFeedIdentityPackets int `json:"sharedFeedIdentityPackets"`
	NotSeenLocallyPackets     int `json:"notSeenLocallyPackets"`
	DuplicateObserverIDs      int `json:"duplicateObserverIds"`
	IdentityUnverifiedPackets int `json:"identityUnverifiedPackets"`
}

type CrossBorderScope struct {
	Kind string  `json:"kind"`
	Name *string `json:"name"`
}

type CrossBorderLocation struct {
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	ReportedAt *string `json:"reportedAt"`
}

type CrossBorderObserver struct {
	ID                           string               `json:"id"`
	Name                         string               `json:"name"`
	IATA                         string               `json:"iata"`
	SharedIdentity               bool                 `json:"sharedIdentity"`
	LastSeenAt                   *string              `json:"lastSeenAt"`
	LatestReportedRadioHardware  *string              `json:"latestReportedRadioHardware"`
	RadioHardwareReportedAt      *string              `json:"radioHardwareReportedAt"`
	ReportedCanRelay             *bool                `json:"reportedCanRelay"`
	CanRelayReportedAt           *string              `json:"canRelayReportedAt"`
	Location                     *CrossBorderLocation `json:"location"`
	SampleObservations           int                  `json:"sampleObservations"`
	UnknownDirectionObservations int                  `json:"unknownDirectionObservations"`
	HopWidths                    []int                `json:"hopWidths"`
	ObservedScopes               []CrossBorderScope   `json:"observedScopes"`
}

type CrossBorderDeclaredScopes struct {
	Names          []string `json:"names"`
	AllowsUnscoped bool     `json:"allowsUnscoped"`
	ReportedAt     *string  `json:"reportedAt"`
	Truncated      *bool    `json:"truncated"`
}

type CrossBorderRepeater struct {
	PublicKey              string                     `json:"publicKey"`
	Name                   string                     `json:"name"`
	Role                   string                     `json:"role"`
	LastSeenAt             *string                    `json:"lastSeenAt"`
	Location               *CrossBorderLocation       `json:"location"`
	DeclaredScopes         *CrossBorderDeclaredScopes `json:"declaredScopes"`
	ObservedScopes         []CrossBorderScope         `json:"observedScopes"`
	ObservedUnscoped       bool                       `json:"observedUnscoped"`
	ObservedUnknownScope   bool                       `json:"observedUnknownScope"`
	HopWidths              []int                      `json:"hopWidths"`
	SampleForwardedPackets int                        `json:"sampleForwardedPackets"`
	LastForwardedAt        *string                    `json:"lastForwardedAt"`
}

type CrossBorderObservation struct {
	ObserverID     string           `json:"observerId"`
	ObservedAt     string           `json:"observedAt"`
	Direction      string           `json:"direction"`
	SNR            *float64         `json:"snr"`
	RSSI           *float64         `json:"rssi"`
	HopWidth       *int             `json:"hopWidth"`
	HopCount       *int             `json:"hopCount"`
	SharedObserver bool             `json:"sharedObserver"`
	RouteType      *int             `json:"routeType"`
	Scope          CrossBorderScope `json:"scope"`
}

type CrossBorderPacket struct {
	Hash                  string                   `json:"hash"`
	PayloadType           int                      `json:"payloadType"`
	RouteType             *int                     `json:"routeType"`
	Scope                 CrossBorderScope         `json:"scope"`
	LastSeenAt            string                   `json:"lastSeenAt"`
	Overlap               string                   `json:"overlap"`
	PeerObservationCount  int                      `json:"peerObservationCount"`
	LocalObservationCount int                      `json:"localObservationCount"`
	ObservationsTruncated bool                     `json:"observationsTruncated"`
	PeerObservations      []CrossBorderObservation `json:"peerObservations"`
	LocalObservations     []CrossBorderObservation `json:"localObservations"`
}

type CrossBorderFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type CrossBorderResponse struct {
	Dashboard     *CrossBorderDashboard   `json:"dashboard,omitempty"`
	Enabled       bool                    `json:"enabled"`
	Status        string                  `json:"status"`
	GeneratedAt   string                  `json:"generatedAt"`
	Filters       CrossBorderFilters      `json:"filters"`
	Sources       CrossBorderSources      `json:"sources"`
	Options       CrossBorderOptions      `json:"options"`
	Limits        CrossBorderLimits       `json:"limits"`
	Completeness  CrossBorderCompleteness `json:"completeness"`
	Summary       CrossBorderSummary      `json:"summary"`
	PeerObservers []CrossBorderObserver   `json:"peerObservers"`
	PeerRepeaters []CrossBorderRepeater   `json:"peerRepeaters"`
	Packets       []CrossBorderPacket     `json:"packets"`
	Findings      []CrossBorderFinding    `json:"findings"`
}

// Internal snapshots contain only metadata used by the diagnostics. RawHex is
// never serialized and only its packet header/path is inspected.
type crossBorderObservationRow struct {
	Hash, ObserverID, Direction, RawHex, FromPubkey string
	ObserverIATA                                    string
	CanonicalRawHex                                 string
	Timestamp                                       int64
	PayloadType, RouteType                          int
	ScopeName                                       *string
	SNR, RSSI                                       *float64
	RawHexLength                                    int
}

type crossBorderNode struct {
	PublicKey, Name, Role                string
	LastSeenAt, ConfiguredScope, ScopeAt *string
	Lat, Lon                             *float64
	DeclaredTruncated                    *bool
	Hidden                               bool
}

type crossBorderSnapshot struct {
	SelectedObserverRows       []crossBorderSelectedObserver
	ObserverSelectionComplete  bool
	DashboardObserverSelection []crossBorderSelectedObserver
	DashboardNodes             []crossBorderNodeIdentity
	DashboardNodeIndexComplete bool
	DashboardMetrics           []crossBorderMetricRow
	DashboardMetricsAvailable  bool
	DashboardMetricsTruncated  bool
	DashboardScanCutoff        *int64 // oldest SQL-sampled second, before privacy filtering
	Available                  bool
	Observers                  []CrossBorderObserver
	Nodes                      []crossBorderNode
	Observations               []crossBorderObservationRow
	OldestStoredObservationAt  *string
	LatestStoredObservationAt  *string
	LatestAfterSnapshotAt      *string
	ObservationsTruncated      bool
	ObserversTruncated         bool
	NodesTruncated             bool
	SharedObserverIDs          []string
	Regions                    []CrossBorderRegion
	SharedIdentityComplete     bool
	SharedIdentityChecked      bool
}

// Selection row IDs belong to the private query snapshot, never the API model.
type crossBorderSelectedObserver struct {
	RowID int64
	ID    string
	IATA  string
}
