package server

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"zipmanga/internal/imgcodec"
)

func TestWebPArchiveJobsDoNotShareOutputs(t *testing.T) {
	source, output := t.TempDir(), t.TempDir()
	for _, name := range []string{"漫画.zip", "漫画.cbz", "漫画.rar", "漫画__1.zip", "独立漫画.CBZ"} {
		writeWebPTestZIP(t, filepath.Join(source, name), webpTestFile{"001.png", webpTestPNG(t, 4)})
	}
	jobs, _, err := scanWebPArchiveJobs(source, output, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 5 {
		t.Fatalf("jobs=%d", len(jobs))
	}
	seen := map[string]string{}
	for _, job := range jobs {
		key := strings.ToLower(job.FinalZip)
		if previous, ok := seen[key]; ok {
			t.Fatalf("%s and %s share output %s", previous, job.RelPath, job.FinalZip)
		}
		seen[key] = job.RelPath
		if job.RelPath == "独立漫画.CBZ" && filepath.Base(job.FinalZip) != "独立漫画.zip" {
			t.Fatalf("unambiguous legacy output changed: %s", job.FinalZip)
		}
		if err := os.WriteFile(job.FinalZip, []byte(job.RelPath), 0600); err != nil {
			t.Fatal(err)
		}
	}
	rescanned, _, err := scanWebPArchiveJobs(source, output, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, job := range rescanned {
		if job.FinalZip != jobs[i].FinalZip {
			t.Fatalf("output changed on rescan: %s", job.FinalZip)
		}
		data, err := os.ReadFile(job.FinalZip)
		if err != nil || string(data) != job.RelPath {
			t.Fatalf("output overwritten for %s: %s %v", job.RelPath, data, err)
		}
	}
}

func TestWebPArchiveJobsBoundLongOutputNames(t *testing.T) {
	source, output := t.TempDir(), t.TempDir()
	for _, ending := range []string{"甲", "乙"} {
		name := strings.Repeat("漫", 81) + ending + ".cbz"
		writeWebPTestZIP(t, filepath.Join(source, name), webpTestFile{"001.png", webpTestPNG(t, 4)})
	}
	jobs, _, err := scanWebPArchiveJobs(source, output, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].FinalZip == jobs[1].FinalZip {
		t.Fatalf("long names collided: %+v", jobs)
	}
	for _, job := range jobs {
		if !utf8.ValidString(job.FinalZip) || len(filepath.Base(job.FinalZip)) > 240 {
			t.Fatalf("unsafe long output name: %s", job.FinalZip)
		}
		entries, err := scanWebPArchiveEntries(job.SourcePath, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeWebPArchiveOutput(job.SourcePath, job.FinalZip, entries, imgcodec.LossyWebPEncodeOptions{}, 1, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWebPTemporaryArchivesAreIndependent(t *testing.T) {
	for _, name := range []string{"comic.zip", strings.Repeat("漫", 83) + ".zip"} {
		t.Run(name, func(t *testing.T) {
			final := filepath.Join(t.TempDir(), name)
			first, out1, _, close1, err := openTempZipWriter(final)
			if err != nil {
				t.Fatal(err)
			}
			defer close1(false)
			info, err := out1.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o644 {
				t.Fatalf("output permissions = %o, want 644", info.Mode().Perm())
			}
			if _, err := out1.WriteString("existing temporary content"); err != nil {
				t.Fatal(err)
			}
			second, _, _, close2, err := openTempZipWriter(final)
			if err != nil {
				t.Fatal(err)
			}
			defer close2(false)
			if first == second {
				t.Fatal("concurrent conversions share a temporary file")
			}
			data, err := os.ReadFile(first)
			if err != nil || string(data) != "existing temporary content" {
				t.Fatalf("temporary file truncated: %s %v", data, err)
			}
		})
	}
}

func TestWebPEntryNamesAvoidGeneratedAndCaseCollisions(t *testing.T) {
	dir := t.TempDir()
	source, output := filepath.Join(dir, "book.cbz"), filepath.Join(dir, "out.zip")
	writeWebPTestZIP(t, source,
		webpTestFile{"001.png", webpTestPNG(t, 4)},
		webpTestFile{"001.jpg", webpTestPNG(t, 5)},
		webpTestFile{"001__1.png", webpTestPNG(t, 6)},
		webpTestFile{"001.PNG", webpTestPNG(t, 7)},
		webpTestFile{"CAFÉ.png", webpTestPNG(t, 8)},
		webpTestFile{"cafe\u0301.jpg", webpTestPNG(t, 9)},
	)
	entries, err := scanWebPArchiveEntries(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeWebPArchiveOutput(source, output, entries, imgcodec.LossyWebPEncodeOptions{}, 2, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	r, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if len(r.File) != 6 {
		t.Fatalf("pages=%d", len(r.File))
	}
	seen := map[string]bool{}
	for i, file := range r.File {
		// This fixture has one decomposed Unicode name, equivalent to CAFÉ.
		key := strings.ReplaceAll(strings.ToLower(file.Name), "e\u0301", "é")
		if seen[key] {
			t.Fatalf("duplicate output name: %s", file.Name)
		}
		seen[key] = true
		f, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		img, _, err := image.Decode(f)
		_ = f.Close()
		if err != nil || img.Bounds().Dx() != i+4 {
			t.Fatalf("page %d changed or lost: %v", i, err)
		}
	}
}

func TestArchiveSanitizersPreserveCompleteUTF8Characters(t *testing.T) {
	for _, tc := range []struct {
		name     string
		limit    int
		sanitize func(string) string
	}{
		{"directory", 120, sanitizeDirectoryName},
		{"file", 180, sanitizeFileName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := strings.Repeat("漫", tc.limit/3-1) + "😊尾"
			got := tc.sanitize(name)
			if !utf8.ValidString(got) || len(got) > tc.limit || !strings.HasPrefix(name, got) {
				t.Fatalf("UTF-8 name cut in the middle of a character: %q", got)
			}
		})
	}
}

func TestWebPZIPNamesHonorVerifiedUnicodePathExtra(t *testing.T) {
	rawName := "\xd6\xd0\xce\xc4/001.png"
	unicodeName := "中文/001.png"
	extra := make([]byte, 9+len(unicodeName))
	binary.LittleEndian.PutUint16(extra, 0x7075)
	binary.LittleEndian.PutUint16(extra[2:], uint16(5+len(unicodeName)))
	extra[4] = 1
	binary.LittleEndian.PutUint32(extra[5:], crc32.ChecksumIEEE([]byte(rawName)))
	copy(extra[9:], unicodeName)
	for _, validCRC := range []bool{true, false} {
		t.Run(map[bool]string{true: "verified", false: "bad checksum"}[validCRC], func(t *testing.T) {
			extra := append([]byte(nil), extra...)
			if !validCRC {
				extra[5] ^= 0xff
			}
			dir := t.TempDir()
			source, output := filepath.Join(dir, "book.cbz"), filepath.Join(dir, "out.zip")
			var data bytes.Buffer
			zw := zip.NewWriter(&data)
			f, err := zw.CreateHeader(&zip.FileHeader{Name: rawName, NonUTF8: true, Extra: extra, Method: zip.Deflate})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write(webpTestPNG(t, 4)); err != nil {
				t.Fatal(err)
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, data.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			entries, err := scanWebPArchiveEntries(source, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeWebPArchiveOutput(source, output, entries, imgcodec.LossyWebPEncodeOptions{}, 1, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			zr, err := zip.OpenReader(output)
			if err != nil {
				t.Fatal(err)
			}
			defer zr.Close()
			want := "中文/001.webp"
			if !validCRC {
				want = "%D6%D0%CE%C4/001.webp"
			}
			if len(zr.File) != 1 || zr.File[0].Name != want || !utf8.ValidString(zr.File[0].Name) {
				t.Fatalf("filename=%q want=%q", zr.File[0].Name, want)
			}
			fread, err := zr.File[0].Open()
			if err != nil {
				t.Fatal(err)
			}
			_, err = io.Copy(io.Discard, fread)
			_ = fread.Close()
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
