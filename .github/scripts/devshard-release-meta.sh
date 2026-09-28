#!/usr/bin/env bash
# Classify gonka release tags / workflow_dispatch inputs for
# publish_upgrade_binaries.yml. Prints key=value lines (no spaces around =).
set -euo pipefail

usage() {
	cat >&2 <<'EOF'
Usage:
  devshard-release-meta.sh tag --ref-name REF [--github-sha SHA]
  devshard-release-meta.sh dispatch --build-inference-chain BOOL --build-dapi BOOL
      --build-edge-api BOOL --build-devshardd BOOL [--chain-version VER]
      [--race-releases-tag TAG] [--devshard-version V] [--devshard-protocol-version V]
      [--devshard-binary-version V] [--github-sha SHA]
  devshard-release-meta.sh leftover-from-ref --ref-name REF
  devshard-release-meta.sh docker-tag --ref-name REF --owner OWNER [--registry ghcr.io]
  devshard-release-meta.sh check-stamps --binary PATH --protocol V --binary-version V
      [--docker-image IMAGE]   # workflow: devshardd-builder:latest (same as make)
EOF
	exit 2
}

die() {
	printf 'devshard-release-meta: %s\n' "$*" >&2
	exit 1
}

is_true() {
	case "${1:-}" in
	true | True | TRUE | yes | YES | 1) return 0 ;;
	*) return 1 ;;
	esac
}

