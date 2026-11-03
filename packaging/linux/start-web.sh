#!/bin/sh
set -eu
package_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$package_root"
chmod u+x "$package_root/maatplug" "$package_root/maatplug-web"
export PATH="$package_root:$PATH"
echo 'Open http://127.0.0.1:4173 in your browser (unless --addr is supplied).'
echo 'Press Ctrl+C to stop.'
exec "$package_root/maatplug-web" --addr 127.0.0.1:4173 "$@"
