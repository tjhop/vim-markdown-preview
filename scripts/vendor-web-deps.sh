#!/usr/bin/env bash
# vendor-web-deps.sh -- Manages browser-side JS/CSS/font dependencies
# defined in web/vendor-manifest.json.
#
# Usage:
#   ./scripts/vendor-web-deps.sh           # Download missing files (idempotent)
#   ./scripts/vendor-web-deps.sh --check   # Verify all files exist (CI guard)
#   ./scripts/vendor-web-deps.sh --force   # Re-download all files
#
# These vendored files are committed to the repo and embedded in the Go
# binary via go:embed at compile time.
#
# Requires: curl, jq

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
WEB_DIR="${PROJECT_ROOT}/web"
MANIFEST="${WEB_DIR}/vendor-manifest.json"

MODE="vendor"
if [[ "${1:-}" == "--check" ]]; then
    MODE="check"
elif [[ "${1:-}" == "--force" ]]; then
    MODE="force"
fi

if ! command -v jq &>/dev/null; then
    echo "error: jq is required but not installed" >&2
    exit 1
fi

if [[ "${MODE}" != "check" ]] && ! command -v curl &>/dev/null; then
    echo "error: curl is required but not installed" >&2
    exit 1
fi

if [[ ! -f "${MANIFEST}" ]]; then
    echo "error: manifest not found at ${MANIFEST}" >&2
    exit 1
fi

total=0
missing=0
downloaded=0
skipped=0
failed=0

lib_count=$(jq '.libraries | length' "${MANIFEST}")
for (( i=0; i<lib_count; i++ )); do
    name=$(jq -r ".libraries[$i].name" "${MANIFEST}")
    version=$(jq -r ".libraries[$i].version" "${MANIFEST}")
    file_count=$(jq ".libraries[$i].files | length" "${MANIFEST}")

    for (( j=0; j<file_count; j++ )); do
        url=$(jq -r ".libraries[$i].files[$j].url" "${MANIFEST}")
        dest=$(jq -r ".libraries[$i].files[$j].dest" "${MANIFEST}")
        dest_path="${WEB_DIR}/${dest}"
        total=$((total + 1))

        if [[ "${MODE}" == "check" ]]; then
            if [[ ! -f "${dest_path}" ]]; then
                echo "MISSING: ${dest} (${name}@${version})" >&2
                missing=$((missing + 1))
            fi
            continue
        fi

        # Idempotent: skip files that already exist unless --force.
        if [[ "${MODE}" != "force" ]] && [[ -f "${dest_path}" ]]; then
            skipped=$((skipped + 1))
            continue
        fi

        mkdir -p "$(dirname "${dest_path}")"

        if curl -fsSL --retry 3 --retry-delay 2 -o "${dest_path}" "${url}"; then
            echo "ok: ${dest} (${name}@${version})"
            downloaded=$((downloaded + 1))
        else
            echo "FAIL: ${dest} (${url})" >&2
            failed=$((failed + 1))
        fi
    done
done

echo ""

if [[ "${MODE}" == "check" ]]; then
    echo "Checked ${total} files, ${missing} missing."
    if [[ ${missing} -gt 0 ]]; then
        echo "error: run scripts/vendor-web-deps.sh to download missing files" >&2
        exit 1
    fi
    echo "All vendored web dependencies are present."
else
    echo "Total: ${total}, downloaded: ${downloaded}, skipped: ${skipped}, failed: ${failed}."
    if [[ ${failed} -gt 0 ]]; then
        echo "error: ${failed} downloads failed" >&2
        exit 1
    fi
fi
