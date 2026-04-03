#!/usr/bin/env bash
# Source this file to use the same Go toolchain as CI/local build:
#   source ./scripts/env-go.sh
# Default: Go from /usr/local/go (install tarball from https://go.dev/dl/, e.g. go1.21.11.linux-amd64.tar.gz)
if [ -x /usr/local/go/bin/go ]; then
  export PATH="/usr/local/go/bin:${PATH}"
fi
