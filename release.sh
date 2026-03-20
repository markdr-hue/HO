#!/bin/bash
set -e

# Read version from main.go (single source of truth)
VERSION="v$(grep -oP 'var Version = "\K[^"]+' main.go)"
if [ "$VERSION" = "v" ]; then
  echo "Error: could not read version from main.go"
  exit 1
fi
BINARY="ho"
EXTRAS="config.json seed.json"
WIN_EXTRAS="start.bat"
UNIX_EXTRAS="start.sh"
OUTDIR="release"
STAGING="release/staging/HO"

rm -rf "$OUTDIR"
mkdir -p "$STAGING"

LDFLAGS="-s -w -X main.Version=$VERSION"

# Parse semver (strip leading v)
SEMVER="${VERSION#v}"
MAJOR="${SEMVER%%.*}"
REST="${SEMVER#*.}"
MINOR="${REST%%.*}"
PATCH="${REST#*.}"
PATCH="${PATCH%%-*}" # strip any pre-release suffix

# Patch versioninfo.json with release version
sed -i "s/\"Major\": [0-9]*/\"Major\": $MAJOR/g" versioninfo.json
sed -i "s/\"Minor\": [0-9]*/\"Minor\": $MINOR/g" versioninfo.json
sed -i "s/\"Patch\": [0-9]*/\"Patch\": $PATCH/g" versioninfo.json
sed -i "s/\"FileVersion\": \"[^\"]*\"/\"FileVersion\": \"$VERSION\"/" versioninfo.json
sed -i "s/\"ProductVersion\": \"[^\"]*\"/\"ProductVersion\": \"$VERSION\"/" versioninfo.json

echo "Building $VERSION..."

# Copy extras into the HO directory inside staging.
for f in $EXTRAS; do
  [ -f "$f" ] && cp "$f" "$STAGING/"
done

# Windows AMD64 (.zip — native for Windows users)
echo "  windows/amd64"
go generate
GOOS=windows GOARCH=amd64 go build -ldflags "$LDFLAGS" -o "$STAGING/$BINARY.exe" .
for f in $WIN_EXTRAS; do
  [ -f "$f" ] && cp "$f" "$STAGING/"
done
powershell -NoProfile -Command "Compress-Archive -Path 'release/staging/HO' -DestinationPath 'release/HO_${VERSION}_windows_amd64.zip'"
rm "$STAGING/$BINARY.exe"
for f in $WIN_EXTRAS; do rm -f "$STAGING/$f"; done

# Add unix extras for Linux/macOS builds.
for f in $UNIX_EXTRAS; do
  [ -f "$f" ] && cp "$f" "$STAGING/"
done

# Linux AMD64
echo "  linux/amd64"
GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o "$STAGING/$BINARY" .
tar -czf "$OUTDIR/HO_${VERSION}_linux_amd64.tar.gz" -C release/staging HO/
rm "$STAGING/$BINARY"

# Linux ARM64
echo "  linux/arm64"
GOOS=linux GOARCH=arm64 go build -ldflags "$LDFLAGS" -o "$STAGING/$BINARY" .
tar -czf "$OUTDIR/HO_${VERSION}_linux_arm64.tar.gz" -C release/staging HO/
rm "$STAGING/$BINARY"

# macOS AMD64
echo "  darwin/amd64"
GOOS=darwin GOARCH=amd64 go build -ldflags "$LDFLAGS" -o "$STAGING/$BINARY" .
tar -czf "$OUTDIR/HO_${VERSION}_darwin_amd64.tar.gz" -C release/staging HO/
rm "$STAGING/$BINARY"

# macOS ARM64
echo "  darwin/arm64"
GOOS=darwin GOARCH=arm64 go build -ldflags "$LDFLAGS" -o "$STAGING/$BINARY" .
tar -czf "$OUTDIR/HO_${VERSION}_darwin_arm64.tar.gz" -C release/staging HO/
rm "$STAGING/$BINARY"

# Clean up staging
rm -rf release/staging

# Restore versioninfo.json to dev defaults
git checkout versioninfo.json

echo ""
echo "Done! Release files in $OUTDIR/:"
ls -lh "$OUTDIR/"
