// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"mime"
	"net/http"
	"testing"

	"github.com/divbast/ci/harness"
)

// pingBody is exactly what /ping answers once Radarr is up: upstream's
// JSON, compact, as it sends it.
const pingBody = `{"status":"OK"}`

// uiTitle and loginTitle are the titles of the UI's own page and of its
// login page, which only the UI the frontend stage built carries.
const (
	uiTitle    = "<title>Radarr</title>"
	loginTitle = "<title>Login - Radarr</title>"
)

// TestServes checks the app port from a client outside the container,
// through the published port, with no session and no API key, as a fresh
// install meets a visitor: /ping answers 200 with exactly upstream's body;
// / answers 200 with the UI, since a fresh install has no login until its
// first-run setup sets one; the login page answers 200 with the UI's
// HTML; and the API refuses a client without the key with 401.
func TestServes(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	ctx := t.Context()
	s := serveFresh(t, e, image)
	checks := harness.NewChecks(t)

	resp, err := s.Get(ctx, pingPath, nil)
	harness.Answers(t, "GET /ping from the host answers 200", http.StatusOK, resp, err)
	if err == nil {
		harness.Equal(checks, "GET /ping from the host is exactly upstream's body", pingBody, string(resp.Body))
	}

	for _, page := range []struct{ path, what, title string }{
		{"/", "the UI", uiTitle},
		{"/login", "the UI's login page", loginTitle},
	} {
		resp, err = s.Get(ctx, page.path, nil)
		harness.Answers(t, "GET "+page.path+" from the host with no session answers 200", http.StatusOK, resp, err)
		if err != nil {
			continue
		}
		media, _, merr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if merr != nil {
			media = merr.Error()
		}
		harness.Equal(checks, "GET "+page.path+" from the host is HTML", "text/html", media)
		harness.Contains(t, "GET "+page.path+" from the host is "+page.what, page.title, string(resp.Body))
	}

	harness.StatusIs(t, s, "GET /api/v3/system/status from the host without the API key answers 401",
		http.StatusUnauthorized, "/api/v3/system/status")
}
