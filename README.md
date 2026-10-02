
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

Smart TVs won't cast arbitrary web video, and screen mirroring is laggy. Castor casts the stream itself, from your terminal.

Point it at a web page or a stream URL: it finds the video, converts it if your TV needs that, and casts it. It can also look up an IMDB/TMDB id in sources you configure, and burn in generated subtitles.

To find the video, it opens the page in headless Chrome, starts playback, and watches the network. That only works on pages that allow automated playback.

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

See [Configuration](#configuration) for subtitles, quality, and title search.

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
| `castor server` | Run casts for castor on other machines, and serve their TVs ([remote server](#remote-server)) |

Run `castor --help` for all flags.


## Installation

Castor runs best as a native binary on the same network as your TV. It needs three tools on your `PATH`.

| Tool | Version | Used for |
| --- | --- | --- |
| **Chrome / Chromium** | Any recent | Headless stream extraction, on the machine that runs the server |
| **ffmpeg** | 7.1+ | Transcoding (and stream copy) |
| **ffprobe** | 7.1+ | Source format detection |

> [!IMPORTANT]
> ffmpeg and ffprobe must be **7.1 or newer**: older builds reject flags Castor uses.

### Homebrew (macOS)

```sh
brew install --cask stupside/tap/castor
```

### Windows

Download `castor_<version>_windows_amd64.zip` (or `_arm64.zip` for Windows on ARM) from the [latest release](https://github.com/stupside/castor/releases/latest), extract `castor.exe` into a folder on your `PATH`, and install the tools:

```powershell
winget install Gyan.FFmpeg     # ffmpeg + ffprobe
winget install Google.Chrome   # skip if Chrome is already installed
```

On first run:

- **SmartScreen** may block the unsigned binary: choose *More info*, then *Run anyway*.
- **Windows Defender Firewall** asks about network access: allow it on **private** networks, or Castor finds no devices.

### Build from source

On macOS or Linux, with **Go 1.26+** and **cmake**. Clone with submodules, then `make`:

```sh
git clone --recurse-submodules https://github.com/stupside/castor.git
cd castor
make          # builds libwhisper.a, then the castor binary
```

`go install` won't work: the whisper.cpp bindings need that locally built library.


## Configuration

Castor reads `config.yaml` from the working directory (or `--config <path>`). Only `device` is required.

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

Subtitles transcribed with whisper and burned into the video. Models download once to your user cache.

```yaml
whisper:
  enable: true             # off by default
  # language: "fr"         # default: English
  # model_path: ""         # default: ggml-tiny.en (~75 MB, auto-downloaded)
```

> [!NOTE]
> DLNA only: Chromecast and Roku don't get burned-in subtitles.

### Video quality

Set `max_height` to your TV's vertical resolution. Nothing taller reaches the device: a taller source is relayed and scaled down, which re-encodes the whole title. Raise it if you'd rather the device play the source as-is.

```yaml
resolver:
  max_height: 2160         # default: 1080
  # playlist_timeout: 30s
  # probe_timeout: 30s
  # probe_max_concurrency: 2
  # ffprobe_path: ffprobe
```

### Sources

`cast movie`, `cast episode`, and the interactive browser turn a title id into a page URL. Castor bundles no sources: you add your own, for sites you are authorized to use. The id is substituted into your `templates` under each of your `proxies`, and the page is extracted like `cast player`.

```yaml
sources:
  - proxies: ["https://your-source.example"]   # base URLs, tried in order
    templates:
      movie: "/embed/movie/{itemID}"
      episode: "/embed/tv/{itemID}/{season}-{episode}"
```

So `castor cast movie tt12300742` opens `https://your-source.example/embed/movie/tt12300742`.

### TMDB key

Only the interactive browser (`castor cast`) needs one. Get a free key from [themoviedb.org](https://www.themoviedb.org/settings/api):

```yaml
tmdb:
  api_key: "<KEY>"
```

### Forcing a relay

Castor decides per source whether the device fetches the stream itself or Castor relays it, and logs the choice and why on its `cast composition` line, shown with `--debug`. Set `delivery: serve` to always relay, for a source a device refuses for a reason Castor can't see:

```yaml
cast:
  delivery: serve   # "auto" (the default) decides per source
```

Relaying costs this machine bandwidth and CPU, so try it once first:

```sh
CASTOR_CAST__DELIVERY=serve castor cast url <url>
```

Any key works that way, e.g. `CASTOR_RESOLVER__MAX_HEIGHT=720`.


### Remote server

Every cast runs on a castor server, by default one inside `castor` itself. Run one elsewhere, say on a NAS next to your TV, and it becomes your media server: it finds the streams on a page, reads, transcodes, burns in subtitles, and serves the TV.

On the server:

```sh
castor server   # listens on :8410 (server.listen)
```

On the machine you cast from:

```yaml
remote:
  url: http://my-nas:8410
```

The command you run finds the devices on your TV's network and starts the cast on the one you pick. The TV then fetches what the server streams from the server itself, so once the cast plays you can close the command: the server keeps serving until the TV stops fetching. Ctrl+C in the command stops the cast instead.

The TV must reach the server. On the TV's network the server finds its own address there (`network.interface` pins which). For a server outside it, say where the TV reaches it, such as a public URL, in the server's config:

```yaml
server:
  advertise: http://castor.example.com:8410
```

A source the TV plays directly never goes through the server, so it plays wherever the server runs.

What you ask of a cast travels with it: `cast.delivery`, `resolver.max_height`, and whether to burn in subtitles (`whisper.enable`, `whisper.language`) are read from the machine you cast from. The server's own config only says how it does the work (ffmpeg, timeouts, the whisper model).

Your terminal shows the cast's progress, its warnings, and how it ended. `castor --debug` adds the server's other lines for your cast, marked `from=server`, wherever the server runs; they are sent live and never stored.

The server answers gRPC health checks and reflection on the same port, so standard tools work against it:

```sh
buf curl --protocol grpc --http2-prior-knowledge http://my-server:8410/grpc.health.v1.Health/Check
buf curl --protocol grpc --http2-prior-knowledge --list-methods http://my-server:8410
```

A server outside your home finds and reads streams from its own IP address, so the machine you cast from needs no Chrome. A site that locks its links to the address that found them still plays when the server serves them, but the TV may be refused when it fetches such a link itself. The API has no authentication, so only expose it on a network you trust.


## Supported devices

Run `castor scan` to list what is on your network.

| Protocol | Works with | Status |
| --- | --- | --- |
| **DLNA / UPnP** (`MediaRenderer:1`) | Most smart TVs, and players like Kodi, VLC, and Plex | Tested on Samsung |
| **Chromecast** | Google Cast devices | Experimental, not yet tried on real hardware |
| **Roku** | Roku TVs and players, through a sideloaded channel | Experimental, not yet tried on real hardware |

### Roku setup

Roku can't play an arbitrary URL from a preinstalled app, so Castor installs a small channel of its own. That needs one extra step.

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
> Roku plays about 30 s behind, so it starts slower than DLNA.


## Troubleshooting

### `castor scan` finds nothing: pin the device by IP

Discovery uses multicast, which doesn't cross VLANs or subnets and is blocked on Android/Termux (`netlinkrib: permission denied`). Pinning a `host` reaches the device directly, and skips the discovery wait.

```yaml
device:
  name: "Living Room TV"   # now just a label
  type: dlna
  host: 192.168.0.3        # the device's LAN IP
```

If a DLNA TV doesn't answer at its IP, use its full description URL instead (e.g. `http://192.168.0.3:9197/dmr`).

> On Android/Termux, also leave `network.interface` empty (the default): pinning one needs the same blocked interface lookup.

### The page won't play

Extraction only works on pages that allow automated playback. DRM-protected streams are refused.

### The device loads the stream but plays nothing

Try [forcing a relay](#forcing-a-relay).


## Docker (optional)

The `ghcr.io/stupside/castor` image bundles Chrome, ffmpeg, and ffprobe. Run it on a Linux host on your TV's network.

> [!WARNING]
> `--network host` is required, and Docker Desktop (macOS/Windows) ignores it, so `scan` finds nothing there. Use the native binary instead ([macOS](#homebrew-macos), [Windows](#windows)).

```sh
# Discover devices (no config needed)
docker run --rm --network host ghcr.io/stupside/castor:latest scan

# Cast, passing a Linux render device through for hardware transcoding
docker run --rm --network host --device /dev/dri \
  -v "$PWD/config.yaml:/config.yaml" \
  -v castor-cache:/root/.cache \
  ghcr.io/stupside/castor:latest \
  cast player https://example.com/watch/some-video
```

- `--device /dev/dri` lets Castor encode on an Intel GPU (VA-API). Without it, Castor encodes in software. Video your TV already plays is copied, not encoded.
- Run from the directory holding your [`config.yaml`](config.yaml).
- The `castor-cache` volume keeps downloaded whisper models.
- `docker run -d --network host ghcr.io/stupside/castor:latest server` runs the image as a [remote server](#remote-server) for castor on your laptop. Host networking lets it find the address your TV reaches it at; without it, set `server.advertise`.

| Tag | Build |
| --- | --- |
| `:latest` | Latest stable release |
| `:canary` | Latest preview build |
| `:v1.7.0` | A specific pinned version |


## Purpose and disclaimer

Castor is a general-purpose caster, not a service tied to any site.

- **It hosts nothing.** No bundled video, catalog, or sources. It casts only what you supply and are authorized to use.
- **It does not touch DRM.** It never decrypts or circumvents DRM, and refuses protected streams.
- **Lawful use is your responsibility.** Check a site's terms and your local law. Do not use it to infringe copyright.

Provided as-is for lawful, personal, and educational use.


## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).
