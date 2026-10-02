#!/bin/sh
set -eu
umask 077

if [ ! -f app.yaml ]; then
    (set -C; cat dev/app.yaml > app.yaml)
fi

if [ -f .env ]; then
    exit 0
fi

password=$(openssl rand -hex 32)
(set -C; printf 'POSTGRES_PASSWORD=%s\n' "$password" > .env)
