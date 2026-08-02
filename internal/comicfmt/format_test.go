package comicfmt

import "testing"

func TestDetect(t *testing.T) {
	cases := []struct {
		path string
		want Format
	}{
		{path: "a.zip", want: FormatZIP},
		{path: "a.cbz", want: FormatZIP},
		{path: "a.rar", want: FormatUnknown},
		{path: "a.cbr", want: FormatUnknown},
		{path: "a.7z", want: FormatUnknown},
		{path: "a.cb7", want: FormatUnknown},
		{path: "a.pdf", want: FormatPDF},
		{path: "a.txt", want: FormatUnknown},
	}
	for _, tc := range cases {
		if got := Detect(tc.path); got != tc.want {
			t.Fatalf("Detect(%q)=%q want=%q", tc.path, got, tc.want)
		}
	}
}

func TestIsArchive(t *testing.T) {
	if !IsArchive(FormatZIP) {
		t.Fatal("expected zip to be archive")
	}
	if IsArchive(FormatPDF) || IsArchive(FormatUnknown) || IsArchive(FormatRAR) || IsArchive(Format7Z) {
		t.Fatal("non-archive formats should not be archive")
	}
}
