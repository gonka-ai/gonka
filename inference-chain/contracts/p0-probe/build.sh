#!/usr/bin/env bash
set -euo pipefail

readonly script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly container_engine="${CONTAINER_ENGINE:-docker}"
readonly optimizer_image="cosmwasm/optimizer:0.17.0@sha256:7e0b9229c1a4118d0c9a2af2e7f5d95a91f264c26a2ce5681c779926e74d7f85"
readonly optimizer_platform="linux/amd64"
readonly artifact_relative="artifacts/p0_probe.wasm"
readonly manifest_relative="artifacts/checksums.txt"

sha256_file() {
    local file="$1"
    if command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "${file}" | awk '{print $1}'
    elif command -v sha256sum >/dev/null 2>&1; then
        sha256sum "${file}" | awk '{print $1}'
    else
        echo "neither shasum nor sha256sum is available" >&2
        return 1
    fi
}

fixture_inputs() {
    printf '%s\n' Cargo.lock Cargo.toml Makefile README.md build.sh
    (
        cd "${script_dir}"
        find src -type f -name '*.rs' -print
    )
    printf '%s\n' "${artifact_relative}"
}

verify_fixture() (
    local manifest_path="${script_dir}/${manifest_relative}"
    test -f "${manifest_path}" || {
        echo "fixture manifest is missing: ${manifest_path}" >&2
        return 1
    }

    local expected_paths actual_paths duplicates
    expected_paths="$(mktemp "${TMPDIR:-/tmp}/p0-probe-expected.XXXXXX")"
    actual_paths="$(mktemp "${TMPDIR:-/tmp}/p0-probe-actual.XXXXXX")"
    duplicates="$(mktemp "${TMPDIR:-/tmp}/p0-probe-duplicates.XXXXXX")"
    trap 'rm -f "${expected_paths:-}" "${actual_paths:-}" "${duplicates:-}"' EXIT

    fixture_inputs | LC_ALL=C sort > "${expected_paths}"
    awk 'NF != 2 { exit 2 } { print $2 }' "${manifest_path}" | LC_ALL=C sort > "${actual_paths}"
    LC_ALL=C sort "${actual_paths}" | uniq -d > "${duplicates}"
    if test -s "${duplicates}"; then
        echo "fixture manifest contains duplicate paths:" >&2
        sed 's/^/  /' "${duplicates}" >&2
        return 1
    fi
    if ! cmp -s "${expected_paths}" "${actual_paths}"; then
        echo "fixture manifest path set is stale; run 'make build CONTAINER_ENGINE=docker'" >&2
        diff -u "${expected_paths}" "${actual_paths}" >&2 || true
        return 1
    fi

    while read -r expected relative_path extra; do
        if test -n "${extra:-}" || [[ ! "${expected}" =~ ^[0-9a-f]{64}$ ]]; then
            echo "invalid checksum entry for ${relative_path:-<missing path>}" >&2
            return 1
        fi
        local source_path="${script_dir}/${relative_path}"
        test -f "${source_path}" || {
            echo "fixture input is missing: ${source_path}" >&2
            return 1
        }
        local actual
        actual="$(sha256_file "${source_path}")"
        if test "${actual}" != "${expected}"; then
            echo "fixture checksum mismatch: ${relative_path}" >&2
            return 1
        fi
    done < "${manifest_path}"
)

build_fixture() (
    command -v "${container_engine}" >/dev/null 2>&1 || {
        echo "container engine is unavailable: ${container_engine}" >&2
        return 1
    }

    mkdir -p "${script_dir}/artifacts"
    local build_root artifact_tmp manifest_tmp
    build_root="$(mktemp -d "${TMPDIR:-/tmp}/p0-probe-build.XXXXXX")"
    artifact_tmp="$(mktemp "${script_dir}/artifacts/p0_probe.wasm.XXXXXX")"
    manifest_tmp="$(mktemp "${script_dir}/artifacts/checksums.txt.XXXXXX")"
    trap 'rm -rf "${build_root:-}"; rm -f "${artifact_tmp:-}" "${manifest_tmp:-}"' EXIT

    mkdir -p "${build_root}/src" "${build_root}/artifacts"
    cp "${script_dir}/Cargo.toml" "${script_dir}/Cargo.lock" "${build_root}/"
    cp -R "${script_dir}/src/." "${build_root}/src/"

    "${container_engine}" run --rm \
        --platform "${optimizer_platform}" \
        --volume "${build_root}:/code" \
        --workdir /code \
        --entrypoint cargo \
        "${optimizer_image}" \
        metadata --locked --format-version 1 --no-deps >/dev/null

    "${container_engine}" run --rm \
        --platform "${optimizer_platform}" \
        --volume "${build_root}:/code" \
        "${optimizer_image}"

    local built_artifact="${build_root}/${artifact_relative}"
    test -s "${built_artifact}" || {
        echo "optimizer did not create ${artifact_relative}" >&2
        return 1
    }
    local wasm_header
    wasm_header="$(od -An -tx1 -N8 "${built_artifact}" | tr -d ' \n')"
    test "${wasm_header}" = "0061736d01000000" || {
        echo "optimizer output is not a WebAssembly v1 module: ${built_artifact}" >&2
        return 1
    }

    cp "${built_artifact}" "${artifact_tmp}"
    while IFS= read -r relative_path; do
        local source_path="${script_dir}/${relative_path}"
        if test "${relative_path}" = "${artifact_relative}"; then
            source_path="${artifact_tmp}"
        fi
        printf '%s  %s\n' "$(sha256_file "${source_path}")" "${relative_path}"
    done < <(fixture_inputs | LC_ALL=C sort) > "${manifest_tmp}"

    mv "${artifact_tmp}" "${script_dir}/${artifact_relative}"
    artifact_tmp=""
    mv "${manifest_tmp}" "${script_dir}/${manifest_relative}"
    manifest_tmp=""
    verify_fixture
)

case "${1:-build}" in
    build)
        build_fixture
        ;;
    verify)
        verify_fixture
        ;;
    *)
        echo "usage: $0 [build|verify]" >&2
        exit 2
        ;;
esac