is_three_part() {
	[[ ${1:-} =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]
}

strip_trailing_zeros() {
	local v=$1
	[[ $v =~ ^v[0-9]+(\.[0-9]+)*$ ]] || die "cannot strip version $v"
	while [[ $v =~ ^(.+)\.0$ ]]; do
		v=${BASH_REMATCH[1]}
	done
	printf '%s' "$v"
}

major_version() {
	local v=$1
	printf '%s' "${v%%.*}"
}

emit() {
	printf '%s=%s\n' "$1" "$2"
}

emit_bool() {
	if is_true "$2"; then
		emit "$1" true
	else
		emit "$1" false
	fi
}

emit_host_fields() {
	local binary=$1
	local version protocol
	is_three_part "$binary" || die "DEVSHARD_BINARY_VERSION must be vX.Y.Z, got ${binary:-<empty>}"
	version=$(strip_trailing_zeros "$binary")
	protocol=$(major_version "$binary")
	emit build_devshardd true
	emit devshard_binary_version "$binary"
	emit devshard_version "$version"
	emit devshard_protocol_version "$protocol"
	emit make_devshard_version "$protocol"
	emit release_name "Devshard Release $binary"
	emit release_tag "devshard/$binary"
	emit release_body_line "devshardd $version protocol $protocol binary stamp $binary"
}

emit_chain_false() {
	emit build_inference_chain false
	emit build_dapi false
	emit build_edge_api false
	emit chain_version ""
	emit race_releases_tag ""
}

emit_host_false() {
	emit build_devshardd false
	emit devshard_binary_version ""
	emit devshard_version ""
	emit devshard_protocol_version ""
	emit make_devshard_version ""
	emit release_name ""
	emit release_tag ""
	emit release_body_line ""
}

# Prints leftover vX.Y.Z with no trailing newline. Returns 1 if REF is not a host tag.
leftover_from_ref() {
	local ref_name=$1 leftover=""
	if [[ $ref_name =~ ^release/devshard/(.*)$ ]]; then
		leftover=${BASH_REMATCH[1]}
		is_three_part "$leftover" || return 1
		printf '%s' "$leftover"
		return 0
	fi
	if [[ $ref_name == *-devshard-* ]]; then
		if [[ $ref_name =~ ^release/v[0-9]+\.[0-9]+\.[0-9]+(.*)-devshard-(v[0-9]+\.[0-9]+\.[0-9]+)$ ]]; then
			printf '%s' "${BASH_REMATCH[2]}"
			return 0
		fi
		return 1
	fi
	return 1
}

leftover_from_ref_cmd() {
	local ref_name=""
	while [[ $# -gt 0 ]]; do
		case "$1" in
		--ref-name)
			ref_name=${2:-}
			shift 2
			;;
		*)
			die "unknown leftover-from-ref argument: $1"
			;;
		esac
	done
	[[ -n "$ref_name" ]] || die "--ref-name is required"
	local leftover=""
	leftover=$(leftover_from_ref "$ref_name") || die "not a host leftover tag: $ref_name"
	printf '%s\n' "$leftover"
}

docker_tag_cmd() {
	local ref_name="" owner="" registry="ghcr.io"
	while [[ $# -gt 0 ]]; do
		case "$1" in
		--ref-name)
			ref_name=${2:-}
			shift 2
			;;
		--owner)
			owner=${2:-}
			shift 2
			;;
		--registry)
			registry=${2:-}
			shift 2
			;;
		*)
			die "unknown docker-tag argument: $1"
			;;
		esac
	done
	[[ -n "$ref_name" ]] || die "--ref-name is required"
	[[ -n "$owner" ]] || die "--owner is required"
	local leftover="" prefix chain leftover_major image_tag version protocol
	leftover=$(leftover_from_ref "$ref_name") || die "docker-tag requires a chain-shaped host tag, got $ref_name"
	prefix=${ref_name#release/}
	local suffix="-devshard-${leftover}"
	[[ $prefix == *"$suffix" ]] || die "docker-tag requires release/vX.Y.Z-devshard-vA.B.C, got $ref_name"
	chain=${prefix%"$suffix"}
	[[ $chain == v* ]] || die "docker-tag chain version must start with v, got $chain from $ref_name"
	[[ $chain != "$prefix" ]] || die "docker-tag requires release/vX.Y.Z-devshard-vA.B.C, got $ref_name"
	leftover_major=$(major_version "$leftover")
	image_tag=${chain#v}-devshard-${leftover_major}
	version=$(strip_trailing_zeros "$leftover")
	protocol=$(major_version "$leftover")
	emit leftover "$leftover"
	emit leftover_major "$leftover_major"
	emit chain_version "$chain"
	emit image_tag "$image_tag"
	emit release_name "Devshard Release $leftover"
	emit release_tag "devshard/$leftover"
	emit release_body_line "devshardd $version protocol $protocol binary stamp $leftover"
	emit image_versiond "${registry}/${owner}/versiond:${image_tag}"
	emit image_versiond_router "${registry}/${owner}/versiond-router:${image_tag}"
}

classify_tag() {
	local ref_name="" github_sha=""
	while [[ $# -gt 0 ]]; do
		case "$1" in
		--ref-name)
			ref_name=${2:-}
			shift 2
			;;
		--github-sha)
			github_sha=${2:-}
			shift 2
			;;
		*)
			die "unknown tag argument: $1"
			;;
		esac
	done
	[[ -n "$ref_name" ]] || die "--ref-name is required"
	: "${github_sha:=}"

	local leftover=""
	if leftover=$(leftover_from_ref "$ref_name"); then
		emit mode host
		emit_chain_false
		emit_host_fields "$leftover"
		return
	fi
	if [[ $ref_name =~ ^release/devshard/ ]] || [[ $ref_name == *-devshard-* ]]; then
		die "host leftover must be vX.Y.Z, got invalid tag $ref_name"
	fi
	if [[ $ref_name =~ ^release/v[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
		[[ $ref_name =~ ^release/v[0-9]+\.[0-9]+\.[0-9]+-.+$ ]]; then
		emit mode chain
		emit build_inference_chain true
		emit build_dapi true
		emit build_edge_api true
		emit chain_version "${ref_name#release/}"
		emit race_releases_tag "$ref_name"
		emit_host_false
		return
	fi
	die "unrecognized release tag $ref_name"
}

classify_dispatch() {
	local build_inference_chain=false build_dapi=false build_edge_api=false build_devshardd=false
	local chain_version="" race_releases_tag=""
	local devshard_version="" devshard_protocol_version="" devshard_binary_version=""
	local github_sha=""
	while [[ $# -gt 0 ]]; do
		case "$1" in
		--build-inference-chain)
			build_inference_chain=${2:-}
			shift 2
			;;
		--build-dapi)
			build_dapi=${2:-}
			shift 2
			;;
		--build-edge-api)
			build_edge_api=${2:-}
			shift 2
			;;
		--build-devshardd)
			build_devshardd=${2:-}
			shift 2
			;;
		--chain-version)
			chain_version=${2:-}
			shift 2
			;;
		--race-releases-tag)
			race_releases_tag=${2:-}
			shift 2
			;;
		--devshard-version)
			devshard_version=${2:-}
			shift 2
			;;
		--devshard-protocol-version)
			devshard_protocol_version=${2:-}
			shift 2
			;;
		--devshard-binary-version)
			devshard_binary_version=${2:-}
			shift 2
			;;
		--github-sha)
			github_sha=${2:-}
			shift 2
			;;
		*)
			die "unknown dispatch argument: $1"
			;;
		esac
	done
	: "${github_sha:=}"

	local want_chain=false want_host=false
	is_true "$build_inference_chain" && want_chain=true
	is_true "$build_dapi" && want_chain=true
	is_true "$build_edge_api" && want_chain=true
	is_true "$build_devshardd" && want_host=true

	if ! $want_chain && ! $want_host; then
		die "select at least one of inference-chain, dapi, edge-api, devshardd"
	fi

	local mode
	if $want_chain && $want_host; then
		mode=both
	elif $want_host; then
		mode=host
	else
		mode=chain
	fi
	emit mode "$mode"
	emit_bool build_inference_chain "$build_inference_chain"
	emit_bool build_dapi "$build_dapi"
	emit_bool build_edge_api "$build_edge_api"

	if $want_chain; then
		[[ -n "$chain_version" ]] || die "chain_version is required when building inference-chain, dapi, or edge-api"
		emit chain_version "$chain_version"
		if [[ -z "$race_releases_tag" ]]; then
			race_releases_tag="release/${chain_version}"
		fi
		emit race_releases_tag "$race_releases_tag"
	else
		emit chain_version ""
		emit race_releases_tag ""
	fi

	if $want_host; then
		[[ -n "$devshard_version" ]] || die "DEVSHARD_VERSION is required when building devshardd"
		[[ -n "$devshard_protocol_version" ]] || die "DEVSHARD_PROTOCOL_VERSION is required when building devshardd"
		[[ -n "$devshard_binary_version" ]] || die "DEVSHARD_BINARY_VERSION is required when building devshardd"
		is_three_part "$devshard_binary_version" || die "DEVSHARD_BINARY_VERSION must be vX.Y.Z, got $devshard_binary_version"
		emit build_devshardd true
		emit devshard_binary_version "$devshard_binary_version"
		emit devshard_version "$devshard_version"
		emit devshard_protocol_version "$devshard_protocol_version"
		emit make_devshard_version "$devshard_protocol_version"
		emit release_name "Devshard Release $devshard_binary_version"
		emit release_tag "devshard/$devshard_binary_version"
		emit release_body_line "devshardd $devshard_version protocol $devshard_protocol_version binary stamp $devshard_binary_version"
	else
		emit_host_false
	fi
}

