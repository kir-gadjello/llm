#!/bin/bash

# Colors for output
GREEN='\033[0;32m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color
BOLD='\033[1m'

echo -e "${BLUE}${BOLD}Starting LLM Test Suite...${NC}
"

# Run tests with JSON output to parse results accurately
TMP_LOG=$(mktemp)
TMP_ERR=$(mktemp)
go test -v -json ./... > "$TMP_LOG" 2> "$TMP_ERR"
TEST_EXIT_CODE=$?

# Check for build errors
if [ "$TEST_EXIT_CODE" -ne 0 ] && [ ! -s "$TMP_LOG" ]; then
    echo -e "${RED}${BOLD}Build or execution error!${NC}"
    cat "$TMP_ERR"
    rm "$TMP_LOG" "$TMP_ERR"
    exit 1
fi

# Parse JSON output for a clean summary
echo -e "${BOLD}Detailed Test Results:${NC}"
echo "--------------------------------"

# Extract individual test results from JSON
# We only show leaf tests (subtests or tests without subtests) to avoid double counting
# but for simplicity, showing all 'pass'/'fail' events for tests.
cat "$TMP_LOG" | jq -r 'select(.Test != null and (.Action == "pass" or .Action == "fail")) | "\(.Action) \(.Test) \(.Elapsed)"' | while read -r action test_name elapsed; do
    if [ "$action" == "pass" ]; then
        echo -e "[ ${GREEN}PASS${NC} ] $test_name (${elapsed}s)"
    else
        echo -e "[ ${RED}FAIL${NC} ] $test_name"
    fi
done

# Print final summary
# The package level summary is also in the JSON
TOTAL_PASS=$(grep -c '"Action":"pass"' "$TMP_LOG")
TOTAL_FAIL=$(grep -c '"Action":"fail"' "$TMP_LOG")

echo "--------------------------------"
if [ "$TEST_EXIT_CODE" -eq 0 ]; then
    echo -e "${GREEN}${BOLD}ALL TESTS PASSED!${NC}"
    exit 0
else
    echo -e "${RED}${BOLD}TEST SUITE FAILED!${NC}"
    rm -f "$TMP_LOG" "$TMP_ERR"
    exit 1
fi

rm -f "$TMP_LOG" "$TMP_ERR"
exit 0
