package promclip

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// BrowseEntry is one directory entry returned by the /browse endpoint.
type BrowseEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size,omitempty"`
}

// BrowseResult is the payload returned for one /browse call.
type BrowseResult struct {
	// Path is the canonical absolute form of the requested directory
	// ("" when listing the synthetic root of Windows drives).
	Path string `json:"path"`
	// Parent is the path the UI's "up" button should navigate to. Empty
	// when there is nowhere to go up (Linux root, Windows synthetic root).
	Parent string `json:"parent"`
	// Sep is the path separator on the server's filesystem ("/" or "\\").
	Sep string `json:"sep"`
	// Drives is non-nil only when listing the Windows synthetic root.
	// Each entry is a drive letter ready to be navigated into (e.g. "C:\").
	Drives []string `json:"drives,omitempty"`
	// Entries are sorted: directories first, then files, both
	// alphabetically (case-insensitive).
	Entries []BrowseEntry `json:"entries"`
}

// Browse lists the contents of dir. On Windows, an empty dir returns the
// synthetic root with the list of accessible drive letters; on other
// platforms an empty dir is treated as "/".
//
// Hidden entries (starting with ".") are kept; permission errors on
// individual children are skipped silently (a single inaccessible file
// shouldn't break listing the rest of a folder).
func Browse(dir string) (*BrowseResult, error) {
	sep := string(filepath.Separator)
	if runtime.GOOS == "windows" && dir == "" {
		return &BrowseResult{
			Sep:    sep,
			Drives: listWindowsDrives(),
		}, nil
	}
	if dir == "" {
		dir = "/"
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("abs path: %w", err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", abs, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", abs)
	}
	parent := ""
	cleanParent := filepath.Dir(abs)
	switch {
	case cleanParent == abs:
		// Already at the root of a filesystem.
		if runtime.GOOS == "windows" {
			// Up from "C:\" lands on the synthetic root (drive list).
			parent = ""
		}
	default:
		parent = cleanParent
	}
	// On Windows, expose the synthetic root via an empty parent so the
	// modal can climb above C:\ to pick another drive.
	if runtime.GOOS == "windows" && parent == abs {
		parent = ""
	}

	rawEntries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("read dir %s: %w", abs, err)
	}
	out := make([]BrowseEntry, 0, len(rawEntries))
	for _, e := range rawEntries {
		entry := BrowseEntry{Name: e.Name(), IsDir: e.IsDir()}
		if !entry.IsDir {
			if info, err := e.Info(); err == nil {
				entry.Size = info.Size()
			}
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return &BrowseResult{
		Path:    abs,
		Parent:  parent,
		Sep:     sep,
		Entries: out,
	}, nil
}

// listWindowsDrives probes A:\ through Z:\ and returns the ones that
// respond to a stat. This is the most portable way to enumerate mounted
// volumes without pulling in syscall-level APIs.
func listWindowsDrives() []string {
	var drives []string
	for c := 'A'; c <= 'Z'; c++ {
		p := string(c) + ":\\"
		if _, err := os.Stat(p); err == nil {
			drives = append(drives, p)
		}
	}
	return drives
}
