# Local squash-integration handoff

This handoff is not authorization to integrate. Run it only after separate
approval for local integration and cleanup.

## Expected relationship

- Source branch and worktree: `skillsbench-analysis`.
- Local target branch and primary worktree: `main`.
- Recorded base-family anchor: `312032de1bf717ee165335074e473c7e5e46f382`.
- At publication, local `main` and the source/target merge base equal that
  anchor, and the anchor is an ancestor of the source. The source tip is not
  pinned because this handoff commit advances it.
- Local `main` has no configured upstream at publication.
- Both worktrees must be clean. The local commit identity must be Jonathan
  Gabor / `230093+jgabor@users.noreply.github.com`.

Worktrunk 0.69.2 squashes by default, rebases the resulting commit onto the
local target when needed, runs its hooks, and fast-forwards the local target.
It normally removes the source afterward; `--no-remove` keeps it for
verification. `wt merge` does not fetch or push. Remote synchronization and
publication are separate, explicitly authorized operations.

The ignored raw campaign authority is checkout-local and currently exists only
in the source worktree. The sequence verifies those bytes after integration and
keeps the source worktree while they remain there. Removing that worktree needs
a separate, explicitly approved preservation step.

## Later sequence

Run this from the source worktree. Do not replace the explicit merge command
with bare `wt merge`.

```bash
set -euo pipefail

source_branch=skillsbench-analysis
target_branch=main
base_anchor=312032de1bf717ee165335074e473c7e5e46f382
expected_target="$base_anchor"
source_path=$(git rev-parse --show-toplevel)

wt list
test "$(git branch --show-current)" = "$source_branch"

target_path=$(
  git worktree list --porcelain |
    awk -v ref="refs/heads/$target_branch" '
      /^worktree / { path = substr($0, 10) }
      $1 == "branch" && $2 == ref { print path; exit }
    '
)
test -n "$target_path"
test "$(git -C "$target_path" branch --show-current)" = "$target_branch"

test -z "$(git status --porcelain=v1 --untracked-files=all)"
test -z "$(git -C "$target_path" status --porcelain=v1 --untracked-files=all)"
test "$(git config --local user.name)" = "Jonathan Gabor"
test "$(git config --local user.email)" = \
  "230093+jgabor@users.noreply.github.com"
test "$(git -C "$target_path" config --local user.name)" = "Jonathan Gabor"
test "$(git -C "$target_path" config --local user.email)" = \
  "230093+jgabor@users.noreply.github.com"

raw_campaign_root="$source_path/.bench-data/skillsbench-campaigns"
raw_digest_manifest="$source_path/tools/bench/evidence/skillsbench-oauth-campaign-artifacts.sha256"
verify_raw_campaign_authority() {
  if ! test -d "$raw_campaign_root" || test -L "$raw_campaign_root"
  then
    echo "raw campaign authority has an unsafe filesystem type; stop and report" >&2
    return 1
  fi
  (
    cd "$source_path"
    sha256sum --check --quiet "$raw_digest_manifest"
  )
}
if test -e "$raw_campaign_root" || test -L "$raw_campaign_root"
then
  if ! verify_raw_campaign_authority
  then
    wt list
    exit 1
  fi
fi

source_tip=$(git rev-parse "$source_branch^{commit}")
target_tip=$(git rev-parse "$target_branch^{commit}")
merge_base=$(git merge-base "$source_branch" "$target_branch")
printf 'source=%s\ntarget=%s\nmerge-base=%s\n' \
  "$source_tip" "$target_tip" "$merge_base"
git rev-list --left-right --count "$target_branch...$source_branch"

git merge-base --is-ancestor "$base_anchor" "$source_branch"
git merge-base --is-ancestor "$base_anchor" "$target_branch"
test "$merge_base" = "$base_anchor"
test "$target_tip" = "$expected_target"

if upstream_ref=$(
  git -C "$target_path" rev-parse \
    --abbrev-ref --symbolic-full-name '@{upstream}' 2>/dev/null
)
then
  upstream_tip=$(git rev-parse "$upstream_ref^{commit}")
  test "$upstream_tip" = "$target_tip" || {
    echo "the existing upstream-tracking ref differs from local main; stop and reconcile" >&2
    exit 1
  }
fi

if ! git diff --binary "$merge_base".."$source_branch" |
  git -C "$target_path" apply --check --3way --quiet -
then
  echo "source changes do not preapply cleanly to local main; stop and report" >&2
  exit 1
fi

source_before_merge=$source_tip
target_before_merge=$target_tip
if ! wt merge --no-remove main
then
  rebase_merge=$(git rev-parse --git-path rebase-merge)
  rebase_apply=$(git rev-parse --git-path rebase-apply)
  if test -d "$rebase_merge" || test -d "$rebase_apply"
  then
    git rebase --abort
  fi
  git status --short --branch
  wt list
  echo "Worktrunk integration failed; stop and report its output" >&2
  exit 1
fi

source_after_merge=$(git rev-parse "$source_branch^{commit}")
target_after_merge=$(git rev-parse "$target_branch^{commit}")
printf 'source-before=%s\nsource-after=%s\ntarget-before=%s\ntarget-after=%s\n' \
  "$source_before_merge" "$source_after_merge" \
  "$target_before_merge" "$target_after_merge"
git merge-base --is-ancestor "$target_before_merge" "$target_after_merge"
test "$source_after_merge" = "$target_after_merge"
test "$(git rev-list --count "$target_before_merge".."$target_branch")" -eq 1
git diff --quiet "$source_branch" "$target_branch"
test -z "$(git -C "$target_path" status --porcelain=v1 --untracked-files=all)"

module_cache=$(go env GOMODCACHE)
go_cache=$(go env GOCACHE)
verify_root=$(mktemp -d)
case "$verify_root" in /tmp/*) ;; *) exit 1 ;; esac
trap 'rm -rf -- "$verify_root"' EXIT
mkdir -p \
  "$verify_root/home" \
  "$verify_root/data" \
  "$verify_root/config" \
  "$verify_root/cache" \
  "$verify_root/state"
(
  cd "$target_path"
  env -u KTX_DB \
    HOME="$verify_root/home" \
    XDG_DATA_HOME="$verify_root/data" \
    XDG_CONFIG_HOME="$verify_root/config" \
    XDG_CACHE_HOME="$verify_root/cache" \
    XDG_STATE_HOME="$verify_root/state" \
    GOCACHE="$go_cache" \
    GOMODCACHE="$module_cache" \
    GOPROXY=off \
    GOSUMDB=off \
    GOTOOLCHAIN=local \
    mage check
)
test -z "$(git -C "$target_path" status --porcelain=v1 --untracked-files=all)"

if test -e "$raw_campaign_root" || test -L "$raw_campaign_root"
then
  if ! verify_raw_campaign_authority
  then
    wt list
    exit 1
  fi
  echo "source worktree retained because it contains checkout-local raw campaign authority" >&2
  wt list
  exit 0
fi

cd "$target_path"
if ! wt remove skillsbench-analysis
then
  wt list
  exit 1
fi
wt list
```

If local `main` differs from `expected_target`, stop before the preapply check.
Review and reconcile the target drift without forcing or rewriting either ref,
then set `expected_target` to the reviewed full target SHA and repeat the whole
gate from `wt list`. The base anchor must remain an ancestor of both branches,
and their merge base must remain the recorded anchor. Any conflict, failed
hook, failed verification, or unexpected ancestry stops the sequence before
cleanup. A successful raw-authority check also stops before cleanup and keeps
the source worktree by design.
