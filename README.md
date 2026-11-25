<h1 align="center">MonitorIQ</h1>

<p align="center"><strong>Know before your users do.</strong></p>

<p align="center">Created by <a href="https://github.com/brainslad">Brainslad</a></p>

<p align="center">
Self-hosted uptime and service monitoring for Rocky Linux 8, 9 and 10.<br>
One Go binary · PostgreSQL · dashboard, back-office and public status pages · up to 2 years of history.
</p>

<p align="center">
  <a href="#quick-install-rocky-linux-8--9--10">Install</a> ·
  <a href="#screens">Screens</a> ·
  <a href="#authentication">Authentication</a> ·
  <a href="#configuration">Configuration</a> ·
  <a href="#license">GPL-3.0</a> ·
  <a href="#author">Author</a>
</p>

![MonitorIQ dashboard](docs/screenshots/dashboard-light.png)

## Overview

MonitorIQ watches your websites and network services and tells you, on one screen, what is up, what is slow and what is down. It is built for small and medium teams that want their own monitoring on their own server, without a SaaS subscription or a stack of containers.

- **What it checks:** websites (HTTP/HTTPS), ping, traceroute, DNS records, SMTP servers, TLS certificate expiry and any TCP port.
- **Where you see it:** a live dashboard with KPIs, charts and incident history, a page per monitor with charts from 1 hour to 2 years, and public status pages you can share with customers or embed in your site.
- **How you run it:** one installer sets up PostgreSQL, a hardened systemd service and the firewall. Upgrades are one command, and data retention is capped at 2 years.
- **Who gets in:** local accounts, LDAP / Active Directory or single sign-on (Microsoft Entra ID / Azure AD, Google, Okta, Keycloak…), with optional mandatory two-factor authentication.

### How it works

```
             ┌──────────────── monitoriq (single binary) ────────────────┐
 browsers ──▶│ web server: dashboard · back-office · status pages · API  │
             │ scheduler: runs due checks concurrently (http, ping, …)   │──▶ your hosts and services
             │ maintenance: hourly retention cleanup                     │
             └───────────────────────────────┬───────────────────────────┘
                                             ▼
                               PostgreSQL: results, hourly
                               aggregates, incidents, users
```

## Features

| Check | What it verifies |
|---|---|
| **HTTP/HTTPS website** | Status code (e.g. `200-399`), keyword present/absent, response time (*degraded* above a threshold), valid certificate, redirects |
| **Ping (ICMP)** | Packet loss and min/avg/max RTT (partial loss = degraded) |
| **Traceroute** | Whether the destination is reached, hop count and RTT per hop (UDP or ICMP) |
| **DNS** | A, AAAA, CNAME, MX, TXT and NS records, with optional DNS server and expected value |
| **SMTP** | 220 banner, EHLO, STARTTLS or implicit TLS (465) |
| **TLS certificate expiry** | Days until expiry, chain and hostname; warns (degraded) when fewer than N days remain |
| **Custom TCP port** | Whether the port accepts connections and how fast |

