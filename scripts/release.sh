#!/usr/bin/env bash
# Stage prebuilt/ into release archives, checksums.txt and the Docker context; run from the repo root in CI.
set -euo pipefail

version="${VERSION:?VERSION must be set}"

# archive packs one binary, with the license and readme, as <name>_<version>_<os>_<arch>.
archive() {
  local name=$1 bin=$2 os=$3 arch=$4 work
  work=$(mktemp -d)
  cp LICENSE README.md "$work/"
  if [ "$os" = windows ]; then
    cp "$bin" "$work/$name.exe"
    zip -qj "dist/${name}_${version}_${os}_${arch}.zip" "$work/$name.exe" "$work/LICENSE" "$work/README.md"
  else
    cp "$bin" "$work/$name"; chmod +x "$work/$name"
    tar -C "$work" -czf "dist/${name}_${version}_${os}_${arch}.tar.gz" "$name" LICENSE README.md
  fi
}

mkdir -p dist docker
for dir in prebuilt/castor-*; do
  target="${dir#prebuilt/castor-}"   # e.g. linux-amd64
  os="${target%-*}"; arch="${target##*-}"

  archive castor "$dir/castor" "$os" "$arch"

  if [ "$os" = linux ]; then
    mkdir -p "docker/$arch"
    cp "$dir/castor" "docker/$arch/castor"
  fi
done

( cd dist && sha256sum *.tar.gz *.zip > checksums.txt )
