package promclip

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// S3Upload uploads path to S3 via the `proj` CLI (which is responsible
// for resolving the current project and credentials). If public is
// true, the file is pushed to the public-share bucket with a direct
// URL; otherwise it lands in the project's private prefix and the
// returned URL is a presigned 1h URL.
//
// Returns the share URL printed by proj. An error is returned if proj
// is not on $PATH, fails, or prints no URL.
func S3Upload(ctx context.Context, log *slog.Logger, path string, public bool) (string, error) {
	if _, err := exec.LookPath("proj"); err != nil {
		return "", fmt.Errorf("proj not found in PATH: %w", err)
	}

	var (
		stdout, stderr bytes.Buffer
		cmd            *exec.Cmd
	)

	if public {
		cmd = exec.CommandContext(ctx, "proj", "put", "--public", path)
	} else {
		// Two-step: upload, then share with default TTL.
		cmd = exec.CommandContext(ctx, "proj", "put", path)
	}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("proj put: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}

	url := extractURL(stdout.String())
	if public {
		if url == "" {
			return "", fmt.Errorf("proj put --public printed no URL (stdout: %s)", strings.TrimSpace(stdout.String()))
		}
		return url, nil
	}

	// Private mode: need a second call to get a presigned URL.
	// `proj put` prints the uploaded key on stdout (e.g. "uploaded
	// ssv-prom-exporter/foo.txt.gz"); fall back to the basename of path.
	key := extractKey(stdout.String(), path)
	if key == "" {
		return "", fmt.Errorf("proj put printed no key (stdout: %s)", strings.TrimSpace(stdout.String()))
	}
	stdout.Reset()
	stderr.Reset()
	share := exec.CommandContext(ctx, "proj", "share", key, "--ttl", "24h")
	share.Stdout = &stdout
	share.Stderr = &stderr
	if err := share.Run(); err != nil {
		return "", fmt.Errorf("proj share %s: %w (stderr: %s)", key, err, strings.TrimSpace(stderr.String()))
	}
	url = extractURL(stdout.String())
	if url == "" {
		return "", fmt.Errorf("proj share printed no URL (stdout: %s)", strings.TrimSpace(stdout.String()))
	}
	return url, nil
}

// extractURL scans output for the first http(s) URL.
func extractURL(s string) string {
	for _, tok := range strings.Fields(s) {
		if strings.HasPrefix(tok, "https://") || strings.HasPrefix(tok, "http://") {
			return tok
		}
	}
	return ""
}

// extractKey scans output for a token ending in the same basename as
// path; falls back to the basename of path itself.
func extractKey(s, path string) string {
	base := path
	if i := strings.LastIndexAny(path, "/\\"); i >= 0 {
		base = path[i+1:]
	}
	for _, line := range strings.Split(s, "\n") {
		for _, tok := range strings.Fields(line) {
			if strings.HasSuffix(tok, base) && !strings.HasPrefix(tok, "http") {
				return tok
			}
		}
	}
	return base
}