- **Dashboard** with a greeting and summary line, a chart of every check by result with the average response time (24 hours, 7 days or 30 days), a fleet-health card per host, an hour-by-hour availability map, a certificate-renewal tracker, live incidents and a monitor table with status/type filters, search and hour-by-hour availability bars.
- **Sidebar navigation** grouped into Monitoring, Administration and Account, with live counts, a collapsible layout and a quick search (press `/`).
- **Dark and light mode**: follows the operating system by default and can be toggled from the top bar (the choice is remembered in the browser). The light theme uses graphite and steel-blue tones with frosted-glass cards.
- **Network map and IP information**: a world map on the dashboard shows where every monitor points, seen from the monitoring system, and each monitor page shows the path from this server to its target (with traceroute hops) next to the public IP, reverse DNS, network operator (ASN), city, country and coordinates of both ends, the distance and the light-speed minimum round-trip time. Lookups use a local database (free DB-IP Lite or MaxMind GeoLite2), so addresses never leave the server.
- **Monitor groups**: gather monitors by service, team, customer or site, across hosts. Each group has a colour and a description; the *Groups* page shows how many monitors are up and the 24-hour uptime of each group, the monitor list can be arranged by group or by host and filtered on one group, several monitors can be moved to a group at once, and the dashboard shows a card per group (problems first) and a group filter on the monitor table. Status page editors can tick a whole group at once.
- **Monitor page** with 24h/7d/30d/1-year uptime, a latency and availability chart from 1 hour to 2 years, last-check details (certificate, DNS answers, traceroute hops…), events and history.
- **Export to Excel**: download the monitoring data of any period as an `.xlsx` workbook (also opens in LibreOffice and Google Sheets), for all monitors, one group or monitors you pick. It holds a summary per monitor (checks, uptime %, average/min/max response time, incidents, downtime) and, as you choose, daily and hourly figures, the list of incidents with their duration and every single check.
- **Back-office** to manage hosts, monitors (interval, timeout, failures before alerting, pause), status pages, users and retention.
- **Public status pages** (`/status/<name>`) with overall status, 90-day availability bars, incidents from the last 14 days, custom public names and a JSON API (`/api/status/<name>`). They can be embedded in another site with an `<iframe>`.
- **Retention** configurable per data type, with a hard limit of 730 days:
  - raw results of every check (default 90 days);
  - hourly aggregates used by charts and long-term uptime (default 730 days);
  - events/incidents (default 730 days).
  Cleanup runs automatically every hour.
- **E-mail alerts** through any SMTP server (STARTTLS, TLS or a local relay, with authentication): a message when a monitor goes down, recovers or becomes degraded, a daily digest of certificates that expire soon, per-monitor recipients and muting, users who opt in from *My account*, a test button and a log of sent messages.
- **Sign-in options**: local accounts, **LDAP / Active Directory** (with group restriction) and **single sign-on** through OpenID Connect: **Microsoft Entra ID (Azure AD)**, Google Workspace, Okta, Keycloak, Authentik, AD FS…
- **Two-factor authentication** with any authenticator app (TOTP, QR code), 10 single-use recovery codes, and an option to make it mandatory.
- **Security**: bcrypt passwords, server-side sessions, CSRF protection, login and 2FA rate limiting, AES-256-GCM encryption of stored secrets, CSP headers, a hardened systemd unit and an unprivileged service user.
- **Responsive**: every screen works on a phone.

## Screens

### Dashboard

Everything at a glance. The top chart stacks every check by result (operational, degraded, down) with the average response time as a dotted line, and shows the weakest period of the range. The dark card sums up fleet health per host, open incidents and 30-day uptime. Below sit live incidents, an hour-by-hour availability map and a certificate-renewal tracker for TLS and HTTPS monitors, the network map, and the filterable table of all monitors. It refreshes every minute.

| Light mode | Dark mode |
|---|---|
| ![Dashboard, light](docs/screenshots/dashboard-light.png) | ![Dashboard, dark](docs/screenshots/dashboard.png) |

### Monitor details

Uptime for 24 hours, 7 days, 30 days and 1 year, a latency and availability chart from 1 hour to 2 years, the details of the last check (certificate, DNS answers, traceroute hops…), status changes and recent results.

![Monitor details](docs/screenshots/monitor.png)

### Network location

Each monitor page has a map from the monitoring system to the target, with the hops of the last traceroute in between. Beside it: the public and local addresses of this server, and every address the target resolves to, with reverse DNS, network operator (ASN), prefix, city, country, coordinates and time zone. Private and loopback targets are marked as such and drawn at the server's own location. The distance gives the light-speed minimum round-trip time to compare with the measured latency.

| Monitor with its route | Settings → Geolocation |
|---|---|
| ![Network location of a monitor](docs/screenshots/monitor-map.png) | ![Geolocation settings](docs/screenshots/geo-settings.png) |

