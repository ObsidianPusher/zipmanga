package server

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

func webPNameKey(name string) string {
	return norm.NFC.String(strings.ToLower(name))
}

// Preserve ordinary output paths for existing manifests. Reserve all natural
// names before assigning suffixes so generated names cannot steal another job's
// output. Hash the source identity rather than relying on enumeration order.
func assignWebPArchiveOutputPaths(jobs []webpConvertArchiveJob, outputDir string) {
	counts := make(map[string]int, len(jobs))
	reserved := make(map[string]bool, len(jobs))
	for i := range jobs {
		jobs[i].FinalZip = webPArchiveOutputPath(outputDir, jobs[i].RelPath, "")
		key := webPNameKey(jobs[i].FinalZip)
		counts[key]++
		reserved[key] = true
	}
	for i := range jobs {
		job := &jobs[i]
		if counts[webPNameKey(job.FinalZip)] > 1 {
			for attempt := 0; ; attempt++ {
				sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", job.RelPath, attempt)))
				candidate := webPArchiveOutputPath(outputDir, job.RelPath, fmt.Sprintf("--%x", sum[:8]))
				key := webPNameKey(candidate)
				if reserved[key] {
					continue
				}
				reserved[key] = true
				job.FinalZip = candidate
				break
			}
		}
		job.WorkDir = job.FinalZip + ".work"
	}
}

func webPArchiveOutputPath(outputDir, relPath, suffix string) string {
	rel := safeWebPArchiveName(relPath)
	stem := strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
	// Leave room for extensions used by cleanup and external copy tools.
	if len(stem)+len(suffix)+len(".zip") > 240 {
		if suffix == "" {
			sum := sha256.Sum256([]byte(relPath))
			suffix = fmt.Sprintf("--%x", sum[:8])
		}
		stem = truncateUTF8(stem, 240-len(suffix)-len(".zip"))
	}
	return filepath.Join(outputDir, filepath.Dir(rel), stem+suffix+".zip")
}

// Do not guess between GBK, Shift-JIS and other ambiguous legacy encodings.
// Escape undecodable path components reversibly, keeping ASCII extensions.
func safeWebPArchiveName(name string) string {
	parts := strings.Split(strings.ReplaceAll(name, "\\", "/"), "/")
	for i, part := range parts {
		if !utf8.ValidString(part) {
			parts[i] = url.PathEscape(part)
		}
	}
	return strings.Join(parts, "/")
}

func webPZIPEntryName(file *zip.File) string {
	rawName := file.Name
	if file.Flags&0x800 != 0 && utf8.ValidString(rawName) {
		return safeWebPArchiveName(rawName)
	}
	for extra := file.Extra; len(extra) >= 4; {
		kind := binary.LittleEndian.Uint16(extra)
		size := int(binary.LittleEndian.Uint16(extra[2:]))
		extra = extra[4:]
		if size > len(extra) {
			break
		}
		field := extra[:size]
		extra = extra[size:]
		// Info-ZIP Unicode Path: version, CRC32 of raw name, UTF-8 name.
		if kind == 0x7075 && len(field) > 5 && field[0] == 1 &&
			binary.LittleEndian.Uint32(field[1:]) == crc32.ChecksumIEEE([]byte(rawName)) && utf8.Valid(field[5:]) {
			return safeWebPArchiveName(string(field[5:]))
		}
	}
	return safeWebPArchiveName(rawName)
}
