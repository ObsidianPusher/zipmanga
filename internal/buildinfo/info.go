package buildinfo

import "strings"

var (
	Version        = "dev"
	Commit         = "nogit"
	BuildTime      = "unknown"
	SourceInputSHA = "unknown"
	WebAssetSHA    = "unknown"
)

type Info struct {
	Version        string
	Commit         string
	BuildTime      string
	SourceInputSHA string
	WebAssetSHA    string
}

func Current() Info {
	return Info{
		Version:        strings.TrimSpace(Version),
		Commit:         strings.TrimSpace(Commit),
		BuildTime:      strings.TrimSpace(BuildTime),
		SourceInputSHA: strings.TrimSpace(SourceInputSHA),
		WebAssetSHA:    strings.TrimSpace(WebAssetSHA),
	}
}
