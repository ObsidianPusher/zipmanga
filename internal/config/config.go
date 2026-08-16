package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	Server         ServerConfig    `json:"server"`
	Database       DatabaseConfig  `json:"database"`
	Cache          CacheConfig     `json:"cache"`
	Scan           ScanConfig      `json:"scan"`
	Runtime        RuntimeConfig   `json:"runtime"`
	BootstrapAdmin BootstrapConfig `json:"bootstrap_admin"`
}

type ServerConfig struct {
	EnableHTTP bool   `json:"enable_http"`
	HTTPAddr   string `json:"http_addr"`

	EnableHTTPS bool   `json:"enable_https"`
	HTTPSAddr   string `json:"https_addr"`
	TLSCert     string `json:"tls_cert"`
	TLSKey      string `json:"tls_key"`

	SessionTTL string `json:"session_ttl"`
}

type DatabaseConfig struct {
	Path string `json:"path"`
}

type CacheConfig struct {
	Dir             string `json:"dir"`
	CoverMaxEntries int64  `json:"cover_max_entries"`
	CoverMaxMB      int64  `json:"cover_max_mb"`
	ThumbWidth      int    `json:"thumb_width"`
	ZipTempMaxMB    int64  `json:"zip_temp_max_mb"`
}

type ScanConfig struct {
	Interval string `json:"interval"`
}

type RuntimeConfig struct {
	MemoryLimitMB       int64  `json:"memory_limit_mb"`
	DiskCacheMB         int64  `json:"disk_cache_mb"`
	PagePrefetchPages   int64  `json:"page_prefetch_pages"`
	PageLoadParallelism int64  `json:"page_load_parallelism"`
	WebPQuality         int64  `json:"webp_quality"`
	WebPMethod          int64  `json:"webp_method"`
	WebPConcurrency     int64  `json:"webp_concurrency"`
	WebPOutputRoot      string `json:"webp_output_root"`
}

type BootstrapConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func Default() Config {
	return Config{
		Server: ServerConfig{
			EnableHTTP:  true,
			HTTPAddr:    ":8080",
			EnableHTTPS: false,
			HTTPSAddr:   ":8443",
			SessionTTL:  "24h",
		},
		Database: DatabaseConfig{Path: "./data/zipmanga.sqlite"},
		Cache: CacheConfig{
			Dir:             "./cache",
			CoverMaxEntries: 5000,
			ThumbWidth:      360,
			ZipTempMaxMB:    2048,
		},
		Scan: ScanConfig{
			Interval: "15m",
		},
		Runtime: RuntimeConfig{
			MemoryLimitMB:       512,
			DiskCacheMB:         10240,
			PagePrefetchPages:   10,
			PageLoadParallelism: 4,
			WebPQuality:         75,
			WebPMethod:          0,
			WebPConcurrency:     2,
		},
		BootstrapAdmin: BootstrapConfig{
			Username: "admin",
			Password: "admin123",
		},
	}
}

func EnsureDefaultFile(path string) (bool, error) {
	configPath := strings.TrimSpace(path)
	if configPath == "" {
		return false, errors.New("config path is required")
	}
	configPath = filepath.Clean(configPath)

	if st, err := os.Stat(configPath); err == nil {
		if st.IsDir() {
			return false, fmt.Errorf("config path is a directory: %s", configPath)
		}
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("check config path: %w", err)
	}

	parentDir := filepath.Dir(configPath)
	if parentDir != "." && parentDir != "" {
		if err := os.MkdirAll(parentDir, 0o755); err != nil {
			return false, fmt.Errorf("create config dir: %w", err)
		}
	}

	b, err := json.MarshalIndent(Default(), "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal default config: %w", err)
	}
	b = append(b, '\n')

	if err := os.WriteFile(configPath, b, 0o644); err != nil {
		return false, fmt.Errorf("write default config: %w", err)
	}
	return true, nil
}

func Load(path string) (Config, error) {
	cfg := Default()

	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config json: %w", err)
	}

	cfg.applyDefaults(filepath.Dir(path))
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}

	return cfg, nil
}

