package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"zipmanga/internal/comicfmt"
)

const legacyRuntimeGCPercent = 100

type Store struct {
	db *sql.DB
}

const currentSchemaVersion = 1

func Open(path string, logger *log.Logger) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)

	store := &Store{db: db}
	if logger != nil {
		logger.Printf("startup: db migrate begin")
	}
	if err := store.Migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if logger != nil {
		logger.Printf("startup: db validate begin")
	}
	if err := store.ValidateAndRepairSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.ensureSchemaVersion(currentSchemaVersion); err != nil {
		_ = db.Close()
		return nil, err
	}
	if logger != nil {
		logger.Printf("startup: db validate complete")
		logger.Printf("startup: db migrate complete")
	}

	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) Migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			is_admin INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			token TEXT NOT NULL UNIQUE,
			user_id INTEGER NOT NULL,
			expires_at TEXT NOT NULL,
			created_at TEXT NOT NULL,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_token ON sessions(token)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at)`,
		`CREATE TABLE IF NOT EXISTS mounts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			root_path TEXT NOT NULL,
			smb_host TEXT,
			smb_port INTEGER,
			smb_share TEXT,
			smb_user TEXT,
			smb_password TEXT,
			smb_domain TEXT,
			webdav_url TEXT,
			webdav_user TEXT,
			webdav_password TEXT,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS comics (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				mount_id INTEGER NOT NULL,
				path TEXT NOT NULL,
				title TEXT NOT NULL,
				file_name_key TEXT NOT NULL DEFAULT '',
				mtime_unix INTEGER NOT NULL,
				size INTEGER NOT NULL,
				page_count INTEGER NOT NULL,
				cover_key TEXT,
				updated_at TEXT NOT NULL,
				FOREIGN KEY(mount_id) REFERENCES mounts(id) ON DELETE CASCADE,
				UNIQUE(mount_id, path)
			)`,
		`CREATE INDEX IF NOT EXISTS idx_comics_mount ON comics(mount_id)`,
		`CREATE INDEX IF NOT EXISTS idx_comics_title ON comics(title)`,
		`CREATE TABLE IF NOT EXISTS cache_entries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			key TEXT NOT NULL UNIQUE,
			rel_path TEXT NOT NULL,
			size INTEGER NOT NULL,
			last_access INTEGER NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS reading_progress (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL,
				comic_id INTEGER,
				comic_file_name_key TEXT NOT NULL DEFAULT '',
				last_page INTEGER NOT NULL DEFAULT 0,
				total_pages INTEGER NOT NULL DEFAULT 0,
				last_read_at TEXT NOT NULL,
				updated_at TEXT NOT NULL,
				FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
				UNIQUE(user_id, comic_id)
			)`,
		`CREATE INDEX IF NOT EXISTS idx_reading_progress_user_updated ON reading_progress(user_id, updated_at DESC)`,
		`CREATE TABLE IF NOT EXISTS comic_reader_states (
            user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
            mount_id INTEGER NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
            comic_path TEXT NOT NULL,
            settings TEXT NOT NULL,
            last_page INTEGER NOT NULL,
            total_pages INTEGER NOT NULL,
            page_offset REAL NOT NULL,
            updated_at INTEGER NOT NULL,
            PRIMARY KEY(user_id, mount_id, comic_path)
        )`,
		`CREATE TABLE IF NOT EXISTS root_directories (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			path TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS user_reader_settings (
            quality TEXT NOT NULL DEFAULT 'standard',
            spread TEXT NOT NULL DEFAULT 'auto',
            cover_offset INTEGER NOT NULL DEFAULT 1,
			user_id INTEGER PRIMARY KEY,
			mode TEXT NOT NULL,
			fit TEXT NOT NULL,
			enhance_zip TEXT NOT NULL,
			enhance_pdf TEXT NOT NULL,
			spread_direction TEXT NOT NULL,
			mobile_compact INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS favorite_comics (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL,
				comic_id INTEGER,
				comic_file_name_key TEXT NOT NULL DEFAULT '',
				created_at TEXT NOT NULL,
				FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
				FOREIGN KEY(comic_id) REFERENCES comics(id) ON DELETE SET NULL
			)`,
		`CREATE INDEX IF NOT EXISTS idx_favorite_comics_user_created ON favorite_comics(user_id, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS app_runtime_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			memory_limit_mb INTEGER NOT NULL,
			disk_cache_mb INTEGER NOT NULL,
			page_prefetch_pages INTEGER NOT NULL,
			frontend_page_prefetch_pages INTEGER NOT NULL,
			page_load_parallelism INTEGER NOT NULL,
			webp_quality INTEGER NOT NULL DEFAULT 75,
			webp_method INTEGER NOT NULL DEFAULT 0,
			webp_concurrency INTEGER NOT NULL DEFAULT 2,
			webp_output_root TEXT NOT NULL DEFAULT '',
			gc_percent INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS webp_tasks (
			id TEXT PRIMARY KEY,
			source_root_path TEXT NOT NULL,
			source_path TEXT NOT NULL,
			output_root_path TEXT NOT NULL,
			output_dir_path TEXT NOT NULL,
			quality INTEGER NOT NULL,
			method INTEGER NOT NULL,
			concurrency INTEGER NOT NULL,
			status TEXT NOT NULL,
			total_archives INTEGER NOT NULL DEFAULT 0,
			done_archives INTEGER NOT NULL DEFAULT 0,
			partial_archives INTEGER NOT NULL DEFAULT 0,
			pending_archives INTEGER NOT NULL DEFAULT 0,
			current_archive TEXT NOT NULL DEFAULT '',
			current_archive_done INTEGER NOT NULL DEFAULT 0,
			current_archive_total INTEGER NOT NULL DEFAULT 0,
			current_phase TEXT NOT NULL DEFAULT '',
			current_item TEXT NOT NULL DEFAULT '',
			cancel_requested INTEGER NOT NULL DEFAULT 0,
			last_seq INTEGER NOT NULL DEFAULT 0,
			summary_json TEXT NOT NULL DEFAULT '{}',
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			started_at TEXT NOT NULL DEFAULT '',
			finished_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS webp_task_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			level TEXT NOT NULL,
			message TEXT NOT NULL,
			created_at TEXT NOT NULL,
			FOREIGN KEY(task_id) REFERENCES webp_tasks(id) ON DELETE CASCADE,
			UNIQUE(task_id, seq)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_webp_tasks_status_created ON webp_tasks(status, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_webp_task_logs_task_seq ON webp_task_logs(task_id, seq ASC)`,
		`CREATE TABLE IF NOT EXISTS webp_archive_manifests (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			source_path TEXT NOT NULL,
			output_zip_path TEXT NOT NULL,
			quality INTEGER NOT NULL,
			method INTEGER NOT NULL,
			source_mtime_unix INTEGER NOT NULL,
			source_size INTEGER NOT NULL,
			source_entry_count INTEGER NOT NULL,
			converted_entries INTEGER NOT NULL,
			kept_entries INTEGER NOT NULL,
			output_size INTEGER NOT NULL,
			completed_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(source_path, output_zip_path, quality, method)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_webp_archive_manifests_source_output ON webp_archive_manifests(source_path, output_zip_path)`,
	}

	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate failed for %q: %w", stmt, err)
		}
	}

	if err := s.migrateReadingProgressForeignKey(); err != nil {
		return err
	}

	return nil
}

func (s *Store) SchemaVersion() (int, error) {
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

func (s *Store) ensureSchemaVersion(version int) error {
	if version < 0 {
		return fmt.Errorf("invalid schema version %d", version)
	}
	_, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version))
	return err
}

func (s *Store) ValidateAndRepairSchema() error {
	if err := s.ensureComicsSchema(); err != nil {
		return err
	}
	if err := s.ensureFavoriteComicsSchema(); err != nil {
		return err
	}
	for _, column := range []struct{ name, ddl string }{
		{"quality", "ALTER TABLE user_reader_settings ADD COLUMN quality TEXT NOT NULL DEFAULT 'standard'"},
		{"spread", "ALTER TABLE user_reader_settings ADD COLUMN spread TEXT NOT NULL DEFAULT 'auto'"},
		{"cover_offset", "ALTER TABLE user_reader_settings ADD COLUMN cover_offset INTEGER NOT NULL DEFAULT 1"},
	} {
		if err := s.ensureColumn("user_reader_settings", column.name, column.ddl); err != nil {
			return err
		}
	}
	if err := s.ensureColumn("reading_progress", "comic_id", `ALTER TABLE reading_progress ADD COLUMN comic_id INTEGER`); err != nil {
		return err
	}
	if err := s.ensureColumn("reading_progress", "comic_file_name_key", `ALTER TABLE reading_progress ADD COLUMN comic_file_name_key TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("favorite_comics", "comic_id", `ALTER TABLE favorite_comics ADD COLUMN comic_id INTEGER`); err != nil {
		return err
	}
	if err := s.ensureColumn("favorite_comics", "comic_file_name_key", `ALTER TABLE favorite_comics ADD COLUMN comic_file_name_key TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("cache_entries", "created_at", `ALTER TABLE cache_entries ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn("cache_entries", "last_access", `ALTER TABLE cache_entries ADD COLUMN last_access INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn("app_runtime_settings", "updated_at", `ALTER TABLE app_runtime_settings ADD COLUMN updated_at TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("app_runtime_settings", "disk_cache_mb", `ALTER TABLE app_runtime_settings ADD COLUMN disk_cache_mb INTEGER NOT NULL DEFAULT 10240`); err != nil {
		return err
	}
	if err := s.ensureColumn("app_runtime_settings", "page_prefetch_pages", `ALTER TABLE app_runtime_settings ADD COLUMN page_prefetch_pages INTEGER NOT NULL DEFAULT 10`); err != nil {
		return err
	}
	if err := s.ensureColumn("app_runtime_settings", "frontend_page_prefetch_pages", `ALTER TABLE app_runtime_settings ADD COLUMN frontend_page_prefetch_pages INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn("app_runtime_settings", "page_load_parallelism", `ALTER TABLE app_runtime_settings ADD COLUMN page_load_parallelism INTEGER NOT NULL DEFAULT 4`); err != nil {
		return err
	}
	if err := s.ensureColumn("app_runtime_settings", "webp_quality", `ALTER TABLE app_runtime_settings ADD COLUMN webp_quality INTEGER NOT NULL DEFAULT 75`); err != nil {
		return err
	}
	if err := s.ensureColumn("app_runtime_settings", "webp_method", `ALTER TABLE app_runtime_settings ADD COLUMN webp_method INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn("app_runtime_settings", "webp_concurrency", `ALTER TABLE app_runtime_settings ADD COLUMN webp_concurrency INTEGER NOT NULL DEFAULT 2`); err != nil {
		return err
	}
	if err := s.ensureColumn("app_runtime_settings", "webp_output_root", `ALTER TABLE app_runtime_settings ADD COLUMN webp_output_root TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("webp_tasks", "partial_archives", `ALTER TABLE webp_tasks ADD COLUMN partial_archives INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn("webp_tasks", "pending_archives", `ALTER TABLE webp_tasks ADD COLUMN pending_archives INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn("webp_tasks", "current_archive_done", `ALTER TABLE webp_tasks ADD COLUMN current_archive_done INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn("webp_tasks", "current_archive_total", `ALTER TABLE webp_tasks ADD COLUMN current_archive_total INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.backfillComicFileNameKeys(); err != nil {
		return err
	}
	if err := s.backfillComicUpdatedAt(); err != nil {
		return err
	}
	if err := s.backfillReadingProgressFileNameKeys(); err != nil {
		return err
	}
	if err := s.backfillFavoriteComicFileNameKeys(); err != nil {
		return err
	}
	if err := s.dedupeReadingProgressByFileName(); err != nil {
		return err
	}
	if err := s.dedupeFavoriteComicsByFileName(); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_comics_file_name_key ON comics(file_name_key)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_cache_last_access ON cache_entries(last_access)`); err != nil {
		return err
	}
	if err := s.ensureUniqueIndex("reading_progress", "idx_reading_progress_user_file_name", []string{"user_id", "comic_file_name_key"}, `CREATE UNIQUE INDEX IF NOT EXISTS idx_reading_progress_user_file_name ON reading_progress(user_id, comic_file_name_key)`); err != nil {
		return err
	}
	if err := s.ensureUniqueIndex("favorite_comics", "idx_favorite_comics_user_file_name", []string{"user_id", "comic_file_name_key"}, `CREATE UNIQUE INDEX IF NOT EXISTS idx_favorite_comics_user_file_name ON favorite_comics(user_id, comic_file_name_key)`); err != nil {
		return err
	}
	if err := s.ensureSchemaVersion(currentSchemaVersion); err != nil {
		return err
	}
	return nil
}

func (s *Store) migrateReadingProgressForeignKey() error {
	var hasOldFK bool
	row := s.db.QueryRow(`
		SELECT 1 FROM pragma_foreign_key_list('reading_progress')
		WHERE "table" = 'comics' AND "to" = 'id' AND on_delete = 'CASCADE'
	`)
	if err := row.Scan(&hasOldFK); err == nil && hasOldFK {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer func() {
			_ = tx.Rollback()
		}()

		_, err = tx.Exec(`
			CREATE TABLE reading_progress_new (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL,
				comic_id INTEGER,
				last_page INTEGER NOT NULL DEFAULT 0,
				total_pages INTEGER NOT NULL DEFAULT 0,
				last_read_at TEXT NOT NULL,
				updated_at TEXT NOT NULL,
				FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
				FOREIGN KEY(comic_id) REFERENCES comics(id) ON DELETE SET NULL,
				UNIQUE(user_id, comic_id)
			)
		`)
		if err != nil {
			return err
		}

		_, err = tx.Exec(`
			INSERT INTO reading_progress_new
			SELECT id, user_id, comic_id, last_page, total_pages, last_read_at, updated_at
			FROM reading_progress
		`)
		if err != nil {
			return err
		}

		_, err = tx.Exec(`DROP TABLE reading_progress`)
		if err != nil {
			return err
		}

		_, err = tx.Exec(`ALTER TABLE reading_progress_new RENAME TO reading_progress`)
		if err != nil {
			return err
		}

		_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_reading_progress_user_updated ON reading_progress(user_id, updated_at DESC)`)
		if err != nil {
			return err
		}

		return tx.Commit()
	}
	return nil
}

func (s *Store) migrateFilenameKeys() error {
	if err := s.ensureComicsSchema(); err != nil {
		return err
	}
	if err := s.ensureFavoriteComicsSchema(); err != nil {
		return err
	}
	if err := s.ensureColumn("reading_progress", "comic_file_name_key", `ALTER TABLE reading_progress ADD COLUMN comic_file_name_key TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("favorite_comics", "comic_file_name_key", `ALTER TABLE favorite_comics ADD COLUMN comic_file_name_key TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_comics_file_name_key ON comics(file_name_key)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_cache_last_access ON cache_entries(last_access)`); err != nil {
		return err
	}
	if err := s.ensureUniqueIndex("reading_progress", "idx_reading_progress_user_file_name", []string{"user_id", "comic_file_name_key"}, `CREATE UNIQUE INDEX IF NOT EXISTS idx_reading_progress_user_file_name ON reading_progress(user_id, comic_file_name_key)`); err != nil {
		return err
	}
	if err := s.ensureUniqueIndex("favorite_comics", "idx_favorite_comics_user_file_name", []string{"user_id", "comic_file_name_key"}, `CREATE UNIQUE INDEX IF NOT EXISTS idx_favorite_comics_user_file_name ON favorite_comics(user_id, comic_file_name_key)`); err != nil {
		return err
	}
	if err := s.backfillComicFileNameKeys(); err != nil {
		return err
	}
	if err := s.backfillReadingProgressFileNameKeys(); err != nil {
		return err
	}
	if err := s.backfillFavoriteComicFileNameKeys(); err != nil {
		return err
	}
	if err := s.dedupeReadingProgressByFileName(); err != nil {
		return err
	}
	if err := s.dedupeFavoriteComicsByFileName(); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureComicsSchema() error {
	if err := s.ensureColumn("comics", "cover_key", `ALTER TABLE comics ADD COLUMN cover_key TEXT`); err != nil {
		return err
	}
	if err := s.ensureColumn("comics", "updated_at", `ALTER TABLE comics ADD COLUMN updated_at TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureComicFileNameKeyColumn(); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureFavoriteComicsSchema() error {
	hasID, err := s.columnExists("favorite_comics", "id")
	if err != nil {
		return err
	}
	if !hasID {
		return s.rebuildFavoriteComicsTable()
	}
	return nil
}

func (s *Store) ensureComicFileNameKeyColumn() error {
	exists, err := s.columnExists("comics", "file_name_key")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := s.db.Exec(`ALTER TABLE comics ADD COLUMN file_name_key TEXT NOT NULL DEFAULT ''`); err == nil {
		ok, checkErr := s.columnExists("comics", "file_name_key")
		if checkErr != nil {
			return checkErr
		}
		if ok {
			return nil
		}
	}
	if err := s.rebuildComicsTableWithFileNameKey(); err != nil {
		return err
	}
	ok, err := s.columnExists("comics", "file_name_key")
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("comics.file_name_key is still missing after rebuild")
	}
	return nil
}

func (s *Store) rebuildComicsTableWithFileNameKey() error {
	if _, err := s.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer func() {
		_, _ = s.db.Exec(`PRAGMA foreign_keys = ON`)
	}()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if _, err := tx.Exec(`
		CREATE TABLE comics_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			mount_id INTEGER NOT NULL,
			path TEXT NOT NULL,
			title TEXT NOT NULL,
			file_name_key TEXT NOT NULL DEFAULT '',
			mtime_unix INTEGER NOT NULL,
			size INTEGER NOT NULL,
			page_count INTEGER NOT NULL,
			cover_key TEXT,
			updated_at TEXT NOT NULL,
			FOREIGN KEY(mount_id) REFERENCES mounts(id) ON DELETE CASCADE,
			UNIQUE(mount_id, path)
		)
	`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO comics_new (id, mount_id, path, title, file_name_key, mtime_unix, size, page_count, cover_key, updated_at)
		SELECT id, mount_id, path, title, '', mtime_unix, size, page_count, cover_key, updated_at
		FROM comics
	`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE comics`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE comics_new RENAME TO comics`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_comics_mount ON comics(mount_id)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_comics_title ON comics(title)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_comics_file_name_key ON comics(file_name_key)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) rebuildFavoriteComicsTable() error {
	hasComicID, err := s.columnExists("favorite_comics", "comic_id")
	if err != nil {
		return err
	}
	hasFileNameKey, err := s.columnExists("favorite_comics", "comic_file_name_key")
	if err != nil {
		return err
	}

	if _, err := s.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer func() {
		_, _ = s.db.Exec(`PRAGMA foreign_keys = ON`)
	}()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if _, err := tx.Exec(`
		CREATE TABLE favorite_comics_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			comic_id INTEGER,
			comic_file_name_key TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY(comic_id) REFERENCES comics(id) ON DELETE SET NULL
		)
	`); err != nil {
		return err
	}
	comicIDExpr := `NULL`
	if hasComicID {
		comicIDExpr = `comic_id`
	}
	fileNameKeyExpr := `''`
	if hasFileNameKey {
		fileNameKeyExpr = `COALESCE(comic_file_name_key, '')`
	}
	insertSQL := fmt.Sprintf(`
		INSERT INTO favorite_comics_new (user_id, comic_id, comic_file_name_key, created_at)
		SELECT user_id, %s, %s, created_at
		FROM favorite_comics
	`, comicIDExpr, fileNameKeyExpr)
	if _, err := tx.Exec(insertSQL); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE favorite_comics`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE favorite_comics_new RENAME TO favorite_comics`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_favorite_comics_user_created ON favorite_comics(user_id, created_at DESC)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ensureColumn(tableName, columnName, alterStmt string) error {
	exists, err := s.columnExists(tableName, columnName)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := s.db.Exec(alterStmt); err != nil {
		return fmt.Errorf("ensure column %s.%s: %w", tableName, columnName, err)
	}
	return nil
}

func (s *Store) columnExists(tableName, columnName string) (bool, error) {
	tableName = strings.TrimSpace(tableName)
	columnName = strings.TrimSpace(columnName)
	if tableName == "" || columnName == "" {
		return false, errors.New("table and column names are required")
	}
	tableName = strings.ReplaceAll(tableName, "'", "''")
	row := s.db.QueryRow(fmt.Sprintf(`SELECT 1 FROM pragma_table_info('%s') WHERE name = ? LIMIT 1`, tableName), columnName)
	var found int
	switch err := row.Scan(&found); {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, err
	}
}

func (s *Store) ensureUniqueIndex(tableName, indexName string, columns []string, createStmt string) error {
	tableName = strings.TrimSpace(tableName)
	indexName = strings.TrimSpace(indexName)
	if tableName == "" || indexName == "" {
		return errors.New("table and index names are required")
	}
	if len(columns) == 0 {
		return errors.New("index columns are required")
	}

	found, unique, existingColumns, err := s.indexDetails(tableName, indexName)
	if err != nil {
		return err
	}
	if found && unique && sameIndexColumns(existingColumns, columns) {
		return nil
	}
	if found {
		if _, err := s.db.Exec(fmt.Sprintf(`DROP INDEX IF EXISTS %s`, quoteSQLiteIdentifier(indexName))); err != nil {
			return fmt.Errorf("drop index %s: %w", indexName, err)
		}
	}
	if _, err := s.db.Exec(createStmt); err != nil {
		return fmt.Errorf("create unique index %s: %w", indexName, err)
	}
	return nil
}

func (s *Store) indexDetails(tableName, indexName string) (bool, bool, []string, error) {
	tableName = strings.TrimSpace(tableName)
	indexName = strings.TrimSpace(indexName)
	if tableName == "" || indexName == "" {
		return false, false, nil, errors.New("table and index names are required")
	}

	tableName = strings.ReplaceAll(tableName, "'", "''")
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA index_list('%s')`, tableName))
	if err != nil {
		return false, false, nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			seq     int
			name    string
			unique  int
			origin  string
			partial int
		)
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return false, false, nil, err
		}
		if name != indexName {
			continue
		}
		columns, err := s.indexColumns(indexName)
		if err != nil {
			return false, false, nil, err
		}
		return true, unique == 1, columns, nil
	}
	if err := rows.Err(); err != nil {
		return false, false, nil, err
	}
	return false, false, nil, nil
}

func (s *Store) indexColumns(indexName string) ([]string, error) {
	indexName = strings.TrimSpace(indexName)
	if indexName == "" {
		return nil, errors.New("index name is required")
	}

	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA index_info(%s)`, quoteSQLiteString(indexName)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := make([]string, 0, 4)
	for rows.Next() {
		var (
			seqno int
			cid   int
			name  string
		)
		if err := rows.Scan(&seqno, &cid, &name); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

func sameIndexColumns(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func quoteSQLiteIdentifier(v string) string {
	return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
}

func quoteSQLiteString(v string) string {
	return `'` + strings.ReplaceAll(v, `'`, `''`) + `'`
}

func (s *Store) backfillComicFileNameKeys() error {
	rows, err := s.db.Query(`SELECT id, path FROM comics WHERE file_name_key = '' OR file_name_key IS NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			return err
		}
		if _, err := s.db.Exec(`UPDATE comics SET file_name_key = ? WHERE id = ?`, comicfmt.FileNameKey(p), id); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Store) backfillComicUpdatedAt() error {
	nowRaw := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`
		UPDATE comics
		SET updated_at = ?
		WHERE updated_at = '' OR updated_at IS NULL
	`, nowRaw)
	return err
}

func (s *Store) backfillReadingProgressFileNameKeys() error {
	_, err := s.db.Exec(`
		UPDATE reading_progress
		SET comic_file_name_key = COALESCE(
			(SELECT file_name_key FROM comics WHERE comics.id = reading_progress.comic_id),
			''
		)
		WHERE comic_file_name_key = ''
	`)
	return err
}

func (s *Store) backfillFavoriteComicFileNameKeys() error {
	_, err := s.db.Exec(`
		UPDATE favorite_comics
		SET comic_file_name_key = COALESCE(
			(SELECT file_name_key FROM comics WHERE comics.id = favorite_comics.comic_id),
			''
		)
		WHERE comic_file_name_key = ''
	`)
	return err
}

func (s *Store) dedupeReadingProgressByFileName() error {
	rows, err := s.db.Query(`
		SELECT id, user_id, comic_file_name_key, updated_at
		FROM reading_progress
		ORDER BY user_id ASC, comic_file_name_key ASC, updated_at DESC, id DESC
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	seen := make(map[string]int64, 256)
	for rows.Next() {
		var (
			id        int64
			userID    int64
			fileKey   string
			updatedAt string
		)
		if err := rows.Scan(&id, &userID, &fileKey, &updatedAt); err != nil {
			return err
		}
		key := fmt.Sprintf("%d:%s", userID, fileKey)
		if keepID, ok := seen[key]; ok {
			if _, err := s.db.Exec(`DELETE FROM reading_progress WHERE id = ?`, id); err != nil {
				return err
			}
			_ = keepID
			continue
		}
		seen[key] = id
	}
	return rows.Err()
}

func (s *Store) dedupeFavoriteComicsByFileName() error {
	rows, err := s.db.Query(`
		SELECT rowid, user_id, comic_file_name_key, created_at
		FROM favorite_comics
		ORDER BY user_id ASC, comic_file_name_key ASC, created_at DESC, rowid DESC
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	seen := make(map[string]int64, 256)
	for rows.Next() {
		var (
			id        int64
			userID    int64
			fileKey   string
			createdAt string
		)
		if err := rows.Scan(&id, &userID, &fileKey, &createdAt); err != nil {
			return err
		}
		key := fmt.Sprintf("%d:%s", userID, fileKey)
		if keepID, ok := seen[key]; ok {
			if _, err := s.db.Exec(`DELETE FROM favorite_comics WHERE rowid = ?`, id); err != nil {
				return err
			}
			_ = keepID
			continue
		}
		seen[key] = id
	}
	return rows.Err()
}

func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) CreateUser(username, passwordHash string, isAdmin bool) (User, error) {
	now := time.Now().UTC()
	res, err := s.db.Exec(`
		INSERT INTO users (username, password_hash, is_admin, created_at)
		VALUES (?, ?, ?, ?)
	`, strings.TrimSpace(username), passwordHash, boolToInt(isAdmin), now.Format(time.RFC3339Nano))
	if err != nil {
		return User{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, err
	}
	return s.GetUserByID(id)
}

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`
		SELECT id, username, password_hash, is_admin, created_at
		FROM users
		ORDER BY id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]User, 0)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetUserByUsername(username string) (User, error) {
	row := s.db.QueryRow(`
		SELECT id, username, password_hash, is_admin, created_at
		FROM users
		WHERE username = ?
	`, strings.TrimSpace(username))
	return scanUser(row)
}

func (s *Store) GetUserByID(id int64) (User, error) {
	row := s.db.QueryRow(`
		SELECT id, username, password_hash, is_admin, created_at
		FROM users
		WHERE id = ?
	`, id)
	return scanUser(row)
}

func (s *Store) UpdateUserPassword(id int64, passwordHash string) error {
	res, err := s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) GetReaderSettings(userID int64) (ReaderSettings, error) {
	row := s.db.QueryRow(`
		SELECT user_id, mode, fit, enhance_zip, enhance_pdf, spread_direction, mobile_compact, quality, spread, cover_offset, created_at, updated_at
		FROM user_reader_settings
		WHERE user_id = ?
	`, userID)
	return scanReaderSettings(row)
}

func (s *Store) UpsertReaderSettings(v ReaderSettings) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	createdAt := now
	if !v.CreatedAt.IsZero() {
		createdAt = v.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`
		INSERT INTO user_reader_settings (
			user_id, mode, fit, enhance_zip, enhance_pdf, spread_direction, mobile_compact, quality, spread, cover_offset, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			mode=excluded.mode,
			fit=excluded.fit,
			enhance_zip=excluded.enhance_zip,
			enhance_pdf=excluded.enhance_pdf,
			spread_direction=excluded.spread_direction,
			mobile_compact=excluded.mobile_compact,
 quality=excluded.quality,spread=excluded.spread,cover_offset=excluded.cover_offset,
			updated_at=excluded.updated_at
	`, v.UserID, strings.TrimSpace(v.Mode), strings.TrimSpace(v.Fit), strings.TrimSpace(v.EnhanceZip),
		strings.TrimSpace(v.EnhancePDF), strings.TrimSpace(v.SpreadDirection), boolToInt(v.MobileCompact), v.Quality, v.Spread, boolToInt(v.CoverOffset), createdAt, now)
	return err
}

func (s *Store) GetRuntimeSettings() (RuntimeSettings, error) {
	row := s.db.QueryRow(`
		SELECT id, memory_limit_mb, disk_cache_mb, page_prefetch_pages, frontend_page_prefetch_pages, page_load_parallelism, webp_quality, webp_method, webp_concurrency, webp_output_root, created_at, updated_at
		FROM app_runtime_settings
		WHERE id = 1
	`)
	return scanRuntimeSettings(row)
}

func (s *Store) UpsertRuntimeSettings(v RuntimeSettings) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	createdAt := now
	if !v.CreatedAt.IsZero() {
		createdAt = v.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`
		INSERT INTO app_runtime_settings (id, memory_limit_mb, disk_cache_mb, page_prefetch_pages, frontend_page_prefetch_pages, page_load_parallelism, webp_quality, webp_method, webp_concurrency, webp_output_root, gc_percent, created_at, updated_at)
		VALUES (1, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			memory_limit_mb=excluded.memory_limit_mb,
			disk_cache_mb=excluded.disk_cache_mb,
			page_prefetch_pages=excluded.page_prefetch_pages,
			frontend_page_prefetch_pages=excluded.frontend_page_prefetch_pages,
			page_load_parallelism=excluded.page_load_parallelism,
			webp_quality=excluded.webp_quality,
			webp_method=excluded.webp_method,
			webp_concurrency=excluded.webp_concurrency,
			webp_output_root=excluded.webp_output_root,
			gc_percent=excluded.gc_percent,
			updated_at=excluded.updated_at
	`, v.MemoryLimitMB, v.DiskCacheMB, v.PagePrefetchPages, v.PageLoadParallelism, v.WebPQuality, v.WebPMethod, v.WebPConcurrency, strings.TrimSpace(v.WebPOutputRoot), legacyRuntimeGCPercent, createdAt, now)
	return err
}

func (s *Store) CreateSession(token string, userID int64, expiresAt time.Time) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT INTO sessions (token, user_id, expires_at, created_at)
		VALUES (?, ?, ?, ?)
	`, token, userID, expiresAt.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	return err
}

func (s *Store) GetSessionWithUser(token string) (SessionUser, error) {
	row := s.db.QueryRow(`
		SELECT
			s.id, s.token, s.user_id, s.expires_at, s.created_at,
			u.id, u.username, u.password_hash, u.is_admin, u.created_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token = ?
	`, token)

	var out SessionUser
	var expRaw, createdRaw, userCreatedRaw string
	var isAdmin int
	if err := row.Scan(
		&out.Session.ID,
		&out.Session.Token,
		&out.Session.UserID,
		&expRaw,
		&createdRaw,
		&out.User.ID,
		&out.User.Username,
		&out.User.PasswordHash,
		&isAdmin,
		&userCreatedRaw,
	); err != nil {
		return SessionUser{}, err
	}

	out.User.IsAdmin = intToBool(isAdmin)
	var err error
	out.Session.ExpiresAt, err = time.Parse(time.RFC3339Nano, expRaw)
	if err != nil {
		return SessionUser{}, err
	}
	out.Session.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return SessionUser{}, err
	}
	out.User.CreatedAt, err = time.Parse(time.RFC3339Nano, userCreatedRaw)
	if err != nil {
		return SessionUser{}, err
	}

	return out, nil
}

func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

func (s *Store) DeleteExpiredSessions(now time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) UpdateSessionExpires(token string, expiresAt time.Time) error {
	_, err := s.db.Exec(`UPDATE sessions SET expires_at = ? WHERE token = ?`, expiresAt.UTC().Format(time.RFC3339Nano), token)
	return err
}

func (s *Store) CreateMount(m Mount) (Mount, error) {
	now := time.Now().UTC()
	encodedRootPath := encodeMountRootPaths(m.RootPath, m.RootPaths)
	res, err := s.db.Exec(`
		INSERT INTO mounts (
			name, type, root_path,
			smb_host, smb_port, smb_share, smb_user, smb_password, smb_domain,
			webdav_url, webdav_user, webdav_password,
			enabled, created_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, m.Name, string(m.Type), encodedRootPath,
		m.SMBHost, m.SMBPort, m.SMBShare, m.SMBUser, m.SMBPassword, m.SMBDomain,
		m.WebDAVURL, m.WebDAVUser, m.WebDAVPassword,
		boolToInt(m.Enabled), now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Mount{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Mount{}, err
	}
	return s.GetMount(id)
}

func (s *Store) UpdateMount(m Mount) error {
	encodedRootPath := encodeMountRootPaths(m.RootPath, m.RootPaths)
	res, err := s.db.Exec(`
		UPDATE mounts
		SET
			name = ?, type = ?, root_path = ?,
			smb_host = ?, smb_port = ?, smb_share = ?, smb_user = ?, smb_password = ?, smb_domain = ?,
			webdav_url = ?, webdav_user = ?, webdav_password = ?,
			enabled = ?
		WHERE id = ?
	`, m.Name, string(m.Type), encodedRootPath,
		m.SMBHost, m.SMBPort, m.SMBShare, m.SMBUser, m.SMBPassword, m.SMBDomain,
		m.WebDAVURL, m.WebDAVUser, m.WebDAVPassword,
		boolToInt(m.Enabled), m.ID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteMount(id int64) error {
	_, err := s.db.Exec(`DELETE FROM mounts WHERE id = ?`, id)
	return err
}

func (s *Store) ListMounts() ([]Mount, error) {
	rows, err := s.db.Query(`
		SELECT
			id, name, type, root_path,
			smb_host, smb_port, smb_share, smb_user, smb_password, smb_domain,
			webdav_url, webdav_user, webdav_password,
			enabled, created_at
		FROM mounts
		ORDER BY id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Mount, 0)
	for rows.Next() {
		m, err := scanMount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ListEnabledMounts() ([]Mount, error) {
	rows, err := s.db.Query(`
		SELECT
			id, name, type, root_path,
			smb_host, smb_port, smb_share, smb_user, smb_password, smb_domain,
			webdav_url, webdav_user, webdav_password,
			enabled, created_at
		FROM mounts
		WHERE enabled = 1
		ORDER BY id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Mount, 0)
	for rows.Next() {
		m, err := scanMount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetMount(id int64) (Mount, error) {
	row := s.db.QueryRow(`
		SELECT
			id, name, type, root_path,
			smb_host, smb_port, smb_share, smb_user, smb_password, smb_domain,
			webdav_url, webdav_user, webdav_password,
			enabled, created_at
		FROM mounts
		WHERE id = ?
	`, id)
	return scanMount(row)
}

func (s *Store) CreateRootDirectory(name, pathValue string) (RootDirectory, error) {
	now := time.Now().UTC()
	res, err := s.db.Exec(`
		INSERT INTO root_directories (name, path, created_at)
		VALUES (?, ?, ?)
	`, strings.TrimSpace(name), strings.TrimSpace(pathValue), now.Format(time.RFC3339Nano))
	if err != nil {
		return RootDirectory{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return RootDirectory{}, err
	}
	return s.GetRootDirectory(id)
}

func (s *Store) ListRootDirectories() ([]RootDirectory, error) {
	rows, err := s.db.Query(`
		SELECT id, name, path, created_at
		FROM root_directories
		ORDER BY id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]RootDirectory, 0, 32)
	for rows.Next() {
		item, err := scanRootDirectory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetRootDirectory(id int64) (RootDirectory, error) {
	row := s.db.QueryRow(`
		SELECT id, name, path, created_at
		FROM root_directories
		WHERE id = ?
	`, id)
	return scanRootDirectory(row)
}

func (s *Store) DeleteRootDirectory(id int64) error {
	res, err := s.db.Exec(`DELETE FROM root_directories WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) GetWebPArchiveManifest(sourcePath, outputZipPath string, quality, method int64) (WebPArchiveManifest, error) {
	row := s.db.QueryRow(`
		SELECT
			id, source_path, output_zip_path, quality, method,
			source_mtime_unix, source_size, source_entry_count,
			converted_entries, kept_entries, output_size,
			completed_at, updated_at
		FROM webp_archive_manifests
		WHERE source_path = ? AND output_zip_path = ? AND quality = ? AND method = ?
	`, strings.TrimSpace(sourcePath), strings.TrimSpace(outputZipPath), quality, method)
	return scanWebPArchiveManifest(row)
}

func (s *Store) UpsertWebPArchiveManifest(v WebPArchiveManifest) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	completedAt := now
	if !v.CompletedAt.IsZero() {
		completedAt = v.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`
		INSERT INTO webp_archive_manifests (
			source_path, output_zip_path, quality, method,
			source_mtime_unix, source_size, source_entry_count,
			converted_entries, kept_entries, output_size,
			completed_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_path, output_zip_path, quality, method) DO UPDATE SET
			source_mtime_unix=excluded.source_mtime_unix,
			source_size=excluded.source_size,
			source_entry_count=excluded.source_entry_count,
			converted_entries=excluded.converted_entries,
			kept_entries=excluded.kept_entries,
			output_size=excluded.output_size,
			completed_at=excluded.completed_at,
			updated_at=excluded.updated_at
	`,
		strings.TrimSpace(v.SourcePath), strings.TrimSpace(v.OutputZipPath), v.Quality, v.Method,
		v.SourceMtimeUnix, v.SourceSize, v.SourceEntryCount,
		v.ConvertedEntries, v.KeptEntries, v.OutputSize,
		completedAt, now,
	)
	return err
}

func (s *Store) ReplaceComicsForMount(mountID int64, comics []Comic) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().UTC().Format(time.RFC3339Nano)

	existingMap := make(map[string]int64)
	rows, err := tx.Query(`SELECT id, path FROM comics WHERE mount_id = ?`, mountID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var path string
		if err = rows.Scan(&id, &path); err != nil {
			_ = rows.Close()
			return err
		}
		existingMap[path] = id
	}
	_ = rows.Close()

	seenPaths := make(map[string]bool, len(comics))
	for _, c := range comics {
		seenPaths[c.Path] = true
		if c.FileNameKey == "" {
			c.FileNameKey = comicfmt.FileNameKey(c.Path)
		}

		if id, exists := existingMap[c.Path]; exists {
			_, err = tx.Exec(`
				UPDATE comics
				SET title = ?, file_name_key = ?, mtime_unix = ?, size = ?, page_count = ?, cover_key = ?, updated_at = ?
				WHERE id = ?
			`, c.Title, c.FileNameKey, c.MtimeUnix, c.Size, c.PageCount, c.CoverKey, now, id)
			if err != nil {
				return err
			}
		} else {
			_, err = tx.Exec(`
				INSERT INTO comics (
					mount_id, path, title, file_name_key, mtime_unix, size, page_count, cover_key, updated_at
				)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, mountID, c.Path, c.Title, c.FileNameKey, c.MtimeUnix, c.Size, c.PageCount, c.CoverKey, now)
			if err != nil {
				return err
			}
		}
	}

	for path, id := range existingMap {
		if !seenPaths[path] {
			_, err = tx.Exec(`DELETE FROM comics WHERE id = ?`, id)
			if err != nil {
				return err
			}
		}
	}

	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *Store) UpsertComicForMount(mountID int64, c Comic) error {
	if mountID <= 0 {
		return errors.New("invalid mount id")
	}
	if strings.TrimSpace(c.Path) == "" {
		return errors.New("comic path is required")
	}
	if c.FileNameKey == "" {
		c.FileNameKey = comicfmt.FileNameKey(c.Path)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.Exec(`
		UPDATE comics
		SET title = ?, file_name_key = ?, mtime_unix = ?, size = ?, page_count = ?, cover_key = ?, updated_at = ?
		WHERE mount_id = ? AND path = ?
	`, c.Title, c.FileNameKey, c.MtimeUnix, c.Size, c.PageCount, c.CoverKey, now, mountID, c.Path)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	_, err = s.db.Exec(`
		INSERT INTO comics (
			mount_id, path, title, file_name_key, mtime_unix, size, page_count, cover_key, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, mountID, c.Path, c.Title, c.FileNameKey, c.MtimeUnix, c.Size, c.PageCount, c.CoverKey, now)
	return err
}

func (s *Store) DeleteMissingComicsForMount(mountID int64, seenPaths []string) error {
	if mountID <= 0 {
		return errors.New("invalid mount id")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if len(seenPaths) == 0 {
		if _, err := tx.Exec(`DELETE FROM comics WHERE mount_id = ?`, mountID); err != nil {
			return err
		}
		return tx.Commit()
	}

	placeholders := make([]string, 0, len(seenPaths))
	args := make([]any, 0, len(seenPaths)+1)
	args = append(args, mountID)
	for _, p := range seenPaths {
		placeholders = append(placeholders, "?")
		args = append(args, p)
	}
	stmt := fmt.Sprintf(`DELETE FROM comics WHERE mount_id = ? AND path NOT IN (%s)`, strings.Join(placeholders, ","))
	if _, err := tx.Exec(stmt, args...); err != nil {
		return err
	}
	return tx.Commit()
}

func dedupedComicBaseQuery() string {
	return `
		WITH ranked_comics AS (
			SELECT
				c.id,
				c.mount_id,
				c.path,
				c.title,
				c.file_name_key,
				c.mtime_unix,
				c.size,
				c.page_count,
				c.cover_key,
				c.updated_at,
				ROW_NUMBER() OVER (
					PARTITION BY c.file_name_key
					ORDER BY c.size DESC, c.updated_at DESC, c.id DESC
				) AS file_rank
			FROM comics c
			WHERE c.file_name_key <> ''
			  AND (LOWER(c.path) LIKE '%.zip' OR LOWER(c.path) LIKE '%.cbz')
		)
	`
}

func comicTopFolderExpr(pathExpr string) string {
	return fmt.Sprintf("CASE WHEN instr(%s, '/') > 0 THEN substr(%s, 1, instr(%s, '/') - 1) ELSE '' END", pathExpr, pathExpr, pathExpr)
}

func normalizeFavoriteFilter(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "all"
	case "all", "favorite", "not_favorite":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "all"
	}
}

func (s *Store) buildComicListFilter(userID int64, query string, mountID int64, statusFilter, formatFilter, favoriteFilter, folderFilter string) (string, []any, error) {
	if userID <= 0 {
		return "", nil, errors.New("invalid user id")
	}
	status := strings.ToLower(strings.TrimSpace(statusFilter))
	switch status {
	case "", "all", "unread", "reading", "finished":
	default:
		status = "all"
	}
	format := strings.ToLower(strings.TrimSpace(formatFilter))
	switch format {
	case "", "all", "zip", "pdf", "archive":
	default:
		format = "all"
	}
	favorite := normalizeFavoriteFilter(favoriteFilter)
	folder := strings.TrimSpace(folderFilter)

	progressTotalExpr := "CASE WHEN rp.total_pages > 0 THEN rp.total_pages WHEN c.page_count > 0 THEN c.page_count ELSE 0 END"
	finishedExpr := fmt.Sprintf("(rp.id IS NOT NULL AND %s > 0 AND (rp.last_page + 1) >= %s)", progressTotalExpr, progressTotalExpr)
	readingExpr := fmt.Sprintf("(rp.id IS NOT NULL AND NOT %s)", finishedExpr)
	topFolderExpr := comicTopFolderExpr("c.path")

	clauses := []string{"c.file_rank = 1"}
	args := make([]any, 0, 8)
	args = append(args, userID)
	args = append(args, userID)
	if mountID > 0 {
		clauses = append(clauses, "c.mount_id = ?")
		args = append(args, mountID)
	}
	if q := strings.TrimSpace(query); q != "" {
		clauses = append(clauses, "c.title LIKE ?")
		args = append(args, "%"+q+"%")
	}
	if folder != "" {
		clauses = append(clauses, topFolderExpr+" = ?")
		args = append(args, folder)
	}
	switch status {
	case "unread":
		clauses = append(clauses, "rp.id IS NULL")
	case "reading":
		clauses = append(clauses, readingExpr)
	case "finished":
		clauses = append(clauses, finishedExpr)
	}
	switch format {
	case "zip":
		clauses = append(clauses, "(LOWER(c.path) LIKE '%.zip' OR LOWER(c.path) LIKE '%.cbz')")
	case "pdf":
		clauses = append(clauses, "LOWER(c.path) LIKE '%.pdf'")
	case "archive":
		clauses = append(clauses, "(LOWER(c.path) LIKE '%.zip' OR LOWER(c.path) LIKE '%.cbz')")
	}
	switch favorite {
	case "favorite":
		clauses = append(clauses, "fc.user_id IS NOT NULL")
	case "not_favorite":
		clauses = append(clauses, "fc.user_id IS NULL")
	}
	return strings.Join(clauses, " AND "), args, nil
}

func (s *Store) CountComicsForUser(userID int64, query string, mountID int64, statusFilter, formatFilter, favoriteFilter, folderFilter string) (int, error) {
	where, args, err := s.buildComicListFilter(userID, query, mountID, statusFilter, formatFilter, favoriteFilter, folderFilter)
	if err != nil {
		return 0, err
	}
	stmt := fmt.Sprintf(`
		%s
		SELECT COUNT(1)
		FROM ranked_comics c
	LEFT JOIN reading_progress rp ON rp.comic_file_name_key = c.file_name_key AND rp.user_id = ?
	LEFT JOIN favorite_comics fc ON fc.comic_file_name_key = c.file_name_key AND fc.user_id = ?
		WHERE %s
	`, dedupedComicBaseQuery(), where)
	var total int
	if err := s.db.QueryRow(stmt, args...).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func (s *Store) ListComicTopFoldersForUser(userID, mountID int64, favoriteFilter string) ([]string, error) {
	if userID <= 0 {
		return nil, errors.New("invalid user id")
	}
	if mountID <= 0 {
		return []string{}, nil
	}
	favorite := normalizeFavoriteFilter(favoriteFilter)
	args := []any{userID, mountID}
	where := []string{
		"c.file_rank = 1",
		"c.mount_id = ?",
		comicTopFolderExpr("c.path") + " <> ''",
	}
	switch favorite {
	case "favorite":
		where = append(where, "fc.user_id IS NOT NULL")
	case "not_favorite":
		where = append(where, "fc.user_id IS NULL")
	}
	stmt := fmt.Sprintf(`
		%s
		SELECT DISTINCT %s AS top_folder
		FROM ranked_comics c
		LEFT JOIN favorite_comics fc ON fc.comic_file_name_key = c.file_name_key AND fc.user_id = ?
		WHERE %s
		ORDER BY LOWER(top_folder), top_folder
	`, dedupedComicBaseQuery(), comicTopFolderExpr("c.path"), strings.Join(where, " AND "))
	rows, err := s.db.Query(stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]string, 0)
	for rows.Next() {
		var folder string
		if err := rows.Scan(&folder); err != nil {
			return nil, err
		}
		if strings.TrimSpace(folder) == "" {
			continue
		}
		out = append(out, folder)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ListComicsForUser(userID int64, query string, mountID int64, statusFilter, formatFilter, favoriteFilter, folderFilter, sortBy, sortOrder string, limit, offset int) ([]ComicWithProgress, error) {
	if limit <= 0 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	where, args, err := s.buildComicListFilter(userID, query, mountID, statusFilter, formatFilter, favoriteFilter, folderFilter)
	if err != nil {
		return nil, err
	}

	sortCol := "c.updated_at"
	switch strings.ToLower(strings.TrimSpace(sortBy)) {
	case "title":
		sortCol = "LOWER(c.title)"
	case "size":
		sortCol = "c.size"
	case "page_count":
		sortCol = "c.page_count"
	case "mtime_unix":
		sortCol = "c.mtime_unix"
	case "last_read":
		sortCol = "COALESCE(rp.updated_at, c.updated_at)"
	case "updated_at", "":
		sortCol = "c.updated_at"
	default:
		sortCol = "c.updated_at"
	}

	order := strings.ToLower(strings.TrimSpace(sortOrder))
	if order != "asc" && order != "desc" {
		order = "desc"
	}

	progressTotalExpr := "CASE WHEN rp.total_pages > 0 THEN rp.total_pages WHEN c.page_count > 0 THEN c.page_count ELSE 0 END"
	finishedExpr := fmt.Sprintf("(rp.id IS NOT NULL AND %s > 0 AND (rp.last_page + 1) >= %s)", progressTotalExpr, progressTotalExpr)
	args = append(args, limit, offset)

	stmt := fmt.Sprintf(`
		%s
		SELECT
			c.id, c.mount_id, c.path, c.title, c.file_name_key, c.mtime_unix, c.size, c.page_count, c.cover_key, c.updated_at,
			COALESCE(rp.id, 0), COALESCE(rp.last_page, 0), COALESCE(rp.total_pages, 0), COALESCE(rp.updated_at, ""),
			CASE WHEN %s THEN 1 ELSE 0 END,
			CASE WHEN fc.user_id IS NULL THEN 0 ELSE 1 END
			FROM ranked_comics c
			LEFT JOIN reading_progress rp ON rp.comic_file_name_key = c.file_name_key AND rp.user_id = ?
			LEFT JOIN favorite_comics fc ON fc.comic_file_name_key = c.file_name_key AND fc.user_id = ?
		WHERE %s
		ORDER BY %s %s, c.id DESC
		LIMIT ? OFFSET ?
	`, dedupedComicBaseQuery(), finishedExpr, where, sortCol, order)

	rows, err := s.db.Query(stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ComicWithProgress, 0)
	for rows.Next() {
		var (
			item         ComicWithProgress
			updatedRaw   string
			progressID   int64
			lastPage     int
			totalPages   int
			lastReadRaw  string
			finishedBool int
			favoriteBool int
			fileNameKey  string
		)
		if err := rows.Scan(
			&item.ID,
			&item.MountID,
			&item.Path,
			&item.Title,
			&fileNameKey,
			&item.MtimeUnix,
			&item.Size,
			&item.PageCount,
			&item.CoverKey,
			&updatedRaw,
			&progressID,
			&lastPage,
			&totalPages,
			&lastReadRaw,
			&finishedBool,
			&favoriteBool,
		); err != nil {
			return nil, err
		}
		item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
		if err != nil {
			return nil, err
		}
		item.FileNameKey = fileNameKey
		item.HasProgress = progressID > 0
		item.LastPage = lastPage
		item.ProgressTotalPages = totalPages
		item.Finished = finishedBool == 1
		item.IsFavorite = favoriteBool == 1
		if item.HasProgress && lastReadRaw != "" {
			if t, e := time.Parse(time.RFC3339Nano, lastReadRaw); e == nil {
				item.LastReadAt = t
			}
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetComic(id int64) (Comic, error) {
	row := s.db.QueryRow(`
		SELECT id, mount_id, path, title, file_name_key, mtime_unix, size, page_count, cover_key, updated_at
		FROM comics
		WHERE id = ?
	`, id)
	return scanComic(row)
}

func (s *Store) ListComicsByMount(mountID int64) ([]Comic, error) {
	rows, err := s.db.Query(`
		SELECT id, mount_id, path, title, file_name_key, mtime_unix, size, page_count, cover_key, updated_at
		FROM comics
		WHERE mount_id = ?
	`, mountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Comic, 0, 1024)
	for rows.Next() {
		c, err := scanComic(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) UpdateComicCoverKey(id int64, coverKey string) error {
	res, err := s.db.Exec(`UPDATE comics SET cover_key = ?, updated_at = ? WHERE id = ?`, coverKey, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpdateComicPageCount(id int64, pageCount int) error {
	res, err := s.db.Exec(`UPDATE comics SET page_count = ?, updated_at = ? WHERE id = ?`, pageCount, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) resolveComicFileNameKey(comicID int64) (string, error) {
	row := s.db.QueryRow(`
		SELECT file_name_key, path
		FROM comics
		WHERE id = ?
	`, comicID)
	var fileNameKey sql.NullString
	var pathRaw sql.NullString
	if err := row.Scan(&fileNameKey, &pathRaw); err != nil {
		return "", err
	}
	if fileNameKey.Valid && fileNameKey.String != "" {
		return fileNameKey.String, nil
	}
	if pathRaw.Valid {
		return comicfmt.FileNameKey(pathRaw.String), nil
	}
	return "", nil
}

func (s *Store) UpsertReadingProgress(userID, comicID int64, fileNameKey string, lastPage, totalPages int) (ReadingProgress, error) {
	now := time.Now().UTC()
	if lastPage < 0 {
		lastPage = 0
	}
	if totalPages < 0 {
		totalPages = 0
	}
	fileNameKey = normalizeFileNameKey(fileNameKey)
	if fileNameKey == "" && comicID > 0 {
		key, err := s.resolveComicFileNameKey(comicID)
		if err != nil {
			return ReadingProgress{}, err
		}
		fileNameKey = key
	}
	if fileNameKey == "" {
		return ReadingProgress{}, errors.New("comic file name key is required")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return ReadingProgress{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	nowRaw := now.Format(time.RFC3339Nano)
	_, err = tx.Exec(`
		INSERT INTO reading_progress (user_id, comic_id, comic_file_name_key, last_page, total_pages, last_read_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, comic_file_name_key) DO UPDATE SET
			comic_id = excluded.comic_id,
			last_page = excluded.last_page,
			total_pages = excluded.total_pages,
			last_read_at = excluded.last_read_at,
			updated_at = excluded.updated_at
	`, userID, comicID, fileNameKey, lastPage, totalPages, nowRaw, nowRaw)
	if err != nil {
		return ReadingProgress{}, err
	}
	if err = tx.Commit(); err != nil {
		return ReadingProgress{}, err
	}
	return s.GetReadingProgressByFileName(userID, fileNameKey)
}

func (s *Store) SetComicFavorite(userID, comicID int64, fileNameKey string, favorite bool) error {
	if userID <= 0 || comicID <= 0 {
		return errors.New("invalid user id or comic id")
	}
	fileNameKey = normalizeFileNameKey(fileNameKey)
	if fileNameKey == "" {
		key, err := s.resolveComicFileNameKey(comicID)
		if err != nil {
			return err
		}
		fileNameKey = key
	}
	if fileNameKey == "" {
		return errors.New("comic file name key is required")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if favorite {
		var existingID int64
		row := tx.QueryRow(`SELECT rowid FROM favorite_comics WHERE user_id = ? AND comic_file_name_key = ? LIMIT 1`, userID, fileNameKey)
		if scanErr := row.Scan(&existingID); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
			return scanErr
		}
		nowRaw := time.Now().UTC().Format(time.RFC3339Nano)
		if existingID > 0 {
			_, err = tx.Exec(`
				UPDATE favorite_comics
				SET comic_id = ?, created_at = ?
				WHERE rowid = ?
			`, comicID, nowRaw, existingID)
		} else {
			_, err = tx.Exec(`
				INSERT INTO favorite_comics (user_id, comic_id, comic_file_name_key, created_at)
				VALUES (?, ?, ?, ?)
			`, userID, comicID, fileNameKey, nowRaw)
		}
	} else {
		_, err = tx.Exec(`DELETE FROM favorite_comics WHERE user_id = ? AND comic_file_name_key = ?`, userID, fileNameKey)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) IsComicFavorite(userID, comicID int64) (bool, error) {
	if userID <= 0 || comicID <= 0 {
		return false, errors.New("invalid user id or comic id")
	}
	fileNameKey, err := s.resolveComicFileNameKey(comicID)
	if err != nil {
		return false, err
	}
	row := s.db.QueryRow(`
		SELECT 1
		FROM favorite_comics
		WHERE user_id = ? AND comic_file_name_key = ?
		LIMIT 1
	`, userID, fileNameKey)
	var found int
	if err := row.Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return found == 1, nil
}

func (s *Store) GetReadingProgress(userID, comicID int64) (ReadingProgress, error) {
	return s.GetReadingProgressForComic(userID, comicID, "")
}

func (s *Store) GetReadingProgressForComic(userID, comicID int64, fileNameKey string) (ReadingProgress, error) {
	row := s.db.QueryRow(`
		SELECT id, user_id, comic_id, comic_file_name_key, last_page, total_pages, last_read_at, updated_at
		FROM reading_progress
		WHERE user_id = ? AND comic_id = ?
	`, userID, comicID)
	p, err := scanReadingProgress(row)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ReadingProgress{}, err
	}
	fileNameKey = normalizeFileNameKey(fileNameKey)
	if fileNameKey == "" && comicID > 0 {
		key, keyErr := s.resolveComicFileNameKey(comicID)
		if keyErr != nil {
			if errors.Is(keyErr, sql.ErrNoRows) {
				return ReadingProgress{}, sql.ErrNoRows
			}
			return ReadingProgress{}, keyErr
		}
		fileNameKey = key
	}
	if fileNameKey == "" {
		return ReadingProgress{}, sql.ErrNoRows
	}
	return s.GetReadingProgressByFileName(userID, fileNameKey)
}

func (s *Store) GetReadingProgressByFileName(userID int64, fileNameKey string) (ReadingProgress, error) {
	fileNameKey = normalizeFileNameKey(fileNameKey)
	row := s.db.QueryRow(`
		SELECT id, user_id, comic_id, comic_file_name_key, last_page, total_pages, last_read_at, updated_at
		FROM reading_progress
		WHERE user_id = ? AND comic_file_name_key = ?
		ORDER BY updated_at DESC, id DESC
		LIMIT 1
	`, userID, fileNameKey)
	return scanReadingProgress(row)
}

func (s *Store) ListReadingHistory(userID int64, limit, offset int) ([]ReadingHistoryItem, error) {
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.Query(`
		SELECT
			rp.id, rp.user_id, rp.comic_id, rp.comic_file_name_key, rp.last_page, rp.total_pages, rp.last_read_at, rp.updated_at,
			c.id, c.mount_id, c.path, c.title, c.file_name_key, c.mtime_unix, c.size, c.page_count, c.cover_key, c.updated_at,
			CASE WHEN fc.user_id IS NULL THEN 0 ELSE 1 END
		FROM reading_progress rp
		JOIN comics c ON c.id = (
			SELECT id
			FROM comics
			WHERE file_name_key = rp.comic_file_name_key
			ORDER BY updated_at DESC, id DESC
			LIMIT 1
		)
		LEFT JOIN favorite_comics fc ON fc.comic_file_name_key = rp.comic_file_name_key AND fc.user_id = ?
		WHERE rp.user_id = ?
		  AND (LOWER(c.path) LIKE '%.zip' OR LOWER(c.path) LIKE '%.cbz')
		ORDER BY rp.updated_at DESC, rp.id DESC
		LIMIT ? OFFSET ?
	`, userID, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ReadingHistoryItem, 0)
	for rows.Next() {
		var item ReadingHistoryItem
		var pLastRead, pUpdated, cUpdated sql.NullString
		var comicID sql.NullInt64
		var joinedComicID sql.NullInt64
		var cMountID sql.NullInt64
		var cPath sql.NullString
		var cTitle sql.NullString
		var cFileNameKey sql.NullString
		var cMtimeUnix sql.NullInt64
		var cSize sql.NullInt64
		var cPageCount sql.NullInt64
		var cCoverKey sql.NullString
		var favoriteBool int
		if err := rows.Scan(
			&item.Progress.ID,
			&item.Progress.UserID,
			&comicID,
			&item.Progress.ComicFileNameKey,
			&item.Progress.LastPage,
			&item.Progress.TotalPages,
			&pLastRead,
			&pUpdated,
			&joinedComicID,
			&cMountID,
			&cPath,
			&cTitle,
			&cFileNameKey,
			&cMtimeUnix,
			&cSize,
			&cPageCount,
			&cCoverKey,
			&cUpdated,
			&favoriteBool,
		); err != nil {
			return nil, err
		}
		if comicID.Valid {
			cid := comicID.Int64
			item.Progress.ComicID = &cid
		}
		if joinedComicID.Valid {
			item.Comic.ID = joinedComicID.Int64
		}
		if pLastRead.Valid {
			item.Progress.LastReadAt, _ = time.Parse(time.RFC3339Nano, pLastRead.String)
		}
		if pUpdated.Valid {
			item.Progress.UpdatedAt, _ = time.Parse(time.RFC3339Nano, pUpdated.String)
		}
		if cMountID.Valid {
			item.Comic.MountID = cMountID.Int64
		}
		if cPath.Valid {
			item.Comic.Path = cPath.String
		}
		if cTitle.Valid {
			item.Comic.Title = cTitle.String
		}
		if cFileNameKey.Valid {
			item.Comic.FileNameKey = cFileNameKey.String
		}
		if cMtimeUnix.Valid {
			item.Comic.MtimeUnix = cMtimeUnix.Int64
		}
		if cSize.Valid {
			item.Comic.Size = cSize.Int64
		}
		if cPageCount.Valid {
			item.Comic.PageCount = int(cPageCount.Int64)
		}
		if cCoverKey.Valid {
			item.Comic.CoverKey = cCoverKey.String
		}
		if cUpdated.Valid {
			item.Comic.UpdatedAt, _ = time.Parse(time.RFC3339Nano, cUpdated.String)
		}
		item.IsFavorite = favoriteBool == 1
		if item.Comic.ID > 0 {
			out = append(out, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) CleanupOldHistory(userID int64, keepCount int) error {
	if keepCount <= 0 {
		keepCount = 100
	}
	_, err := s.db.Exec(`
		DELETE FROM reading_progress
		WHERE user_id = ? AND id NOT IN (
			SELECT id FROM reading_progress
			WHERE user_id = ?
			ORDER BY updated_at DESC, id DESC
			LIMIT ?
		)
	`, userID, userID, keepCount)
	return err
}

func normalizeFileNameKey(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func (s *Store) GetCacheEntry(key string) (CacheEntry, error) {
	row := s.db.QueryRow(`
		SELECT id, key, rel_path, size, last_access, created_at
		FROM cache_entries
		WHERE key = ?
	`, key)
	return scanCacheEntry(row)
}

func (s *Store) UpsertCacheEntry(e CacheEntry) error {
	_, err := s.db.Exec(`
		INSERT INTO cache_entries (key, rel_path, size, last_access, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			rel_path = excluded.rel_path,
			size = excluded.size,
			last_access = excluded.last_access
	`, e.Key, e.RelPath, e.Size, e.LastAccess, e.CreatedAt)
	return err
}

func (s *Store) TouchCacheEntry(key string, nowUnix int64) error {
	_, err := s.db.Exec(`UPDATE cache_entries SET last_access = ? WHERE key = ?`, nowUnix, key)
	return err
}

func (s *Store) TotalCacheEntries() (int64, error) {
	var total sql.NullInt64
	err := s.db.QueryRow(`SELECT COUNT(1) FROM cache_entries`).Scan(&total)
	if err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

func (s *Store) TotalCacheEntriesByRelPathPrefix(prefix string) (int64, error) {
	var total sql.NullInt64
	err := s.db.QueryRow(`SELECT COUNT(1) FROM cache_entries WHERE rel_path LIKE ?`, cacheRelPathPrefixLike(prefix)).Scan(&total)
	if err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

func (s *Store) TotalCacheSize() (int64, error) {
	var total sql.NullInt64
	err := s.db.QueryRow(`SELECT COALESCE(SUM(size), 0) FROM cache_entries`).Scan(&total)
	if err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

func (s *Store) TotalCacheSizeWithPrefix(prefix string) (int64, error) {
	var total sql.NullInt64
	err := s.db.QueryRow(`SELECT COALESCE(SUM(size), 0) FROM cache_entries WHERE key LIKE ?`, cacheKeyPrefixLike(prefix)).Scan(&total)
	if err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

func (s *Store) ListOldestCacheEntries(limit int) ([]CacheEntry, error) {
	if limit <= 0 {
		limit = 32
	}
	rows, err := s.db.Query(`
		SELECT id, key, rel_path, size, last_access, created_at
		FROM cache_entries
		ORDER BY last_access ASC, created_at ASC, id ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]CacheEntry, 0)
	for rows.Next() {
		e, err := scanCacheEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ListOldestCacheEntriesWithPrefix(prefix string, limit int) ([]CacheEntry, error) {
	if limit <= 0 {
		limit = 32
	}
	rows, err := s.db.Query(`
		SELECT id, key, rel_path, size, last_access, created_at
		FROM cache_entries
		WHERE key LIKE ?
		ORDER BY last_access ASC, created_at ASC, id ASC
		LIMIT ?
	`, cacheKeyPrefixLike(prefix), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]CacheEntry, 0)
	for rows.Next() {
		e, err := scanCacheEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ListOldestCacheEntriesByRelPathPrefix(prefix string, limit int) ([]CacheEntry, error) {
	if limit <= 0 {
		limit = 32
	}
	rows, err := s.db.Query(`
		SELECT id, key, rel_path, size, last_access, created_at
		FROM cache_entries
		WHERE rel_path LIKE ?
		ORDER BY last_access ASC, created_at ASC, id ASC
		LIMIT ?
	`, cacheRelPathPrefixLike(prefix), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]CacheEntry, 0)
	for rows.Next() {
		e, err := scanCacheEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) DeleteCacheEntryByID(id int64) error {
	_, err := s.db.Exec(`DELETE FROM cache_entries WHERE id = ?`, id)
	return err
}

func cacheKeyPrefixLike(prefix string) string {
	if prefix == "" {
		return "%"
	}
	return prefix + "%"
}

func cacheRelPathPrefixLike(prefix string) string {
	prefix = filepath.ToSlash(strings.TrimSpace(prefix))
	if prefix == "" {
		return "%"
	}
	return prefix + "%"
}

func scanUser(scanner interface{ Scan(dest ...any) error }) (User, error) {
	var u User
	var createdRaw string
	var isAdmin int
	if err := scanner.Scan(&u.ID, &u.Username, &u.PasswordHash, &isAdmin, &createdRaw); err != nil {
		return User{}, err
	}
	u.IsAdmin = intToBool(isAdmin)
	t, err := time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return User{}, err
	}
	u.CreatedAt = t
	return u, nil
}

func scanMount(scanner interface{ Scan(dest ...any) error }) (Mount, error) {
	var m Mount
	var createdRaw string
	var isEnabled int
	var rootPathRaw string
	if err := scanner.Scan(
		&m.ID,
		&m.Name,
		&m.Type,
		&rootPathRaw,
		&m.SMBHost,
		&m.SMBPort,
		&m.SMBShare,
		&m.SMBUser,
		&m.SMBPassword,
		&m.SMBDomain,
		&m.WebDAVURL,
		&m.WebDAVUser,
		&m.WebDAVPassword,
		&isEnabled,
		&createdRaw,
	); err != nil {
		return Mount{}, err
	}

	m.Enabled = intToBool(isEnabled)
	t, err := time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return Mount{}, err
	}
	m.CreatedAt = t

	if m.Type == "" {
		return Mount{}, errors.New("mount type cannot be empty")
	}
	m.RootPaths = decodeMountRootPaths(rootPathRaw)
	if len(m.RootPaths) == 0 {
		m.RootPaths = []string{"/"}
	}
	m.RootPath = m.RootPaths[0]
	return m, nil
}

func decodeMountRootPaths(raw string) []string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return nil
	}

	if strings.HasPrefix(v, "[") {
		var arr []string
		if err := json.Unmarshal([]byte(v), &arr); err == nil {
			return normalizeRootPathList(arr)
		}
	}

	return normalizeRootPathList([]string{v})
}

func encodeMountRootPaths(rootPath string, rootPaths []string) string {
	paths := normalizeRootPathList(rootPaths)
	if len(paths) == 0 {
		paths = normalizeRootPathList([]string{rootPath})
	}
	if len(paths) == 0 {
		paths = []string{"/"}
	}
	if len(paths) == 1 {
		return paths[0]
	}

	b, err := json.Marshal(paths)
	if err != nil {
		return paths[0]
	}
	return string(b)
}

func normalizeRootPathList(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, item := range paths {
		v := strings.TrimSpace(item)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func scanComic(scanner interface{ Scan(dest ...any) error }) (Comic, error) {
	var c Comic
	var updatedRaw string
	var coverKey sql.NullString
	var fileNameKey sql.NullString
	if err := scanner.Scan(&c.ID, &c.MountID, &c.Path, &c.Title, &fileNameKey, &c.MtimeUnix, &c.Size, &c.PageCount, &coverKey, &updatedRaw); err != nil {
		return Comic{}, err
	}
	if fileNameKey.Valid {
		c.FileNameKey = fileNameKey.String
	}
	if coverKey.Valid {
		c.CoverKey = coverKey.String
	}
	t, err := time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil {
		return Comic{}, err
	}
	c.UpdatedAt = t
	return c, nil
}

func scanCacheEntry(scanner interface{ Scan(dest ...any) error }) (CacheEntry, error) {
	var e CacheEntry
	if err := scanner.Scan(&e.ID, &e.Key, &e.RelPath, &e.Size, &e.LastAccess, &e.CreatedAt); err != nil {
		return CacheEntry{}, err
	}
	return e, nil
}

func scanRootDirectory(scanner interface{ Scan(dest ...any) error }) (RootDirectory, error) {
	var item RootDirectory
	var createdRaw string
	if err := scanner.Scan(&item.ID, &item.Name, &item.Path, &createdRaw); err != nil {
		return RootDirectory{}, err
	}
	t, err := time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return RootDirectory{}, err
	}
	item.CreatedAt = t
	return item, nil
}

func scanWebPArchiveManifest(scanner interface{ Scan(dest ...any) error }) (WebPArchiveManifest, error) {
	var item WebPArchiveManifest
	var completedRaw, updatedRaw string
	if err := scanner.Scan(
		&item.ID, &item.SourcePath, &item.OutputZipPath, &item.Quality, &item.Method,
		&item.SourceMtimeUnix, &item.SourceSize, &item.SourceEntryCount,
		&item.ConvertedEntries, &item.KeptEntries, &item.OutputSize,
		&completedRaw, &updatedRaw,
	); err != nil {
		return WebPArchiveManifest{}, err
	}
	var err error
	item.CompletedAt, err = time.Parse(time.RFC3339Nano, completedRaw)
	if err != nil {
		return WebPArchiveManifest{}, err
	}
	item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil {
		return WebPArchiveManifest{}, err
	}
	return item, nil
}

func scanReadingProgress(scanner interface{ Scan(dest ...any) error }) (ReadingProgress, error) {
	var p ReadingProgress
	var lastReadRaw, updatedRaw string
	var comicID sql.NullInt64
	var fileNameKey sql.NullString
	if err := scanner.Scan(&p.ID, &p.UserID, &comicID, &fileNameKey, &p.LastPage, &p.TotalPages, &lastReadRaw, &updatedRaw); err != nil {
		return ReadingProgress{}, err
	}
	if comicID.Valid {
		id := comicID.Int64
		p.ComicID = &id
	}
	if fileNameKey.Valid {
		p.ComicFileNameKey = fileNameKey.String
	}
	var err error
	p.LastReadAt, err = time.Parse(time.RFC3339Nano, lastReadRaw)
	if err != nil {
		return ReadingProgress{}, err
	}
	p.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil {
		return ReadingProgress{}, err
	}
	return p, nil
}

func scanReaderSettings(scanner interface{ Scan(dest ...any) error }) (ReaderSettings, error) {
	var v ReaderSettings
	var mobileCompact, coverOffset int
	var createdRaw, updatedRaw string
	if err := scanner.Scan(&v.UserID, &v.Mode, &v.Fit, &v.EnhanceZip, &v.EnhancePDF, &v.SpreadDirection, &mobileCompact, &v.Quality, &v.Spread, &coverOffset, &createdRaw, &updatedRaw); err != nil {
		return ReaderSettings{}, err
	}
	v.MobileCompact = intToBool(mobileCompact)
	v.CoverOffset = intToBool(coverOffset)
	var err error
	v.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return ReaderSettings{}, err
	}
	v.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil {
		return ReaderSettings{}, err
	}
	return v, nil
}

func scanRuntimeSettings(scanner interface{ Scan(dest ...any) error }) (RuntimeSettings, error) {
	var v RuntimeSettings
	var legacyFrontendPagePrefetchPages int64
	var createdRaw, updatedRaw string
	if err := scanner.Scan(&v.ID, &v.MemoryLimitMB, &v.DiskCacheMB, &v.PagePrefetchPages, &legacyFrontendPagePrefetchPages, &v.PageLoadParallelism, &v.WebPQuality, &v.WebPMethod, &v.WebPConcurrency, &v.WebPOutputRoot, &createdRaw, &updatedRaw); err != nil {
		return RuntimeSettings{}, err
	}
	var err error
	v.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return RuntimeSettings{}, err
	}
	v.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil {
		return RuntimeSettings{}, err
	}
	return v, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func intToBool(v int) bool {
	return v != 0
}

var ErrReaderStateConflict = errors.New("reader state is newer than submitted update")

func (s *Store) GetComicReaderState(userID, comicID int64) (ComicReaderState, error) {
	var state ComicReaderState
	var settings string
	var updated int64
	err := s.db.QueryRow(`SELECT rs.settings, rs.last_page, rs.total_pages, rs.page_offset, rs.updated_at
 FROM comic_reader_states rs JOIN comics c ON c.mount_id=rs.mount_id AND c.path=rs.comic_path
 WHERE rs.user_id=? AND c.id=?`, userID, comicID).Scan(&settings, &state.LastPage, &state.TotalPages, &state.PageOffset, &updated)
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal([]byte(settings), &state.Settings); err != nil {
		return state, err
	}
	state.UpdatedAt = time.Unix(0, updated).UTC()
	return state, nil
}

// The conditional UPSERT compares event times atomically, including concurrent requests.
// Identity survives re-indexing while keeping same-name comics in different directories separate.
func (s *Store) UpsertComicReaderState(userID, comicID int64, state ComicReaderState) (ComicReaderState, error) {
	settings, err := json.Marshal(state.Settings)
	if err != nil {
		return ComicReaderState{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return ComicReaderState{}, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`INSERT INTO comic_reader_states
 (user_id,mount_id,comic_path,settings,last_page,total_pages,page_offset,updated_at)
 SELECT ?,mount_id,path,?,?,?,?,? FROM comics WHERE id=?
 ON CONFLICT(user_id,mount_id,comic_path) DO UPDATE SET
 settings=excluded.settings,last_page=excluded.last_page,total_pages=excluded.total_pages,
 page_offset=excluded.page_offset,updated_at=excluded.updated_at
 WHERE excluded.updated_at > comic_reader_states.updated_at`, userID, string(settings), state.LastPage, state.TotalPages, state.PageOffset, state.UpdatedAt.UnixNano(), comicID)
	if err != nil {
		return ComicReaderState{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return ComicReaderState{}, err
	}
	if count == 0 {
		_ = tx.Rollback()
		current, err := s.GetComicReaderState(userID, comicID)
		if err != nil {
			return current, err
		}
		return current, ErrReaderStateConflict
	}

	// Mirror only accepted events for legacy list/history views, in the same transaction.
	stamp := state.UpdatedAt.UTC().Format(time.RFC3339Nano)
	_, err = tx.Exec(`INSERT INTO reading_progress
 (user_id,comic_id,comic_file_name_key,last_page,total_pages,last_read_at,updated_at)
 SELECT ?,id,file_name_key,?,?,?,? FROM comics WHERE id=?
 ON CONFLICT(user_id,comic_file_name_key) DO UPDATE SET
 comic_id=excluded.comic_id,last_page=excluded.last_page,total_pages=excluded.total_pages,
 last_read_at=excluded.last_read_at,updated_at=excluded.updated_at`, userID, state.LastPage, state.TotalPages, stamp, stamp, comicID)
	if err != nil {
		return ComicReaderState{}, err
	}
	if err = tx.Commit(); err != nil {
		return ComicReaderState{}, err
	}
	return state, nil
}

func (s *Store) CountReadingHistory(userID int64) (int, error) {
	var total int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM reading_progress rp WHERE rp.user_id=?
 AND EXISTS(SELECT 1 FROM comics c WHERE c.file_name_key=rp.comic_file_name_key
 AND (LOWER(c.path) LIKE '%.zip' OR LOWER(c.path) LIKE '%.cbz'))`, userID).Scan(&total)
	return total, err
}