### Public status pages

A page to share with customers at `/status/<name>`: overall status, 90 days of daily availability, and incidents from the last 14 days. It shows the logo and footer line from **Settings → Branding**. Also available as JSON at `/api/status/<name>` (not linked from the page) and embeddable with an `<iframe>`.

| Light | Dark |
|---|---|
| ![Status page, light](docs/screenshots/status.png) | ![Status page, dark](docs/screenshots/status-dark.png) |

### Back-office

| Hosts | Monitors |
|---|---|
| ![Hosts](docs/screenshots/hosts.png) | ![Monitors](docs/screenshots/monitors.png) |
| **Groups:** status and 24-hour uptime per group | **Moving monitors:** tick monitors and move them to a group, or to a new one |
| ![Groups](docs/screenshots/groups.png) | ![Moving monitors to a group](docs/screenshots/groups-move.png) |
| **Monitor editor:** type, target, interval, timeout, failures before alerting and type-specific options | **Status page editor:** pick monitors, public names and order |
| ![Monitor editor](docs/screenshots/monitor-form.png) | ![Status page editor](docs/screenshots/status-page-form.png) |
| **Users:** sign-in source and 2FA state, reset 2FA | **Settings → Data retention:** 1 to 730 days per data type |
| ![Users](docs/screenshots/users.png) | ![Retention settings](docs/screenshots/settings.png) |
| **Settings → Email:** SMTP server, recipients, which changes to send, certificate digest, test message and log | **Alert e-mail:** what a down alert looks like |
| ![Email settings](docs/screenshots/email-settings.png) | ![Alert e-mail](docs/screenshots/email-alert.png) |

### Sign-in and security

| Sign-in with SSO | Two-factor code |
|---|---|
| ![Sign-in](docs/screenshots/login.png) | ![Two-factor code](docs/screenshots/2fa-login.png) |
| **Mandatory 2FA set-up** | **My account** |
| ![2FA set-up](docs/screenshots/2fa-setup.png) | ![My account](docs/screenshots/account.png) |

**Settings → Authentication:** two-factor policy, LDAP / Active Directory and OpenID Connect single sign-on.

![Authentication settings](docs/screenshots/auth-settings.png)

### On a phone

<p>
  <img src="docs/screenshots/mobile.png" alt="Dashboard on a phone" width="300">
  &nbsp;
  <img src="docs/screenshots/mobile-status.png" alt="Status page on a phone" width="300">
</p>

## Quick install (Rocky Linux 8 / 9 / 10)

### Option A: release package (recommended)

```bash
# Replace vX.Y.Z with the latest version from https://github.com/brainslad/monitoriq/releases
curl -LO https://github.com/brainslad/monitoriq/releases/download/vX.Y.Z/monitoriq-vX.Y.Z-linux-amd64.tar.gz
tar xzf monitoriq-vX.Y.Z-linux-amd64.tar.gz
cd monitoriq-vX.Y.Z-linux-amd64
sudo ./install.sh --open-firewall
```

### Option B: from source

```bash
sudo dnf install -y git
git clone https://github.com/brainslad/monitoriq.git
cd monitoriq
sudo ./install.sh --open-firewall      # installs Go (golang) and builds automatically
```

When it finishes, the installer prints the URL and the initial password of the `admin` user:

```
MonitorIQ installed successfully.
  URL:           http://192.0.2.10:8080
  Username:      admin
  Password:      9fK2…   (change it under Users after the first login)
```

### What the installer does

1. Installs `iputils` and `traceroute`.
2. Installs PostgreSQL (stream 16 on EL8/EL9 via dnf modules; native package on EL10), initialises it, creates the `monitoriq` database and role with a random password and enables `scram-sha-256` authentication on localhost.
3. Uses the bundled binary or builds it (Go ≥ 1.26; an older Go 1.21+ downloads the right toolchain by itself).
4. Creates the `monitoriq` system user, installs `/usr/local/bin/monitoriq`, writes `/etc/monitoriq/monitoriq.env` and the `monitoriq.service` unit.
5. Applies database migrations, creates the `admin` user and starts the service.

