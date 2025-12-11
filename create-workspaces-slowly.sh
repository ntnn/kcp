#!/bin/bash

PREFIX=a
COUNT=${1:-0}
MAX=$(( $COUNT + ${2:-1000} ))

while true; do
  WORKSPACE_NAME="$(printf "test-%s-%04d" "$PREFIX" $COUNT)"
  (set -x; kubectl create-workspace "$WORKSPACE_NAME")

  COUNT=$((COUNT+1))

  if [[ "$COUNT" -ge "$MAX" ]]; then
    echo "Reached max count of $MAX workspaces. Exiting."
    exit 0
  fi

  sleep 1
done
