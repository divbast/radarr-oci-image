// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/divbast/ci/harness"
)

// notWritable is what Radarr says when it may not write in /config. The
// first thing it writes there is the folder its error reporting keeps,
// /config/Sentry, as it sets up its logging, before any other check: a
// /config it may not write fails there.
const notWritable = "Access to the path '/config/Sentry' is denied."

// unknownFailure is the exit status Radarr's ConsoleApp gives a failure it
// has no name for, a logging setup that failed among them. It then waits
// a second and reads a line from its input, which in a container run
// without -i ends at once, and exits.
const unknownFailure = 1

// nonRecoverableFailure is the exit status Radarr's ConsoleApp gives a
// RadarrStartupException, which -exitimmediately has it exit with at once
// rather than wait an hour for a key no one presses in a container.
const nonRecoverableFailure = 3

// TestRefusesUnwritableConfig runs the image on a /config its user cannot
// write: a volume root owns, mode 0755, as a directory root made and bound
// in, or a volume an image run as root left, would be. The container must
// exit within a minute, with UnknownFailure, rather than serve with
// nowhere to keep its state, or wait to be told to stop, and its log must
// name /config, since the log is all a user sees of why.
func TestRefusesUnwritableConfig(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	vol := configVolume(t, e)
	prepare(t, e, image, asRoot, vol, "root-own")
	harness.Refuses(t, e, refusing(e, image, vol), unknownFailure, "on a /config root owns", notWritable)
}

// TestLogWaitEndsWhenStopped holds the suite's own helper, not anything
// the image guarantees: logged, which the tests that wait for a line in
// Radarr's log share, must return the moment the container stops, as
// it does on a /config root owns, rather than wait out its budget for a
// line that can no longer come. It asserts on the error, not on the time
// taken, which a slow runner would make flaky: had logged waited, its
// error would be Poll's, "still failing after", wrapping the stopped
// container's.
func TestLogWaitEndsWhenStopped(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	ctx := t.Context()
	vol := configVolume(t, e)
	prepare(t, e, image, asRoot, vol, "root-own")
	c, err := e.Start(ctx, t, refusing(e, image, vol))
	if err != nil {
		t.Fatal(err)
	}
	err = logged(ctx, c, "a line Radarr never logs", harness.ServeWithin)
	text := errText(err)
	harness.NewChecks(t).True("logged returns the stopped container's error at once, not its budget's",
		err != nil && strings.Contains(text, "the container stopped") && !strings.Contains(text, "still failing after"),
		harness.Clip(text))
}

// TestLogWaitRereadsAStoppedLog holds logged, the suite's own helper, on
// a container whose answers it scripts, so it needs no image: a container
// found stopped has its log read once more before logged reports that the
// line never came. A container can log the line and stop between the
// look at its log and the look at its state, as Radarr does a second
// after it logs a failed restore, and the line is then in its log for
// good. One that stopped without the line is still the stopped
// container's error, given at once with its log, not the budget's.
func TestLogWaitRereadsAStoppedLog(t *testing.T) {
	t.Parallel()
	const needle = "Restoring Database"
	tests := []struct {
		name string
		// logs is what each read of the log returns, the last again
		// once they run out
		logs      []string
		wantReads int
		wantErr   []string
	}{
		{
			name:      "logged-then-stopped",
			logs:      []string{"Restarting\n", "Restarting\nRestoring Database\n"},
			wantReads: 2,
		},
		{
			name:      "stopped-without-it",
			logs:      []string{"Restarting\n"},
			wantReads: 2,
			wantErr:   []string{"the container stopped, exit status 3", "its log:\nRestarting"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &stoppedContainer{logs: tt.logs}
			err := logged(t.Context(), c, needle, harness.ServeWithin)
			checks := harness.NewChecks(t)
			if tt.wantErr == nil {
				checks.True("logged finds the line in the stopped container's log", err == nil, errText(err))
			}
			for _, want := range tt.wantErr {
				checks.True("logged's error says "+strconv.Quote(want), err != nil && strings.Contains(err.Error(), want), errText(err))
			}
			checks.True("logged returns at once, not at its budget's end",
				err == nil || !strings.Contains(err.Error(), "still failing after"), errText(err))
			harness.Equal(checks, "reads of the log", tt.wantReads, c.reads)
		})
	}
}

// stoppedContainer is a container that has stopped, exit status 3, for
// logged: each read of its log returns the next of logs, and the last
// again once they run out.
type stoppedContainer struct {
	logs  []string
	reads int
}

func (c *stoppedContainer) Logs(context.Context) (string, error) {
	logs := c.logs[min(c.reads, len(c.logs)-1)]
	c.reads++
	return logs, nil
}

func (c *stoppedContainer) Inspect(context.Context) (harness.Inspect, error) {
	return harness.Inspect{State: harness.State{Status: "exited", ExitCode: nonRecoverableFailure}}, nil
}

// pidUnwritable is what Radarr says when it cannot write its PID file.
const pidUnwritable = "Radarr failed to start: Unable to write PID file /config/radarr.pid"

// TestExitsOnStartupFailure runs the image on a /config it can write, in
// which its PID file, radarr.pid, is a directory. Radarr cannot write
// the file, and fails to start with a RadarrStartupException, which
// upstream calls non-recoverable: unless given -exitimmediately, it then
// waits an hour for a key that no one presses in a container. The
// container must exit within a minute with NonRecoverableFailure, the
// status only -exitimmediately gives that wait, and its log must say why.
func TestExitsOnStartupFailure(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	vol := configVolume(t, e)
	prepare(t, e, image, harness.Contract(tmpfsSize), vol, "mkdir", "radarr.pid")
	harness.Refuses(t, e, refusing(e, image, vol), nonRecoverableFailure, "with radarr.pid a directory", pidUnwritable)
}

