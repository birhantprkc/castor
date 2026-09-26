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

## Local stream server

While casting, Castor serves the stream to the TV over HTTP on your local network interface, for the length of the cast. It has no authentication, because DLNA renderers can't authenticate: anyone on your network who knows the URL can fetch it. Use Castor on a trusted network.

## Headless Chrome

- Runs headless, separate from your own browser: no access to your profile, cookies, or passwords.
- Presents a fresh randomized fingerprint on each run, unrelated to your real browser.
- Visits only the page you give it, plus whatever that page loads.

## Image provenance

The `ghcr.io/stupside/castor` image is built only by the [release workflow](.github/workflows/_delivery.yml) and ships with an SPDX SBOM and SLSA provenance:

```sh
docker buildx imagetools inspect ghcr.io/stupside/castor:latest --format '{{ json .SBOM }}'
docker buildx imagetools inspect ghcr.io/stupside/castor:latest --format '{{ json .Provenance }}'
```

## Reporting a vulnerability

Open a [GitHub issue](https://github.com/stupside/castor/issues) marked **[security]**, or for sensitive reports, email the maintainer (address on their GitHub profile).
