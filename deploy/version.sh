#!/bin/sh
# 版本号规则
#
#   zip<YY><MM>.<修订号>
#
#   例：zip2609.001
#        26  年份后两位
#        09  月份
#        001 该月内的第几次修改，从 001 开始，每次改动 +1
#
# 这是本项目的唯一版本来源：VERSION 文件。Docker 标签、二进制内的
# main.version 全部由它推导，不手写第二份。
#
# 例外：fnpack 打包 fpk 时只接受 semver（x.y.z[-r]），所以 manifest 里的
# 版本用 `version.sh fpk` 换算出的 26.9.1，两边靠 `version.sh sync-manifest`
# 和 CI 校验保持一致。

set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)

read_version () {
  if [ -n "${VERSION:-}" ]; then
    echo "$VERSION"
    return
  fi
  if [ -f "$ROOT/VERSION" ]; then
    tr -d ' \t\r\n' < "$ROOT/VERSION"
    return
  fi
  echo "dev"
}

# 校验格式，避免把 1.0.0 这种旧写法混进来
assert_version () {
  v=$1
  if ! echo "$v" | grep -Eq '^zip[0-9]{4}\.[0-9]{3}$'; then
    echo "版本号格式不符合 zip<YY><MM>.<NNN>：$v" >&2
    return 1
  fi
}

case "${1:-}" in
  show) read_version ;;
  check)
    v=$(read_version)
    assert_version "$v"
    echo "版本号合法：$v"
    ;;
  # fnpack 只接受 semver（x.y.z[-r]），所以 fpk 的 manifest 用这个换算值：
  #   zip2609.001 -> 26.9.1   （年.月.修订）
  # 项目本身、Docker 标签、二进制内版本仍然用 zip<YYMM>.<NNN>。
  fpk)
    v=$(read_version)
    assert_version "$v" || exit 1
    yy=$(echo "$v" | sed -E 's/^zip([0-9]{2})[0-9]{2}\..*/\1/')
    mm=$(echo "$v" | sed -E 's/^zip[0-9]{2}([0-9]{2})\..*/\1/')
    rev=$(echo "$v" | sed -E 's/.*\.0*([0-9]+)$/\1/')
    # semver 的数字段不能有前导零
    printf '%d.%d.%d\n' "$yy" "$mm" "$rev"
    ;;
  # 推进修订号。用法：version.sh bump [--month]
  bump)
    cur=$(read_version)
    assert_version "$cur" || exit 1
    yymm=$(echo "$cur" | sed -E 's/^zip([0-9]{4})\..*/\1/')
    rev=$(echo "$cur" | sed -E 's/.*\.0*([0-9]+)$/\1/')
    now=$(date +%y%m)
    if [ "${2:-}" = "--month" ] || [ "$now" != "$yymm" ]; then
      yymm=$now
      rev=1
    else
      rev=$((rev + 1))
    fi
    new=$(printf 'zip%s.%03d' "$yymm" "$rev")
    printf '%s\n' "$new" > "$ROOT/VERSION"
    echo "版本号已更新：$cur -> $new"
    ;;
  # 把 VERSION 换算后的 semver 写进 fpk 的 manifest，保持两边一致
  sync-manifest)
    want=$(sh "$0" fpk)
    manifest="$ROOT/deploy/fpk/manifest"
    [ -f "$manifest" ] || { echo "找不到 $manifest" >&2; exit 1; }
    if sed -i.bak -E "s|^version[[:space:]]*=.*|version               = $want|" "$manifest"; then
      rm -f "$manifest.bak"
    fi
    echo "manifest 版本已同步：$want"
    ;;
  *)
    echo "用法：deploy/version.sh {show|check|fpk|bump [--month]|sync-manifest}" >&2
    exit 2
    ;;
esac
