#!/bin/sh
set -eu
printf 'ajiasu 4.2.3.0 (fake)\n'
printf 'Command: %s\n' "${1:-help}"
case "${1:-help}" in
  login) printf 'Login Result: OK\n' ;;
  list) printf 'vvn-test-1 ok Test Node #1\n' ;;
  connect) exec sleep "${FAKE_CONNECT_SECONDS:-1}" ;;
  *) printf 'usage: ajiasu {login|list|connect|logout}\n' ;;
esac
