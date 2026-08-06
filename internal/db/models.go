package db

import "time"

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	IsAdmin      bool
	CreatedAt    time.Time
}

type Session struct {
	ID        int64
	Token     string
	UserID    int64
	ExpiresAt time.Time
	CreatedAt time.Time
}

type SessionUser struct {
	Session
	User User
}

type MountType string

const (
	MountTypeLocal  MountType = "local"
	MountTypeSMB    MountType = "smb"
	MountTypeWebDAV MountType = "webdav"
)

type Mount struct {
	ID        int64
	Name      string
	Type      MountType
	RootPath  string
	RootPaths []string

	SMBHost     string
	SMBPort     int
	SMBShare    string
	SMBUser     string
	SMBPassword string
	SMBDomain   string

	WebDAVURL      string
	WebDAVUser     string
	WebDAVPassword string

	Enabled   bool
	CreatedAt time.Time
}

type Comic struct {
	ID          int64
	MountID     int64
	Path        string
	Title       string
	FileNameKey string
	MtimeUnix   int64
	Size        int64
	PageCount   int
	CoverKey    string
	UpdatedAt   time.Time
}

type ComicWithProgress struct {
	Comic
	HasProgress        bool
	LastPage           int
	ProgressTotalPages int
	LastReadAt         time.Time
	Finished           bool
	IsFavorite         bool
}

type CacheEntry struct {
	ID         int64
	Key        string
	RelPath    string
	Size       int64
	LastAccess int64
	CreatedAt  int64
}

type ReadingProgress struct {
	ID               int64
	UserID           int64
	ComicID          *int64
	ComicFileNameKey string
	LastPage         int
	TotalPages       int
	LastReadAt       time.Time
	UpdatedAt        time.Time
}

type ReaderSettings struct {
	UserID          int64
	Mode            string
	Fit             string
	EnhanceZip      string
	EnhancePDF      string
	SpreadDirection string
	MobileCompact   bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type RuntimeSettings struct {
	ID                  int64
	MemoryLimitMB       int64
	DiskCacheMB         int64
	PagePrefetchPages   int64
	PageLoadParallelism int64
	WebPQuality         int64
	WebPMethod          int64
	WebPConcurrency     int64
	WebPOutputRoot      string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type ReadingHistoryItem struct {
	Progress   ReadingProgress
	Comic      Comic
	IsFavorite bool
}

type RootDirectory struct {
	ID        int64
	Name      string
	Path      string
	CreatedAt time.Time
}

type WebPArchiveManifest struct {
	ID               int64
	SourcePath       string
	OutputZipPath    string
	Quality          int64
	Method           int64
	SourceMtimeUnix  int64
	SourceSize       int64
	SourceEntryCount int
	ConvertedEntries int
	KeptEntries      int
	OutputSize       int64
	CompletedAt      time.Time
	UpdatedAt        time.Time
}
