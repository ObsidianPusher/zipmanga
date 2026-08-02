package comicutil

import (
	"fmt"

	"zipmanga/internal/archiveutil"
	"zipmanga/internal/comicfmt"
	"zipmanga/internal/mounts"
	"zipmanga/internal/pdfutil"
)

type ProbeOptions struct {
	NeedPageCount bool
	NeedCover     bool
	ThumbWidth    int
	Title         string
}

type ProbeResult struct {
	Format    comicfmt.Format
	PageCount int
	CoverJPEG []byte
}

func Probe(provider mounts.Provider, relPath string, opt ProbeOptions) (ProbeResult, error) {
	res := ProbeResult{Format: comicfmt.Detect(relPath)}
	switch res.Format {
	case comicfmt.FormatZIP, comicfmt.FormatRAR, comicfmt.Format7Z:
		probe, err := archiveutil.Probe(provider, relPath, res.Format, archiveutil.ProbeOptions{
			NeedPageCount: opt.NeedPageCount,
			NeedCover:     opt.NeedCover,
			ThumbWidth:    opt.ThumbWidth,
		})
		if err != nil {
			return ProbeResult{}, err
		}
		res.PageCount = probe.PageCount
		res.CoverJPEG = probe.CoverJPEG
		return res, nil

	case comicfmt.FormatPDF:
		if opt.NeedPageCount {
			n, err := pdfutil.PageCount(provider, relPath)
			if err != nil {
				return ProbeResult{}, err
			}
			res.PageCount = n
		}
		if opt.NeedCover {
			cover, err := pdfutil.BuildPlaceholderCoverJPEG(opt.Title, opt.ThumbWidth)
			if err != nil {
				return ProbeResult{}, err
			}
			res.CoverJPEG = cover
		}
		return res, nil
	}
	return ProbeResult{}, fmt.Errorf("unsupported comic format: %s", relPath)
}
