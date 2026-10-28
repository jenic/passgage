#!/usr/bin/env bash
# Release automation only; never called by the passgage executable.
set -euo pipefail

fail() { echo "release: $*" >&2; exit 1; }

validate_tag() {
  local number='(0|[1-9][0-9]*)'
  local identifier='(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)'
  local pattern="^v${number}\\.${number}\\.${number}(-${identifier}(\\.${identifier})*)?$"
  [[ ${RELEASE_TAG:-} =~ $pattern ]] || fail 'expected a v-prefixed SemVer tag without build metadata'
}

validate_module() {
  local directive path rest component major path_major
  module_path=
  [[ -f go.mod ]] || fail 'go.mod is required in the module root.'
  while read -r directive path rest; do
    if [[ $directive == module ]]; then
      [[ -z $rest || $rest == //* ]] || fail 'invalid module directive in go.mod.'
      module_path=$path
      # Go permits quoted module paths as well as the usual unquoted form.
      if [[ $module_path == \"*\" ]]; then module_path=${module_path:1:${#module_path}-2}; fi
      break
    fi
  done < go.mod
  local host='[a-z0-9]([a-z0-9-]*[a-z0-9])?'
  local pattern="^(${host}\\.)+${host}(/[A-Za-z0-9_~.-]+)+$"
  [[ $module_path =~ $pattern ]] || fail "Module path '$module_path' is not a publishable import path."
  local components=()
  IFS=/ read -r -a components <<< "$module_path"
  for component in "${components[@]}"; do
    [[ $component != .* && $component != *. ]] || fail "Invalid module path component: $component"
  done
  major=${RELEASE_TAG#v}
  major=${major%%.*}
  if [[ $module_path =~ /v([0-9]+)$ ]]; then
    path_major=${BASH_REMATCH[1]}
    [[ $path_major =~ ^([2-9]|[1-9][0-9]+)$ && $path_major == "$major" ]] ||
      fail "Module path suffix /v$path_major does not match VERSION $RELEASE_TAG (suffixes start at /v2)."
  else
    [[ $major == 0 || $major == 1 ]] || fail "VERSION $RELEASE_TAG requires the module path to end with /v$major."
  fi
}

prerelease() {
  if [[ $RELEASE_TAG == *-* ]]; then echo true; else echo false; fi
}

archives=(
  passgage_darwin_amd64.tar.gz passgage_darwin_arm64.tar.gz
  passgage_linux_amd64.tar.gz passgage_linux_arm64.tar.gz
  passgage_windows_amd64.zip passgage_windows_arm64.zip
)
assets=("${archives[@]}" SHA256SUMS)
signed_assets=("${assets[@]}")
for asset in "${assets[@]}"; do signed_assets+=("$asset.sigstore.json"); done

check_assets() (
  cd "$1"
  local mode=$2 expected actual asset
  [[ $mode == unsigned || $mode == signed ]] || fail 'invalid asset mode'
  local files=("${assets[@]}")
  if [[ $mode == signed ]]; then files=("${signed_assets[@]}"); fi
  expected=$(printf '%s\n' "${files[@]}" | LC_ALL=C sort)
  actual=$(find . -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort)
  [[ $actual == "$expected" ]] || fail 'unexpected or missing release assets'
  for asset in "${files[@]}"; do
    [[ -f $asset && ! -L $asset && -s $asset ]] || fail "invalid asset: $asset"
  done
  # Compare the complete manifest, including names and ordering, before trusting it.
  diff -u SHA256SUMS <(sha256sum "${archives[@]}")
)

check_tag() {
  local object type sha depth
  object=$(gh api "repos/$GITHUB_REPOSITORY/git/ref/tags/$RELEASE_TAG" --jq '[.object.type, .object.sha] | @tsv')
  # Dereference annotated tags, including nested tags, without local Git writes.
  for ((depth=0; depth<16; depth++)); do
    IFS=$'\t' read -r type sha <<< "$object"
    if [[ $type == commit ]]; then
      [[ $sha == "$GITHUB_SHA" ]] || fail 'remote tag no longer matches the triggering commit'
      return
    fi
    [[ $type == tag && $sha =~ ^[0-9a-f]{40}$ ]] || fail 'invalid remote tag object'
    object=$(gh api "repos/$GITHUB_REPOSITORY/git/tags/$sha" --jq '[.object.type, .object.sha] | @tsv')
  done
  fail 'too many nested annotated tags'
}

verify_blob() {
  cosign verify-blob --bundle "$1.sigstore.json" \
    --certificate-identity "$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/.github/workflows/release.yml@refs/tags/$RELEASE_TAG" \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com "$1"
}

verify_signed() (
  check_assets "$1" signed
  cd "$1"
  for asset in "${assets[@]}"; do verify_blob "$asset"; done
)

sign_assets() (
  check_tag
  check_assets "$1" unsigned
  cd "$1"
  for asset in "${assets[@]}"; do
    cosign sign-blob --yes --oidc-provider github-actions --bundle "$asset.sigstore.json" "$asset"
    verify_blob "$asset"
  done
  check_assets . signed
)

publish() (
  check_tag
  verify_signed "$1"
  cd "$1"
  local draft download_dir asset
  local flags=()
  if [[ $(prerelease) == true ]]; then flags+=(--latest=false); fi
  # Listing distinguishes a missing release from API/authentication failures.
  draft=$(gh api --paginate "repos/$GITHUB_REPOSITORY/releases?per_page=100" \
    --jq ".[] | select(.tag_name == \"$RELEASE_TAG\") | .draft")
  if [[ -n $draft ]]; then
    [[ $draft == true ]] || fail 'release is already published; refusing to change its assets'
  else
    gh release create "$RELEASE_TAG" --verify-tag --draft --title "$RELEASE_TAG" --generate-notes \
      --prerelease="$(prerelease)" "${flags[@]}"
  fi
  # Only drafts are replaced on retry. A published release is never overwritten.
  gh release upload "$RELEASE_TAG" "${signed_assets[@]}" --clobber
  download_dir=$(mktemp -d)
  trap 'rm -rf "$download_dir"' EXIT
  gh release download "$RELEASE_TAG" --dir "$download_dir"
  verify_signed "$download_dir"
  for asset in "${signed_assets[@]}"; do cmp "$asset" "$download_dir/$asset"; done
  check_tag
  gh release edit "$RELEASE_TAG" --draft=false --prerelease="$(prerelease)" \
    "${flags[@]}"
)

validate_tag
case ${1:-} in
  metadata)
    validate_module
    printf 'tag=%s\nprerelease=%s\nmodule_path=%s\n' "$RELEASE_TAG" "$(prerelease)" "$module_path"
    ;;
  check-assets) check_assets "${2:?artifact directory required}" "${3:-unsigned}" ;;
  sign) sign_assets "${2:?artifact directory required}" ;;
  publish) publish "${2:?artifact directory required}" ;;
  *) fail 'usage: release.sh metadata|check-assets|sign|publish [directory] [unsigned|signed]' ;;
esac
