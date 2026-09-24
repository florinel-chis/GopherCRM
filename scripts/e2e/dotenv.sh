# shellcheck shell=bash
# load_dotenv FILE KEY...
#
# Exports each listed KEY from FILE unless it is already set, reading the file
# the way godotenv does for the cases that matter here: an optional `export `
# prefix, values that contain '=' (base64 secrets end in it), quoted values
# kept verbatim, a ` #` comment stripped from unquoted values, CRLF line
# endings, and a last line without a newline. The environment always wins.
load_dotenv() {
  local file=$1
  shift
  [ -f "$file" ] || return 0
  local line key value wanted want
  while IFS= read -r line || [ -n "$line" ]; do
    line=${line%$'\r'}
    line=${line#export }
    case "$line" in
      '' | \#*) continue ;;
      *=*) ;;
      *) continue ;;
    esac
    key=${line%%=*}
    value=${line#*=}
    wanted=
    for want in "$@"; do
      [ "$want" = "$key" ] && wanted=1
    done
    [ -n "$wanted" ] || continue
    [ -n "${!key:-}" ] && continue
    case "$value" in
      \"*\") value=${value#\"}; value=${value%\"} ;;
      \'*\') value=${value#\'}; value=${value%\'} ;;
      *) value=${value%% \#*}; value=${value%"${value##*[![:space:]]}"} ;;
    esac
    export "$key=$value"
  done <"$file"
}
