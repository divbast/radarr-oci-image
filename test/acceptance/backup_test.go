// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/divbast/ci/harness"
)

// seedTags and seedFormats are how many tags and custom formats the test
// makes before the backup, so the database it backs up holds what the API
// wrote: as many as tmpfsSize was measured with.
const (
	seedTags    = 50
	seedFormats = 10
)

// backupWithin bounds the backup command: it took under a second when
// tmpfsSize was measured.
const backupWithin = time.Minute

// command is a command as Radarr's API gives it.
type command struct {
	ID      int    `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// backup is a backup as GET /api/v3/system/backup lists it.
type backup struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
	Size int    `json:"size"`
}

// TestBackupFitsTmpfs checks that a backup fits the tmpfs the contract
// grants: Radarr copies the database and config.xml into /tmp before it
// zips them into /config. A Backup command sent through the API, after 50
// tags and 10 custom formats, completes; GET /api/v3/system/backup lists
// the backup it made; and downloading it as the UI does answers 200 with
// every byte the listing gives.
func TestBackupFitsTmpfs(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	ctx := t.Context()
	s := serveFresh(t, e, image)
	checks := harness.NewChecks(t)

	made, last := 0, ""
	for i := range seedTags {
		resp, err := api(ctx, s, http.MethodPost, "/api/v3/tag", tag{Label: "seed-" + strconv.Itoa(i)})
		switch {
		case err != nil:
			last = err.Error()
		case resp.Status != http.StatusCreated:
			last = fmt.Sprintf("status %d: %s", resp.Status, harness.Clip(string(resp.Body)))
		default:
			made++
		}
	}
	detail := fmt.Sprintf("%d made", made)
	if made != seedTags {
		detail += "; the last refused with " + last
	}
	checks.True(fmt.Sprintf("POST /api/v3/tag makes %d tags", seedTags), made == seedTags, detail)
	seedCustomFormats(t, s)

	cmd, ok := callAPI[command](t, s, "POST /api/v3/command for a Backup answers 201", http.StatusCreated,
		http.MethodPost, "/api/v3/command", map[string]string{"name": "Backup"})
	if !ok {
		return
	}
	done, err := finished(ctx, s, cmd.ID)
	if err != nil {
		checks.True("the Backup command finishes within "+backupWithin.String(), false, err.Error())
		return
	}
	checks.True("the Backup command completes", done.Status == "completed",
		fmt.Sprintf("status %q: %s", done.Status, done.Message))

	backups, ok := callAPI[[]backup](t, s, "GET /api/v3/system/backup answers 200", http.StatusOK,
		http.MethodGet, "/api/v3/system/backup", nil)
	if !ok {
		return
	}
	checks.True("GET /api/v3/system/backup lists one backup, a manual one", len(backups) == 1 && backups[0].Type == "manual",
		fmt.Sprintf("%+v", backups))
	if len(backups) != 1 {
		return
	}
	resp, err := s.Get(ctx, backups[0].Path, nil)
	harness.Answers(t, "GET "+backups[0].Path+" answers 200", http.StatusOK, resp, err)
	if err == nil {
		harness.Equal(checks, "the download is as long as the listing says", backups[0].Size, len(resp.Body))
	}
}

// customFormat is the part of a custom format the test sends: a name, and
// one release title rule.
type customFormat struct {
	Name           string          `json:"name"`
	Rename         bool            `json:"includeCustomFormatWhenRenaming"`
	Specifications []specification `json:"specifications"`
}

// specification is one rule of a custom format.
type specification struct {
	Name           string  `json:"name"`
	Implementation string  `json:"implementation"`
	Negate         bool    `json:"negate"`
	Required       bool    `json:"required"`
	Fields         []field `json:"fields"`
}

// field is one setting of a rule.
type field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// seedCustomFormats makes seedFormats custom formats through s's API, each
// matching a release title, and fails a check unless every one is made.
func seedCustomFormats(t *testing.T, s *harness.Serving) {
	t.Helper()
	made, last := 0, ""
	for i := range seedFormats {
		n := strconv.Itoa(i)
		resp, err := api(t.Context(), s, http.MethodPost, "/api/v3/customformat", customFormat{
			Name: "seed-" + n,
			Specifications: []specification{{
				Name: "title", Implementation: "ReleaseTitleSpecification",
				Fields: []field{{Name: "value", Value: "seed" + n}},
			}},
		})
		switch {
		case err != nil:
			last = err.Error()
		case resp.Status != http.StatusCreated:
			last = fmt.Sprintf("status %d: %s", resp.Status, harness.Clip(string(resp.Body)))
		default:
			made++
		}
	}
	detail := fmt.Sprintf("%d made", made)
	if made != seedFormats {
		detail += "; the last refused with " + last
	}
	harness.NewChecks(t).True(fmt.Sprintf("POST /api/v3/customformat makes %d custom formats", seedFormats), made == seedFormats, detail)
}

// finished asks for command id every 250 ms until it is no longer queued
// or started, and returns it then, or an error with the last answer once
// backupWithin has passed. Every answer short of that, a failed request
// among them, is one to ask again after.
func finished(ctx context.Context, s *harness.Serving, id int) (command, error) {
	path := "/api/v3/command/" + strconv.Itoa(id)
	var done command
	err := harness.Poll(ctx, backupWithin, 250*time.Millisecond, func(ctx context.Context) error {
		resp, err := api(ctx, s, http.MethodGet, path, nil)
		switch {
		case err != nil:
			return harness.NotYet(err)
		case resp.Status != http.StatusOK:
			return harness.NotYet(fmt.Errorf("status %d", resp.Status))
		}
		var cmd command
		if err := jsonInto(resp, &cmd); err != nil {
			return harness.NotYet(err)
		}
		if cmd.Status == "queued" || cmd.Status == "started" {
			return harness.NotYet(errors.New("command status " + cmd.Status))
		}
		done = cmd
		return nil
	})
	if err != nil {
		return command{}, fmt.Errorf("GET %s: %w", path, err)
	}
	return done, nil
}

// TestRestoresBackup restores a backup as a user moving between images
// does, through the UI's upload: a tag is made and backed up, the backup
// downloaded, the tag deleted, and the backup uploaded to
// /api/v3/system/backup/restore/upload, which Radarr stages in /tmp and
// applies once it restarts. Restarted as the UI then restarts it, in the
// one process, Radarr must serve again, having put the restored database
// in place. Nothing in Radarr clears SQLite's connection pool, which only
// the process ending closes, so the new host can go on reading the
// database it replaced, through a connection the old one left open. A
// restart of the container, which the README tells users to make after a
// restore, opens the restored file whatever the pool held, and the tag
// must be back then.
func TestRestoresBackup(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	ctx := t.Context()
	s := serveFresh(t, e, image)
	checks := harness.NewChecks(t)

	made, ok := callAPI[tag](t, s, "POST /api/v3/tag answers 201", http.StatusCreated,
		http.MethodPost, "/api/v3/tag", tag{Label: "restored"})
	if !ok {
		return
	}
	zip, ok := backupNow(t, s)
	if !ok {
		return
	}
	resp, err := api(ctx, s, http.MethodDelete, "/api/v3/tag/"+strconv.Itoa(made.ID), nil)
	checks.True("DELETE /api/v3/tag/<its id> succeeds", err == nil && resp.Status < 300, fmt.Sprintf("%v %d", err, resp.Status))
	// Without the tag gone, the restore below would have nothing to bring
	// back, and the check after it would pass whatever the restore did
	gone := labels(t, s, "after the delete")
	if gone == nil {
		return
	}
	checks.True("after the delete, the tag is gone", !slices.Contains(gone, "restored"), fmt.Sprintf("tags: %q", gone))

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "radarr_backup.zip")
	if err == nil {
		_, err = part.Write(zip)
	}
	if err == nil {
		err = form.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	resp, err = s.Do(ctx, nil, http.MethodPost, "/api/v3/system/backup/restore/upload",
		http.Header{"X-Api-Key": {testAPIKey}, "Content-Type": {form.FormDataContentType()}}, body.Bytes())
	harness.Answers(t, "POST /api/v3/system/backup/restore/upload with the backup answers 200", http.StatusOK, resp, err)
	if err != nil || resp.Status != http.StatusOK {
		return
	}

	resp, err = api(ctx, s, http.MethodPost, "/api/v3/system/restart", nil)
	harness.Answers(t, "POST /api/v3/system/restart answers 200", http.StatusOK, resp, err)
	err = servesAgain(ctx, s)
	checks.True("Radarr restarts and /ping answers 200 again within "+restartWithin.String(), err == nil, errText(err))
	harness.LogContains(t, s.C, "Radarr says it puts the restored database in place", restoringDatabase)

	if err := s.Restart(ctx); err != nil {
		t.Fatalf("restarting the container after the restore: %v", err)
	}
	if got := labels(t, s, "after the restore and a restart of the container"); got != nil {
		checks.True("after the restore and a restart of the container, the tag is back", slices.Contains(got, "restored"), fmt.Sprintf("tags: %q", got))
	}
}

// restoringDatabase is what Radarr logs as the host a restart builds puts
// the restored database in place of the old one.
const restoringDatabase = "DatabaseRestorationService: Restoring Database"

// backupNow has s back itself up, as a Backup command through the API,
// and returns the backup's bytes as the UI downloads them, or false,
// having failed a check, when it cannot. A fresh install has no login, so
// the download takes no session.
func backupNow(t *testing.T, s *harness.Serving) ([]byte, bool) {
	t.Helper()
	ctx := t.Context()
	checks := harness.NewChecks(t)
	cmd, ok := callAPI[command](t, s, "POST /api/v3/command for a Backup answers 201", http.StatusCreated,
		http.MethodPost, "/api/v3/command", map[string]string{"name": "Backup"})
	if !ok {
		return nil, false
	}
	done, err := finished(ctx, s, cmd.ID)
	if err != nil || done.Status != "completed" {
		checks.True("the Backup command completes within "+backupWithin.String(), false, fmt.Sprintf("%v %+v", err, done))
		return nil, false
	}
	backups, ok := callAPI[[]backup](t, s, "GET /api/v3/system/backup answers 200", http.StatusOK,
		http.MethodGet, "/api/v3/system/backup", nil)
	if !ok || len(backups) == 0 {
		checks.True("GET /api/v3/system/backup lists the backup", false, fmt.Sprintf("%+v", backups))
		return nil, false
	}
	resp, err := s.Get(ctx, backups[0].Path, nil)
	harness.Answers(t, "GET "+backups[0].Path+" answers 200", http.StatusOK, resp, err)
	if err != nil || resp.Status != http.StatusOK {
		return nil, false
	}
	return resp.Body, true
}

// labels returns the labels of every tag s's API lists, as the check
// when, or nil, having failed a check, when it cannot.
func labels(t *testing.T, s *harness.Serving, when string) []string {
	t.Helper()
	tags, ok := callAPI[[]tag](t, s, when+", GET /api/v3/tag answers 200", http.StatusOK,
		http.MethodGet, "/api/v3/tag", nil)
	if !ok {
		return nil
	}
	got := []string{}
	for _, tg := range tags {
		got = append(got, tg.Label)
	}
	return got
}