Installer options:

| Option | Description |
|---|---|
| `--listen 0.0.0.0:8080` | Web server address and port |
| `--base-url https://status.example.com` | Public URL (used in status page links) |
| `--open-firewall` | Opens the port in firewalld |
| `--db-url postgres://…` | Use an existing PostgreSQL instead of installing a local one |
| `--pg-version 15` | PostgreSQL version to install (default 16) |

**Upgrade:** download the new version and run `sudo ./install.sh` again (from a git clone: `git pull` and then `sudo ./install.sh`, which always rebuilds from source). Configuration and data are kept, migrations are applied automatically, and the installer shows the old and the new version. Browsers pick up the new interface at once: pages link the styles and scripts with a version in the address. A browser that still holds files from a version without this (up to 0.4.0) may need one hard refresh (Ctrl+Shift+R, or Cmd+Shift+R on a Mac).

**Uninstall:** `sudo ./uninstall.sh` (keeps data) or `sudo ./uninstall.sh --purge` (deletes the database and configuration).

## Usage

1. Open `http://<server>:8080` and sign in as `admin`.
2. **Hosts → New host**: enter a name and the address (hostname or IP).
3. Optionally, **Groups → New group** to gather monitors (for example "Web shop" or "Customer A") and tick the monitors that belong to it. A monitor can also be given a group in its editor, or several can be moved at once from the monitor list.
4. Add **monitors** to that host. *Target* is optional: when empty the host address is used (for HTTP, `https://<address>`). *Port* 0 uses the type's default (443 for TLS, 25 for SMTP, 53 for DNS).
5. **Failures before alerting**: with 1, a monitor only turns *Down* after 2 consecutive failures, avoiding false alarms.
6. **Status pages → New**: pick the monitors, their public names and order. The page is served at `/status/<address>`.

### Statuses

- **Operational**: the check passed.
- **Degraded**: working but with problems (slow response, partial packet loss, certificate about to expire). Counts as available for uptime.
- **Down**: the check failed the configured number of times.
- **Unknown**: not checked yet, or paused.

### Authentication

The dashboard and back-office require a sign-in. Status pages and their JSON API are public.
Sessions last 7 days and are stored in the database; cookies are HttpOnly, SameSite=Lax and Secure under HTTPS.
After 10 failed attempts (password or 2FA code) an IP is blocked for 15 minutes.

Everything below is configured under **Settings → Authentication**; secrets (LDAP bind password, OIDC client secret, TOTP keys) are stored encrypted with `MONITORIQ_SECRET_KEY`.

#### Two-factor authentication (2FA)

- Each user turns it on under **My account** (click your username): scan the QR code with Microsoft Authenticator, Google Authenticator, 1Password, Bitwarden, Authy…, type the code and save the 10 **recovery codes** (each works once).
- **Require 2FA for all local and LDAP users**: users without it must set it up at their next sign-in. SSO users rely on the identity provider's MFA (for Entra ID, use Conditional Access).
- Codes cannot be reused, and a sign-in waiting for its code can reach nothing else.
- Lost phone and recovery codes: an administrator clicks **Reset 2FA** on the Users page, or on the server:

```bash
sudo -u monitoriq monitoriq --env /etc/monitoriq/monitoriq.env user reset-2fa jdoe
```

#### LDAP / Active Directory

Users type their directory login and password in the normal sign-in form. MonitorIQ binds with a service account, finds the user with the filter, checks the password by binding as the user and, optionally, checks membership of a group.

