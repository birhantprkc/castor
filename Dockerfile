# Chainguard Wolfi: glibc-based, rolling, and scans 0 critical / 0 high. Debian
# trixie ships the same ffmpeg 7.x but drags unfixed perl/ncurses/util-linux CVEs
# in its Essential packages (no upstream patch, apt upgrade is a no-op); Wolfi
# carries ffmpeg + chromium with a clean scan. castor shells out at runtime to
# ffmpeg/ffprobe (transcode + the PCM feed for whisper) and headless chromium
# (stream extractor), and fetches models/APIs over HTTPS; libgomp/libstdc++ are
# the whisper cgo runtime and font-liberation feeds drawtext + chromium.
FROM cgr.dev/chainguard/wolfi-base:latest
ARG TARGETARCH
RUN apk add --no-cache \
      ca-certificates-bundle ffmpeg chromium font-liberation libgomp libstdc++

# chromium's path in this image; as root in a container it can't use its sandbox.
ENV CASTOR_BROWSER__CHROME_PATH=/usr/bin/chromium \
    CASTOR_BROWSER__HEADLESS=true \
    CASTOR_BROWSER__NO_SANDBOX=true

COPY docker/${TARGETARCH}/castor /usr/local/bin/castor
RUN chmod +x /usr/local/bin/castor
ENTRYPOINT ["castor"]
