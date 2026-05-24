#!/usr/bin/env sh
set -eu

bad_paths=""
for path in static/manage.html static/management.html static/panel-meta.json static/dist static/panel-dist.zip; do
	if [ -e "$path" ]; then
		bad_paths="${bad_paths}
${path}"
	fi
done

if [ -n "$bad_paths" ]; then
	echo "Vendored panel build output must not be committed to the static/ directory."
	echo "Panel source lives in panel/ and is built during Docker image creation."
	echo "Unexpected path(s):${bad_paths}"
	exit 1
fi