func (c *Config) applyDefaults(baseDir string) {
	def := Default()

	if c.Server.HTTPAddr == "" {
		c.Server.HTTPAddr = def.Server.HTTPAddr
	}
	if c.Server.HTTPSAddr == "" {
		c.Server.HTTPSAddr = def.Server.HTTPSAddr
	}
	if c.Server.SessionTTL == "" {
		c.Server.SessionTTL = def.Server.SessionTTL
	}

	if c.Database.Path == "" {
		c.Database.Path = def.Database.Path
	}
	if c.Cache.Dir == "" {
		c.Cache.Dir = def.Cache.Dir
	}
	if c.Cache.CoverMaxEntries <= 0 {
		c.Cache.CoverMaxEntries = def.Cache.CoverMaxEntries
	}
	if c.Cache.ThumbWidth <= 0 {
		c.Cache.ThumbWidth = def.Cache.ThumbWidth
	}
	if c.Cache.ZipTempMaxMB <= 0 {
		c.Cache.ZipTempMaxMB = def.Cache.ZipTempMaxMB
	}
	if c.Scan.Interval == "" {
		c.Scan.Interval = def.Scan.Interval
	}
	if c.Runtime.MemoryLimitMB <= 0 {
		c.Runtime.MemoryLimitMB = def.Runtime.MemoryLimitMB
	}
	if c.Runtime.DiskCacheMB <= 0 {
		c.Runtime.DiskCacheMB = def.Runtime.DiskCacheMB
	}
	if c.Runtime.PagePrefetchPages < 0 {
		c.Runtime.PagePrefetchPages = def.Runtime.PagePrefetchPages
	}
	if c.Runtime.PageLoadParallelism <= 0 {
		c.Runtime.PageLoadParallelism = def.Runtime.PageLoadParallelism
	}
	if c.Runtime.WebPQuality <= 0 {
		c.Runtime.WebPQuality = def.Runtime.WebPQuality
	}
	if c.Runtime.WebPMethod < 0 {
		c.Runtime.WebPMethod = def.Runtime.WebPMethod
	}
	if c.Runtime.WebPConcurrency <= 0 {
		c.Runtime.WebPConcurrency = def.Runtime.WebPConcurrency
	}
	if c.BootstrapAdmin.Username == "" {
		c.BootstrapAdmin.Username = def.BootstrapAdmin.Username
	}
	if c.BootstrapAdmin.Password == "" {
		c.BootstrapAdmin.Password = def.BootstrapAdmin.Password
	}

	c.Database.Path = resolvePath(baseDir, c.Database.Path)
	c.Cache.Dir = resolvePath(baseDir, c.Cache.Dir)
}

func (c Config) Validate() error {
	if !c.Server.EnableHTTP && !c.Server.EnableHTTPS {
		return errors.New("at least one of enable_http or enable_https must be true")
	}

	if c.Server.EnableHTTPS {
		if c.Server.TLSCert == "" || c.Server.TLSKey == "" {
			return errors.New("tls_cert and tls_key are required when enable_https is true")
		}
	}

	if _, err := time.ParseDuration(c.Server.SessionTTL); err != nil {
		return fmt.Errorf("invalid server.session_ttl: %w", err)
	}
	if _, err := time.ParseDuration(c.Scan.Interval); err != nil {
		return fmt.Errorf("invalid scan.interval: %w", err)
	}
	if c.Runtime.MemoryLimitMB < 256 {
		return errors.New("runtime.memory_limit_mb must be >= 256")
	}
	if c.Runtime.DiskCacheMB < 256 {
		return errors.New("runtime.disk_cache_mb must be >= 256")
	}
	if c.Runtime.PagePrefetchPages < 0 {
		return errors.New("runtime.page_prefetch_pages must be >= 0")
	}
	if c.Runtime.PageLoadParallelism < 1 {
		return errors.New("runtime.page_load_parallelism must be >= 1")
	}
	if c.Runtime.PageLoadParallelism > 16 {
		return errors.New("runtime.page_load_parallelism must be <= 16")
	}
	if c.Runtime.WebPQuality < 1 || c.Runtime.WebPQuality > 100 {
		return errors.New("runtime.webp_quality must be between 1 and 100")
	}
	if c.Runtime.WebPMethod < 0 || c.Runtime.WebPMethod > 6 {
		return errors.New("runtime.webp_method must be between 0 and 6")
	}
	if c.Runtime.WebPConcurrency < 1 || c.Runtime.WebPConcurrency > 16 {
		return errors.New("runtime.webp_concurrency must be between 1 and 16")
	}

	if c.Database.Path == "" {
		return errors.New("database.path is required")
	}
	if c.Cache.Dir == "" {
		return errors.New("cache.dir is required")
	}
	if c.BootstrapAdmin.Username == "" || c.BootstrapAdmin.Password == "" {
		return errors.New("bootstrap_admin.username and bootstrap_admin.password are required")
	}

	return nil
}

func resolvePath(baseDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	if baseDir == "" || baseDir == "." {
		return p
	}
	return filepath.Clean(filepath.Join(baseDir, p))
}
