
<p align="center">
  <img src=".github/images/castor.svg" alt="Castor" width="200"/>
</p>

<p align="center">
  <a href="https://trendshift.io/repositories/86848?utm_source=trendshift-badge&amp;utm_medium=badge&amp;utm_campaign=badge-trendshift-86848" target="_blank" rel="noopener noreferrer"><img src="https://trendshift.io/api/badge/trendshift/repositories/86848/daily?language=Go" alt="stupside%2Fcastor | Trendshift" width="250" height="55"/></a>
</p>

<p align="center">
  <a href="https://github.com/stupside/castor/releases/latest">
    <img src="https://img.shields.io/github/v/release/stupside/castor?style=flat-square" alt="Latest Release">
  </a>
  <a href="https://pkg.go.dev/github.com/stupside/castor">
    <img src="https://img.shields.io/badge/Go-Reference-00ADD8?style=flat-square&logo=go" alt="Go Reference">
  </a>
  <a href="https://github.com/stupside/homebrew-tap/blob/main/Casks/castor.rb">
    <img src="https://img.shields.io/badge/Homebrew-Available-FBB040?style=flat-square&logo=homebrew" alt="Homebrew">
  </a>
  <a href="https://github.com/stupside/castor/blob/main/LICENSE">
    <img src="https://img.shields.io/github/license/stupside/castor?style=flat-square" alt="License">
  </a>
  <a href="https://github.com/stupside/castor/actions">
    <img src="https://img.shields.io/github/actions/workflow/status/stupside/castor/continuous-integration.yml?style=flat-square" alt="Build Status">
  </a>
</p>

# Castor

Smart TVs won't cast arbitrary web video, and screen mirroring is laggy and drops resolution. Castor casts the real stream instead, at full quality, from your terminal.

Point Castor at a web page you are watching, or at a direct stream URL, and it finds the video, extracts the stream, transcodes it for your TV, and casts in real time. It can also resolve an IMDB/TMDB id against sources you configure yourself, and burn in auto-generated subtitles.

To extract, it launches headless Chrome and watches network traffic over the Chrome DevTools Protocol, then runs a short action pipeline to start playback: click the page, navigate into the largest iframe, and click again as a fallback. This works on pages that allow automated playback, and won't work everywhere.

