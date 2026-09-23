// Package upgrade replaces the running cpa binary with a published release.
//
// It talks to GitHub the way install.sh does: the newest tag is read off the
// redirect that /releases/latest answers with, not from the API, so an
// unauthenticated upgrade does not start by running into a rate limit. Nothing
// is installed that does not verify against the release's own checksums.txt,
// and the staged binary is run once before it is swapped in, so a download
// that cannot execute never replaces one that can.
//
// Release archives exist for darwin and linux (arm64, amd64).
package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Repo is where cpa's releases are published.
const Repo = "shichao-wang/cpa"

// DefaultBase is the release host. Tests point it at an httptest server.
const DefaultBase = "https://github.com"

// Limits: a cpa archive is a few megabytes. Anything far larger is not one,
// and is worth refusing before it is held in memory.
const (
	maxDownload = 64 << 20
	maxBinary   = 200 << 20
)

// releaseRe matches a plain release tag. Source builds report something git
// describe produced (v2026.09.23-a1b2c3-11-gb7fc3aa, and -dirty on top),
// which says nothing about how it compares to a release.
//
// Two shapes count: the v<date>-<commit> tags releases are cut with now, and
// the vX.Y.Z tags published before that, which installed binaries still
// report as their own version.
var releaseRe = regexp.MustCompile(
	`^v?(?:[0-9]+\.[0-9]+\.[0-9]+|[0-9]{4}\.[0-9]{2}\.[0-9]{2}-[0-9a-f]{6,})$`)

// Options configures a run.
type Options struct {
	// Current is the version the running binary reports.
	Current string
	// Version is the release tag to install; empty means the latest release.
	Version string
	// Check reports what an upgrade would do and changes nothing.
	Check bool
	// Force installs over a binary that is not a release build.
	Force bool
	// Dest is the file to replace; empty means the running executable.
	Dest string
	// Base is the release host; empty means DefaultBase.
	Base string
	// Out receives progress and results; empty means os.Stdout.
	Out io.Writer
	// HTTP is the client to use; empty means one with a generous timeout.
	HTTP *http.Client
	// GOOS and GOARCH default to the running platform. Tests pin them so a
	// test machine builds the same archive name as CI.
	GOOS, GOARCH string
}

// Result reports what happened, or what --check found.
type Result struct {
	Current   string // version of the running binary
	Latest    string // release tag that was considered
	Available bool   // that tag differs from the running version
	Updated   bool   // the binary on disk was replaced
	Path      string // file that was, or would be, replaced
}

// Run resolves a release, verifies it, and puts it where the running cpa is.
func Run(ctx context.Context, o Options) (Result, error) {
	o.applyDefaults()
	res := Result{Current: o.Current}

	dest := o.Dest
	if dest == "" {
		self, err := os.Executable()
		if err != nil {
			return res, fmt.Errorf("cannot tell which file to replace: %w", err)
		}
		// Replace what the path resolves to, so a symlinked entry keeps
		// pointing where it pointed before.
		if real, err := filepath.EvalSymlinks(self); err == nil {
			self = real
		}
		dest = self
	}
	res.Path = dest

	tag := o.Version
	if tag == "" {
		var err error
		if tag, err = latestTag(ctx, o); err != nil {
			return res, err
		}
	}
	res.Latest = tag
	res.Available = normalize(res.Current) != normalize(tag)

	// Pinned to the version already installed: the ask is a reinstall, so the
	// "already latest" shortcut below stays out of the way.
	pinned := o.Version != ""

	if o.Check {
		switch {
		case !res.Available:
			fmt.Fprintf(o.Out, "cpa %s is the latest release\n", o.Current)
		case !isRelease(o.Current):
			fmt.Fprintf(o.Out, "cpa %s is a source build; the latest release is %s\n", o.Current, tag)
		default:
			fmt.Fprintf(o.Out, "cpa %s -> %s available\n", o.Current, tag)
		}
		return res, nil
	}

	if !res.Available && !pinned {
		fmt.Fprintf(o.Out, "cpa %s is the latest release; nothing to do\n", o.Current)
		return res, nil
	}
	if !isRelease(o.Current) && !o.Force {
		return res, fmt.Errorf("cpa %s is not a release build, so there is no telling which of the two is newer;"+
			"\n  install %s anyway with --force, or update from source (git pull && make install)", o.Current, tag)
	}

	archive, err := archiveName(tag, o.GOOS, o.GOARCH)
	if err != nil {
		return res, err
	}
	base := strings.TrimRight(o.Base, "/") + "/" + Repo + "/releases/download/" + tag

	fmt.Fprintf(o.Out, "downloading %s\n", archive)
	blob, err := get(ctx, o, base+"/"+archive)
	if err != nil {
		return res, err
	}
	sums, err := get(ctx, o, base+"/checksums.txt")
	if err != nil {
		return res, err
	}
	want, err := checksumFor(sums, archive)
	if err != nil {
		return res, err
	}
	sum := sha256.Sum256(blob)
	if !strings.EqualFold(want, hex.EncodeToString(sum[:])) {
		return res, fmt.Errorf("checksum mismatch for %s\n  expected %s\n  got      %s\nrefusing to install",
			archive, want, hex.EncodeToString(sum[:]))
	}
	fmt.Fprintln(o.Out, "checksum ok")

	bin, err := binaryFrom(blob, strings.TrimSuffix(archive, ".tar.gz"))
	if err != nil {
		return res, err
	}
	reported, err := install(dest, bin, tag)
	if err != nil {
		return res, err
	}
	res.Updated = true
	fmt.Fprintf(o.Out, "installed %s (%s -> %s)\n", dest, o.Current, reported)
	return res, nil
}

