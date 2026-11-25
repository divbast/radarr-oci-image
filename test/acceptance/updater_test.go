// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"net/http"
	"testing"

	"github.com/divbast/ci/harness"
)

// dockerMechanism is how Radarr's status names the update mechanism
// that leaves updates to the image, which the image's package_info asks
// for: the UI then offers no update of its own, which in a read-only
// image could only fail.
const dockerMechanism = "docker"

// masterBranch is the branch Radarr reports, which package_info names:
// upstream's own name for its stable releases, the line the image
// follows. The branch only says what Radarr's own update check asks
// upstream about.
const masterBranch = "master"

// TestUpdaterIsDocker checks that Radarr reports Docker as its update
// mechanism, and the master branch.
func TestUpdaterIsDocker(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	s := serveFresh(t, e, image)

	status, ok := callAPI[systemStatus](t, s, "GET /api/v3/system/status answers 200", http.StatusOK,
		http.MethodGet, "/api/v3/system/status", nil)
	if ok {
		checks := harness.NewChecks(t)
		harness.Equal(checks, "Radarr's update mechanism is docker", dockerMechanism, status.PackageUpdateMechanism)
		harness.Equal(checks, "Radarr's branch is master", masterBranch, status.Branch)
	}
}
