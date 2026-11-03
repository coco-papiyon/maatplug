#!/bin/sh
set -eu
trap 'echo "Installation failed. Check the CLI error above; completed steps may remain." >&2' 0
provider=${1:?Expected codex or claude}
case "$provider" in
    codex) selector=maatplug@maatplug-local; manifest=.agents/plugins/marketplace.json ;;
    claude) selector=maatplug@maatplug-claude-local; manifest=.claude-plugin/marketplace.json ;;
    *) echo 'Expected codex or claude' >&2; exit 1 ;;
esac
package_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
command -v "$provider" >/dev/null 2>&1 || { echo "$provider CLI is required on PATH." >&2; exit 1; }
test -f "$package_root/$manifest"
chmod u+x "$package_root/maatplug" "$package_root/maatplug-web"

# Stable per-user command paths, without modifying shell startup files.
bin_dir="$HOME/.local/bin"
mkdir -p "$bin_dir"
for binary in maatplug maatplug-web; do
    target="$bin_dir/$binary"
    if [ -e "$target" ] || [ -L "$target" ]; then
        if [ ! -L "$target" ] || [ "$(readlink "$target")" != "$package_root/$binary" ]; then
            echo "$target already exists. Move it aside before installing this package." >&2
            exit 1
        fi
    else
        ln -s "$package_root/$binary" "$target"
    fi
done
export PATH="$bin_dir:$PATH"
"$provider" plugin marketplace add "$package_root"
if [ "$provider" = codex ]; then
    "$provider" plugin add "$selector"
else
    "$provider" plugin install "$selector" --scope user
fi
trap - 0
printf '\nInstalled: %s\nNext steps:\n' "$selector"
echo '1. Ensure ~/.local/bin is on PATH in the environment that launches your provider.'
echo '   For this terminal: export PATH="$HOME/.local/bin:$PATH"'
echo '   Add that line to your shell startup file if necessary, then log in again.'
if [ "$provider" = codex ]; then
    echo '2. Start a new Codex session; open /plugins and enable maatplug.'
    echo '   Review and approve/trust its hooks when prompted.'
else
    echo '2. Restart Claude Code; check maatplug in /plugin and its hooks in /hooks.'
    echo '   Remove old maatplug init hook entries first to prevent duplicate recording.'
    echo '   See README.txt for the migration steps.'
fi
echo '3. Run sh start-web.sh and open http://127.0.0.1:4173.'
echo '4. Send a prompt in a project and check the dashboard.'
echo 'Keep this extracted directory in place. See README.txt before moving it.'
