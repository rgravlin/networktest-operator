#!/usr/bin/env sh
set -oeu pipefail
OUTPUT_FILE=$(mktemp)
HTTP_CODE=$(curl --silent --output "$OUTPUT_FILE" --write-out "%{http_code}" "$@")
CONFIG_DEBUG=${CONFIG_DEBUG:-true}

debugLog() {
  if [ "$CONFIG_DEBUG" = "true" ]; then
    echo "statusCode: $HTTP_CODE" >> "$OUTPUT_FILE"
    cat "$OUTPUT_FILE"
  fi
}

exitWithCode() {
  rm -f "$OUTPUT_FILE"
  exit $EXIT_CODE
}

# exit early
if [ "$HTTP_CODE" = "000" ] || [ "$HTTP_CODE" = "" ]; then
  EXIT_CODE=1
  debugLog
  exitWithCode $EXIT_CODE
fi

# any response code is a successful Layer 7 connection
# https://www.rfc-editor.org/rfc/rfc9110.html#name-overview-of-status-codes
if [ "$HTTP_CODE" -gt 0 ] && [ "$HTTP_CODE" -lt 600 ]; then
  EXIT_CODE=0
else
  EXIT_CODE=1
fi

debugLog
exitWithCode $EXIT_CODE