*A general-purpose casting tool: it casts only what you point it at. See [Purpose and disclaimer](#purpose-and-disclaimer).*

<p align="center">
  <img src=".github/images/screen-selection.png" alt="Browsing titles in the castor TUI" width="640"/>
  <br/>
  <sub><em>Run <code>castor cast</code> to browse titles and cast, without leaving the terminal.</em></sub>
</p>


## Quick start

**1. Install** (macOS. [Other options](#installation))

```sh
brew install --cask stupside/tap/castor
```

**2. Find your TV**

```sh
castor scan
```

**3. Save it to `config.yaml`**

```yaml
device:
  name: "Living Room TV"   # exact name from `castor scan`
  type: dlna               # or: chromecast, roku
```

**4. Cast**

```sh
castor cast player https://example.com/watch/some-video
```

That's it. Nothing else is required. See [Configuration](#configuration) for subtitles, quality, and title search.

> `castor scan` found nothing? See [Troubleshooting](#troubleshooting).


## Commands

| Command | What it does |
| --- | --- |
| `castor scan` | List cast targets on your network |
| `castor cast player <url>` | Cast a web page with an embedded video player |
| `castor cast url <url>` | Cast a direct stream or video URL |
| `castor cast` | Browse titles and cast, interactively (needs a [TMDB key](#tmdb-key)) |
| `castor cast movie <id>` | Resolve a movie id against your [sources](#sources) and cast |
| `castor cast episode <id> --season N --episode N` | Same, for a TV episode |

Run `castor --help` for all flags.


## Installation

Castor runs best as a **native binary**: it shares your TV's network, which device discovery needs. It shells out to three tools that must be on your `PATH`.

| Tool | Version | Used for |
| --- | --- | --- |
| **Chrome / Chromium** | Any recent | Headless stream extraction |
| **ffmpeg** | 7.1+ | Transcoding (and stream copy) |
| **ffprobe** | 7.1+ | Source format detection |

> [!IMPORTANT]
> ffmpeg and ffprobe must be **7.1 or newer**. Castor uses flags older builds reject (`-readrate_initial_burst`, stricter HLS extension handling).

### Homebrew (macOS)

```sh
brew install --cask stupside/tap/castor
```

### Build from source

Needs **Go 1.26+** and **cmake**. Clone with submodules, then `make`:

```sh
git clone --recurse-submodules https://github.com/stupside/castor.git
cd castor
make          # builds libwhisper.a, then the castor binary
```

`go install` won't work: the whisper.cpp bindings are cgo, come in through a local `replace`, and need that prebuilt static lib.


## Configuration

Castor reads `config.yaml` from the working directory (or `--config <path>`). **The only required key is `device`.** Everything else has working defaults.

| Key | What it does | Reach for it when |
| --- | --- | --- |
| [`whisper.enable`](#subtitles) | Transcribes the audio and burns subtitles into the video | You want subtitles a source doesn't provide |
| [`resolver.max_height`](#video-quality) | Caps both the stream picked and the encoder output | Your TV isn't 1080p, or you want to save bandwidth |
| [`sources`](#sources) | Turns a movie/episode id into a page URL to extract from | Using `cast movie`, `cast episode`, or the browser |
| [`tmdb.api_key`](#tmdb-key) | Searches titles by name | Using the interactive browser |
| [`cast.delivery`](#forcing-a-relay) | Forces Castor to relay instead of handing over the URL | A device refuses a source and you can't see why |

> [!TIP]
> Keep secrets out of the committed file. Put them in a git-ignored `config.local.yaml` that overlays `config.yaml`, or in `CASTOR_SECTION__FIELD` environment variables. See [SECURITY.md](SECURITY.md).

### Subtitles

Auto-generated subtitles, transcribed with whisper and burned into the video. Models download once to your user cache.

```yaml
whisper:
  enable: true             # off by default
  # language: "fr"         # default: English
  # model_path: ""         # default: ggml-tiny.en (~75 MB, auto-downloaded)
```

> [!NOTE]
> Burn-in applies to devices Castor streams to directly (DLNA). Devices that fetch the stream themselves (Chromecast, Roku) don't get burned-in subtitles.

### Video quality

Set `max_height` to your TV's vertical resolution. It is a maximum on what reaches the device, so it caps the stream Castor picks and everything Castor produces, whether that is the spool it serves a DLNA renderer or the remux it serves a Chromecast.

> [!NOTE]
> It binds the hand-off too. Castor cannot scale a stream it never reads, so a source whose playlist declares a rendition taller than `max_height` is relayed and scaled down instead of being handed to a device that fetches for itself. That relay decodes and re-encodes the whole title, which wants hardware encoding to keep up, so raise `max_height` if you would rather the device play the source as it is. A source that declares no resolution is handed over as before: Castor never measures one on that path, and refusing on a resolution nobody stated would rule out nearly every hand-off.

```yaml
resolver:
  max_height: 2160         # default: 1080
  # hls_timeout: 30s
  # probe_timeout: 30s
  # probe_max_concurrency: 2
  # ffprobe_path: ffprobe
```

### Sources

`cast movie`, `cast episode`, and the interactive browser turn a title id into a page URL. **Castor bundles no sources**. You add your own (sites you are authorized to use).

There is no catalog and no lookup. Castor substitutes the id into your `templates`, prefixes each of your `proxies`, opens the page, and extracts exactly like `cast player`.

```yaml
sources:
  - proxies: ["https://your-source.example"]   # base URLs, tried in order
    templates:
      movie: "/embed/movie/{itemID}"
      episode: "/embed/tv/{itemID}/{season}-{episode}"
```

So `castor cast movie tt12300742` opens `https://your-source.example/embed/movie/tt12300742`.

### TMDB key

Only the interactive browser (`castor cast`) needs one. `cast movie <id>` and friends don't. Get a free key from [themoviedb.org](https://www.themoviedb.org/settings/api):

```yaml
tmdb:
  api_key: "<KEY>"
```

### Forcing a relay

Castor decides per source whether the **device fetches the stream itself** or **Castor relays it**. It hands over the URL only after establishing that the device can actually fetch it: a source that answers only with the request headers Castor captured, one that publishes its audio as a separate rendition, and one taller than `max_height` are all relayed instead. The full set of checks is not reproduced here because it changes; every cast logs the path it took and the reason on its `cast composition` line.

Set `delivery: serve` to relay **always**, for a source a device refuses for some reason Castor cannot see:

```yaml
cast:
  delivery: serve   # "auto" (the default) decides per source
```

Relaying spends this machine's bandwidth and CPU on every cast, so try it as a one-off first:

```sh
CASTOR_CAST__DELIVERY=serve castor cast url <url>
```

Any key works that way, e.g. `CASTOR_RESOLVER__MAX_HEIGHT=720`.


## Supported devices

Run `castor scan` to list what is on your network.

| Protocol | Works with | Status |
| --- | --- | --- |
| **DLNA / UPnP** (`MediaRenderer:1`) | Virtually every smart TV from the last decade (Samsung, LG, Sony Bravia, Panasonic Viera, Philips, Hisense, TCL, VIZIO, Sharp), plus networked players like Kodi, VLC, and Plex | Tested on Samsung |
| **Chromecast** | Google Cast devices | Experimental, untested (contributions welcome) |
| **Roku** | Roku TVs and streaming players (via a sideloaded channel over ECP) | Experimental, untested (contributions welcome) |

### Roku setup

Roku needs **one extra step** the others don't. It isn't a DLNA renderer, so Castor reaches it over Roku's ECP, and since Roku can't play an arbitrary URL from a preinstalled app, Castor ships a tiny channel of its own.

**1. Turn on Developer Mode** (once, by hand: there's no remote API for it)

On the Roku remote press **Home x3, Up x2, Right, Left, Right, Left, Right**, enable developer mode, and set a **web-server password**. The device reboots.

**2. Put the password in your config**

```yaml
device:
  name: "Living Room"   # from `castor scan`
  type: roku
  roku:
    password: "<dev-web-server-password>"   # first cast only
```

Keep it in a git-ignored `config.local.yaml`, or set `CASTOR_DEVICE__ROKU__PASSWORD`.

**3. Cast.** Castor sideloads its channel automatically; later casts reuse it.

Already published the channel to your account? Set `device.roku.app_id` to its numeric id instead: no dev mode, no password.

> [!NOTE]
> Roku playback is a sliding-window HLS stream and stays roughly 30 s behind the live edge. Expect a longer start delay than DLNA.


## Troubleshooting

### `castor scan` finds nothing: pin the device by IP

Discovery uses SSDP/mDNS **multicast**, which doesn't cross VLANs/subnets and is blocked on Android/Termux (you'll see `netlinkrib: permission denied`). Pinning a `host` reaches the device by **unicast** instead, which works across those networks and skips the discovery wait on every cast.

```yaml
device:
  name: "Living Room TV"   # now just a label
  type: dlna
  host: 192.168.0.3        # the device's LAN IP
```

- **DLNA**: `host` is the device IP. If your TV doesn't answer Castor's description request, use the full description URL instead (e.g. `http://192.168.0.3:9197/dmr`).
- **Chromecast / Roku**: `host` is the device IP.

> On Android/Termux, also leave `network.interface` empty (the default): pinning one needs the same blocked interface lookup.

### The page won't play

Extraction only works on pages that **allow automated playback**, and won't work everywhere. It does not touch DRM. See the intro for what the extractor actually does.

### The device loads the stream but plays nothing

Try [forcing a relay](#forcing-a-relay).


## Docker (optional)

The prebuilt `ghcr.io/stupside/castor` image bundles Chrome, ffmpeg, and ffprobe. Run it on a **Linux host on the same LAN as your TV**.

> [!WARNING]
> `--network host` is required: discovery (SSDP multicast) and the TV streaming back both need the container on your real LAN.
>
> On Docker Desktop (macOS/Windows) that flag is a no-op, so the container never reaches your TV and `scan` finds nothing. Use the [native binary](#homebrew-macos) there instead.

```sh
# Discover devices (no config needed)
docker run --rm --network host ghcr.io/stupside/castor:latest scan

# Cast, passing the Intel GPU through for hardware transcoding
docker run --rm --network host --device /dev/dri \
  -v "$PWD/config.yaml:/config.yaml" \
  -v castor-cache:/root/.cache \
  ghcr.io/stupside/castor:latest \
  cast player https://example.com/watch/some-video
```

- `--device /dev/dri` hands the container your Intel GPU for VA-API hardware H.264 encoding. Without it (or on a non-Intel host) Castor falls back to software `libx264`.
- Either way, when your TV already accepts the source video, Castor stream-copies it and skips encoding entirely.
- Run from the directory holding your [`config.yaml`](config.yaml) (mounted at `/config.yaml`).
- The `castor-cache` volume persists auto-downloaded whisper models.

| Tag | Build |
| --- | --- |
| `:latest` | Latest stable release |
| `:canary` | Latest preview build |
| `:v1.7.0` | A specific pinned version |


## Purpose and disclaimer

Castor is a general-purpose caster, not a service tied to any particular site.

- **It hosts nothing.** No bundled video, catalog, or sources. Castor casts only a page, stream URL, or source you supply and are authorized to use, much like a Chromecast.
- **It does not touch DRM.** Castor does not decrypt or circumvent DRM, and cannot cast DRM-protected services.
- **Using it lawfully is your responsibility.** Whether a site's terms of use and your local law allow what you do with Castor is on you. Do not use it to infringe copyright.

Castor is provided as-is for lawful, personal, and educational use.


## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).
