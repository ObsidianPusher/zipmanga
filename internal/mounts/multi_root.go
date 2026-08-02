package mounts

import (
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"zipmanga/internal/db"
)

const multiRootTokenPrefix = "__root"
const multiRootTokenSuffix = "__"

type MultiRootProvider struct {
	mount    db.Mount
	children []Provider
	labels   []string
}

func NewMultiRootProvider(m db.Mount) (*MultiRootProvider, error) {
	roots := normalizeMountRootPaths(m)
	if len(roots) <= 1 {
		return nil, fmt.Errorf("multi root provider requires at least 2 roots")
	}

	children := make([]Provider, 0, len(roots))
	labels := buildMultiRootLabels(roots)
	for _, root := range roots {
		childMount := m
		childMount.RootPath = root
		childMount.RootPaths = []string{root}
		child, err := newSingleRootProvider(childMount)
		if err != nil {
			for _, c := range children {
				_ = c.Close()
			}
			return nil, err
		}
		children = append(children, child)
	}

	return &MultiRootProvider{
		mount:    m,
		children: children,
		labels:   labels,
	}, nil
}

func (p *MultiRootProvider) ID() int64 { return p.mount.ID }

func (p *MultiRootProvider) Name() string { return p.mount.Name }

func (p *MultiRootProvider) Type() db.MountType { return p.mount.Type }

func (p *MultiRootProvider) ListDir(rel string) ([]FileEntry, error) {
	trimmed := trimPath(rel)
	if trimmed == "" {
		entries := make([]FileEntry, 0, len(p.children))
		for idx := range p.children {
			token := multiRootToken(idx)
			entries = append(entries, FileEntry{
				Path:    token,
				Name:    p.labels[idx],
				IsDir:   true,
				Size:    0,
				ModTime: time.Time{},
			})
		}
		return entries, nil
	}

	rootIdx, childRel, err := parseMultiRootPath(trimmed)
	if err != nil {
		return nil, err
	}
	if rootIdx < 0 || rootIdx >= len(p.children) {
		return nil, fmt.Errorf("invalid root index in path: %s", rel)
	}

	childEntries, err := p.children[rootIdx].ListDir(childRel)
	if err != nil {
		return nil, err
	}

	prefix := multiRootToken(rootIdx)
	out := make([]FileEntry, 0, len(childEntries))
	for _, item := range childEntries {
		itemPath := trimPath(item.Path)
		if itemPath == "" {
			item.Path = prefix
		} else {
			item.Path = trimPath(path.Join(prefix, itemPath))
		}
		out = append(out, item)
	}
	return out, nil
}

func (p *MultiRootProvider) Open(rel string) (io.ReadCloser, error) {
	rootIdx, childRel, err := parseMultiRootPath(rel)
	if err != nil {
		return nil, err
	}
	return p.children[rootIdx].Open(childRel)
}

func (p *MultiRootProvider) OpenReaderAt(rel string) (ReadAtCloser, int64, error) {
	rootIdx, childRel, err := parseMultiRootPath(rel)
	if err != nil {
		return nil, 0, err
	}
	return p.children[rootIdx].OpenReaderAt(childRel)
}

func (p *MultiRootProvider) Close() error {
	errs := make([]string, 0)
	for _, child := range p.children {
		if err := child.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("close multi root provider: %s", strings.Join(errs, "; "))
	}
	return nil
}

func normalizeMountRootPaths(m db.Mount) []string {
	if len(m.RootPaths) > 0 {
		return append([]string(nil), m.RootPaths...)
	}
	if strings.TrimSpace(m.RootPath) != "" {
		return []string{m.RootPath}
	}
	return []string{"/"}
}

func newSingleRootProvider(mt db.Mount) (Provider, error) {
	switch mt.Type {
	case db.MountTypeLocal:
		return NewLocalProvider(mt)
	case db.MountTypeSMB:
		return NewSMBProvider(mt)
	case db.MountTypeWebDAV:
		return NewWebDAVProvider(mt)
	default:
		return nil, fmt.Errorf("unsupported mount type: %s", mt.Type)
	}
}

func multiRootToken(idx int) string {
	return fmt.Sprintf("%s%d%s", multiRootTokenPrefix, idx, multiRootTokenSuffix)
}

func parseMultiRootPath(rel string) (rootIdx int, childRel string, err error) {
	trimmed := trimPath(rel)
	if trimmed == "" {
		return 0, "", fmt.Errorf("path is empty")
	}
	parts := strings.SplitN(trimmed, "/", 2)
	token := strings.TrimSpace(parts[0])
	if !strings.HasPrefix(token, multiRootTokenPrefix) || !strings.HasSuffix(token, multiRootTokenSuffix) {
		return 0, "", fmt.Errorf("path missing root token: %s", rel)
	}
	numRaw := strings.TrimSuffix(strings.TrimPrefix(token, multiRootTokenPrefix), multiRootTokenSuffix)
	idx, convErr := strconv.Atoi(numRaw)
	if convErr != nil || idx < 0 {
		return 0, "", fmt.Errorf("invalid root token: %s", token)
	}
	if len(parts) == 1 {
		return idx, "", nil
	}
	return idx, trimPath(parts[1]), nil
}

func buildMultiRootLabels(roots []string) []string {
	labels := make([]string, 0, len(roots))
	used := make(map[string]int, len(roots))
	for i, root := range roots {
		trimmed := strings.TrimSpace(root)
		label := path.Base(strings.ReplaceAll(trimmed, "\\", "/"))
		if label == "." || label == "/" || label == "" {
			label = trimmed
		}
		if label == "" {
			label = fmt.Sprintf("root-%d", i+1)
		}
		used[label]++
		if used[label] > 1 {
			label = fmt.Sprintf("%s (%d)", label, used[label])
		}
		labels = append(labels, label)
	}

	// 让根节点显示顺序稳定，避免 UI/扫描波动。
	type pair struct {
		idx int
		val string
	}
	tmp := make([]pair, 0, len(labels))
	for i, v := range labels {
		tmp = append(tmp, pair{idx: i, val: v})
	}
	sort.SliceStable(tmp, func(i, j int) bool {
		return tmp[i].idx < tmp[j].idx
	})
	out := make([]string, 0, len(tmp))
	for _, p := range tmp {
		out = append(out, p.val)
	}
	return out
}