| Field | Active Directory | OpenLDAP / FreeIPA |
|---|---|---|
| URL | `ldaps://dc01.example.com:636` | `ldaps://ldap.example.com:636` |
| Bind DN | `CN=svc-monitoriq,OU=Service Accounts,DC=example,DC=com` | `uid=svc-monitoriq,cn=users,cn=accounts,dc=example,dc=com` |
| Base DN | `DC=example,DC=com` | `dc=example,dc=com` |
| User filter | `(&(objectClass=user)(objectCategory=person)(sAMAccountName={username}))` | `(&(objectClass=inetOrgPerson)(uid={username}))` |
| Required group | `CN=MonitorIQ Admins,OU=Groups,DC=example,DC=com` (tick *nested groups* to include sub-groups) | `cn=monitoriq-admins,cn=groups,cn=accounts,dc=example,dc=com` |

- Use `ldaps://` or `ldap://` with StartTLS: passwords must not travel in clear text.
- **Test connection** checks the form values (and looks a user up) before you save.
- Local accounts are checked first, so the built-in `admin` keeps working if the directory is down.
- With *Create users automatically* off, add users on the Users page with **Signs in with: LDAP**; they are linked at their first sign-in.

#### Single sign-on: Microsoft Entra ID (Azure AD)

1. In the Azure portal, **Microsoft Entra ID → App registrations → New registration**.
   Redirect URI (platform *Web*): `https://status.example.com/auth/oidc/callback` (shown on the settings page; set `MONITORIQ_BASE_URL`).
2. **Certificates & secrets → New client secret**, copy its *Value*.
3. In MonitorIQ, **Settings → Authentication → Single sign-on**: paste the **Directory (tenant) ID** (fills the issuer `https://login.microsoftonline.com/<tenant-id>/v2.0`), the **Application (client) ID** and the secret. Tick *Show a single sign-on button*.
4. To limit access to a group: in the app registration, **Token configuration → Add groups claim** (security groups), then put the group's *Object ID* in *Required groups*. Alternatively set *Assignment required* on the Enterprise application and assign users there.

Other providers work the same way with their issuer URL, e.g. `https://accounts.google.com`, `https://<org>.okta.com`, `https://sso.example.com/realms/<realm>` (Keycloak). The username comes from the `preferred_username` claim by default (the UPN for Entra ID); *Allowed e-mail domains* and *Required groups* restrict who may sign in. The login uses the authorization code flow with PKCE, state and nonce.

*Show only the SSO button* hides the password form; it stays reachable at `/login?local=1`. If a misconfiguration locks you out, run `monitoriq auth local-login on` on the server.

### E-mail alerts

Open **Settings → Email**, enter your SMTP server and sender address, tick *Send e-mail* and press **Send test**.

| Provider | Host | Port | Connection |
|---|---|---|---|
| Microsoft 365 / Exchange Online | `smtp.office365.com` | 587 | STARTTLS (the mailbox needs *Authenticated SMTP* enabled) |
| Google Workspace / Gmail | `smtp.gmail.com` | 587 | STARTTLS, with an app password |
| Your own mail server | `mail.example.com` | 587 or 465 | STARTTLS or TLS |
| Postfix relay on the same server | `localhost` | 25 | No encryption, no username |

- **What is sent:** an alert when a monitor goes **down** and when it **recovers** (on by default), optionally when it becomes **degraded**, and a daily **certificate digest** listing TLS/HTTPS certificates that expire within N days. Changes within 20 seconds are grouped into one message. *Failures before alerting* on each monitor avoids alerts for a single failed check.
- **Who gets it:** the addresses under *Send every alert to*, users who ticked *Email me* under **My account** (local users enter their address there; LDAP and SSO users use the address from the directory), and the extra recipients of each monitor. A monitor can also be muted.
- **Log:** the last messages and any errors are listed on the same page and in the service log (`journalctl -u monitoriq`).
- The SMTP password is encrypted in the database with `MONITORIQ_SECRET_KEY`. Usernames and passwords are only sent over STARTTLS or TLS (except to `localhost`).

### Network map and geolocation