func (o *Options) applyDefaults() {
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 5 * time.Minute}
	}
	if o.Base == "" {
		o.Base = DefaultBase
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.GOARCH == "" {
		o.GOARCH = runtime.GOARCH
	}
}

// latestTag asks GitHub which release is newest. The tag is read off the
// redirect /releases/latest answers with rather than from the API: an upgrade
// should not open with a rate limit, and this is what install.sh does too.
func latestTag(ctx context.Context, o Options) (string, error) {
	url := strings.TrimRight(o.Base, "/") + "/" + Repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot reach %s: %w", url, err)
	}
	defer resp.Body.Close()
	// The tag page itself is not needed; drain it so the connection can be
	// reused, but do not let a huge page into memory.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	final := resp.Request.URL.Path
	tag := path.Base(final)
	if !strings.Contains(final, "/releases/tag/") || !isRelease(tag) {
		return "", fmt.Errorf("no published release found at %s", url)
	}
	return tag, nil
}

// archiveName is the release asset for a platform.
func archiveName(tag, goos, goarch string) (string, error) {
	if goos != "darwin" && goos != "linux" {
		return "", fmt.Errorf("no release archives for %s; install from source with `go install github.com/%s/cmd/cpa@latest`", goos, Repo)
	}
	if goarch != "arm64" && goarch != "amd64" {
		return "", fmt.Errorf("no release archives for %s/%s", goos, goarch)
	}
	return fmt.Sprintf("cpa_%s_%s_%s.tar.gz", normalize(tag), goos, goarch), nil
}

// get fetches one release asset.
func get(ctx context.Context, o Options, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	blob, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	if len(blob) > maxDownload {
		return nil, fmt.Errorf("%s: larger than %d bytes; that is not a cpa archive", url, maxDownload)
	}
	return blob, nil
}

// checksumFor reads the SHA-256 recorded for name in a checksums.txt,
// tolerating the "./" or "*" prefixes the different sha256 tools emit.
func checksumFor(checksums []byte, name string) (string, error) {
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if strings.TrimLeft(fields[1], "./*") == name {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s; refusing to install an unverified binary", name)
}

// binaryFrom pulls the cpa binary out of a release tarball.
func binaryFrom(archive []byte, dir string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("not a gzip archive: %w", err)
	}
	defer gz.Close()
	want := dir + "/cpa"
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || path.Clean(hdr.Name) != want {
			continue
		}
		blob, err := io.ReadAll(io.LimitReader(tr, maxBinary+1))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", want, err)
		}
		if len(blob) == 0 || len(blob) > maxBinary {
			return nil, fmt.Errorf("%s: implausible binary size (%d bytes)", want, len(blob))
		}
		return blob, nil
	}
	return nil, fmt.Errorf("archive has no %s", want)
}

// install puts bin at dest, replacing whatever is there, and returns the
// version the new binary reports.
//
// The new binary is staged next to the target and renamed into place: the
// rename is atomic and on the same filesystem, so a cpa running right now
// keeps reading its own inode instead of a half-written file. It is run once
// before the swap, which both proves it works and confirms it is the release
// it claims to be.
func install(dest string, bin []byte, expect string) (string, error) {
	dir := filepath.Dir(dest)
	f, err := os.CreateTemp(dir, ".cpa-*")
	if err != nil {
		return "", fmt.Errorf("cannot write next to %s: %w\n  cpa was installed there; reinstall with install.sh and CPA_INSTALL_DIR, or with sufficient permissions", dest, err)
	}
	staged := f.Name()
	// No-op once the rename below has moved it into place.
	defer os.Remove(staged)

	if _, err := f.Write(bin); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		return "", err
	}

	reported, err := reportedVersion(staged)
	if err != nil {
		return "", fmt.Errorf("the downloaded binary does not run (%v); keeping %s", err, dest)
	}
	if normalize(reported) != normalize(expect) {
		return "", fmt.Errorf("the downloaded binary reports %s, expected %s; keeping %s", reported, expect, dest)
	}
	if err := os.Rename(staged, dest); err != nil {
		return "", fmt.Errorf("cannot replace %s: %w", dest, err)
	}
	return reported, nil
}

// reportedVersion runs a staged binary and reads back what it says it is.
func reportedVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 || fields[0] != "cpa" {
		return "", fmt.Errorf("unexpected output %q", strings.TrimSpace(string(out)))
	}
	return fields[1], nil
}

// isRelease reports whether v is a plain release tag rather than a source
// build's git describe string.
func isRelease(v string) bool { return releaseRe.MatchString(strings.TrimSpace(v)) }

// normalize drops the leading v so v0.2.1 and 0.2.1 compare equal.
func normalize(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }
