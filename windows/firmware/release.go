// Package firmware finds light firmware on GitHub Releases and runs updates of
// the connected light (see docs/FIRMWARE_UPDATES.md).
package firmware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"signallight/ota"
)

// Release is the newest published release that has firmware attached.
type Release struct {
	Version string `json:"version"`
	URL     string `json:"url"` // Release page
	Size    int    `json:"size"`

	binURL, shaURL string
}

const userAgent = "SignalLight (+https://github.com/jdtommy/SignalLight)"

// latestRelease returns the newest non-draft, non-prerelease release carrying
// signallight-firmware-<version>.bin and its .sha256 (the release workflow's
// names), or nil if there is none. Older releases predate firmware publishing,
// so it looks past releases without firmware.
func latestRelease(ctx context.Context, client *http.Client, apiBase, repo string) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/repos/"+repo+"/releases?per_page=20", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("check GitHub for firmware: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("check GitHub for firmware: %s", resp.Status)
	}

	var releases []struct {
		TagName    string `json:"tag_name"`
		HTMLURL    string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int    `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&releases); err != nil {
		return nil, fmt.Errorf("read GitHub releases: %w", err)
	}

	for _, r := range releases {
		if r.Draft || r.Prerelease {
			continue
		}
		version := strings.TrimPrefix(r.TagName, "v")
		bin := "signallight-firmware-" + version + ".bin"
		rel := Release{Version: version, URL: r.HTMLURL}
		for _, a := range r.Assets {
			switch a.Name {
			case bin:
				rel.binURL, rel.Size = a.URL, a.Size
			case bin + ".sha256":
				rel.shaURL = a.URL
			}
		}
		if rel.binURL != "" && rel.shaURL != "" {
			return &rel, nil
		}
	}
	return nil, nil
}

// download fetches the release's firmware and checks it against the published
// SHA-256 (catches truncated or corrupted downloads; the light re-checks the hash
// it's given as the image arrives).
func download(ctx context.Context, client *http.Client, rel *Release) ([]byte, error) {
	image, err := get(ctx, client, rel.binURL, ota.MaxImageSize)
	if err != nil {
		return nil, fmt.Errorf("download firmware: %w", err)
	}
	shaFile, err := get(ctx, client, rel.shaURL, 1024)
	if err != nil {
		return nil, fmt.Errorf("download firmware checksum: %w", err)
	}
	fields := strings.Fields(string(shaFile)) // sha256sum format: "<hex>  <name>"
	if len(fields) == 0 {
		return nil, errors.New("firmware checksum file is empty")
	}
	want, err := hex.DecodeString(fields[0])
	if err != nil || len(want) != sha256.Size {
		return nil, fmt.Errorf("firmware checksum file is malformed: %q", fields[0])
	}
	if got := sha256.Sum256(image); !bytes.Equal(got[:], want) {
		return nil, errors.New("downloaded firmware doesn't match its published checksum; try again")
	}
	if err := ota.ValidateImage(image); err != nil {
		return nil, err
	}
	return image, nil
}

func get(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("file is larger than %d bytes", limit)
	}
	return b, nil
}

// Newer reports whether version latest is newer than current. known is false
// when current isn't a release version (e.g. "dev" from an Arduino IDE build, or
// empty), so the two can't be compared.
func Newer(latest, current string) (newer, known bool) {
	l, okL := parseVersion(latest)
	c, okC := parseVersion(current)
	if !okL || !okC {
		return false, false
	}
	return compareVersions(l, c) > 0, true
}

type version struct {
	core [3]int
	pre  string // "" for a release; "-rc.1" style suffix otherwise
}

func parseVersion(s string) (version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return version{}, false
	}
	var v version
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return version{}, false
		}
		v.core[i] = n
	}
	v.pre = pre
	return v, true
}

func compareVersions(a, b version) int {
	for i := range a.core {
		if a.core[i] != b.core[i] {
			if a.core[i] > b.core[i] {
				return 1
			}
			return -1
		}
	}
	switch {
	case a.pre == b.pre:
		return 0
	case a.pre == "": // 1.2.0 is newer than 1.2.0-rc.1
		return 1
	case b.pre == "":
		return -1
	}
	return strings.Compare(a.pre, b.pre)
}
