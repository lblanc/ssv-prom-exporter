package promclip

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PruneExports keeps the `keep` most recent *.txt.gz files in dir and
// removes the rest. Returns the number of files removed.
//
// keep <= 0 disables pruning. A missing directory is not an error.
// Deletion errors on individual files are returned but do not abort
// the sweep — best-effort is the right policy here, the next call will
// clean up whatever survived.
func PruneExports(dir string, keep int) (int, error) {
	if keep <= 0 {
		return 0, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read %s: %w", dir, err)
	}
	type entry struct {
		path  string
		mtime int64
	}
	var files []entry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".txt.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, entry{
			path:  filepath.Join(dir, e.Name()),
			mtime: info.ModTime().UnixNano(),
		})
	}
	if len(files) <= keep {
		return 0, nil
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].mtime > files[j].mtime
	})
	removed := 0
	var firstErr error
	for _, f := range files[keep:] {
		if err := os.Remove(f.path); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}
