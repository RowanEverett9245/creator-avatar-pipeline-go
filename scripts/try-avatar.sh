#!/bin/sh
set -eu

: "${USER_ID:?set USER_ID to an Infrai user ID}"
: "${AVATAR_FILE:?set AVATAR_FILE to a JPEG, PNG, or WebP file}"

curl --fail-with-body \
  -X POST "http://localhost:8080/avatars/${USER_ID}" \
  -H "X-Request-ID: avatar-$(date +%s)" \
  -F "avatar=@${AVATAR_FILE}"
