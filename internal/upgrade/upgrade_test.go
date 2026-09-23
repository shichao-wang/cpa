package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// release stands in for github.com: /releases/latest redirects to the tag (or
// answers with a page when nothing has been published), and the assets hang
// off /releases/download/<tag>/, the same shape the real ones have.
type release struct {
	tag     string
	archive string // archive file name
	blob    []byte
	sums    string // body of checksums.txt; empty means the asset is missing
	none    bool   // no release published at all
}

func (r release) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/"+Repo+"/releases/latest", func(w http.ResponseWriter, req *http.Request) {
		if r.none {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html>There aren't any releases here</html>")
			return
		}
		http.Redirect(w, req, "/"+Repo+"/releases/tag/"+r.tag, http.StatusFound)
	})
	mux.HandleFunc("/"+Repo+"/releases/download/"+r.tag+"/"+r.archive, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(r.blob)
	})
	mux.HandleFunc("/"+Repo+"/releases/download/"+r.tag+"/checksums.txt", func(w http.ResponseWriter, req *http.Request) {
		if r.sums == "" {
			http.NotFound(w, req)
			return
		}
		_, _ = io.WriteString(w, r.sums)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// tarball builds a release archive with files at their published paths.
func tarball(t *testing.T, dir string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{
			Name:     dir + "/" + name,
			Mode:     0o755,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeBinary is a stand-in for the cpa binary. An upgrade runs whatever it
// downloaded before installing it, so the fixture has to be something the
// kernel will actually execute.
func fakeBinary(tag string) string {
	return "#!/bin/sh\necho \"cpa " + tag + "\"\n"
}

// sumsFor builds the checksums.txt body a release carries for one archive.
func sumsFor(blob []byte, name string) string {
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:]) + "  " + name + "\n"
}

// existingFile lays down a stand-in for the cpa being upgraded.
func existingFile(t *testing.T, dir string) string {
	t.Helper()
	dest := filepath.Join(dir, "cpa")
	if err := os.WriteFile(dest, []byte("old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dest
}

// published builds a complete, valid release of v9.9.9 for darwin/arm64.
func published(t *testing.T) (release, string, []byte) {
	t.Helper()
	const tag = "v9.9.9"
	dir := "cpa_9.9.9_darwin_arm64"
	archive := dir + ".tar.gz"
	blob := tarball(t, dir, map[string]string{
		"cpa":       fakeBinary(tag),
		"README.md": "docs\n",
	})
	return release{tag: tag, archive: archive, blob: blob, sums: sumsFor(blob, archive)}, archive, blob
}

// The whole path in one go: redirect, download, checksum, extract, smoke test,
// rename into place.
func TestRunInstallsTheLatestRelease(t *testing.T) {
	rel, _, _ := published(t)
	srv := rel.server(t)
	dir := t.TempDir()
	dest := existingFile(t, dir)

	var out bytes.Buffer
	res, err := Run(context.Background(), Options{
		Current: "v9.9.8",
		Dest:    dest,
		Base:    srv.URL,
		Out:     &out,
		GOOS:    "darwin",
		GOARCH:  "arm64",
	})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if !res.Updated {
		t.Errorf("Updated = false, want true (output: %s)", out.String())
	}
	if res.Latest != "v9.9.9" || !res.Available {
		t.Errorf("Latest = %q, Available = %v; want v9.9.9 and true", res.Latest, res.Available)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != fakeBinary("v9.9.9") {
		t.Errorf("dest holds %q, want the downloaded binary", got)
	}
	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("dest mode = %v, want 0755", fi.Mode().Perm())
	}
	// The staged copy is renamed into place, so nothing is left beside it.
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Errorf("temp files left next to dest: %v (%v)", entries, err)
	}
}

// A source build's version (git describe) says nothing about whether a release
// is newer, so replacing one is the user's call to make.
func TestRunRefusesASourceBuild(t *testing.T) {
	rel, _, _ := published(t)
	srv := rel.server(t)
	dest := existingFile(t, t.TempDir())

	var out bytes.Buffer
	_, err := Run(context.Background(), Options{
		Current: "v9.9.8-3-gabc1234-dirty",
		Dest:    dest,
		Base:    srv.URL,
		Out:     &out,
		GOOS:    "darwin",
		GOARCH:  "arm64",
	})
	if err == nil {
		t.Fatal("Run replaced a source build, want a refusal")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error %q does not offer --force", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "old\n" {
		t.Errorf("dest holds %q, want the old binary untouched", got)
	}
	if strings.Contains(out.String(), "downloading") {
		t.Errorf("a refused upgrade still downloaded something: %s", out.String())
	}
}

// --force is what makes that replacement possible.
func TestRunForceReplacesASourceBuild(t *testing.T) {
	rel, _, _ := published(t)
	srv := rel.server(t)
	dest := existingFile(t, t.TempDir())

	res, err := Run(context.Background(), Options{
		Current: "v9.9.8-3-gabc1234",
		Dest:    dest,
		Base:    srv.URL,
		Out:     &bytes.Buffer{},
		Force:   true,
		GOOS:    "darwin",
		GOARCH:  "arm64",
	})
	if err != nil {
		t.Fatalf("Run --force: %v", err)
	}
	if !res.Updated {
		t.Error("Updated = false, want true")
	}
	if got, _ := os.ReadFile(dest); string(got) != fakeBinary("v9.9.9") {
		t.Errorf("dest holds %q, want the downloaded binary", got)
	}
}

func TestRunStopsWhenAlreadyLatest(t *testing.T) {
	rel, _, _ := published(t)
	srv := rel.server(t)
	dest := existingFile(t, t.TempDir())

	var out bytes.Buffer
	res, err := Run(context.Background(), Options{
		Current: "v9.9.9",
		Dest:    dest,
		Base:    srv.URL,
		Out:     &out,
		GOOS:    "darwin",
		GOARCH:  "arm64",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Updated || res.Available {
		t.Errorf("Updated = %v, Available = %v; want both false", res.Updated, res.Available)
	}
	if got, _ := os.ReadFile(dest); string(got) != "old\n" {
		t.Errorf("dest holds %q, want the old binary untouched", got)
	}
	if !strings.Contains(out.String(), "latest release") {
		t.Errorf("output does not say it is up to date: %s", out.String())
	}
}

// Asking for a version that is already installed is a reinstall, not a no-op:
// a binary that got damaged or replaced needs to be able to ask for its own
// release back.
func TestRunReinstallsAPinnedVersion(t *testing.T) {
	rel, _, _ := published(t)
	srv := rel.server(t)
	dest := existingFile(t, t.TempDir())

	res, err := Run(context.Background(), Options{
		Current: "v9.9.9",
		Version: "v9.9.9",
		Dest:    dest,
		Base:    srv.URL,
		Out:     &bytes.Buffer{},
		GOOS:    "darwin",
		GOARCH:  "arm64",
	})
	if err != nil {
		t.Fatalf("Run --tag: %v", err)
	}
	if !res.Updated {
		t.Error("Updated = false, want the pinned version reinstalled")
	}
}

func TestRunCheckReportsWithoutInstalling(t *testing.T) {
	rel, _, _ := published(t)
	srv := rel.server(t)
	dest := existingFile(t, t.TempDir())

	var out bytes.Buffer
	res, err := Run(context.Background(), Options{
		Current: "v9.9.8",
		Check:   true,
		Dest:    dest,
		Base:    srv.URL,
		Out:     &out,
		GOOS:    "darwin",
		GOARCH:  "arm64",
	})
	if err != nil {
		t.Fatalf("Run --check: %v", err)
	}
	if !res.Available || res.Updated {
		t.Errorf("Available = %v, Updated = %v; want true and false", res.Available, res.Updated)
	}
	if got, _ := os.ReadFile(dest); string(got) != "old\n" {
		t.Errorf("dest holds %q, want --check to change nothing", got)
	}
	if !strings.Contains(out.String(), "v9.9.9") {
		t.Errorf("output does not name the available release: %s", out.String())
	}
}

// A tampered or truncated download must not be installed.
func TestRunRefusesAWrongChecksum(t *testing.T) {
	rel, archive, _ := published(t)
	rel.sums = strings.Repeat("0", 64) + "  " + archive + "\n"
	srv := rel.server(t)
	dest := existingFile(t, t.TempDir())

	_, err := Run(context.Background(), Options{
		Current: "v9.9.8",
		Dest:    dest,
		Base:    srv.URL,
		Out:     &bytes.Buffer{},
		GOOS:    "darwin",
		GOARCH:  "arm64",
	})
	if err == nil {
		t.Fatal("Run installed a download whose checksum did not match")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error %q does not mention the checksum", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "old\n" {
		t.Errorf("dest holds %q, want the old binary untouched", got)
	}
}

// No checksums.txt for the archive means nothing can be verified.
func TestRunRefusesWhenTheChecksumIsMissing(t *testing.T) {
	rel, _, _ := published(t)
	rel.sums = "deadbeef  cpa_9.9.9_linux_amd64.tar.gz\n"
	srv := rel.server(t)
	dest := existingFile(t, t.TempDir())

	_, err := Run(context.Background(), Options{
		Current: "v9.9.8",
		Dest:    dest,
		Base:    srv.URL,
		Out:     &bytes.Buffer{},
		GOOS:    "darwin",
		GOARCH:  "arm64",
	})
	if err == nil {
		t.Fatal("Run installed an archive with no checksum entry")
	}
	if !strings.Contains(err.Error(), "no entry") {
		t.Errorf("error %q does not say the entry is missing", err)
	}
}

// With no releases published, /releases/latest does not redirect anywhere.
func TestRunReportsWhenThereIsNoRelease(t *testing.T) {
	srv := release{none: true}.server(t)
	dest := existingFile(t, t.TempDir())

	_, err := Run(context.Background(), Options{
		Current: "v9.9.8",
		Dest:    dest,
		Base:    srv.URL,
		Out:     &bytes.Buffer{},
		GOOS:    "darwin",
		GOARCH:  "arm64",
	})
	if err == nil {
		t.Fatal("Run succeeded with no release published")
	}
	if !strings.Contains(err.Error(), "no published release") {
		t.Errorf("error = %q, want it to say no release was found", err)
	}
}

func TestArchiveName(t *testing.T) {
	cases := []struct {
		tag, goos, goarch, want string
	}{
		{tag: "v9.9.9", goos: "darwin", goarch: "arm64", want: "cpa_9.9.9_darwin_arm64.tar.gz"},
		{tag: "9.9.9", goos: "darwin", goarch: "amd64", want: "cpa_9.9.9_darwin_amd64.tar.gz"},
		{tag: "v9.9.9", goos: "linux", goarch: "arm64", want: "cpa_9.9.9_linux_arm64.tar.gz"},
		{tag: "v9.9.9", goos: "linux", goarch: "amd64", want: "cpa_9.9.9_linux_amd64.tar.gz"},
	}
	for _, c := range cases {
		got, err := archiveName(c.tag, c.goos, c.goarch)
		if err != nil {
			t.Errorf("archiveName(%q, %s, %s): %v", c.tag, c.goos, c.goarch, err)
			continue
		}
		if got != c.want {
			t.Errorf("archiveName(%q, %s, %s) = %q, want %q", c.tag, c.goos, c.goarch, got, c.want)
		}
	}
	// No archives are built for these, so an upgrade must say so rather than
	// ask GitHub for a file that is not there.
	for _, c := range [][2]string{{"windows", "amd64"}, {"linux", "386"}, {"darwin", "riscv64"}} {
		if _, err := archiveName("v9.9.9", c[0], c[1]); err == nil {
			t.Errorf("archiveName(%s, %s) succeeded, want a refusal", c[0], c[1])
		}
	}
}

func TestIsRelease(t *testing.T) {
	cases := map[string]bool{
		"v0.2.0":             true,
		"0.2.0":              true,
		"v0.2.0-11-gb7fc3aa": false, // git describe: a source build
		"v0.2.0-dirty":       false,
		"v0.2":               false,
		"nightly":            false,
		"":                   false,
		"v0.2.0.1":           false,
	}
	for in, want := range cases {
		if got := isRelease(in); got != want {
			t.Errorf("isRelease(%q) = %v, want %v", in, got, want)
		}
	}
}

// checksums.txt is written by sha256sum on Linux and shasum on macOS, which
// disagree about the prefix they put on the file name.
func TestChecksumForToleratesPathPrefixes(t *testing.T) {
	const name = "cpa_9.9.9_darwin_arm64.tar.gz"
	sums := "aaaa1111  ./" + name + "\nbbbb2222  *cpa_9.9.9_linux_amd64.tar.gz\n"
	got, err := checksumFor([]byte(sums), name)
	if err != nil {
		t.Fatalf("checksumFor: %v", err)
	}
	if got != "aaaa1111" {
		t.Errorf("checksumFor = %q, want aaaa1111", got)
	}
	if _, err := checksumFor([]byte(sums), "cpa_9.9.9_windows_amd64.tar.gz"); err == nil {
		t.Error("checksumFor found an entry for an archive that is not listed")
	}
}

func TestBinaryFromNeedsTheBinary(t *testing.T) {
	blob := tarball(t, "cpa_9.9.9_darwin_arm64", map[string]string{"README.md": "docs\n"})
	if _, err := binaryFrom(blob, "cpa_9.9.9_darwin_arm64"); err == nil {
		t.Fatal("binaryFrom returned a binary from an archive that has none")
	}
	if _, err := binaryFrom([]byte("not a gzip stream"), "cpa_9.9.9_darwin_arm64"); err == nil {
		t.Fatal("binaryFrom accepted something that is not an archive")
	}
}

// The point of staging: the old binary survives a download that cannot run.
func TestInstallKeepsTheOldBinaryWhenTheNewOneDoesNotRun(t *testing.T) {
	dir := t.TempDir()
	dest := existingFile(t, dir)

	if _, err := install(dest, []byte("this is not a program\n"), "v9.9.9"); err == nil {
		t.Fatal("install accepted a file that does not run")
	}
	if got, _ := os.ReadFile(dest); string(got) != "old\n" {
		t.Errorf("dest holds %q, want the old binary kept", got)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Errorf("staged file left behind: %v (%v)", entries, err)
	}
}

// A binary that runs but is not the release that was asked for is a bug worth
// stopping on, and it is stopped before the old binary is touched.
func TestInstallRefusesABinaryThatIsNotTheRelease(t *testing.T) {
	dir := t.TempDir()
	dest := existingFile(t, dir)

	_, err := install(dest, []byte(fakeBinary("v1.0.0")), "v9.9.9")
	if err == nil {
		t.Fatal("install accepted a binary reporting a different version")
	}
	if !strings.Contains(err.Error(), "reports v1.0.0") {
		t.Errorf("error = %q, want it to name the version found", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "old\n" {
		t.Errorf("dest holds %q, want the old binary kept", got)
	}
}
