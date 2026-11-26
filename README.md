<!-- SPDX-License-Identifier: GPL-3.0-only -->
<!-- SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee> -->

# radarr-oci-image

[Radarr](https://github.com/Radarr/Radarr), the movie collection manager,
in an image that runs at zero Linux capabilities, with no new privileges,
on a read-only root filesystem as a non-root user, with no shell in it. It
is built from Radarr's source, and goes through divbast/ci's shared
pipeline: lint, a build for amd64 and arm64, the confinement contract and
its own acceptance suite under Docker and rootless Podman, an image scan,
and a daily publish to `ghcr.io/divbast/radarr`.
[Publishing](#publishing) says when it publishes and under which tags.

The image follows Radarr's stable releases, the ones upstream does not
mark as a pre-release on GitHub. Radarr's develop builds never reach it.

**No image is published yet.** Until the first one reaches
`ghcr.io/divbast/radarr`, the publish job rehearses, as the template's
does, and fails by design, since divbast/ci lets only the template
rehearse. Build the image yourself, as [Running](#running) says.

## Base

The image is a Radarr stable release, built from source: the version and
commit the Containerfile's `RADARR_VERSION` and `RADARR_COMMIT` name,
which are the release and the commit its tag names. It runs on Docker
Hardened Images' [`aspnetcore`](https://hub.docker.com/hardened-images/catalog)
for Debian 13: the .NET runtime, ICU and OpenSSL, no shell and no package
manager. The app is built framework-dependent, so rebuilding the image
patches the runtime. Every stage of the build is also a DHI image, and CI
refuses to build until Docker's signature on each is verified:

- `fetch` checks out the commit and asserts the release's tag still names
  it.
- `frontend` builds the UI with yarn and webpack, from `yarn.lock`, which
  pins every package by its hash, and runs no package's install script.
- `backend` publishes the .NET app, with NuGet checking the signature of
  each package. Upstream keeps no lock file and pins every package by an
  exact version from nuget.org alone, so there is none here. NuGet's
  advisories stay warnings in the build log, where upstream makes them
  errors: the scan already blocks a package with an advisory that the
  image does not ship yet, and an error would hold back every other update
  until upstream moved.
- `probe` builds the health probe, a static Go binary on the standard
  library alone.

Radarr is never patched. The one change the build makes to an upstream
file is to `global.json`, in the backend stage, so that any later .NET SDK
of the same major and minor can build it; that file does not ship.

## Container Features

| Property | Value |
|---|---|
| Capabilities | none (`--cap-drop ALL`) |
| Privileges | no new ones (`--security-opt no-new-privileges`), under the engine's seccomp filter |
| Root filesystem | read-only |
| User | uid/gid 65532 (`nonroot`) |
| Port | 8080 (the UI, the API and `/ping`), and nothing else listens |
| tmpfs required | `/tmp`, which nothing can run from (`--tmpfs /tmp:size=64m,noexec,nosuid,nodev`); [size it](#tmp) to your database |
| Volume | `/config`, which you mount, owned by 65532; and your [media and downloads](#media-and-hardlinks) |
| Processes | one: `Radarr`, as PID 1, which runs `ffprobe` on media files |
| Shell in image | none |
| Package manager | none |
| Outbound network | none needed to serve or be healthy; [Radarr reaches out at rest](#egress-at-rest) |
| Healthy | the shipped probe: `/ping` 200 **and** `GET /api/v3/system/status` reporting Radarr at this image's version |

## Running

Once the first image is published, pull `ghcr.io/divbast/radarr:latest`,
which follows Radarr's stable releases; a version tag, `:<version>` with the Radarr release in place of
`<version>`, pins one. The package page on ghcr.io lists the tags. To
build it yourself: `docker login dhi.io`, since every image the build
starts from is Docker Hardened, then `just setup`, then
`ENGINE=podman just build`, which tags it `localhost/radarr:dev`.
Substitute that tag for `ghcr.io/divbast/radarr:latest` in the command
below.

```bash
podman run -d --name radarr \
  --cap-drop ALL --read-only --read-only-tmpfs=false \
  --security-opt no-new-privileges \
  --user 65532:65532 \
  --tmpfs /tmp:size=64m,noexec,nosuid,nodev \
  -v radarr-config:/config \
  -v /srv/data:/data \
  -p 127.0.0.1:8080:8080 \
  --health-cmd '["/usr/local/bin/divbast-radarr","health"]' \
  ghcr.io/divbast/radarr:latest
```

`--read-only-tmpfs=false` and `--health-cmd` are Podman's alone. Without
the first, Podman mounts writable tmpfs of its own at `/run`, `/var/tmp`
and elsewhere beside the read-only root; without the second, it never
runs the image's health check, as [Health](#health) says. Docker takes
the same command without them, and runs the image's `HEALTHCHECK` itself.

Radarr serves its UI and API on 8080. **A first start on an empty
`/config` leaves authentication method `None` with no user, so anyone who
can reach the port can use Radarr.** That is why the command publishes the
port on the host's loopback alone. Open `http://127.0.0.1:8080` on the
host first: Radarr's first-run prompt sets the authentication method, a
username and a password. Only then publish the port beyond the host, or
put Radarr behind your reverse proxy. [Authentication](#authentication)
says more. A stop (SIGTERM) ends Radarr well inside the engine's timeout,
with exit 0. Restarting it from its UI happens inside the same process, so
the container keeps running.

### `/config`

The image carries an empty `/config` owned by 65532, and declares no
`VOLUME`. A new named volume takes its owner from it on first mount, so
`-v radarr-config:/config` just works. Radarr keeps everything it writes
there: `config.xml`, its databases, logs, backups and posters.

- A `/config` that 65532 cannot write, such as a bind mount root made, or a
  volume an image run as root left, makes Radarr exit 1 at once, with
  `Access to the path '/config/Sentry' is denied.` in its log: the folder
  its error reports use is the first thing it writes there. Give the
  directory, and everything in it, to 65532 instead of running the image
  as root: `chown -R 65532:65532 <dir>` as root, or under rootless Podman
  `podman unshare chown -R 65532:65532 <dir>`. For a Podman bind mount,
  the `:U` option does the same at start: `-v ./config:/config:U`.
- On an SELinux host, such as Fedora or RHEL, a bind mount also needs
  Podman to relabel it, or the container cannot use it: add `:Z`, as in
  `-v ./config:/config:U,Z`. `/config` is Radarr's alone, so the private
  label `Z` fits it.
- With no mount at all, the read-only root refuses Radarr's first write,
  and it exits at once, with `Read-only file system : '/config/Sentry'` in
  its log. Nothing would have been kept.
- A startup failure Radarr calls non-recoverable, such as a PID file it
  cannot write, exits 3. The image passes `-exitimmediately`; without it
  Radarr waits an hour for a key press no one can make in a container.

### Coming from another image

**Back up first.** A newer Radarr migrates an older Radarr's database as
it opens it, and the older Radarr may not open it again: keep the old
`/config`, or a backup from the old install's System, Backup, until you
are sure.

**Move only from a Radarr at this image's version or older.** A database
that a develop or nightly build of Radarr has migrated can hold changes
this release does not know, and Radarr does not check for them before it
starts.

Copy the old `/config` into the new volume or directory, then give it all
to 65532 as above, with `chown -R`: the files another image wrote are
usually its PUID's, such as 1000. Or start fresh, set up the login, and
restore the backup under System, Backup. For a restore, size `/tmp` from
the old `radarr.db` first, as [`/tmp`](#tmp) says: the upload and the
database it holds pass through `/tmp`.

- **After a restore, restart the container.** Radarr applies a restore by
  restarting inside the same process, and it then serves again, but that
  restart can keep reading the database it replaced. A restart of the
  container (`podman restart radarr`, or `docker restart`) opens the
  restored database for certain.
- This image serves on 8080, not Radarr's usual 7878, and its
  `RADARR__SERVER__PORT` wins over the old `config.xml`. Whatever reaches
  Radarr by its address, Prowlarr's Radarr application among them, needs
  the new one, or the old number published to the new port
  (`-p 127.0.0.1:7878:8080`).
- An old `config.xml` that says the `BuiltIn` update mechanism, or a
  branch other than `master`, is switched to Docker and `master` at start,
  as [Updates](#updates) says.
- Your media and download folders must be mounted at the paths the old
  install used, or remapped in Radarr, and writable by 65532:
  [Media and hardlinks](#media-and-hardlinks).

### `/tmp`

`/tmp` is the contract's one tmpfs, and Radarr uses it twice over. A
backup, the weekly scheduled one included, copies the database there
before it zips the backup into `/config`. A restore receives the upload
there, and unpacks the database there. So `/tmp` must hold at least twice
`radarr.db`, or a backup or a restore fails.

64 MiB, the size above and the one the acceptance suite runs under, is far
more than a new install needs: a backup of a database holding 50 tags and
10 custom formats, 618,496 bytes, fitted in 1 MiB. A library of thousands
of movies makes a far larger database. To size it for your own, read the
size of `radarr.db` in the `/config` volume, with `ls -l` on the host path
`docker volume inspect` or `podman volume inspect` gives, double it and
keep to 64 MiB or more. For example, a 300 MiB database wants
`--tmpfs /tmp:size=640m,noexec,nosuid,nodev`. A tmpfs counts against the
container's memory, and a backup fills it only while it runs, so size it
for the backup and no further.

## Configuration

Any `config.xml` key can be set as `RADARR__<SECTION>__<KEY>`, and the
environment wins over the file. The image sets `RADARR__SERVER__PORT=8080`,
which also wins over a `config.xml` brought from another image that says
7878. The keys most needed:

| Variable | What it does |
|---|---|
| `RADARR__AUTH__METHOD` | `None`, a fresh install's; `Forms`, the login page; `External`, behind a proxy that authenticates ([Authentication](#authentication)) |
| `RADARR__AUTH__REQUIRED` | `Enabled`, `DisabledForLocalAddresses` or `DisabledForLocalhost` |
| `RADARR__AUTH__APIKEY` | The API key, over the one in `config.xml` |
| `RADARR__SERVER__URLBASE` | The path a reverse proxy serves Radarr under, such as `/radarr` |
| `RADARR__SERVER__TRUSTEDNETWORKS` | Comma-separated networks, in CIDR form, whose `X-Forwarded-For`, `-Proto` and `-Host` headers Radarr trusts |
| `RADARR__LOG__ANALYTICSENABLED` | `false` turns [analytics](#analytics) off |
| `RADARR__POSTGRES__HOST`, `__PORT`, `__USER`, `__PASSWORD`, `__MAINDB`, `__LOGDB` | [PostgreSQL](#postgresql) in place of SQLite |

### Authentication

**A fresh install starts with the authentication method `None`, and no
user.** Upstream's `AuthenticationRequired` defaults to `Enabled`, but a
method of `None` leaves nothing to require. **With `None`, anyone who can
reach the port can read the API key from the UI, and can download a
backup, which holds `config.xml` and so the key too**, and can use every
indexer and download client you have set up.

So keep the port on the host's loopback, as [Running](#running) does,
and open the UI there first. While the method is `None`, Radarr opens a
first-run prompt that sets the method, a username and a password, and
creates that user. Then publish the port, or put Radarr behind your
proxy. A user is made only there, or later under Settings, General, both
of which save through `PUT /api/v3/config/host`.

**Do not set `RADARR__AUTH__METHOD=Forms` on a fresh install.** With
`Forms` in force and no user, there is nobody to log in as, and the
first-run prompt, which opens only while the method is `None`, never
appears. Set the variable once a user exists, or set it to `External`
when Radarr sits behind a proxy that authenticates every request itself:
`External` makes Radarr let in everyone who reaches it, so nothing but
that proxy may.

The probe does not mind which method is in force: it sends the API key,
which every method accepts.

### Behind a reverse proxy

Set `RADARR__SERVER__TRUSTEDNETWORKS` to the proxy's address or network,
such as `172.18.0.0/16`, so that Radarr takes the client's address and
scheme from the headers the proxy sends, and not the proxy's own. Without
it, every request looks to be from the proxy.

**Do not use `RADARR__AUTH__REQUIRED=DisabledForLocalAddresses` behind a
proxy** unless the proxy sets `X-Forwarded-For` and is listed in
`RADARR__SERVER__TRUSTEDNETWORKS`. Radarr treats a client as local when
its address is local and the request carries no `X-Forwarded-For` header.
A proxy on a private network that sends none makes every proxied client
local, and authentication is skipped for everyone who can reach the proxy.
`RADARR__AUTH__TRUSTCGNATIPADDRESSES=true` counts 100.64.0.0/10, such as
Tailscale's, as local too, and has the same edge.

If the proxy serves Radarr under a path, set the URL base, in the
environment or in the UI. Radarr answers nothing at the bare path but a
redirect. The health probe reads the base from the environment, else from
`config.xml`, so either works, and it never follows a redirect.

### Analytics

Analytics are on, as upstream ships them. They and the error reports
Radarr sends to Sentry go to Servarr. Turn them off in the UI, under
Settings, General, or with `RADARR__LOG__ANALYTICSENABLED=false`.

### Updates

Radarr's own updater is off. The image's `/app/package_info` names Docker
as the update mechanism, on the `master` branch, upstream's branch for
its stable releases, so the UI offers no update, and a `config.xml`
brought from another install that says `BuiltIn`, or another branch, is
switched at start. Radarr's System, Updates page may still say a newer
release exists: it arrives as a new image, and you update by pulling it.

## Egress at rest

The contract's no-egress check holds: the image serves and is healthy on a
network with no route out. Radarr, though, reaches out while it runs, as
upstream ships it:

- `radarr.servarr.com`, for upstream's notices, the time and update
  checks. With no route out, Radarr logs `Failed to retrieve
  notifications` and `Unable to verify system time`, and carries on.
- `api.radarr.video`, for movie metadata, and the posters and fanart it
  names, which Radarr keeps under `/config/MediaCover`. Without it, no
  movie can be added or refreshed.
- Analytics and error reports go to Servarr, until turned off.
- Then whatever indexers, download clients, lists and connections you
  configure.

None of it is needed to serve or to be healthy. One thing does change
with no route out, once 14 days have passed since the image was built,
as they soon do for a published image: Radarr then checks for an update
with every round of its health checks, the check fails, and the round
shows nothing, so the UI's System, Status page is empty rather than
listing the warnings it would. The probe stays healthy; give Radarr a
route to `radarr.servarr.com` to see the page.

## Not supported

There is no shell in the image, so these cannot work:

- Custom Script connections.
- Import Using Script.
- The Script update mechanism.
- Any other interpreted script.

A static binary on a mount still runs, unless you mount it from somewhere
`noexec`. `ffprobe`, which Radarr runs to read media files, is a static
binary in the image itself.

## ffprobe

`/app/bin/ffprobe`, which Radarr runs to read a media file's streams, is
a static FFmpeg 9.0.2 build, from upstream's pin of `Openur.FFprobeStatic`
on NuGet, as upstream's own packages ship it. **The image scan cannot see
it.** A static build carries no package record, so a scan finding against
FFmpeg, or a library linked into it, blocks nothing here. It moves only
when upstream moves that package.

## Media and hardlinks

Radarr imports what your download client finishes into your library, so
it needs both, mounted and writable by 65532. Hardlinking, which imports
instantly and without a second copy, needs the source and the destination
on one filesystem, so give Radarr and the download client one shared
`/data` mount, such as `/data/downloads` and `/data/movies` beside each
other, not a mount each. Under `--cap-drop ALL`, the kernel's
protected-hardlinks rule lets a process link only a file it owns or can
read and write; when 65532 can do neither, Radarr copies the file
instead, so run the download client as 65532 too, or give 65532 its
files. On an SELinux host, a `/data` bind mount several containers share
takes Podman's `:z`, the shared label, not `:Z`, which would keep it to
one container.

## PostgreSQL

Radarr can use PostgreSQL through the `RADARR__POSTGRES__*` variables
above. This image has not been tested with it.

## Health

A server that answers can still be broken, so the image ships its own probe
and declares a `HEALTHCHECK`: `--interval=30s --timeout=10s
--start-period=300s --retries=3`. The start period is long because
Radarr answers nothing until its database migrations finish, and an older
Radarr's database, or an upgrade's on a large library, can be slow; the
first check that passes ends it, so a healthy start loses nothing. Docker
runs the check with no configuration.

Podman does not. Podman's `libimage` reads a healthcheck only out of a
Docker-media-type manifest, and divbast images are published as an OCI
index, so Podman's inspection path never looks at the config's healthcheck
field — [podman/podman#25454](https://github.com/containers/podman/issues/25454)
and [#18904](https://github.com/containers/podman/issues/18904) track it. A
Podman user passes it explicitly, with `--health-cmd` as in
[Running](#running). Under a Quadlet, the equivalent is:

```
HealthCmd=["/usr/local/bin/divbast-radarr","health"]
```

The array above has no leading `"CMD"` on purpose: Podman before 5.8.0
re-splits an array that starts with `"CMD"` into one useless token, so
omitting it is what makes the check work across versions. Podman takes
none of the image's timings either, and by default allows no start
period: add `--health-start-period=300s` (`HealthStartPeriod=300s` in a
Quadlet) if migrations could outlast three of its checks.

| | |
|---|---|
| `/usr/local/bin/divbast-radarr health` | The in-container probe `HEALTHCHECK` runs. Asserts that `/ping` answers 200 with `{"status":"OK"}` **and** that `GET /api/v3/system/status`, with the API key, says `appName` is `Radarr` and `version` is this image's. Prints nothing when healthy; otherwise one line, `ping: <reason>` or `status: <reason>`, for each half that failed, and exits 1 |
| `/usr/local/bin/divbast-radarr version` | Prints the version |

`/ping` runs a database query, so it shows the server and its database
answer; only the status shows the build is the pinned one. The probe reads
the API key from `RADARR__AUTH__APIKEY`, else `/config/config.xml`, and
fails with a line saying so when it finds neither. Radarr makes a key for
itself on its first start, so a plain run needs nothing set.

There is no shell in this image, so a health command given as a plain
string, which Docker and Podman both run through `/bin/sh -c`, can never
pass.

## The pipeline

Three workflows, each a short caller of divbast/ci's reusable workflow of
the same purpose, at one pinned release of it:

- `build.yml` calls `app-check.yml` on every push, pull request, daily slot
  and button run: lint, the image built for amd64 and arm64 once every
  image the Containerfile builds from is verified, the acceptance suite
  against each under Docker and rootless Podman, and the scan. Its
  `required` job is the one check `main`'s ruleset requires. From the daily
  slot and the button, on `main`, it then calls `app-publish.yml`.
- `renovate.yml` runs Renovate, as below.
- `stall.yml` fails, and GitHub emails you, when a Renovate pull request
  has been open more than 14 days or red or pending for more than a day, or
  when Renovate has not succeeded in a day.

The acceptance suite, on top of the contract, checks that Radarr serves
`/ping`, its UI and its login page, refuses the API without a key, stops
on SIGTERM and exits 0, keeps its state across a restart, restarts from
its UI inside the process, reports Docker as its updater on `master`, is
healthy under every authentication method and URL base, refuses an
unwritable or missing `/config`, exits on a startup failure, runs on a
`/config` another image left, makes and serves a backup within the `/tmp`
it is given, restores one through the UI's upload and a restart of the
container, and runs `ffprobe`.

### Publishing

A merge never publishes. The daily slot publishes `main` when `main` moved
since the commit the published `:latest` was built from, and the button, a
manual run of `build` from `main`, publishes at once.

Until the first publish, the publish job calls divbast/ci's
`app-publish.yml` with the template's `image` and `rehearse: true`, which
divbast/ci refuses outside the template, so it fails by design. Once
switched, it calls it with `image: ghcr.io/divbast/radarr`. A publish pushes an index of the amd64 and arm64
images, attests it, verifies the attestation, and only then tags it twice:
with Radarr's version, the Containerfile's `RADARR_VERSION`, and with
`latest`. It ends by checking, as an anonymous client, that both tags are
the attested index.

## Dependency updates

Every input the Containerfile names is pinned by digest: the DHI runtime,
and the DHI Node, .NET SDK and Go images the stages build on. Radarr's own
version and commit are pinned together in two adjacent `ARG`s. The probe
is the standard library alone, so `go.mod` requires nothing but
divbast/ci's harness for its tests.

A pin only moves through a pull request, and
[Renovate](https://docs.renovatebot.com/) opens those. It runs every
morning, after every merge to `main`, whenever CI finishes on one of its
branches and when the Dependency Dashboard issue is edited, from
`.github/workflows/renovate.yml`, as the divbast org's `divbast-renovate`
GitHub App. Its settings are divbast/renovate-config's shared presets
(`:app` among them), and `renovate.json` adds what they cannot know: where
Radarr's version lives. The pull requests Renovate opens here are:

- A new Radarr stable release moves `RADARR_VERSION` and `RADARR_COMMIT`
  together. Renovate follows Radarr's GitHub releases (the
  `github-releases` datasource), and offers only those GitHub does not mark
  as a pre-release, so no develop build is ever offered. It never follows
  Radarr's tags, which name every develop build too. The commit is the one
  the release's tag names, and the build fails if the tag ever names
  another.
- A new DHI image arrives as a pull request that moves its digest, and CI
  verifies Docker's signature before it builds. The .NET SDK and the
  ASP.NET Core runtime move together in one pull request, and a new .NET
  major waits for approval on the Dependency Dashboard.
- A release of divbast/ci arrives as one pull request that moves every
  workflow's pin of it together.
- The toolchain in `mise.toml` moves by pull requests of its own.

Renovate merges its own pull requests once every check on one has passed,
while the repository variable `RADARR_AUTOMERGE` is `enabled`. Three
kinds first wait for someone to approve them on the Dependency Dashboard:
a new .NET major, a new major version of divbast/ci, and a raise of
`go.mod`'s `go` line. A new Radarr major is not among them: it merges once
the acceptance suite passes on it, as any release does, and migrates the
database as it starts, so keep backups.
Any other value of the variable, or none, stops merging from the next run,
and Renovate goes on opening and rebasing pull requests for a person to
merge:

    gh variable set RADARR_AUTOMERGE --body disabled

After a merge it rebases every open update the merge left behind, so each
is tested again against the new `main` before it merges. It merges with a
squash, which GitHub signs, because `main` accepts only signed commits.

The App, the `renovate` environment holding its key, `RADARR_AUTOMERGE`
and this repository's rulesets are all managed from
[divbast/infra](https://github.com/divbast/infra).

## Development

- `just setup` fetches divbast/ci into `.ci/` at the commit the workflows
  pin, then installs the pinned toolchain from `mise.toml`.
- `just build` builds the image locally, without verifying the images it
  builds from, and needs `docker login dhi.io`; `just ci-tools &&
  .ci/bin/divbast-verify-images` does verify them, with cosign from
  `mise.toml`.
- `just scan` reads the published image with
  [skopeo](https://github.com/containers/skopeo), which must be on `PATH`;
  so must it for `just ci`. CI runners already have it.
- `just test` builds, then runs the acceptance suite against the image,
  under `ENGINE`, docker unless it says podman. Under Docker it needs
  `zstd` on `PATH`, since every layer of a Docker Hardened Image is zstd
  and the suite reads the image's layers through it; no `mise.toml` entry
  provides it. CI runners already have it, and a Mac gets it with `brew
  install zstd`.
- `just lint` runs every lint gate, and needs a running Docker daemon, for
  `docker build --check`.
- `just ci` runs every gate, the scan included. Run it before committing.

The recipes that call GitHub, pinact's and zizmor's, read a fine-grained
personal access token from `GITHUB_TOKEN`: divbast/ci's README says which,
and how to pass it.

## AI/LLM Disclosure

This project was developed with significant LLM involvement. Each git commit contains an `Assisted-by:` tag detailing the particular model/tool used.
