package store

// User is an account that can sign in. The first user is the admin; admins
// manage other accounts. Everyone's history is their own.
type User struct {
	ID           int64  `json:"id"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"` // admin | member
	CreatedAt    string `json:"created_at"`
}

// APIToken is a personal access token for scripts (hash stored, plaintext shown once).
type APIToken struct {
	ID         int64   `json:"id"`
	UserID     int64   `json:"user_id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"`
	LastUsedAt *string `json:"last_used_at"`
	CreatedAt  string  `json:"created_at"`
}

// Device scopes.
const (
	ScopeTrack = "track" // send points
	ScopeRead  = "read"  // read history
	ScopeFull  = "full"  // both
)

// Device is one sender or reader of positions with its own key.
type Device struct {
	ID               int64    `json:"id"`
	UserID           int64    `json:"user_id"`
	Name             string   `json:"name"`
	Kind             string   `json:"kind"`
	Scope            string   `json:"scope"`
	APIKey           string   `json:"api_key,omitempty"`
	TrackerID        string   `json:"tracker_id"`
	LastSeenAt       int64    `json:"last_seen_at"`
	LastPointAt      int64    `json:"last_point_at"`
	LastLat          *float64 `json:"last_lat"`
	LastLng          *float64 `json:"last_lng"`
	LastBattery      *int     `json:"last_battery"`
	LastBatteryState string   `json:"last_battery_state"`
	LastIP           string   `json:"last_ip"`
	UserAgent        string   `json:"user_agent"`
	Points           int64    `json:"points"`
	Revoked          bool     `json:"revoked"`
	CreatedAt        int64    `json:"created_at"`
}

// CanTrack reports whether the device may send points.
func (d *Device) CanTrack() bool { return d.Scope == ScopeTrack || d.Scope == ScopeFull }

// CanRead reports whether the device may read history.
func (d *Device) CanRead() bool { return d.Scope == ScopeRead || d.Scope == ScopeFull }

// Point flags.
const (
	FlagOutlier = 1
)

// Point is one recorded position.
type Point struct {
	ID               int64    `json:"id"`
	UserID           int64    `json:"user_id"`
	DeviceID         *int64   `json:"device_id,omitempty"`
	ImportID         *int64   `json:"import_id,omitempty"`
	TS               int64    `json:"ts"`
	Lat              float64  `json:"lat"`
	Lng              float64  `json:"lng"`
	Altitude         *float64 `json:"altitude,omitempty"`
	Accuracy         *float64 `json:"accuracy,omitempty"`
	VerticalAccuracy *float64 `json:"vertical_accuracy,omitempty"`
	Speed            *float64 `json:"speed,omitempty"`
	Course           *float64 `json:"course,omitempty"`
	Battery          *int     `json:"battery,omitempty"`
	BatteryState     string   `json:"battery_state,omitempty"`
	Motion           string   `json:"motion,omitempty"`
	Connection       string   `json:"connection,omitempty"`
	SSID             string   `json:"ssid,omitempty"`
	TrackerID        string   `json:"tracker_id,omitempty"`
	Source           string   `json:"source,omitempty"`
	Country          string   `json:"country,omitempty"`
	Flags            int      `json:"flags,omitempty"`
	Extra            string   `json:"extra,omitempty"`
}

// Place is somewhere you stop.
type Place struct {
	ID        int64   `json:"id"`
	UserID    int64   `json:"user_id"`
	Name      string  `json:"name"`
	Kind      string  `json:"kind"` // saved | discovered
	Category  string  `json:"category"`
	Color     string  `json:"color"`
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	Radius    float64 `json:"radius"`
	Address   string  `json:"address"`
	City      string  `json:"city"`
	Country   string  `json:"country"`
	Geocoded  int     `json:"geocoded"`
	Renamed   bool    `json:"renamed"`
	Hidden    bool    `json:"hidden"`
	Notes     string  `json:"notes"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
	// Filled by listings.
	Visits     int   `json:"visits"`
	TotalS     int64 `json:"total_s"`
	FirstVisit int64 `json:"first_visit"`
	LastVisit  int64 `json:"last_visit"`
}

// Place kinds.
const (
	PlaceSaved      = "saved"
	PlaceDiscovered = "discovered"
)

// Visit statuses.
const (
	VisitSuggested = "suggested"
	VisitConfirmed = "confirmed"
	VisitDeclined  = "declined"
)

// Visit is a stay at one spot.
type Visit struct {
	ID        int64   `json:"id"`
	UserID    int64   `json:"user_id"`
	PlaceID   *int64  `json:"place_id"`
	StartTS   int64   `json:"start_ts"`
	EndTS     int64   `json:"end_ts"`
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	Radius    float64 `json:"radius"`
	Points    int     `json:"points"`
	Status    string  `json:"status"`
	Name      string  `json:"name"`
	Notes     string  `json:"notes"`
	Manual    bool    `json:"manual"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
	// Joined from the place.
	Place *Place `json:"place,omitempty"`
}

