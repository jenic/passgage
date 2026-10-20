#!/usr/bin/env bash
# Maintainer tooling only; never called by the passgage executable.
set -euo pipefail

fail() { echo "publish: $*" >&2; exit 1; }

check() {
  # Keep local publishing and the tag-triggered workflow on the same SemVer rules.
  bash "$(dirname "$0")/release.sh" metadata >/dev/null
  for tool in git git-cliff; do
    command -v "$tool" >/dev/null 2>&1 || fail "$tool is required before publishing."
  done
  [[ $(git rev-parse --is-inside-work-tree) == true ]] || fail 'A Git worktree is required.'
  local status tags result
  status=$(git status --porcelain)
  [[ -z $status ]] || fail 'Refusing to publish from a dirty worktree. Commit or stash changes first.'
  git remote get-url origin >/dev/null || fail "The Git remote 'origin' is required before publishing."
  tags=$(git tag --list "$RELEASE_TAG")
  [[ -z $tags ]] || fail "Git tag $RELEASE_TAG already exists locally."
  if git ls-remote --exit-code --tags origin "refs/tags/$RELEASE_TAG" >/dev/null; then
    fail "Git tag $RELEASE_TAG already exists on origin."
  else
    result=$?
    [[ $result == 2 ]] || fail 'Unable to check tags on origin.'
  fi
}

publish() (
  if [[ ${PUBLISH:-0} != 1 ]]; then
    fail "Preflight complete. To create and push a signed tag, run: make publish VERSION=$RELEASE_TAG PUBLISH=1"
  fi
  # Tests and formatters may have changed tracked files since the initial check.
  check
  local commit tag_message_file status
  commit=$(git rev-parse --verify 'HEAD^{commit}')
  tag_message_file=$(mktemp)
  trap 'rm -f -- "$tag_message_file"' EXIT
  git-cliff --unreleased --tag "$RELEASE_TAG" > "$tag_message_file"
  [[ -s $tag_message_file ]] || fail 'git-cliff produced an empty tag message.'
  status=$(git status --porcelain)
  [[ -z $status && $(git rev-parse --verify 'HEAD^{commit}') == "$commit" ]] ||
    fail 'The worktree or HEAD changed while preparing the tag. Run publishing again.'
  echo "Tagging $RELEASE_TAG..."
  git tag -s --cleanup=verbatim -F "$tag_message_file" "$RELEASE_TAG" "$commit"
  echo "Pushing $RELEASE_TAG to origin..."
  git push origin "refs/tags/$RELEASE_TAG:refs/tags/$RELEASE_TAG"
)

case ${1:-} in
  check) check ;;
  publish) publish ;;
  *) fail 'usage: publish.sh check|publish' ;;
esac
