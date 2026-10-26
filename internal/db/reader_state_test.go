package db

import (
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestComicReaderStateIsolationAndConflict(t *testing.T) {
	s := openTestStore(t)
	u, _ := s.CreateUser("reader", "hash", false)
	m, _ := s.CreateMount(Mount{Name: "local", Type: MountTypeLocal, RootPath: "/manga", Enabled: true})
	for _, p := range []string{"one/Book.zip", "two/Book.zip"} {
		if err := s.UpsertComicForMount(m.ID, Comic{Path: p, Title: p, PageCount: 20}); err != nil {
			t.Fatal(err)
		}
	}
	books, err := s.ListComicsByMount(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	event := time.Now().UTC().Add(-time.Minute)
	state := ComicReaderState{LastPage: 10, TotalPages: 20, PageOffset: 0.4, UpdatedAt: event, Settings: ComicReaderPreferences{Mode: "scroll", Quality: "original"}}
	got, err := s.UpsertComicReaderState(u.ID, books[0].ID, state)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastPage != 10 {
		t.Fatalf("page=%d", got.LastPage)
	}
	if _, err = s.GetComicReaderState(u.ID, books[1].ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("same filename leaked: %v", err)
	}
	state.LastPage = 2
	state.UpdatedAt = event.Add(time.Second)
	if _, err = s.UpsertComicReaderState(u.ID, books[0].ID, state); err != nil {
		t.Fatal(err)
	}
	state.LastPage = 15
	state.UpdatedAt = event
	got, err = s.UpsertComicReaderState(u.ID, books[0].ID, state)
	if !errors.Is(err, ErrReaderStateConflict) || got.LastPage != 2 {
		t.Fatalf("stale update got %+v err=%v", got, err)
	}
	// Re-scans may assign a different comic ID; path identity must preserve state.
	old := books[0]
	if _, err = s.DB().Exec("DELETE FROM comics WHERE id=?", old.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.UpsertComicForMount(m.ID, old); err != nil {
		t.Fatal(err)
	}
	books, err = s.ListComicsByMount(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, book := range books {
		if book.Path == old.Path {
			got, err = s.GetComicReaderState(u.ID, book.ID)
			if err != nil || got.LastPage != 2 {
				t.Fatalf("state lost after reindex: %+v %v", got, err)
			}
		}
	}
}

func TestReadingHistoryCountAndBoundaries(t *testing.T) {
	s := openTestStore(t)
	u, _ := s.CreateUser("reader", "hash", false)
	m, _ := s.CreateMount(Mount{Name: "local", Type: MountTypeLocal, RootPath: "/manga", Enabled: true})
	for _, p := range []string{"a.zip", "b.zip", "c.zip"} {
		if err := s.UpsertComicForMount(m.ID, Comic{Path: p, Title: p}); err != nil {
			t.Fatal(err)
		}
	}
	books, _ := s.ListComicsByMount(m.ID)
	for _, c := range books {
		if _, err := s.UpsertReadingProgress(u.ID, c.ID, "", 1, 10); err != nil {
			t.Fatal(err)
		}
	}
	count, err := s.CountReadingHistory(u.ID)
	if err != nil || count != 3 {
		t.Fatalf("count=%d %v", count, err)
	}
	items, err := s.ListReadingHistory(u.ID, 2, 2)
	if err != nil || len(items) != 1 {
		t.Fatalf("last page=%d %v", len(items), err)
	}
	items, err = s.ListReadingHistory(u.ID, 2, 3)
	if err != nil || len(items) != 0 {
		t.Fatalf("past end=%d %v", len(items), err)
	}
}

func TestComicReaderStateConcurrentEventsKeepNewest(t *testing.T) {
	s := openTestStore(t)
	u, _ := s.CreateUser("reader", "hash", false)
	m, _ := s.CreateMount(Mount{Name: "local", Type: MountTypeLocal, RootPath: "/manga", Enabled: true})
	if err := s.UpsertComicForMount(m.ID, Comic{Path: "book.zip", Title: "book"}); err != nil {
		t.Fatal(err)
	}
	books, _ := s.ListComicsByMount(m.ID)
	event := time.Now().UTC().Add(-time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(page int) {
			defer wg.Done()
			_, err := s.UpsertComicReaderState(u.ID, books[0].ID, ComicReaderState{LastPage: page, TotalPages: 20, UpdatedAt: event.Add(time.Duration(page) * time.Millisecond)})
			if err != nil && !errors.Is(err, ErrReaderStateConflict) {
				t.Errorf("save page %d: %v", page, err)
			}
		}(i)
	}
	wg.Wait()
	state, err := s.GetComicReaderState(u.ID, books[0].ID)
	if err != nil || state.LastPage != 11 {
		t.Fatalf("newest state=%+v err=%v", state, err)
	}
	progress, err := s.GetReadingProgress(u.ID, books[0].ID)
	if err != nil || progress.LastPage != 11 {
		t.Fatalf("history mirror=%+v err=%v", progress, err)
	}
}
