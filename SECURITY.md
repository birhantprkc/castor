# Security

How Castor handles secrets, your network, and the browser it launches. Castor collects no telemetry.

## Keeping secrets out of git

Put secrets in `config.local.yaml`, which is git-ignored and overlays `config.yaml`:

```yaml
# config.local.yaml
tmdb:
  api_key: "your-real-key-here"
```

Or use environment variables, which override both files:

```sh
export CASTOR_TMDB__API_KEY="your-real-key-here"
```

If a key lands in `config.yaml` by mistake, revoke it at the issuing service.

## TMDB API key

Used only by the interactive `castor cast` browser, and sent only to TMDB. Manage keys at [themoviedb.org/settings/api](https://www.themoviedb.org/settings/api).

## Media server and API server

- **What devices fetch is open.** The media server serves a device over HTTP until it stops fetching: inside `castor` on your local network interface, or on `server.listen` (and `server.advertise`) for `castor media-server`. Devices can't authenticate, so anyone who reaches it and knows a cast's URL can fetch it.
- **Their APIs take a token.** With `server.token` or `api.token` set, every request to that server's API must carry `Authorization: Bearer <token>`, or it is refused as unauthenticated. Health checks (`grpc.health.v1.Health`) are the one exemption. Without a token, anyone who reaches the port can start casts, and the media server's browser opens any page they name, including pages on your own network.
- **The default listen addresses are open.** `:8410` and `:8411` bind every interface, so set both tokens whenever a server runs on a network. Each logs a warning at startup when it listens beyond loopback without a token.
- **Captured headers stay out of listings.** `ListCasts` returns casts without the headers captured for their streams.
- **Both speak plain HTTP.** Bearer tokens, page URLs, and any headers an integrator supplies on a stream cross between the API server and the media server in the clear. Beyond a network you trust, put each behind a reverse proxy that terminates HTTPS.

## Headless Chrome

- Runs headless on the media server, separate from your own browser: no access to your profile, cookies, or passwords.
- Presents a fresh randomized fingerprint on each run, unrelated to your real browser.
- Visits only the page you give it, plus whatever that page loads; it follows frames only to web pages (`http`, `https`).

## Image provenance

The `ghcr.io/stupside/castor` image is built only by the [release workflow](.github/workflows/_delivery.yml) and ships with an SPDX SBOM and SLSA provenance:

```sh
docker buildx imagetools inspect ghcr.io/stupside/castor:latest --format '{{ json .SBOM }}'
docker buildx imagetools inspect ghcr.io/stupside/castor:latest --format '{{ json .Provenance }}'
```

## Reporting a vulnerability

Open a [GitHub issue](https://github.com/stupside/castor/issues) marked **[security]**, or for sensitive reports, email the maintainer (address on their GitHub profile).
