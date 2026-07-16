#!/bin/sh
set -eu

test -r "${AJIASU_CONFIG:-/run/ajiasu/ajiasu.conf}"
kill -0 1
