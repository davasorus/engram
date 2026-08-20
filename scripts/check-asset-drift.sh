#!/usr/bin/env sh
# Fails if the vendored JS in internal/web/static doesn't match the versions
# declared in package.json. Keeps dependabot's manifest honest: a merged bump
# PR without a re-vendor (`npm ci && npm run assets`) turns CI red.
set -eu
want_htmx=$(node -p "require('./package.json').dependencies['htmx.org']")
want_alpine=$(node -p "require('./package.json').dependencies['alpinejs']")
have_htmx=$(grep -o 'version:"[0-9.]*"' internal/web/static/htmx.min.js | head -1 | grep -o '[0-9.]*')
have_alpine=$(grep -o 'version:"[0-9.]*"' internal/web/static/alpine.min.js | head -1 | grep -o '[0-9.]*')
status=0
[ "$want_htmx" = "$have_htmx" ] || { echo "DRIFT: htmx.org manifest=$want_htmx vendored=$have_htmx"; status=1; }
[ "$want_alpine" = "$have_alpine" ] || { echo "DRIFT: alpinejs manifest=$want_alpine vendored=$have_alpine"; status=1; }
[ $status -eq 0 ] && echo "vendored assets match package.json (htmx $have_htmx, alpine $have_alpine)"
exit $status
