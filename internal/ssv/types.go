package ssv

// Bytes models SSV's recurring {"Value": <int64>} byte counter shape.
type Bytes struct {
	Value int64 `json:"Value"`
}

// ServerGroup is the subset of /serverGroups fields surfaced as metrics.
type ServerGroup struct {
	ID                 string `json:"Id"`
	Caption            string `json:"Caption"`
	Alias              string `json:"Alias"`
	State              int    `json:"State"`
	OurGroup           bool   `json:"OurGroup"`
	OutOfCompliance    bool   `json:"OutOfCompliance"`
	StorageUsed        Bytes  `json:"StorageUsed"`
	MaxStorage         Bytes  `json:"MaxStorage"`
	NextExpirationDate Time   `json:"NextExpirationDate"`
}

// Server is the subset of /servers fields surfaced as metrics.
type Server struct {
	ID                     string `json:"Id"`
	Caption                string `json:"Caption"`
	HostName               string `json:"HostName"`
	GroupID                string `json:"GroupId"`
	State                  int    `json:"State"`
	SupportState           int    `json:"SupportState"`
	PowerState             int    `json:"PowerState"`
	CacheState             int    `json:"CacheState"`
	DiagnosticMode         int    `json:"DiagnosticMode"`
	MaintenanceModeEnabled bool   `json:"MaintenanceModeEnabled"`
	StorageUsed            Bytes  `json:"StorageUsed"`
	TotalSystemMemory      Bytes  `json:"TotalSystemMemory"`
	AvailableSystemMemory  Bytes  `json:"AvailableSystemMemory"`
	ProductName            string `json:"ProductName"`
	ProductVersion         string `json:"ProductVersion"`
	ProductBuild           string `json:"ProductBuild"`
	OsVersion              string `json:"OsVersion"`
	IsLicensed             bool   `json:"IsLicensed"`
	LicenseExceeded        bool   `json:"LicenseExceeded"`
	OutOfCompliance        bool   `json:"OutOfCompliance"`
}

// Pool is the subset of /pools fields surfaced as metrics.
type Pool struct {
	ID             string `json:"Id"`
	Caption        string `json:"Caption"`
	Alias          string `json:"Alias"`
	ServerID       string `json:"ServerId"`
	PoolStatus     int    `json:"PoolStatus"`
	PresenceStatus int    `json:"PresenceStatus"`
	PoolMode       int    `json:"PoolMode"`
	Type           int    `json:"Type"`
	ChunkSize      Bytes  `json:"ChunkSize"`
}

// VirtualDisk is the subset of /virtualDisks fields surfaced as metrics.
type VirtualDisk struct {
	ID         string `json:"Id"`
	Caption    string `json:"Caption"`
	Alias      string `json:"Alias"`
	Size       Bytes  `json:"Size"`
	DiskStatus int    `json:"DiskStatus"`
	Type       int    `json:"Type"`
	SubType    int    `json:"SubType"`
	Offline    bool   `json:"Offline"`
	Disabled   bool   `json:"Disabled"`
	IsServed   bool   `json:"IsServed"`
}
