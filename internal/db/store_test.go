package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestReadingProgressAndFavoritesPersistByFileName(t *testing.T) {
	store := openTestStore(t)

	user, err := store.CreateUser("tester", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	mount, err := store.CreateMount(Mount{
		Name:     "local",
		Type:     MountTypeLocal,
		RootPath: "/manga",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateMount failed: %v", err)
	}

	firstScan := []Comic{
		{
			MountID:     mount.ID,
			Path:        "old/Series 01.zip",
			Title:       "Series 01",
			FileNameKey: "series 01.zip",
			MtimeUnix:   100,
			Size:        1024,
			PageCount:   12,
			CoverKey:    "cv6:cover:file:series 01.zip",
		},
	}
	if err := store.ReplaceComicsForMount(mount.ID, firstScan); err != nil {
		t.Fatalf("ReplaceComicsForMount first scan failed: %v", err)
	}

	initialComics, err := store.ListComicsByMount(mount.ID)
	if err != nil {
		t.Fatalf("ListComicsByMount initial failed: %v", err)
	}
	if len(initialComics) != 1 {
		t.Fatalf("initial comics count=%d want=1", len(initialComics))
	}
	initialComic := initialComics[0]

	progress, err := store.UpsertReadingProgress(user.ID, initialComic.ID, "", 5, 12)
	if err != nil {
		t.Fatalf("UpsertReadingProgress failed: %v", err)
	}
	if progress.ComicFileNameKey != "series 01.zip" {
		t.Fatalf("progress key=%q want=%q", progress.ComicFileNameKey, "series 01.zip")
	}
	if err := store.SetComicFavorite(user.ID, initialComic.ID, "", true); err != nil {
		t.Fatalf("SetComicFavorite failed: %v", err)
	}

	secondScan := []Comic{
		{
			MountID:     mount.ID,
			Path:        "new/Series 01.zip",
			Title:       "Series 01 Rematched",
			FileNameKey: "series 01.zip",
			MtimeUnix:   200,
			Size:        2048,
			PageCount:   16,
			CoverKey:    "cv6:cover:file:series 01.zip",
		},
	}
	if err := store.ReplaceComicsForMount(mount.ID, secondScan); err != nil {
		t.Fatalf("ReplaceComicsForMount second scan failed: %v", err)
	}

	latestComics, err := store.ListComicsByMount(mount.ID)
	if err != nil {
		t.Fatalf("ListComicsByMount latest failed: %v", err)
	}
	if len(latestComics) != 1 {
		t.Fatalf("latest comics count=%d want=1", len(latestComics))
	}
	latestComic := latestComics[0]
	if latestComic.Path != "new/Series 01.zip" {
		t.Fatalf("latest comic path=%q want=%q", latestComic.Path, "new/Series 01.zip")
	}

	rematchedProgress, err := store.GetReadingProgress(user.ID, latestComic.ID)
	if err != nil {
		t.Fatalf("GetReadingProgress failed: %v", err)
	}
	if rematchedProgress.LastPage != 5 {
		t.Fatalf("rematched progress last_page=%d want=5", rematchedProgress.LastPage)
	}
	if rematchedProgress.ComicFileNameKey != "series 01.zip" {
		t.Fatalf("rematched progress key=%q want=%q", rematchedProgress.ComicFileNameKey, "series 01.zip")
	}

	isFavorite, err := store.IsComicFavorite(user.ID, latestComic.ID)
	if err != nil {
		t.Fatalf("IsComicFavorite failed: %v", err)
	}
	if !isFavorite {
		t.Fatal("latest comic should remain favorite after path rematch")
	}

	history, err := store.ListReadingHistory(user.ID, 100, 0)
	if err != nil {
		t.Fatalf("ListReadingHistory failed: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history count=%d want=1", len(history))
	}
	if history[0].Comic.ID != latestComic.ID {
		t.Fatalf("history comic id=%d want=%d", history[0].Comic.ID, latestComic.ID)
	}
	if history[0].Comic.Path != "new/Series 01.zip" {
		t.Fatalf("history comic path=%q want=%q", history[0].Comic.Path, "new/Series 01.zip")
	}
	if !history[0].IsFavorite {
		t.Fatal("history item should keep favorite state after path rematch")
	}
}

func TestCleanupOldHistoryKeepsLatestHundred(t *testing.T) {
	store := openTestStore(t)

	user, err := store.CreateUser("history-user", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	mount, err := store.CreateMount(Mount{
		Name:     "history-mount",
		Type:     MountTypeLocal,
		RootPath: "/history",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateMount failed: %v", err)
	}

	comics := make([]Comic, 0, 101)
	for i := 1; i <= 101; i++ {
		name := fmt.Sprintf("Series-%03d.zip", i)
		comics = append(comics, Comic{
			MountID:     mount.ID,
			Path:        filepath.ToSlash(filepath.Join("library", name)),
			Title:       name,
			FileNameKey: normalizeFileNameKey(name),
			MtimeUnix:   int64(i),
			Size:        int64(1024 + i),
			PageCount:   10 + i,
			CoverKey:    "cv6:cover:file:" + normalizeFileNameKey(name),
		})
	}
	if err := store.ReplaceComicsForMount(mount.ID, comics); err != nil {
		t.Fatalf("ReplaceComicsForMount failed: %v", err)
	}

	saved, err := store.ListComicsByMount(mount.ID)
	if err != nil {
		t.Fatalf("ListComicsByMount failed: %v", err)
	}
	if len(saved) != 101 {
		t.Fatalf("saved comics count=%d want=101", len(saved))
	}

	for idx, comic := range saved {
		if _, err := store.UpsertReadingProgress(user.ID, comic.ID, "", idx, comic.PageCount); err != nil {
			t.Fatalf("UpsertReadingProgress #%d failed: %v", idx, err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	var progressCount int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM reading_progress WHERE user_id = ?`, user.ID).Scan(&progressCount); err != nil {
		t.Fatalf("count reading_progress failed: %v", err)
	}
	if progressCount != 100 {
		t.Fatalf("reading_progress count=%d want=100", progressCount)
	}

	history, err := store.ListReadingHistory(user.ID, 150, 0)
	if err != nil {
		t.Fatalf("ListReadingHistory failed: %v", err)
	}
	if len(history) != 100 {
		t.Fatalf("history count=%d want=100", len(history))
	}
	if history[len(history)-1].Comic.FileNameKey != "series-002.zip" {
		t.Fatalf("oldest retained history key=%q want=%q", history[len(history)-1].Comic.FileNameKey, "series-002.zip")
	}

	if _, err := store.GetReadingProgressByFileName(user.ID, "series-001.zip"); err == nil {
		t.Fatal("series-001.zip should have been trimmed from history")
	} else if err != sql.ErrNoRows {
		t.Fatalf("GetReadingProgressByFileName trimmed item err=%v", err)
	}
}

func TestLibraryDedupesByFileNameAndPrefersLargerFile(t *testing.T) {
	store := openTestStore(t)

	user, err := store.CreateUser("library-user", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	mountA, err := store.CreateMount(Mount{
		Name:     "mount-a",
		Type:     MountTypeLocal,
		RootPath: "/a",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateMount mountA failed: %v", err)
	}
	mountB, err := store.CreateMount(Mount{
		Name:     "mount-b",
		Type:     MountTypeLocal,
		RootPath: "/b",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateMount mountB failed: %v", err)
	}

	if err := store.ReplaceComicsForMount(mountA.ID, []Comic{
		{
			MountID:     mountA.ID,
			Path:        "dup/MyBook.zip",
			Title:       "MyBook small",
			FileNameKey: "mybook.zip",
			MtimeUnix:   100,
			Size:        1024,
			PageCount:   10,
			CoverKey:    "cv6:cover:file:mybook.zip",
		},
	}); err != nil {
		t.Fatalf("ReplaceComicsForMount mountA failed: %v", err)
	}
	if err := store.ReplaceComicsForMount(mountB.ID, []Comic{
		{
			MountID:     mountB.ID,
			Path:        "dup/MyBook.zip",
			Title:       "MyBook large",
			FileNameKey: "mybook.zip",
			MtimeUnix:   200,
			Size:        4096,
			PageCount:   20,
			CoverKey:    "cv6:cover:file:mybook.zip",
		},
	}); err != nil {
		t.Fatalf("ReplaceComicsForMount mountB failed: %v", err)
	}

	total, err := store.CountComicsForUser(user.ID, "", 0, "all", "all", "all")
	if err != nil {
		t.Fatalf("CountComicsForUser failed: %v", err)
	}
	if total != 1 {
		t.Fatalf("deduped total=%d want=1", total)
	}

	items, err := store.ListComicsForUser(user.ID, "", 0, "all", "all", "all", "size", "desc", 50, 0)
	if err != nil {
		t.Fatalf("ListComicsForUser failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("deduped items len=%d want=1", len(items))
	}
	if items[0].MountID != mountB.ID {
		t.Fatalf("selected mount=%d want=%d", items[0].MountID, mountB.ID)
	}
	if items[0].Size != 4096 {
		t.Fatalf("selected size=%d want=4096", items[0].Size)
	}
	if items[0].Title != "MyBook large" {
		t.Fatalf("selected title=%q want=%q", items[0].Title, "MyBook large")
	}
}

func TestOpenRepairsMissingColumnsAtStartup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "legacy.sqlite")

	rawDB, err := sql.Open("sqlite", fmt.Sprintf("file:%s", dbPath))
	if err != nil {
		t.Fatalf("sql.Open failed: %v", err)
	}
	legacyStmts := []string{
		`CREATE TABLE comics (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			mount_id INTEGER NOT NULL,
			path TEXT NOT NULL,
			title TEXT NOT NULL,
			mtime_unix INTEGER NOT NULL,
			size INTEGER NOT NULL,
			page_count INTEGER NOT NULL
		)`,
		`CREATE TABLE reading_progress (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			last_page INTEGER NOT NULL DEFAULT 0,
			total_pages INTEGER NOT NULL DEFAULT 0,
			last_read_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE favorite_comics (
			user_id INTEGER NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE cache_entries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			key TEXT NOT NULL UNIQUE,
			rel_path TEXT NOT NULL,
			size INTEGER NOT NULL
		)`,
		`CREATE TABLE app_runtime_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			memory_limit_mb INTEGER NOT NULL,
			gc_percent INTEGER NOT NULL,
			created_at TEXT NOT NULL
		)`,
	}
	for _, stmt := range legacyStmts {
		if _, err := rawDB.Exec(stmt); err != nil {
			_ = rawDB.Close()
			t.Fatalf("create legacy schema failed for %q: %v", stmt, err)
		}
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw db failed: %v", err)
	}

	store, err := Open(dbPath, nil)
	if err != nil {
		t.Fatalf("Open should repair legacy schema, got err=%v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	columnChecks := []struct {
		table  string
		column string
	}{
		{table: "comics", column: "file_name_key"},
		{table: "comics", column: "cover_key"},
		{table: "comics", column: "updated_at"},
		{table: "reading_progress", column: "comic_id"},
		{table: "reading_progress", column: "comic_file_name_key"},
		{table: "favorite_comics", column: "comic_id"},
		{table: "favorite_comics", column: "comic_file_name_key"},
		{table: "cache_entries", column: "last_access"},
		{table: "cache_entries", column: "created_at"},
		{table: "app_runtime_settings", column: "updated_at"},
	}
	for _, check := range columnChecks {
		ok, err := store.columnExists(check.table, check.column)
		if err != nil {
			t.Fatalf("columnExists(%s, %s) failed: %v", check.table, check.column, err)
		}
		if !ok {
			t.Fatalf("expected repaired column %s.%s to exist", check.table, check.column)
		}
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	store, err := Open(dbPath, nil)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}