// refusing is a run of the image under the contract, with vol at /config
// and no network, for a start Radarr must refuse, under engine e, which
// keeps every line it logs.
func refusing(e harness.Engine, image string, vol harness.Mount) *harness.RunSpec {
	return &harness.RunSpec{
		Image:   image,
		Network: "none",
		Confine: harness.Contract(tmpfsSize),
		Mounts:  []harness.Mount{vol},
		Extra:   engineLogs(e),
	}
}

// readOnlyConfig is what Radarr says when nothing is mounted at /config:
// the first write it tries there, the folder its error reporting keeps,
// meets the read-only root.
const readOnlyConfig = "Read-only file system : '/config/Sentry'"

// unmountedWithin is how long Radarr may take to give up on a /config
// nothing is mounted at. Measured, it exited 0.6 s after it started: a
// minute leaves room for the slowest runner.
const unmountedWithin = time.Minute

// TestRefusesNoConfigMount runs the image with nothing mounted at /config,
// as a user who forgets the volume does: the image's own /config is empty
// and 65532's, on a read-only root. The container must exit within a
// minute, with a status that is not 0, naming /config, rather than serve
// with nowhere to keep its state or sit there looking alive. Radarr
// rethrows that error out of its main, so the .NET runtime ends the
// process, with a status of the runtime's choosing: the test asks only
// that it is not 0.
func TestRefusesNoConfigMount(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	ctx := t.Context()
	checks := harness.NewChecks(t)
	c, err := e.Start(ctx, t, &harness.RunSpec{
		Image:   image,
		Network: "none",
		Confine: harness.Contract(tmpfsSize),
		Extra:   engineLogs(e),
	})
	if err != nil {
		t.Fatal(err)
	}
	code, err := c.WaitExit(ctx, unmountedWithin)
	if err != nil {
		checks.True("with no /config mounted, the container exits within "+unmountedWithin.String(), false, harness.WithLog(ctx, c, err).Error())
		return
	}
	checks.True("with no /config mounted, the container exits with a status that is not 0", code != 0, fmt.Sprintf("exit status %d", code))
	harness.LogContains(t, c, "with no /config mounted, the log says why", readOnlyConfig)
}

// migratedConfig is a config.xml as another Radarr image leaves it in
// /config: upstream's port, 7878, no authentication, the built-in updater
// on upstream's stable branch, master, and the API key that install made.
const migratedConfig = `<Config>
  <BindAddress>*</BindAddress>
  <Port>7878</Port>
  <SslPort>9898</SslPort>
  <EnableSsl>False</EnableSsl>
  <LaunchBrowser>True</LaunchBrowser>
  <ApiKey>` + testAPIKey + `</ApiKey>
  <AuthenticationMethod>None</AuthenticationMethod>
  <AuthenticationRequired>Enabled</AuthenticationRequired>
  <Branch>master</Branch>
  <LogLevel>info</LogLevel>
  <UrlBase></UrlBase>
  <InstanceName>Radarr</InstanceName>
  <UpdateMechanism>BuiltIn</UpdateMechanism>
</Config>
`

// mechanismChanged is what Radarr logs as it changes config.xml's update
// mechanism to the Docker mechanism package_info names, once its host has
// started. branchChange opens the line it logs when it changes the
// branch, which it does only for a branch other than the one package_info
// names, master.
const (
	mechanismChanged = "Update mechanism BuiltIn not supported in the current configuration, changing to Docker."
	branchChange     = "External updater uses branch"
)

// TestMigratedConfig runs the image on a /config another image left, whose
// config.xml says port 7878, no authentication, and the built-in updater
// on master, with no API key in the environment. It must serve on 8080,
// which the image sets over config.xml; answer the API with config.xml's
// key; keep no authentication, as config.xml says; once its host has
// started, have changed config.xml's update mechanism to Docker and kept
// its branch, master, which is package_info's; and be healthy, the probe
// reading the API key from config.xml.
func TestMigratedConfig(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	vol := configVolume(t, e)
	prepare(t, e, image, harness.Contract(tmpfsSize), vol, "write", "config.xml", migratedConfig)
	s := serveRadarr(t, e, image, pingPath, nil, vol)
	checks := harness.NewChecks(t)

	harness.StatusIs(t, s, "GET /ping on 8080, from the host, answers 200", http.StatusOK, pingPath)
	err := logged(t.Context(), s.C, hostStarted, harness.ServeWithin)
	checks.True("Radarr's host finishes starting", err == nil, errText(err))
	harness.LogContains(t, s.C, "Radarr says it changes BuiltIn to Docker", mechanismChanged)
	logs, err := s.C.Logs(t.Context())
	checks.True("Radarr's log can be read", err == nil, errText(err))
	harness.Equal(checks, "Radarr changes no branch", 0, strings.Count(logs, branchChange))
	host, ok := callAPI[hostConfig](t, s, "GET /api/v3/config/host with config.xml's key answers 200", http.StatusOK,
		http.MethodGet, "/api/v3/config/host", nil)
	if ok {
		harness.Equal(checks, "config.xml's update mechanism is docker", dockerMechanism, host.UpdateMechanism)
		harness.Equal(checks, "config.xml's branch is master", masterBranch, host.Branch)
		harness.Equal(checks, "Radarr listens on 8080", appPort, host.Port)
	}
	status, ok := callAPI[systemStatus](t, s, "GET /api/v3/system/status with config.xml's key answers 200", http.StatusOK,
		http.MethodGet, "/api/v3/system/status", nil)
	if ok {
		harness.Equal(checks, "the authentication in force is none", "none", status.Authentication)
	}
	healthy(t, s, "the probe, with config.xml's key, exits 0 and prints nothing")
}
