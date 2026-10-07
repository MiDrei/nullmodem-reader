#!/usr/bin/env bash
# Builds NullModem Reader for every supported platform and publishes it
# as a release on GitHub.
#
#   scripts/release.sh v0.2.0             test, build, tag, push, publish
#   DRY_RUN=1 scripts/release.sh v0.2.0   test and build into dist/ only
#
# Publishing needs GITHUB_TOKEN: a personal access token allowed to
# write the repository's contents (fine-grained: Contents read/write).
#
# Everything is built with CGO_ENABLED=0 -- Ebitengine drives X11,
# Cocoa and Win32 without cgo -- so one machine builds all targets.
# Builds use GOWORK=off: a release must build from the kit version
# go.mod pins, not from a local ../kit checkout.
set -euo pipefail

TARGETS=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64)
REPO=${REPO:-midrei/nullmodem-reader}
DRY_RUN=${DRY_RUN:-0}

die() { echo "release: $*" >&2; exit 1; }

version=${1:-}
[[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || die "usage: $0 vX.Y.Z[-suffix]"

cd "$(dirname "$0")/.."
root=$(pwd)

# ---- preconditions ---------------------------------------------------
if [[ $DRY_RUN != 1 ]]; then
	[[ -n ${GITHUB_TOKEN:-} ]] || die "GITHUB_TOKEN is not set (or run with DRY_RUN=1)"
	[[ -z $(git status --porcelain) ]] || die "working tree is not clean"
	[[ $(git rev-parse --abbrev-ref HEAD) == main ]] || die "not on main"
	git fetch -q origin
	[[ $(git rev-parse HEAD) == $(git rev-parse origin/main) ]] || die "main is not in sync with origin/main"
	! git rev-parse -q --verify "refs/tags/$version" >/dev/null || die "tag $version already exists locally"
	[[ -z $(git ls-remote --tags origin "refs/tags/$version") ]] || die "tag $version already exists on origin"
fi
! grep -q '^replace' go.mod || die "go.mod has a replace directive -- a release must use the published kit"

kit=$(GOWORK=off go list -m -f '{{.Version}}' github.com/midrei/nullmodem-kit)
echo "release: $version (kit $kit)"

# ---- test --------------------------------------------------------------
GOWORK=off go vet ./...
GOWORK=off go test ./...

# ---- build -------------------------------------------------------------
dist="$root/dist/$version"
rm -rf "$dist"
mkdir -p "$dist"

# Icon and version details for nmr.exe (Explorer's properties, the
# taskbar). go-winres writes them as .syso files that go build links
# into Windows builds only; they carry this version, so they are made
# fresh here and removed again afterwards.
trap 'rm -f "$root"/cmd/nmr/rsrc_windows_*.syso' EXIT
GOWORK=off go run github.com/tc-hib/go-winres@v0.3.3 simply \
	--arch amd64,arm64 --out cmd/nmr/rsrc --manifest cli \
	--product-name "NullModem Reader" \
	--file-description "NullModem Reader - QWK offline reader" \
	--product-version "$version" --file-version "$version" \
	--original-filename nmr.exe --copyright "NullModem" \
	--icon assets/icon/nmr.png
for target in "${TARGETS[@]}"; do
	os=${target%/*}
	arch=${target#*/}
	name="nmr-$version-$os-$arch"
	stage="$dist/$name"
	mkdir -p "$stage"
	exe=nmr
	[[ $os == windows ]] && exe=nmr.exe
	echo "release: building $target"
	GOWORK=off CGO_ENABLED=0 GOOS=$os GOARCH=$arch \
		go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$stage/$exe" ./cmd/nmr
	# The reader's own license (MIT) and the embedded CP437 font's
	# (Spleen, BSD-2-Clause) both have to travel with every binary.
	cp README.md LICENSE "$stage/"
	cp assets/font/LICENSE.spleen "$stage/"
	if [[ $os == windows ]]; then
		(cd "$dist" && python3 -m zipfile -c "$name.zip" "$name/")
	else
		tar -C "$dist" -czf "$dist/$name.tar.gz" "$name"
	fi
	rm -rf "$stage"
done
(cd "$dist" && sha256sum -- *.tar.gz *.zip > SHA256SUMS)
echo "release: artifacts in $dist"
ls -l "$dist"

if [[ $DRY_RUN == 1 ]]; then
	echo "release: DRY_RUN=1 -- not tagging or publishing"
	exit 0
fi

# ---- tag and publish ---------------------------------------------------
previous=$(git describe --tags --abbrev=0 2>/dev/null || true)
git tag -a "$version" -m "NullModem Reader $version"
git push -q origin "$version"

if [[ -n $previous ]]; then
	changes=$(git log --format='- %s' "$previous..$version")
else
	changes=$(git log --format='- %s' "$version")
fi
body=$(printf 'Built against NullModem Kit %s.\n\n%s\n\nVerify downloads with SHA256SUMS (`sha256sum -c SHA256SUMS`).\n' "$kit" "$changes")

prerelease=false
[[ $version == *-* ]] && prerelease=true

api="https://api.github.com/repos/$REPO"
auth=(-H "Authorization: Bearer $GITHUB_TOKEN" -H "Accept: application/vnd.github+json")
payload=$(VERSION=$version BODY=$body PRE=$prerelease python3 -c '
import json, os
print(json.dumps({"tag_name": os.environ["VERSION"], "name": "NullModem Reader " + os.environ["VERSION"],
                  "body": os.environ["BODY"], "draft": False, "prerelease": os.environ["PRE"] == "true"}))')
release=$(curl -fsS -X POST "$api/releases" "${auth[@]}" -H 'Content-Type: application/json' -d "$payload") ||
	die "creating the release failed -- the tag $version is pushed; delete it or create the release by hand"
id=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$release")

for file in "$dist"/*.tar.gz "$dist"/*.zip "$dist/SHA256SUMS"; do
	echo "release: uploading $(basename "$file")"
	curl -fsS -o /dev/null -X POST "https://uploads.github.com/repos/$REPO/releases/$id/assets?name=$(basename "$file")" \
		"${auth[@]}" -H 'Content-Type: application/octet-stream' --data-binary "@$file" ||
		die "uploading $(basename "$file") failed"
done

echo "release: published https://github.com/$REPO/releases/tag/$version"
