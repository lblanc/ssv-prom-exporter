package promclip

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Upload sends localPath into the bucket described by target and
// returns a share URL.
//
//   - When target.Public is true, the returned URL is a direct
//     <scheme>://<endpoint>/<bucket>/<key> link (or
//     <public_base_url>/<key> when an override is set).
//   - Otherwise a 24h presigned GET URL is returned.
//
// Returns an error if target is not configured (empty Endpoint or
// Bucket) or if the underlying minio-go call fails.
func S3Upload(ctx context.Context, log *slog.Logger, localPath string, target S3Target) (string, error) {
	if target.Endpoint == "" || target.Bucket == "" {
		return "", errors.New("S3 target is not configured (Settings → S3 target)")
	}

	endpoint, secure := normalizeEndpoint(target.Endpoint, target.UseSSL)
	opts := &minio.Options{
		Secure: secure,
		Region: target.Region,
	}
	if target.AccessKey != "" || target.SecretKey != "" {
		opts.Creds = credentials.NewStaticV4(target.AccessKey, target.SecretKey, "")
	}
	if target.PathStyle {
		// MinIO defaults to virtual-hosted style for AWS hostnames and
		// path-style otherwise; this hint forces path-style when the
		// user knows their server needs it (most non-AWS S3 do).
		opts.BucketLookup = minio.BucketLookupPath
	}
	cli, err := minio.New(endpoint, opts)
	if err != nil {
		return "", fmt.Errorf("s3 client: %w", err)
	}

	key := buildObjectKey(target.Prefix, localPath)
	st, err := os.Stat(localPath)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", localPath, err)
	}
	log.Info("s3 upload start", "endpoint", endpoint, "bucket", target.Bucket, "key", key, "size", st.Size())
	if _, err := cli.FPutObject(ctx, target.Bucket, key, localPath,
		minio.PutObjectOptions{ContentType: detectContentType(localPath)}); err != nil {
		return "", fmt.Errorf("s3 put: %w", err)
	}
	log.Info("s3 upload done", "bucket", target.Bucket, "key", key)

	if target.Public {
		return buildPublicURL(target, endpoint, secure, key), nil
	}
	presigned, err := cli.PresignedGetObject(ctx, target.Bucket, key, 24*time.Hour, nil)
	if err != nil {
		return "", fmt.Errorf("s3 presign: %w", err)
	}
	return presigned.String(), nil
}

// S3Test runs a cheap, non-mutating call against target to validate
// credentials and connectivity. Returns a human-readable status string
// on success and an error on failure.
func S3Test(ctx context.Context, target S3Target) (string, error) {
	if target.Endpoint == "" || target.Bucket == "" {
		return "", errors.New("endpoint and bucket are required")
	}
	endpoint, secure := normalizeEndpoint(target.Endpoint, target.UseSSL)
	opts := &minio.Options{Secure: secure, Region: target.Region}
	if target.AccessKey != "" || target.SecretKey != "" {
		opts.Creds = credentials.NewStaticV4(target.AccessKey, target.SecretKey, "")
	}
	if target.PathStyle {
		opts.BucketLookup = minio.BucketLookupPath
	}
	cli, err := minio.New(endpoint, opts)
	if err != nil {
		return "", err
	}
	ok, err := cli.BucketExists(ctx, target.Bucket)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("bucket %q does not exist (or is not visible with the given credentials)", target.Bucket)
	}
	return fmt.Sprintf("bucket %q reachable on %s", target.Bucket, endpoint), nil
}

// normalizeEndpoint strips any leading scheme from raw and infers the
// secure flag when one is present, otherwise honors the explicit useSSL
// preference. minio.New expects a bare "host[:port]".
func normalizeEndpoint(raw string, useSSL bool) (string, bool) {
	r := strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(r, "https://"):
		return strings.TrimPrefix(r, "https://"), true
	case strings.HasPrefix(r, "http://"):
		return strings.TrimPrefix(r, "http://"), false
	}
	return r, useSSL
}

// buildObjectKey joins prefix and the local file's basename so the
// generated key always lives under the configured prefix and never
// contains directory separators leaking from the local path.
func buildObjectKey(prefix, localPath string) string {
	base := localPath
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if prefix == "" {
		return base
	}
	// path.Join cleans up duplicate slashes; key components always use "/".
	return path.Join(prefix, base)
}

// buildPublicURL constructs the share URL for a public bucket. When the
// user configured PublicBaseURL, it wins; otherwise we synthesize a
// path-style URL against the endpoint.
func buildPublicURL(t S3Target, endpoint string, secure bool, key string) string {
	if t.PublicBaseURL != "" {
		base := strings.TrimRight(t.PublicBaseURL, "/")
		return base + "/" + key
	}
	scheme := "http"
	if secure {
		scheme = "https"
	}
	// url.PathEscape mangles "/"; build the path by hand from segments.
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return scheme + "://" + endpoint + "/" + t.Bucket + "/" + strings.Join(parts, "/")
}

// detectContentType picks a sensible Content-Type for the object.
// Exports are gzipped OpenMetrics, so .gz wins by extension.
func detectContentType(p string) string {
	low := strings.ToLower(p)
	switch {
	case strings.HasSuffix(low, ".gz"):
		return "application/gzip"
	case strings.HasSuffix(low, ".txt"):
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}
