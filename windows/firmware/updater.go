package firmware

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"signallight/ota"
)

// Light is the part of ble.Client the updater uses.
type Light interface {
	IsConnected() bool
	CanUpdateFirmware() bool
	FirmwareVersion() string
	UpdateFirmware(image []byte, progress func(done, total int)) error
}

// Job states.
const (
	StateIdle        = ""
	StateDownloading = "downloading"
	StateInstalling  = "installing" // Sending to the light
	StateRebooting   = "rebooting"  // Installed; waiting for the light to reconnect
	StateDone        = "done"
	StateFailed      = "failed"
)

// Job is the current or most recent update.
type Job struct {
	State   string `json:"state"`
	Target  string `json:"target,omitempty"` // Version being installed; "" for a file upload
	Done    int    `json:"done"`             // Bytes the light has acknowledged
	Total   int    `json:"total"`
	Message string `json:"message,omitempty"`
}

func (j Job) active() bool {
	return j.State == StateDownloading || j.State == StateInstalling || j.State == StateRebooting
}

// Status is what the dashboard shows.
type Status struct {
	Current         string   `json:"current"`          // Light's firmware version; "" if not connected or unknown
	CanUpdate       bool     `json:"can_update"`       // Connected, and its firmware supports Bluetooth updates
	Latest          *Release `json:"latest,omitempty"` // Newest release with firmware; nil if none found (yet)
	UpdateAvailable bool     `json:"update_available"` // Latest is newer than Current
	CurrentKnown    bool     `json:"current_known"`    // Current is a release version (not "dev" or unknown)
	Checking        bool     `json:"checking"`
	CheckError      string   `json:"check_error,omitempty"`
	Job             Job      `json:"job"`
}

// Updater checks GitHub for firmware and installs it on the light. Safe for
// concurrent use.
type Updater struct {
	light   Light
	client  *http.Client
	apiBase string
	repo    string

	checkEvery       time.Duration
	reconnectTimeout time.Duration
	pollInterval     time.Duration

	mu        sync.Mutex
	latest    *Release
	checkedAt time.Time
	checking  bool
	checkErr  string
	job       Job
}

func NewUpdater(light Light) *Updater {
	return &Updater{
		light:            light,
		client:           &http.Client{Timeout: 2 * time.Minute},
		apiBase:          "https://api.github.com",
		repo:             "jdtommy/SignalLight",
		checkEvery:       6 * time.Hour,
		reconnectTimeout: 90 * time.Second,
		pollInterval:     time.Second,
	}
}

// Status returns the current state. If the last check for new firmware is stale
// it starts one in the background; the dashboard calls this while it's open, so
// the app only contacts GitHub when someone is looking.
func (u *Updater) Status() Status {
	u.mu.Lock()
	if !u.checking && time.Since(u.checkedAt) > u.checkEvery {
		u.startCheckLocked()
	}
	st := Status{
		Latest:     u.latest,
		Checking:   u.checking,
		CheckError: u.checkErr,
		Job:        u.job,
	}
	u.mu.Unlock()

	st.Current = u.light.FirmwareVersion()
	st.CanUpdate = u.light.CanUpdateFirmware()
	if st.Latest != nil {
		st.UpdateAvailable, st.CurrentKnown = Newer(st.Latest.Version, st.Current)
	} else {
		_, st.CurrentKnown = parseVersion(st.Current)
	}
	return st
}

// CheckNow starts a check for new firmware unless one is running.
func (u *Updater) CheckNow() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.checking {
		u.startCheckLocked()
	}
}

func (u *Updater) startCheckLocked() {
	u.checking = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		rel, err := latestRelease(ctx, u.client, u.apiBase, u.repo)
		u.mu.Lock()
		defer u.mu.Unlock()
		u.checking = false
		u.checkedAt = time.Now() // Also after errors, so a failing check isn't retried every second
		if err != nil {
			log.Printf("[Firmware] Update check failed: %v", err)
			u.checkErr = err.Error()
			return
		}
		u.checkErr = ""
		u.latest = rel
		if rel != nil {
			log.Printf("[Firmware] Latest published firmware: %s", rel.Version)
		}
	}()
}

