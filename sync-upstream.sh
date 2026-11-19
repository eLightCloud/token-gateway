#!/usr/bin/env bash

set -Eeuo pipefail

usage() {
  cat <<'HELP'
用法：./sync-upstream.sh [vX.Y.Z-rc.N]

将指定的官方 New API RC 合并到本地 main。不指定时取上游最新 RC 标签。
合并会创建备份分支，并停在提交前。请解决冲突并按仓库指南验证后提交。
HELP
}

fail() {
  echo "错误：$*" >&2
  exit 1
}

is_rc_tag() {
  [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$ ]]
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi
if (( $# > 1 )); then
  usage >&2
  exit 2
fi

target_tag="${1:-}"
if [[ -n "$target_tag" ]] && ! is_rc_tag "$target_tag"; then
  fail "目标必须是 RC 标签，例如 v1.0.0-rc.51。"
fi

repo_root="$(git rev-parse --show-toplevel)" || fail "当前目录不在 Git 仓库中。"
cd "$repo_root"
[[ "$(git branch --show-current)" == main ]] || fail "请从本地 main 执行同步。"
[[ -z "$(git status --porcelain)" ]] || fail "请先保存未提交改动，再执行同步。"
for operation in MERGE_HEAD CHERRY_PICK_HEAD REVERT_HEAD rebase-merge rebase-apply sequencer; do
  [[ ! -e "$(git rev-parse --git-path "$operation")" ]] || fail "请先完成当前 Git 操作。"
done

upstream_url="$(git config --get remote.upstream.url)" || fail "请先配置 upstream 远端。"
case "${upstream_url%.git}" in
  https://github.com/QuantumNous/new-api | git@github.com:QuantumNous/new-api | ssh://git@github.com/QuantumNous/new-api) ;;
  *) fail "upstream 必须指向官方 QuantumNous/new-api 仓库。" ;;
esac

if [[ -z "$target_tag" ]]; then
  remote_tags="$(git ls-remote --tags --refs --sort=-version:refname upstream 'refs/tags/v*-rc.*')" ||
    fail "无法查询上游 RC 标签。"
  while read -r _ remote_ref; do
    candidate="${remote_ref#refs/tags/}"
    if is_rc_tag "$candidate"; then
      target_tag="$candidate"
      break
    fi
  done <<< "$remote_tags"
  [[ -n "$target_tag" ]] || fail "上游没有可用的 RC 标签。"
fi

target_ref="refs/tags/$target_tag"
git fetch --no-tags upstream "$target_ref:$target_ref"
target_commit="$(git rev-parse --verify "$target_ref^{commit}")"
local_commit="$(git rev-parse --verify HEAD)"
printf '上游：%s\n本地起点：%s\n目标：%s (%s)\n' "$upstream_url" "$local_commit" "$target_tag" "$target_commit"

if git merge-base --is-ancestor "$target_commit" "$local_commit"; then
  echo "本地 main 已包含 $target_tag。"
  exit 0
fi
git merge-base "$local_commit" "$target_commit" >/dev/null ||
  fail "找不到共同基线。若仓库是浅克隆，请先补齐必要的提交历史。"

backup_branch="backup/main-before-sync-$(date +%Y%m%d-%H%M%S)"
git branch "$backup_branch" "$local_commit"
echo "恢复点：$backup_branch"

if ! git merge --no-commit --no-ff "$target_commit"; then
  if git rev-parse -q --verify MERGE_HEAD >/dev/null; then
    git diff --name-only --diff-filter=U
    echo "请按仓库指南解决冲突、验证并提交。需要撤销时可执行 git merge --abort。" >&2
  fi
  exit 1
fi

echo "已停在合并提交前。请完成仓库指南中的验证和同步记录后提交。"