# When --docker-image is set, exec the copied binary inside that image.
# make devshardd-release tags the CGO builder as devshardd-builder:latest
# (golang alpine + gcc). Bare alpine:3.23 has no libgcc_s.so.1.
print_stamp() {
	local binary=$1 docker_image=$2 flag=$3
	if [[ -n $docker_image ]]; then
		local abs
		abs=$(python3 -c 'import os, sys; print(os.path.realpath(sys.argv[1]))' "$binary")
		docker run --rm --entrypoint /devshardd -v "${abs}:/devshardd:ro" "$docker_image" "$flag"
	else
		"$binary" "$flag"
	fi
}

check_stamps() {
	local binary="" protocol="" binary_version="" docker_image=""
	while [[ $# -gt 0 ]]; do
		case "$1" in
		--binary)
			binary=${2:-}
			shift 2
			;;
		--protocol)
			protocol=${2:-}
			shift 2
			;;
		--binary-version)
			binary_version=${2:-}
			shift 2
			;;
		--docker-image)
			docker_image=${2:-}
			shift 2
			;;
		*)
			die "unknown check-stamps argument: $1"
			;;
		esac
	done
	[[ -n "$binary" && -n "$protocol" && -n "$binary_version" ]] || die "check-stamps requires --binary --protocol --binary-version"
	[[ -x "$binary" ]] || die "binary is not executable: $binary"
	if [[ -n $docker_image ]]; then
		command -v docker >/dev/null || die "docker not found (needed for --docker-image)"
	fi
	local got_protocol got_binary
	got_protocol=$(print_stamp "$binary" "$docker_image" --print-protocol-version | tr -d '\r\n')
	got_binary=$(print_stamp "$binary" "$docker_image" --print-binary-version | tr -d '\r\n')
	[[ $got_protocol == "$protocol" ]] || die "protocol stamp '$got_protocol' != expected '$protocol'"
	[[ $got_binary == "$binary_version" ]] || die "binary stamp '$got_binary' != expected '$binary_version'"
}

cmd=${1:-}
[[ -n "$cmd" ]] || usage
shift || true
case "$cmd" in
tag) classify_tag "$@" ;;
dispatch) classify_dispatch "$@" ;;
leftover-from-ref) leftover_from_ref_cmd "$@" ;;
docker-tag) docker_tag_cmd "$@" ;;
check-stamps) check_stamps "$@" ;;
-h | --help) usage ;;
*)
	die "unknown command: $cmd"
	;;
esac
