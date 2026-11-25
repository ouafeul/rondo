#!/bin/sh
# The page: the Target message, and the length of the token from the site Secret.
set -eu
mkdir -p /tmp/www
cat > /tmp/www/index.html <<EOF
<!doctype html>
<title>__NAME__</title>
<h1>${MESSAGE}</h1>
<p>$(hostname): token of ${#TOKEN} characters</p>
EOF
exec httpd -f -p 8080 -h /tmp/www