// DisplayName is the visit's own name, else its place's, else "".
func (v *Visit) DisplayName() string {
	if v.Name != "" {
		return v.Name
	}
	if v.Place != nil {
		return v.Place.Name
	}
	return ""
}

// Track is a journey between two stays.
type Track struct {
	ID            int64   `json:"id"`
	UserID        int64   `json:"user_id"`
	StartTS       int64   `json:"start_ts"`
	EndTS         int64   `json:"end_ts"`
	Distance      float64 `json:"distance"`
	Points        int     `json:"points"`
	AvgSpeed      float64 `json:"avg_speed"`
	MaxSpeed      float64 `json:"max_speed"`
	ElevationGain float64 `json:"elevation_gain"`
	ElevationLoss float64 `json:"elevation_loss"`
	Mode          string  `json:"mode"`
	ModeManual    bool    `json:"mode_manual"`
	StartLat      float64 `json:"start_lat"`
	StartLng      float64 `json:"start_lng"`
	EndLat        float64 `json:"end_lat"`
	EndLng        float64 `json:"end_lng"`
	Polyline      string  `json:"polyline"`
	Countries     string  `json:"countries"`
	CreatedAt     int64   `json:"created_at"`
}

// Day is one person's local day, summarised.
type Day struct {
	Date        string             `json:"date"`
	Points      int                `json:"points"`
	Distance    float64            `json:"distance"`
	MovingS     int64              `json:"moving_s"`
	FirstTS     int64              `json:"first_ts"`
	LastTS      int64              `json:"last_ts"`
	Countries   []string           `json:"countries"`
	Modes       map[string]float64 `json:"modes"`
	Visits      int                `json:"visits"`
	Places      []int64            `json:"places"`
	MaxFromHome float64            `json:"max_from_home"`
}

// Import is one background import.
type Import struct {
	ID          int64   `json:"id"`
	UserID      int64   `json:"user_id"`
	Name        string  `json:"name"`
	Format      string  `json:"format"`
	Status      string  `json:"status"`
	FilePath    string  `json:"-"`
	Size        int64   `json:"size"`
	SourceURL   string  `json:"source_url,omitempty"`
	SourceKey   string  `json:"-"`
	PointsRead  int64   `json:"points_read"`
	PointsAdded int64   `json:"points_added"`
	VisitsAdded int64   `json:"visits_added"`
	FirstTS     int64   `json:"first_ts"`
	LastTS      int64   `json:"last_ts"`
	Error       string  `json:"error"`
	Progress    float64 `json:"progress"`
	CreatedAt   int64   `json:"created_at"`
	FinishedAt  int64   `json:"finished_at"`
}

// Share lets someone else see where you are.
type Share struct {
	ID         int64  `json:"id"`
	OwnerID    int64  `json:"owner_id"`
	ViewerID   *int64 `json:"viewer_id"`
	Token      string `json:"token,omitempty"`
	Name       string `json:"name"`
	Level      string `json:"level"` // live | history
	PrecisionM int    `json:"precision_m"`
	ExpiresAt  int64  `json:"expires_at"`
	CreatedAt  int64  `json:"created_at"`
	// Joined.
	OwnerName   string `json:"owner_name,omitempty"`
	OwnerEmail  string `json:"owner_email,omitempty"`
	ViewerName  string `json:"viewer_name,omitempty"`
	ViewerEmail string `json:"viewer_email,omitempty"`
}

// PrivacyZone keeps a circle out of shared views and exports.
type PrivacyZone struct {
	ID         int64   `json:"id"`
	UserID     int64   `json:"user_id"`
	Name       string  `json:"name"`
	Lat        float64 `json:"lat"`
	Lng        float64 `json:"lng"`
	Radius     float64 `json:"radius"`
	DropPoints bool    `json:"drop_points"`
	CreatedAt  int64   `json:"created_at"`
}
