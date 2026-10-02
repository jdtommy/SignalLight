package firmware

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		newer, known    bool
	}{
		{"1.2.0", "1.1.0", true, true},
		{"1.2.0", "1.2.0", false, true},
		{"1.10.0", "1.9.3", true, true},
		{"1.2.0", "1.3.0", false, true},
		{"v2.0", "1.9.9", true, true},
		{"1.2.0", "1.2.0-rc.1", true, true},
		{"1.2.0", "0.0.0-dev.42", true, true},
		{"1.2.0", "dev", false, false},
		{"1.2.0", "", false, false},
	}
	for _, c := range cases {
		newer, known := Newer(c.latest, c.current)
		if newer != c.newer || known != c.known {
			t.Errorf("Newer(%q, %q) = %v, %v; want %v, %v", c.latest, c.current, newer, known, c.newer, c.known)
		}
	}
}

func testImage() []byte {
	b := make([]byte, 1000)
	b[0] = 0xE9
	return b
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int    `json:"size"`
}

type release struct {
	TagName    string  `json:"tag_name"`
	HTMLURL    string  `json:"html_url"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []asset `json:"assets"`
}

// fakeGitHub serves a releases list plus the firmware files for version 1.3.0.
func fakeGitHub(t *testing.T, image []byte, shaOverride string) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(image)
	sha := hex.EncodeToString(sum[:])
	if shaOverride != "" {
		sha = shaOverride
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("request without User-Agent (GitHub's API rejects those)")
		}
		fw := func(v string) []asset {
			return []asset{
				{Name: "signallight-firmware-" + v + ".bin", URL: srv.URL + "/fw.bin", Size: len(image)},
				{Name: "signallight-firmware-" + v + ".bin.sha256", URL: srv.URL + "/fw.sha256"},
			}
		}
		switch r.URL.Path {
		case "/repos/o/r/releases":
			_ = json.NewEncoder(w).Encode([]release{
				{TagName: "v2.0.0-beta", Prerelease: true, Assets: fw("2.0.0-beta")},
				{TagName: "v1.4.0", Assets: []asset{{Name: "signallight.exe"}}}, // No firmware attached
				{TagName: "v1.3.0", HTMLURL: "https://example/v1.3.0", Assets: fw("1.3.0")},
				{TagName: "v1.2.0", Assets: fw("1.2.0")},
			})
		case "/fw.bin":
			_, _ = w.Write(image)
		case "/fw.sha256":
			_, _ = w.Write([]byte(sha + "  signallight-firmware-1.3.0.bin\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type fakeLight struct {
	mu         sync.Mutex
	connected  bool
	version    string
	canUpdate  bool
	updateErr  error
	comesBack  bool   // Reconnects after a successful update
	newVersion string // Version it reports after reconnecting
	got        []byte
}

func (l *fakeLight) IsConnected() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.connected }
func (l *fakeLight) CanUpdateFirmware() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.connected && l.canUpdate
}
func (l *fakeLight) FirmwareVersion() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.connected {
		return ""
	}
	return l.version
}

func (l *fakeLight) UpdateFirmware(image []byte, progress func(done, total int)) error {
	if l.updateErr != nil {
		return l.updateErr
	}
	progress(len(image)/2, len(image))
	progress(len(image), len(image))
	l.mu.Lock()
	l.got = image
	l.connected = false // Reboots
	l.mu.Unlock()
	if l.comesBack {
		go func() {
			time.Sleep(20 * time.Millisecond)
			l.mu.Lock()
			l.connected, l.version = true, l.newVersion
			l.mu.Unlock()
		}()
	}
	return nil
}

func newTestUpdater(light Light, srv *httptest.Server) *Updater {
	u := NewUpdater(light)
	if srv != nil {
		u.apiBase, u.client = srv.URL, srv.Client()
	} else {
		u.apiBase = "http://127.0.0.1:1" // Unreachable; tests not about checking
	}
	u.repo = "o/r"
	u.reconnectTimeout = 300 * time.Millisecond
	u.pollInterval = 5 * time.Millisecond
	return u
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitJob(t *testing.T, u *Updater) Job {
	t.Helper()
	var j Job
	waitFor(t, "update to finish", func() bool {
		j = u.Status().Job
		return j.State == StateDone || j.State == StateFailed
	})
	return j
}

func TestCheckFindsNewestReleaseWithFirmware(t *testing.T) {
	light := &fakeLight{connected: true, canUpdate: true, version: "1.2.0"}
	u := newTestUpdater(light, fakeGitHub(t, testImage(), ""))
	var st Status
	waitFor(t, "check", func() bool { st = u.Status(); return st.Latest != nil || st.CheckError != "" })
	if st.CheckError != "" {
		t.Fatal(st.CheckError)
	}
	if st.Latest.Version != "1.3.0" || st.Latest.URL != "https://example/v1.3.0" {
		t.Errorf("latest = %+v, want 1.3.0 (skipping the prerelease and the release without firmware)", st.Latest)
	}
	if !st.UpdateAvailable || !st.CurrentKnown {
		t.Errorf("update_available=%v current_known=%v, want both true", st.UpdateAvailable, st.CurrentKnown)
	}
}

func TestInstallLatest(t *testing.T) {
	img := testImage()
	light := &fakeLight{connected: true, canUpdate: true, version: "1.2.0", comesBack: true, newVersion: "1.3.0"}
	u := newTestUpdater(light, fakeGitHub(t, img, ""))
	waitFor(t, "check", func() bool { return u.Status().Latest != nil })

	if err := u.InstallLatest(); err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, u)
	if j.State != StateDone || j.Message != "Updated to 1.3.0." {
		t.Fatalf("job = %+v", j)
	}
	if string(light.got) != string(img) {
		t.Error("light got a different image")
	}
	if j.Done != len(img) || j.Total != len(img) {
		t.Errorf("progress %d/%d, want %d/%d", j.Done, j.Total, len(img), len(img))
	}
}

func TestInstallLatestRejectsBadChecksum(t *testing.T) {
	light := &fakeLight{connected: true, canUpdate: true, version: "1.2.0", comesBack: true, newVersion: "1.3.0"}
	u := newTestUpdater(light, fakeGitHub(t, testImage(), strings.Repeat("ab", 32)))
	waitFor(t, "check", func() bool { return u.Status().Latest != nil })
	if err := u.InstallLatest(); err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, u)
	if j.State != StateFailed || !strings.Contains(j.Message, "checksum") {
		t.Fatalf("job = %+v, want checksum failure", j)
	}
	if light.got != nil {
		t.Error("sent an image that failed its checksum")
	}
}

func TestInstallImageReportsLightError(t *testing.T) {
	light := &fakeLight{connected: true, canUpdate: true, version: "dev", updateErr: errors.New("light reported error: SHA_MISMATCH")}
	u := newTestUpdater(light, nil)
	if err := u.InstallImage(testImage()); err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, u)
	if j.State != StateFailed || !strings.Contains(j.Message, "SHA_MISMATCH") || !strings.Contains(j.Message, "previous firmware") {
		t.Fatalf("job = %+v", j)
	}
}

func TestInstallImageLightNeverReturns(t *testing.T) {
	light := &fakeLight{connected: true, canUpdate: true, version: "1.2.0"}
	u := newTestUpdater(light, nil)
	if err := u.InstallImage(testImage()); err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, u)
	if j.State != StateFailed || !strings.Contains(j.Message, "returns to firmware 1.2.0") {
		t.Fatalf("job = %+v", j)
	}
}

func TestInstallImageFromDevBuild(t *testing.T) {
	light := &fakeLight{connected: true, canUpdate: true, version: "dev", comesBack: true, newVersion: "dev"}
	u := newTestUpdater(light, nil)
	if err := u.InstallImage(testImage()); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, u); j.State != StateDone {
		t.Fatalf("job = %+v", j)
	}
}

func TestInstallRefusals(t *testing.T) {
	cases := map[string]*fakeLight{
		"isn't connected": {connected: false, canUpdate: true},
		"doesn't support": {connected: true, canUpdate: false},
	}
	for want, light := range cases {
		if err := newTestUpdater(light, nil).InstallImage(testImage()); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("got %v, want error containing %q", err, want)
		}
	}
	if err := newTestUpdater(&fakeLight{connected: true, canUpdate: true}, nil).InstallImage([]byte{0, 1}); err == nil {
		t.Error("accepted an image without the ESP32 magic byte")
	}
	if err := newTestUpdater(&fakeLight{connected: true, canUpdate: true}, nil).InstallLatest(); err == nil {
		t.Error("InstallLatest worked without a known release")
	}

	// Only one update at a time.
	light := &fakeLight{connected: true, canUpdate: true, version: "1.2.0", comesBack: true, newVersion: "1.2.0"}
	u := newTestUpdater(light, nil)
	u.job.State = StateInstalling
	if err := u.InstallImage(testImage()); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("got %v, want already-in-progress error", err)
	}
}