1. Open **Settings → Geolocation** and press **Download DB-IP Lite** (or run `sudo -u monitoriq monitoriq geoip update`). This saves the free [DB-IP Lite](https://db-ip.com/db/lite.php) city and ASN databases (about 130 MB, CC BY 4.0) to `/var/lib/monitoriq/geoip`. Tick *Download the new database edition every month* to keep them current. To use MaxMind GeoLite2 instead, copy `GeoLite2-City.mmdb` and `GeoLite2-ASN.mmdb` there as `city.mmdb` and `asn.mmdb`; the server reloads changed files within an hour.
2. **Public IP of this server:** detected automatically by asking `https://api.ipify.org` every 6 hours (any service that returns the address as plain text works), typed in by hand, or not shown at all.
3. **Location of this server:** taken from the database for its public address. Behind NAT, a VPN or a proxy, set the latitude and longitude by hand.

**Zooming the map:** the maps open zoomed to the locations they show. Zoom with the **+** and **−** buttons, the mouse wheel or a double click (Shift + double click zooms out), and drag to move around. Two more buttons go back to the fitted view and to the whole world. Markers and labels keep their size, and markers that would overlap at the current zoom (for example this server and a target in the same city) are fanned out until there is room for them. Targets in the same place are drawn as one marker with a count; hover it to see their names.

Lookups of targets happen locally: names are resolved with the server's DNS (results cached for 10 minutes), and the database is read from disk. Only the optional public-IP detection and the database download make outbound requests. Maps credit the database as its licence requires.

### Export to Excel

Open **Export** in the sidebar (or **Export** on a monitor's page, which picks that monitor):

1. **Period:** pick the first and last day (both included), or a quick period such as *Last 30 days*, *Last month* or *This year*. Days and times follow the server's time zone.
2. **Monitors:** all monitors, one group, or monitors ticked one by one (ticked monitors win over the group).
3. **Sheets:** *Summary* is always there. *Daily* and *Incidents* are ticked by default; *Hourly* and *Every check* are optional.
4. Press **Download .xlsx**. The file is named after the period, e.g. `monitoriq-2026-11-01_2026-11-30.xlsx`.

Summary, daily and hourly figures come from the hourly aggregates, so they reach back as far as that retention (730 days by default). *Every check* reads the raw results, which are kept for less time (90 days by default); one export holds at most 3,000,000 checks and moves to a new sheet every 1,048,575 rows, Excel's limit. Uptime is `(up + degraded) / checks`, the same as on the dashboard; downtime is the time a monitor spent *down* within the period. Dates are real Excel dates and figures are numbers, so the sheets can be sorted, filtered and charted straight away. Two exports run at a time at most.

### Customising the dashboard

- **Display name:** each user can set a display name under **My account**, and administrators can set it under **Users** (also when creating a user). It is shown in the menu and in the dashboard greeting ("Good morning, Jane Doe"). Without one, the greeting uses the first part of the username. A name set in MonitorIQ is kept when LDAP or SSO report another one at sign-in.
- **Footer:** **Settings → Branding** sets a line shown at the bottom of every page (including the sign-in page and the public status pages), above the *MonitorIQ by Brainslad* credit, which always stays (see NOTICE). Without it, the footer shows only the credit.
- **Logo:** the same page takes a logo (PNG, JPEG or GIF up to 5 MB, shrunk automatically to at most 600 × 150 px; SVG or WebP up to 512 KB; optionally a second one for the dark theme, and a square symbol for the compact menu, shown at 38 × 38 px when the menu is collapsed; without it the logo is shrunk to fit), shown at a fixed height whatever its size. An SVG logo must be a plain drawing: files with scripts, event handlers (`onload`…), embedded HTML, a DOCTYPE or links to other files are refused. **Reset to the default logos** brings back the MonitorIQ logo. It replaces the MonitorIQ logo in the menu (centred in the menu column), on the sign-in page (shown a little larger, without the slogan) and on the public status pages. In the menu and on the status pages the slogan *Know before your users do.* stays underneath.
- **Drill-down:** click a column of *Checks and response time* to see, for that hour (or 6 hours / day), each monitor's checks by result and response time, and the status changes and failed or degraded checks recorded then. The arrows step to the previous and next column.
- **Sign out:** the red button next to your name at the bottom of the menu.
- **Availability map:** the gear button on the card lets each user pick up to 10 monitors, in order. Free rows are filled with other monitors at random; the random choice stays the same for the day.

### Command line

```bash
monitoriq serve                                # server (used by systemd)
monitoriq migrate                              # apply migrations
monitoriq user set admin                       # create a user / reset its password
monitoriq user list                            # with sign-in source and 2FA state
monitoriq user reset-2fa jdoe                  # turn 2FA off for a user (lost phone)
monitoriq auth local-login on                  # show the password form again (SSO-only lock-out)
monitoriq check http https://example.com       # one-off check, handy for diagnostics
monitoriq check tls example.com
monitoriq check dns example.com --config '{"record_type":"MX"}'
monitoriq check smtp mail.example.com 587 --config '{"smtp_tls":"starttls"}'
monitoriq check tcp example.com 3306
monitoriq email test you@example.com           # send a test e-mail with the saved settings
monitoriq email digest                         # send the certificate digest now
monitoriq geoip update                         # download the DB-IP Lite geolocation databases
monitoriq geoip lookup example.com             # location, network and reverse DNS of a host or IP
monitoriq version
```

## Configuration

File `/etc/monitoriq/monitoriq.env` (see [`deploy/monitoriq.env.example`](deploy/monitoriq.env.example)):

| Variable | Default | Description |
|---|---|---|
| `MONITORIQ_DATABASE_URL` | — | PostgreSQL connection |
| `MONITORIQ_LISTEN` | `127.0.0.1:8080` | Web server address |
| `MONITORIQ_BASE_URL` | — | Public URL, e.g. `https://status.example.com` |
| `MONITORIQ_TRUST_PROXY` | `false` | Read the client address and protocol from `X-Forwarded-For`/`-Proto` (behind nginx). Set it to the proxy's address, e.g. `10.61.1.101` or `10.0.0.0/24, 192.0.2.7`, so only requests from the proxy are believed; `true` trusts any sender, which is safe only when MonitorIQ listens on `127.0.0.1` |
| `MONITORIQ_COOKIE_SECURE` | automatic | HTTPS-only cookies. Automatic: on when the browser comes in over HTTPS (directly, through a trusted proxy, or on the host of an `https://` base URL), off for plain HTTP such as `http://<ip>:8080`, so both keep working. `true` or `false` force it |
| `MONITORIQ_SECRET_KEY` | generated by the installer | Encrypts 2FA keys and LDAP/SSO secrets. **Back it up and never change it** |
| `MONITORIQ_DATA_DIR` | `/var/lib/monitoriq` | Where a key file is created when `MONITORIQ_SECRET_KEY` is empty |
| `MONITORIQ_MAX_CONCURRENCY` | `50` | Concurrent checks |
| `MONITORIQ_GEOIP_DIR` | `$MONITORIQ_DATA_DIR/geoip` | Geolocation databases (`city.mmdb`, `asn.mmdb`) |
| `MONITORIQ_DEBUG` | — | `1` logs every HTTP request |

After changing it: `sudo systemctl restart monitoriq`.

### HTTPS with nginx

Running MonitorIQ behind nginx with a Let's Encrypt certificate is recommended. The steps are in [`deploy/nginx/monitoriq.conf`](deploy/nginx/monitoriq.conf). In short: `MONITORIQ_LISTEN=127.0.0.1:8080`, `MONITORIQ_TRUST_PROXY=true`, `setsebool -P httpd_can_network_connect 1` and `certbot --nginx`.

## Database and retention

- Every check stores a raw result in `check_results` and incrementally updates an hourly aggregate in `check_results_hourly`.
- Charts up to 48h use raw results; up to 120 days hourly aggregates; beyond that daily aggregates. This keeps **2 years** of history cheap: a monitor checked every minute uses about 17,500 hourly rows over 2 years, versus over 1 million raw results.
- Periods are set under **Settings** (1 to 730 days). The 2-year limit is enforced in the UI and in the database.

**Backup:** the database plus `/etc/monitoriq/monitoriq.env` (it holds `MONITORIQ_SECRET_KEY`; without it, restored 2FA keys and LDAP/SSO secrets cannot be decrypted).

```bash
sudo -u postgres pg_dump -Fc monitoriq > monitoriq-$(date +%F).dump
# restore: sudo -u postgres pg_restore -d monitoriq --clean monitoriq-YYYY-MM-DD.dump
```

## Development

Requirements: Go ≥ 1.26 and PostgreSQL ≥ 12.

```bash
createdb monitoriq
cat > monitoriq.env <<'EOF'
MONITORIQ_DATABASE_URL=postgres://localhost/monitoriq?sslmode=disable
MONITORIQ_LISTEN=127.0.0.1:8080
EOF
make test
# optional: LDAP integration test against a throw-away OpenLDAP
#   docker run -d --name ldap -p 389:389 -e LDAP_DOMAIN=example.org -e LDAP_ADMIN_PASSWORD=adminpw osixia/openldap:1.5.0
#   docker exec -i ldap ldapadd -x -H ldap://localhost -D cn=admin,dc=example,dc=org -w adminpw < internal/auth/testdata/seed.ldif
#   MIQ_LDAP_URL=ldap://127.0.0.1:389 go test ./internal/auth -run LDAP
make run                 # http://127.0.0.1:8080 — with no users, 'admin' is created and its password is logged
make dist VERSION=v0.1.0 # builds dist/monitoriq-v0.1.0-linux-{amd64,arm64}.tar.gz
```

Layout:

```
cmd/monitoriq/          entry point and command line
internal/auth/          2FA (TOTP), LDAP / Active Directory, OpenID Connect, secret encryption
internal/checks/        checks (http, ping, traceroute, dns, smtp, tls, tcp)
internal/scheduler/     concurrent scheduler and retention cleanup
internal/store/         PostgreSQL: migrations, queries, aggregates
internal/web/           HTTP server, templates, CSS/JS (embedded in the binary)
deploy/                 systemd unit, example configuration and nginx
install.sh              installer for Rocky Linux 8/9/10
```

To publish a release: `git tag v0.4.0 && git push --tags`. The `.github/workflows/release.yml` workflow builds the amd64/arm64 packages and creates the GitHub release.

## Author

MonitorIQ is created and maintained by **[Brainslad](https://github.com/brainslad)**. If you use it, a star on the [repository](https://github.com/brainslad/MonitorIQ) or a mention is appreciated.

## License

Copyright (C) 2026 Brainslad. [GNU GPL v3 or later](LICENSE), with one additional term under section 7(b) (see [NOTICE](NOTICE)).

- You may use, modify and redistribute MonitorIQ, also commercially. Modified versions you distribute must keep the same license and make their source code available.
- **Author attribution must be kept.** The "MonitorIQ by Brainslad" credit with a link to this repository must stay visible in the footer of the web interface, the sign-in pages and the public status pages, together with the copyright notices, the NOTICE file and a credit in the documentation of modified versions.

Bundles [Chart.js](https://www.chartjs.org/) (MIT), the [JetBrains Mono](https://www.jetbrains.com/lp/mono/) and [Inter](https://rsms.me/inter/) fonts (SIL OFL 1.1), and a world map from [Natural Earth](https://www.naturalearthdata.com/) (public domain) via [world-atlas](https://github.com/topojson/world-atlas) (ISC). Reads geolocation databases with [maxminddb-golang](https://github.com/oschwald/maxminddb-golang) (ISC). The DB-IP Lite data it downloads is © DB-IP.com, CC BY 4.0. See [NOTICE](NOTICE).