// InstallLatest downloads the latest release's firmware and installs it. It
// returns once the job has started; follow it with Status.
func (u *Updater) InstallLatest() error {
	u.mu.Lock()
	rel := u.latest
	u.mu.Unlock()
	if rel == nil {
		return errors.New("no published firmware found; check for updates first")
	}
	return u.start(rel.Version, func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		return download(ctx, u.client, rel)
	})
}

// InstallImage installs a firmware image the user supplied (e.g. a local build).
func (u *Updater) InstallImage(image []byte) error {
	if err := ota.ValidateImage(image); err != nil {
		return err
	}
	return u.start("", func() ([]byte, error) { return image, nil })
}

func (u *Updater) start(target string, fetch func() ([]byte, error)) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.job.active() {
		return errors.New("an update is already in progress")
	}
	if !u.light.CanUpdateFirmware() {
		if !u.light.IsConnected() {
			return errors.New("the light isn't connected")
		}
		return errors.New("this light's firmware doesn't support Bluetooth updates yet; flash it over USB once")
	}
	u.job = Job{State: StateDownloading, Target: target}
	previous := u.light.FirmwareVersion()
	go u.run(target, previous, fetch)
	return nil
}

func (u *Updater) run(target, previous string, fetch func() ([]byte, error)) {
	image, err := fetch()
	if err != nil {
		u.finish(StateFailed, err.Error())
		return
	}
	u.update(func(j *Job) { j.State, j.Total = StateInstalling, len(image) })
	log.Printf("[Firmware] Installing %s (%d bytes)...", describe(target), len(image))

	err = u.light.UpdateFirmware(image, func(done, total int) {
		u.update(func(j *Job) { j.Done, j.Total = done, total })
	})
	if err != nil {
		u.finish(StateFailed, "Update failed: "+err.Error()+". The light is still running its previous firmware.")
		return
	}
	u.update(func(j *Job) { j.State, j.Done = StateRebooting, j.Total })

	// The light reboots about a second after confirming. Wait for the old
	// connection to drop, then for the reconnect (which also confirms the new
	// firmware on the light, cancelling its automatic rollback).
	deadline := time.Now().Add(u.reconnectTimeout)
	for u.light.IsConnected() && time.Now().Before(deadline) {
		time.Sleep(u.pollInterval)
	}
	for time.Now().Before(deadline) {
		if u.light.IsConnected() {
			// The version is read just after the connection comes up.
			for i := 0; i < 5 && u.light.FirmwareVersion() == ""; i++ {
				time.Sleep(u.pollInterval)
			}
			u.reconnected(target, previous, u.light.FirmwareVersion())
			return
		}
		time.Sleep(u.pollInterval)
	}
	u.finish(StateFailed, fmt.Sprintf("The light installed the update but hasn't reconnected. "+
		"If the new firmware can't connect, the light returns to %s on its own within 5 minutes.", describePrevious(previous)))
}

func (u *Updater) reconnected(target, previous, now string) {
	switch {
	case now == "":
		u.finish(StateDone, "Update installed. The light reconnected but didn't report a version.")
	case target != "" && now == target:
		u.finish(StateDone, "Updated to "+now+".")
	case target != "" && now == previous:
		u.finish(StateFailed, "The light came back on its previous firmware ("+now+"); the update didn't take.")
	default:
		u.finish(StateDone, "Update installed. The light is now running "+now+".")
	}
}

func (u *Updater) update(f func(*Job)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	f(&u.job)
}

func (u *Updater) finish(state, msg string) {
	log.Printf("[Firmware] %s", msg)
	u.update(func(j *Job) { j.State, j.Message = state, msg })
}

func describePrevious(version string) string {
	if version == "" {
		return "its previous firmware"
	}
	return "firmware " + version
}

func describe(version string) string {
	if version == "" {
		return "the uploaded firmware"
	}
	return "firmware " + version
}
