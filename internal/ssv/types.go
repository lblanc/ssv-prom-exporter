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
	IpAddresses            []string `json:"IpAddresses"`
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

// Host is the subset of /hosts fields surfaced as metrics.
//
// Hosts are the SAN clients (vSphere ESXi, Hyper-V, etc.) consuming
// virtual disks. State / ConnectionState / Type are vendor-defined
// numeric enums and are exposed as-is (mapping is not in the REST
// help). Description usually carries the OS / hypervisor build
// string ("VMware ESXi 8.0.3 build-24585383").
type Host struct {
	ID                string `json:"Id"`
	Caption           string `json:"Caption"`
	HostName          string `json:"HostName"`
	ExtendedCaption   string `json:"ExtendedCaption"`
	Description       string `json:"Description"`
	Version           string `json:"Version"`
	State             int    `json:"State"`
	ConnectionState   int    `json:"ConnectionState"`
	Type              int    `json:"Type"`
	InMaintenanceMode bool   `json:"InMaintenanceMode"`
	Internal          bool   `json:"Internal"`
}

// Port is the subset of /ports fields surfaced as metrics.
//
// HostId points at the Host that owns / exposes the port (which on
// SDS servers themselves is also a "host" in SSV's topology). PortType
// and PortMode are vendor-defined enums (3 = iSCSI in our lab, 4 = FC).
// RoleCapability is a bitmap mixing front-end / mirror / back-end
// roles; we expose it as-is and let the dashboard interpret.
type Port struct {
	ID              string `json:"Id"`
	Caption         string `json:"Caption"`
	Alias           string `json:"Alias"`
	HostID          string `json:"HostId"`
	PortName        string `json:"PortName"`
	PhysicalName    string `json:"PhysicalName"`
	PortType        int    `json:"PortType"`
	PortMode        int    `json:"PortMode"`
	PresenceStatus  int    `json:"PresenceStatus"`
	RoleCapability  int    `json:"RoleCapability"`
	Connected       bool   `json:"Connected"`
	Internal        bool   `json:"Internal"`
}

// PhysicalDisk is the subset of /physicalDisks fields surfaced as
// metrics. Only entries with Type == 4 are real physical disks (the
// rest of /physicalDisks is mirror pseudo-disks, system disks, etc.);
// callers should filter on Type before emitting.
type PhysicalDisk struct {
	ID            string  `json:"Id"`
	Caption       string  `json:"Caption"`
	Alias         string  `json:"Alias"`
	HostID        string  `json:"HostId"`
	Type          int     `json:"Type"`
	BusType       int     `json:"BusType"`
	DiskStatus    int     `json:"DiskStatus"`
	IsSolidState  bool    `json:"IsSolidState"`
	IsDataCoreDisk bool   `json:"IsDataCoreDisk"`
	Internal      bool    `json:"Internal"`
	Size          Bytes   `json:"Size"`
	FreeSpace     Bytes   `json:"FreeSpace"`
	PoolMemberID  string  `json:"PoolMemberId"`
	InquiryData   Inquiry `json:"InquiryData"`
}

// Inquiry mirrors SSV's nested SCSI inquiry block.
type Inquiry struct {
	Vendor   string `json:"Vendor"`
	Product  string `json:"Product"`
	Revision string `json:"Revision"`
	Serial   string `json:"Serial"`
}

// PoolMember is the subset of /poolMembers surfaced as metrics. Each
// real physical disk that backs a pool has exactly one PoolMember
// (matched by Id ↔ PhysicalDisk.PoolMemberID); the entry carries the
// pool ID and the tier number.
type PoolMember struct {
	ID          string `json:"Id"`
	Caption     string `json:"Caption"`
	DiskPoolID  string `json:"DiskPoolId"`
	DiskTier    int    `json:"DiskTier"`
	MemberState int    `json:"MemberState"`
	IsMirrored  bool   `json:"IsMirrored"`
	Internal    bool   `json:"Internal"`
	Size        Bytes  `json:"Size"`
}

// Monitor is the subset of /monitors fields surfaced as metrics.
//
// State is vendor-defined; in the PSP 20 lab we observe values 1, 2 and 4
// (the latter being the threshold-warning monitors). The mapping is not
// documented in the REST help, so callers should expose State as-is.
type Monitor struct {
	ID              string `json:"Id"`
	TemplateID      string `json:"TemplateId"`
	MonitoredObject string `json:"MonitoredObjectId"`
	State           int    `json:"State"`
	Caption         string `json:"Caption"`
	ExtendedCaption string `json:"ExtendedCaption"`
	Description     string `json:"Description"`
	MessageText     string `json:"MessageText"`
	TimeStamp       Time   `json:"TimeStamp"`
	Internal        bool   `json:"Internal"`
	SequenceNumber  int64  `json:"SequenceNumber"`
}
