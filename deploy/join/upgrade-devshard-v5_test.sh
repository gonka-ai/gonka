#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
tmpdir=$(mktemp -d)
export FAKE_POSTGRES_DIR=$tmpdir/postgres
if [[ ${KEEP_TEST_TMPDIR:-false} == true ]]; then
    echo "upgrade-devshard-v5_test tmpdir: $tmpdir" >&2
else
    trap 'rm -rf "$tmpdir"' EXIT
fi

# Unit tests exercise a working-tree build before the release tag exists.
export DEVSHARD_V5_ALLOW_UNRELEASED_SOURCE=true
export DEVSHARD_V5_MAINTENANCE_ACK=true
export DEVSHARD_V5_PREFLIGHT_CPUS=16
export DEVSHARD_V5_PREFLIGHT_MEMORY_MIB=65536

fail() {
    echo "upgrade-devshard-v5_test: $*" >&2
    exit 1
}

write_fake_docker() {
    cat >"$tmpdir/docker" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail

printf 'EDGE_API_IMAGE=%s VERSIOND_IMAGE=%s EDGE_API_ROUTER_IMAGE=%s VERSIOND_ROUTER_IMAGE=%s PROXY_POLICY_IMAGE=%s PROXY_ROUTER_IMAGE=%s DEVSHARD_POSTGRES_IMAGE=%s' \
    "${EDGE_API_IMAGE-}" "${VERSIOND_IMAGE-}" \
    "${EDGE_API_ROUTER_IMAGE-}" "${VERSIOND_ROUTER_IMAGE-}" \
    "${PROXY_POLICY_IMAGE-}" "${PROXY_ROUTER_IMAGE-}" \
    "${DEVSHARD_POSTGRES_IMAGE-}" >>"$DOCKER_LOG"
printf ' ::' >>"$DOCKER_LOG"
printf ' %q' "$@" >>"$DOCKER_LOG"
printf '\n' >>"$DOCKER_LOG"

if [[ ${1:-} == info ]]; then
    exit 0
fi
if [[ ${1:-} == compose && ${2:-} == version && ${3:-} == --short ]]; then
    printf '%s\n' "${FAKE_COMPOSE_VERSION:-2.24.4}"
    exit 0
fi

if [[ ${1:-} == inspect ]]; then
    if [[ $# -eq 2 ]]; then
		requested=${2#cid-}
		container=${requested%%.gonka-displaced-*}
		if [[ $requested == *.gonka-displaced-* ]]; then
			if [[ ! -f $FAKE_STATE_DIR/backup-$container ]] && \
				{ [[ ! -f $FAKE_STATE_DIR/image-$container ]] || \
				  [[ $(<"$FAKE_STATE_DIR/image-$container") != \
					gonka-upgrade-rollback/* ]]; }; then
				exit 1
			fi
			printf '[]\n'
			exit 0
		fi
		[[ ! -f $FAKE_STATE_DIR/original-missing-$container ]] || exit 1
        case " ${EXISTING_CONTAINERS-} " in
			*" $container "*)
				if [[ $2 == cid-* ]]; then
					jq -cn --arg name "$container" --arg image "old-$container" \
						--argjson exec_health "${EXEC_HEALTH_OVERRIDE:-false}" \
						--argjson custom_runtime "${CUSTOM_RUNTIME_OVERRIDE:-false}" \
						'[{Name:("/" + $name),Config:{Image:$image,Hostname:$name,
						MacAddress:"02:42:ac:1e:00:2a",
						Env:[],Labels:{"com.docker.compose.project":"gonka-test",
						"com.docker.compose.service":$name},Cmd:["versiond"],
						Entrypoint:["tini","--"],
						Healthcheck:{Test:(if $exec_health then
						["CMD","/usr/local/bin/check","ready"] else
						["CMD-SHELL","true"] end),Interval:1000000000,
						Timeout:1000000000,Retries:1,StartPeriod:0,
						StartInterval:500000000}},
						HostConfig:{RestartPolicy:{Name:"always",MaximumRetryCount:0},
						ReadonlyRootfs:false,Privileged:false,
						ExtraHosts:(if $custom_runtime then ["database:192.0.2.10"] else [] end),
						Dns:(if $custom_runtime then ["192.0.2.53"] else [] end),
						PortBindings:(if $custom_runtime then
						{"8080/tcp":[{HostIp:"127.0.0.1",HostPort:"18080"}]}
						else {} end),
						Ulimits:(if $custom_runtime then
						[{Name:"nofile",Soft:4096,Hard:8192}] else [] end),
						Sysctls:(if $custom_runtime then
						{"net.core.somaxconn":"1024"} else {} end),
						Devices:(if $custom_runtime then
						[{PathOnHost:"/dev/null",PathInContainer:"/dev/test",
						CgroupPermissions:"r"}] else [] end),
						Memory:(if $custom_runtime then 536870912 else 0 end),
						Tmpfs:{"/run":"rw,noexec,size=65536k"},
						Binds:(if $name == "devshard-postgres" then
						["/srv/gonka/postgres:/var/lib/postgresql/gonka:rw"] else [] end),
						Mounts:[]},
						Mounts:(if $name == "devshard-postgres" then [
						{Type:"volume",Name:"postgres-v4-volume",Source:"/docker/volumes/postgres-v4-volume/_data",Destination:"/var/lib/postgresql/data",RW:true,Propagation:""},
						{Type:"bind",Name:"",Source:"/srv/gonka/postgres",Destination:"/var/lib/postgresql/gonka",Mode:"Z",RW:true,Propagation:"rprivate"}]
						else [{Type:"bind",Name:"",Source:"/srv/gonka/state",Destination:"/var/lib/gonka",Mode:"Z",RW:true,Propagation:"rprivate"},
						{Type:"tmpfs",Name:"",Source:"",Destination:"/run",Mode:"",RW:true,Propagation:""}] end),
						NetworkSettings:{Networks:{"gonka-test_default":{
						Aliases:[$name],NetworkID:"network-default",
						MacAddress:(if $custom_runtime then
						"02:42:ac:1e:00:7f" else "" end),
						IPAMConfig:{IPv4Address:"172.30.0.42",IPv6Address:"",
						LinkLocalIPs:["169.254.10.1"]},
						DriverOpts:{test:"preserved"},Links:["versiond:legacy"],
						GwPriority:10}}}}]'
				fi
				exit 0
				;;
            *) exit 1 ;;
        esac
    fi
    case ${3:-} in
        '{{json .Config.Labels}}')
            container=${4:-unknown}
            files=${RUNTIME_COMPOSE_FILES-}
            if [[ -z $files ]]; then
                files=$JOIN_DIR/docker-compose.yml
                case " ${EXISTING_CONTAINERS-} " in
                    *' versiond2 '* | *' devshard-postgres '*)
                        files+=",$JOIN_DIR/docker-compose.versiond.yml"
                        ;;
                esac
                case " ${EXISTING_CONTAINERS-} " in
                    *' edge-api2 '* | *' edge-api3 '*)
                        files+=",$JOIN_DIR/docker-compose.edge-api-multi.yml"
                        ;;
                esac
            fi
            if [[ -n ${INCOMPATIBLE_COMPOSE_CONTAINER-} && \
                $container == "$INCOMPATIBLE_COMPOSE_CONTAINER" ]]; then
                files=$JOIN_DIR/docker-compose.observability.yml
            fi
            jq -cn --arg files "$files" \
                --arg workdir "${RUNTIME_COMPOSE_WORKDIR:-$JOIN_DIR}" \
                '{"com.docker.compose.project":"gonka-test",
                  "com.docker.compose.project.config_files":$files,
                  "com.docker.compose.project.working_dir":$workdir}'
            ;;
        '{{index .Config.Labels "ai.gonka.component"}}')
            printf '%s\n' "${EXISTING_PROXY_COMPONENT-}"
            ;;
        '{{json .HostConfig.PortBindings}}')
            jq -cn \
                --arg http "${FAKE_PROXY_HTTP_PORT:-8000}" \
                --arg https "${FAKE_PROXY_HTTPS_PORT:-8443}" \
                '{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":$http}],
                  "443/tcp":[{"HostIp":"0.0.0.0","HostPort":$https}]}'
            ;;
        '{{.Image}}')
			service=${4#cid-}
			service=${service%%.gonka-displaced-*}
            if [[ -f $FAKE_STATE_DIR/image-$service ]]; then
                cat "$FAKE_STATE_DIR/image-$service"
            else
                printf 'sha256:old-%s\n' "${4:-unknown}"
            fi
            ;;
        '{{.Config.Image}}|{{.State.Running}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}')
            service=${4#cid-}
            image=old-$service
            if [[ -f $FAKE_STATE_DIR/image-$service ]]; then
                image=$(<"$FAKE_STATE_DIR/image-$service")
            fi
            if [[ ${ASSUME_RELEASE_STATE-} == true ]]; then
                case $service in
                    versiond | versiond2) image=$VERSIOND_IMAGE ;;
                    edge-api | edge-api2 | edge-api3) image=$EDGE_API_IMAGE ;;
                    proxy) image=$PROXY_ROUTER_IMAGE ;;
                    proxy-policy | proxy-policy2) image=$PROXY_POLICY_IMAGE ;;
                    devshard-postgres) image=$DEVSHARD_POSTGRES_IMAGE ;;
                esac
            fi
            printf '%s|true|healthy\n' "$image"
            ;;
        '{{.State.Running}}')
            service=${4#cid-}
			service=${service%%.gonka-displaced-*}
            if [[ $service == "${STOPPED_VERSIOND_SERVICE-}" && \
                ! -f $FAKE_STATE_DIR/running-$service ]] || \
                [[ -f $FAKE_STATE_DIR/stopped-$service ]]; then
                printf 'false\n'
            else
                printf 'true\n'
            fi
            ;;
		'{{json .NetworkSettings.Networks}}')
			service=${4#cid-}
			service=${service%%.gonka-displaced-*}
			if [[ -f $FAKE_STATE_DIR/backup-disconnected-$service ]]; then
				printf '{}\n'
			else
				printf '{"gonka-test_default":{"NetworkID":"network-default"}}\n'
			fi
			;;
        '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}')
            printf 'healthy\n'
            ;;
		'{{.Image}} {{or (index .Config.Labels "com.docker.compose.config-hash") ""}} {{.State.Running}}/{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}')
			service=${4#cid-}
			image=sha256:old-$service
			hash=old-hash-$service
			if [[ ${ASSUME_RELEASE_STATE-} == true ]]; then
				case $service in
					versiond | versiond2) image=$VERSIOND_IMAGE ;;
					versiond-router) image=$VERSIOND_ROUTER_IMAGE ;;
					devshard-postgres) image=$DEVSHARD_POSTGRES_IMAGE ;;
				esac
				hash=hash-$service
			fi
			printf '%s %s true/healthy\n' "$image" "$hash"
			;;
        '{{range .Config.Env}}{{println .}}{{end}}')
            case ${4:-} in
				versiond | versiond2)
					printf 'PGHOST=%s\nPGDATABASE=devshardd\nPGUSER=devshardd\nVERSIOND_ORACLE_URL=http://api:9100/versions\n' \
                        "${RUNTIME_PGHOST:-devshard-postgres}"
					[[ -z ${RUNTIME_PGSERVICE:-} ]] || \
						printf 'PGSERVICE=%s\n' "$RUNTIME_PGSERVICE"
					if [[ ${4:-} == versiond2 ]]; then
						[[ -z ${RUNTIME_PGOPTIONS2:-} ]] || \
							printf 'PGOPTIONS=%s\n' "$RUNTIME_PGOPTIONS2"
					else
						[[ -z ${RUNTIME_PGOPTIONS:-} ]] || \
							printf 'PGOPTIONS=%s\n' "$RUNTIME_PGOPTIONS"
					fi
					[[ -z ${RUNTIME_DATABASE_URL:-} ]] || \
						printf 'DATABASE_URL=%s\n' "$RUNTIME_DATABASE_URL"
                    ;;
                versiond-router)
                    printf 'VERSIOND_HOSTS=versiond versiond2\n'
                    ;;
                edge-api-router)
                    printf 'EDGE_API_HOSTS=edge-api edge-api2 edge-api3\n'
                    ;;
                *) exit 1 ;;
            esac
            ;;
		'{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}')
			printf 'postgres-v4-volume\n'
			;;
		'{{json .Config.Env}}')
			service=${4#cid-}
			case $service in
				devshard-postgres)
					pgdata=/var/lib/postgresql/data
					[[ ${ASSUME_RELEASE_STATE-} != true ]] || pgdata=/var/lib/postgresql/gonka/data
					printf '["PGDATA=%s","POSTGRES_PASSWORD=secret"]\n' "$pgdata"
					;;
				versiond | versiond2)
					printf '["PGHOST=devshard-postgres","PGPORT=5432","PGDATABASE=devshardd","PGUSER=devshardd","PGPASSWORD=secret"]\n'
					;;
				*) printf '[]\n' ;;
			esac
			;;
        *) exit 1 ;;
    esac
    exit 0
fi

if [[ ${1:-} == exec ]]; then
    container=${2:-}
    service=${container#cid-}
    barrier_install=false
    barrier_remove=false
    for arg in "$@"; do
        case $arg in
            nginx)
                [[ ! -f $FAKE_STATE_DIR/haproxy-$service ]]
                exit
                ;;
            haproxy)
                [[ -f $FAKE_STATE_DIR/haproxy-$service ]]
                exit
                ;;
            GONKA_BARRIER_HOSTS=versiond | VERSIOND_HOSTS=versiond)
                barrier_install=true
                ;;
            /etc/gonka-upgrade-barrier)
                barrier_remove=true
                ;;
			*"to_regclass('public.devshard_session_index')"*)
				printf '%s\n' "${DEVSHARD_SCHEMA_STATE:-t}"
				exit 0
				;;
        esac
        case $arg in
            http://*) probe_url=$arg ;;
        esac
    done
    if [[ $barrier_install == true ]]; then
        : >"$FAKE_STATE_DIR/barrier-versiond-router"
    fi
    if [[ $barrier_remove == true ]]; then
        rm -f "$FAKE_STATE_DIR/barrier-versiond-router"
    fi
    if [[ -n ${probe_url:-} ]]; then
        is_rollback=false
        [[ ! -f $FAKE_STATE_DIR/rollback-$service ]] || is_rollback=true
        if [[ $is_rollback == true && \
            $service == "${ROLLBACK_PROBE_FAIL_SERVICE-}" ]]; then
            exit 1
        fi
        if [[ $is_rollback == false && \
            $service == "${INTERRUPT_TARGET_BASELINE_SERVICE-}" && \
            -f $FAKE_STATE_DIR/running-$service && \
            ! -f $FAKE_STATE_DIR/interrupted-target-baseline-$service && \
            $probe_url == http://127.0.0.1:8080/healthz ]]; then
            : >"$FAKE_STATE_DIR/interrupted-target-baseline-$service"
            sleep 1
        fi
        if [[ $is_rollback == true && \
            $service == "${ROLLBACK_MISSING_ROUTE_SERVICE-}" && \
            $probe_url == http://127.0.0.1:8080/v3/healthz ]]; then
            exit 1
        fi
        if [[ $service == versiond-router && \
            -f $FAKE_STATE_DIR/barrier-versiond-router && \
            $probe_url == http://127.0.0.1:8080/v5/healthz ]]; then
            exit 1
        fi
        if [[ $service == versiond-router && \
            -n ${TARGET_ROUTER_MISSING_VERSION-} && \
            $probe_url == "http://127.0.0.1:8080/$TARGET_ROUTER_MISSING_VERSION/healthz" ]]; then
            exit 1
        fi
        if [[ $service == versiond-router && \
            -n ${ROLLBACK_ROUTER_PROBE_FAIL_SERVICE-} && \
            -f $FAKE_STATE_DIR/rollback-$ROLLBACK_ROUTER_PROBE_FAIL_SERVICE ]]; then
            exit 1
        fi
        case $container:$probe_url in
            versiond*:http://api:9100/versions | cid-versiond*:http://api:9100/versions)
                if ! printenv VERSION_CATALOG_JSON; then
                    printf '%s\n' '{"versions":[{"name":"v3"},{"name":"v4"}]}'
                fi
                exit 0
                ;;
            cid-versiond*:http://127.0.0.1:8080/internal/storage-identity)
                if [[ $service == versiond2 && -n ${VERSIOND2_STORAGE_IDENTITY-} ]]; then
                    jq -cn --arg identity "$VERSIOND2_STORAGE_IDENTITY" '{identity:$identity}'
                else
                    printf '{"identity":"shared-database"}\n'
                fi
                exit 0
                ;;
            cid-versiond*:http://127.0.0.1:8080/healthz)
                if [[ $is_rollback == true && \
                    $service == "${ROLLBACK_EMPTY_VERSIOND_SERVICE-}" ]]; then
                    printf '[]\n'
                elif [[ $is_rollback == true && \
                    $service == "${ROLLBACK_MISSING_VERSION_SERVICE-}" ]]; then
                    printf '[{"name":"v3","port":5000,"status":"running"}]\n'
                elif [[ $service == "${SPECIAL_VERSIOND_HEALTH_SERVICE-}" ]]; then
                    printf '[{"name":"v4+hotfix","port":5000,"status":"running"},{"name":"v4}x","port":5001,"status":"running"}]\n'
                else
                    case $service in
                        versiond)
                            printf '[{"name":"v3","port":5000,"status":"running"},{"name":"v4","port":5001,"status":"running"}]\n'
                            ;;
                        versiond2)
                            if [[ ${VERSIOND2_UNIQUE_VERSION-} == true ]]; then
                                printf '[{"name":"v4","port":5001,"status":"running"},{"name":"v5","port":5002,"status":"running"}]\n'
                            else
                                printf '[{"name":"v4","port":5001,"status":"running"}]\n'
                            fi
                            ;;
                        versiond-router)
                            printf '[{"name":"v4","port":5001,"status":"running"}]\n'
                            ;;
                    esac
                fi
                exit 0
                ;;
                cid-versiond*:http://127.0.0.1:8080/v3/healthz | \
                cid-versiond*:http://127.0.0.1:8080/v4/healthz | \
                cid-versiond*:http://127.0.0.1:8080/v5/healthz | \
                cid-versiond*:http://127.0.0.1:8080/v4%2Bhotfix/healthz | \
                cid-versiond*:http://127.0.0.1:8080/v4%7Dx/healthz | \
                cid-edge-api*:http://127.0.0.1:18080/v1/versions)
                exit 0
                ;;
            *) exit 1 ;;
        esac
    fi
    exit 0
fi

if [[ ${1:-} == run ]]; then
    printf '%s\n' "${POSTGRES_MIGRATION_PROBE:-source 1 0 1000000000000000000}"
    exit 0
fi

if [[ ${1:-} == cp || ${1:-} == tag || \
    (${1:-} == image && ${2:-} == rm) ]]; then
    exit 0
fi
if [[ ${1:-} == image && ${2:-} == inspect && \
    ${3:-} == gonka-upgrade-rollback/* ]]; then
    exit 0
fi
if [[ ${1:-} == image && ${2:-} == inspect ]]; then
	if [[ ${3:-} == --format && ${4:-} == '{{.Id}}' ]]; then
		printf '%s\n' "${5:-}"
	fi
	exit 0
fi
if [[ ${1:-} == rm ]]; then
	container=${!#}
	requested=${container#cid-}
	service=${requested%%.gonka-displaced-*}
	if [[ $requested == *.gonka-displaced-* ]]; then
		[[ ${FAIL_DISPLACED_REMOVE_SERVICE-} != "$service" ]] || exit 1
		rm -f "$FAKE_STATE_DIR/backup-$service" \
			"$FAKE_STATE_DIR/backup-disconnected-$service"
		exit 0
	fi
	rm -f "$FAKE_STATE_DIR/stopped-$service" "$FAKE_STATE_DIR/running-$service"
	: >"$FAKE_STATE_DIR/original-missing-$service"
	exit 0
fi
if [[ ${1:-} == rename ]]; then
	old=${2#cid-}
	new=$3
	service=${old%%.gonka-displaced-*}
	if [[ $new == *.gonka-displaced-* ]]; then
		: >"$FAKE_STATE_DIR/backup-$service"
		: >"$FAKE_STATE_DIR/original-missing-$service"
	fi
	exit 0
fi
if [[ ${1:-} == stop ]]; then
	service=${!#}
	service=${service#cid-}
	service=${service%%.gonka-displaced-*}
	rm -f "$FAKE_STATE_DIR/running-$service"
	: >"$FAKE_STATE_DIR/stopped-$service"
	exit 0
fi
if [[ ${1:-} == create ]]; then
	name=
	image=
	previous=
	for arg in "$@"; do
		[[ $previous != --name ]] || name=$arg
		[[ $arg != gonka-upgrade-rollback/* ]] || image=$arg
		previous=$arg
	done
	[[ -n $name ]] || exit 1
	rm -f "$FAKE_STATE_DIR/original-missing-$name"
	if [[ -n $image ]]; then
		: >"$FAKE_STATE_DIR/rollback-$name"
		printf '%s\n' "$image" >"$FAKE_STATE_DIR/image-$name"
	fi
	printf 'cid-%s\n' "$name"
	exit 0
fi
if [[ ${1:-} == network ]]; then
	if [[ ${2:-} == disconnect ]]; then
		requested=${!#}
		service=${requested%%.gonka-displaced-*}
		: >"$FAKE_STATE_DIR/backup-disconnected-$service"
	fi
	exit 0
fi
if [[ ${1:-} == start ]]; then
	service=${2#cid-}
	service=${service%%.gonka-displaced-*}
	rm -f "$FAKE_STATE_DIR/stopped-$service"
	: >"$FAKE_STATE_DIR/running-$service"
	exit 0
fi

[[ ${1:-} == compose ]] || exit 1

previous=
for arg in "$@"; do
	if [[ $previous == --hash ]]; then
		printf '%s hash-%s\n' "$arg" "$arg"
		exit 0
	fi
	previous=$arg
done

for arg in "$@"; do
    if [[ $arg == config ]]; then
        pg_host=${RENDERED_PGHOST:-devshard-postgres}
        jq -cn --arg pg "$pg_host" \
			--arg database_url "${RENDERED_DATABASE_URL:-}" \
			--arg pgservice "${RENDERED_PGSERVICE:-}" \
			--arg pgservicefile "${RENDERED_PGSERVICEFILE:-}" \
			--arg pgoptions "${RENDERED_PGOPTIONS:-}" \
			--arg pgoptions2 "${RENDERED_PGOPTIONS2:-${RENDERED_PGOPTIONS:-}}" \
            --arg postgres_image "${DEVSHARD_POSTGRES_IMAGE-}" \
			--arg versiond_image "${VERSIOND_IMAGE-}" \
			--arg router_image "${VERSIOND_ROUTER_IMAGE-}" \
            --argjson versiond2_replicas \
                "${RENDERED_VERSIOND2_REPLICAS:-1}" \
            --arg pg2db "${RENDERED_PGDATABASE2:-devshardd}" \
            --arg join "$JOIN_DIR" \
			--arg postgres_dir "$FAKE_POSTGRES_DIR" \
            --arg proxy_http "${RENDERED_PROXY_HTTP_PORT:-8000}" \
            --arg proxy_https "${RENDERED_PROXY_HTTPS_PORT:-8443}" \
            --arg policy "${RENDERED_POLICY_NETWORK:-gonka-proxy-policy-front}" \
            --arg front "${RENDERED_ROUTER_FRONT_NETWORK:-gonka-versiond-router-front}" \
            --arg back "${RENDERED_ROUTER_BACK_NETWORK:-gonka-versiond-router-back}" \
            '{name:"gonka-test",networks:{
                default:{name:"gonka-test_default"},
                "proxy-policy-front":{name:$policy},
                "versiond-router-front":{name:$front},
                "versiond-router-back":{name:$back}
            },services:{
                proxy:{container_name:"proxy",ports:[
                    {target:80,published:$proxy_http,protocol:"tcp"},
                    {target:443,published:$proxy_https,protocol:"tcp"}
                ]},
                "proxy-policy":{},
                "proxy-policy2":{},
				versiond:{container_name:"versiond",image:$versiond_image,environment:{DATABASE_URL:$database_url,PGSERVICE:$pgservice,PGSERVICEFILE:$pgservicefile,PGOPTIONS:$pgoptions,PGHOST:$pg,PGDATABASE:"devshardd",PGUSER:"devshardd",PGPORT:"5432",DEVSHARD_STORAGE_MODE:"postgres"}},
				versiond2:{container_name:"versiond2",image:$versiond_image,deploy:{replicas:$versiond2_replicas},environment:{DATABASE_URL:$database_url,PGSERVICE:$pgservice,PGSERVICEFILE:$pgservicefile,PGOPTIONS:$pgoptions2,PGHOST:$pg,PGDATABASE:$pg2db,PGUSER:"devshardd",PGPORT:"5432",DEVSHARD_STORAGE_MODE:"postgres"}},
				"versiond-router":{container_name:"versiond-router",image:$router_image},
                "devshard-postgres":{container_name:"devshard-postgres",image:$postgres_image,volumes:[{type:"bind",source:$postgres_dir,target:"/var/lib/postgresql/gonka"}]},
                "edge-api":{container_name:"edge-api"},
                "edge-api2":{container_name:"edge-api2"},
                "edge-api3":{container_name:"edge-api3"}
            }}
			| if $pg == "devshard-postgres" then .
			  else del(.services["devshard-postgres"]) end'
        exit 0
    fi
done

service=${!#}
for arg in "$@"; do
    if [[ $arg == ps ]]; then
        if [[ $service == "${MISSING_COMPOSE_SERVICE-}" && \
            ! -f $FAKE_STATE_DIR/running-$service ]]; then
            exit 0
        fi
		case " ${EXISTING_CONTAINERS-} " in
			*" $service "*) ;;
			*) [[ -f $FAKE_STATE_DIR/running-$service || \
				-f $FAKE_STATE_DIR/image-$service ]] || exit 0 ;;
		esac
        printf 'cid-%s\n' "$service"
        exit 0
    fi
done

for arg in "$@"; do
    if [[ $arg == stop ]]; then
        rm -f "$FAKE_STATE_DIR/running-$service"
        : >"$FAKE_STATE_DIR/stopped-$service"
        exit 0
    fi
    if [[ $arg == pull ]]; then
        exit 0
    fi
    if [[ $arg == start ]]; then
        rm -f "$FAKE_STATE_DIR/stopped-$service"
        : >"$FAKE_STATE_DIR/running-$service"
        exit 0
    fi
done

is_up=false
no_start=false
for arg in "$@"; do
    [[ $arg == up ]] && is_up=true
    [[ $arg == --no-start ]] && no_start=true
done
image=
if [[ $is_up == true ]]; then
    case $service in
        versiond | versiond2) image=${VERSIOND_IMAGE-} ;;
        versiond-router) image=${VERSIOND_ROUTER_IMAGE-} ;;
        edge-api | edge-api2 | edge-api3) image=${EDGE_API_IMAGE-} ;;
        edge-api-router) image=${EDGE_API_ROUTER_IMAGE-} ;;
        devshard-postgres) image=${DEVSHARD_POSTGRES_IMAGE-} ;;
    esac
fi
if [[ $is_up == true && $service == "${FAIL_SERVICE-}" ]]; then
    [[ $image == gonka-upgrade-rollback/* ]] || exit 1
fi
if [[ $is_up == true ]]; then
    if [[ $image == gonka-upgrade-rollback/* ]]; then
        : >"$FAKE_STATE_DIR/rollback-$service"
    else
        rm -f "$FAKE_STATE_DIR/rollback-$service"
    fi
    [[ -z $image ]] || printf '%s\n' "$image" >"$FAKE_STATE_DIR/image-$service"
    if [[ $no_start == true ]]; then
        rm -f "$FAKE_STATE_DIR/running-$service"
        : >"$FAKE_STATE_DIR/stopped-$service"
    else
        rm -f "$FAKE_STATE_DIR/stopped-$service"
        : >"$FAKE_STATE_DIR/running-$service"
    fi
    if [[ $service == versiond-router ]]; then
        if [[ $image == gonka-upgrade-rollback/* ]]; then
            rm -f "$FAKE_STATE_DIR/haproxy-versiond-router"
        else
            : >"$FAKE_STATE_DIR/haproxy-versiond-router"
        fi
    fi
fi
if [[ $is_up == true && $service == "${BLOCK_SERVICE-}" ]]; then
    if [[ $image != gonka-upgrade-rollback/* ]]; then
        trap 'exit 143' TERM HUP
        trap 'exit 130' INT
        if [[ ${BLOCK_SIGNAL-} != none ]]; then
            kill -"$BLOCK_SIGNAL" "$PPID"
        fi
        while :; do
            sleep 1
        done
    fi
fi
exit 0
EOF
    chmod +x "$tmpdir/docker"
}

run_upgrade() {
    local mode=$1
    local failed_service=$2
    local log=$3
    local versiond_mode=${4:-ha}
    local state_dir=$log.state

    rm -rf "$state_dir"
    mkdir -p "$state_dir"
    rm -f "$tmpdir/upgrade-complete" "$tmpdir/upgrade-complete.in-progress"
    if [[ ${PERSISTED_VERSIOND_BARRIER-} == true ]]; then
        : >"$state_dir/barrier-versiond-router"
    fi
    : >"$log"
    if DOCKER_BIN="$tmpdir/docker" \
        DOCKER_LOG="$log" \
        FAIL_SERVICE="$failed_service" \
        BLOCK_SERVICE=none \
        BLOCK_SIGNAL=none \
        EXISTING_CONTAINERS="proxy versiond versiond2 versiond-router devshard-postgres edge-api edge-api2 edge-api3 edge-api-router" \
        FAKE_STATE_DIR="$state_dir" \
        JOIN_DIR="$script_dir" \
        GONKA_CONFIG_ENV="$tmpdir/config.env" \
        INTERRUPT_TARGET_BASELINE_SERVICE="${INTERRUPT_TARGET_BASELINE_SERVICE-}" \
        ROLLBACK_EMPTY_VERSIOND_SERVICE="${ROLLBACK_EMPTY_VERSIOND_SERVICE-}" \
        ROLLBACK_MISSING_VERSION_SERVICE="${ROLLBACK_MISSING_VERSION_SERVICE-}" \
        ROLLBACK_MISSING_ROUTE_SERVICE="${ROLLBACK_MISSING_ROUTE_SERVICE-}" \
        ROLLBACK_PROBE_FAIL_SERVICE="${ROLLBACK_PROBE_FAIL_SERVICE-}" \
        ROLLBACK_ROUTER_PROBE_FAIL_SERVICE="${ROLLBACK_ROUTER_PROBE_FAIL_SERVICE-}" \
        SPECIAL_VERSIOND_HEALTH_SERVICE="${SPECIAL_VERSIOND_HEALTH_SERVICE-}" \
        EXEC_HEALTH_OVERRIDE="${EXEC_HEALTH_OVERRIDE:-false}" \
        CUSTOM_RUNTIME_OVERRIDE="${CUSTOM_RUNTIME_OVERRIDE:-false}" \
        STOPPED_VERSIOND_SERVICE="${STOPPED_VERSIOND_SERVICE-}" \
        TARGET_ROUTER_MISSING_VERSION="${TARGET_ROUTER_MISSING_VERSION-}" \
        VERSIOND2_UNIQUE_VERSION="${VERSIOND2_UNIQUE_VERSION-}" \
        VERSIOND2_STORAGE_IDENTITY="${VERSIOND2_STORAGE_IDENTITY-}" \
        UPGRADE_ROLLBACK_VERIFY_TIMEOUT="${UPGRADE_ROLLBACK_VERIFY_TIMEOUT-}" \
        UPGRADE_ROLLBACK_VERIFY_INTERVAL="${UPGRADE_ROLLBACK_VERIFY_INTERVAL:-1}" \
        UPGRADE_ROLLBACK_STABILITY_CHECKS="${UPGRADE_ROLLBACK_STABILITY_CHECKS:-1}" \
        UPGRADE_ROUTER_RELOAD_SETTLE=0 \
        RUNTIME_COMPOSE_FILES="${RUNTIME_COMPOSE_FILES-}" \
        RUNTIME_COMPOSE_WORKDIR="${RUNTIME_COMPOSE_WORKDIR-}" \
        RUNTIME_PGHOST="${RUNTIME_PGHOST-}" \
        RENDERED_PGHOST="${RENDERED_PGHOST-}" \
        RENDERED_VERSIOND2_REPLICAS="${RENDERED_VERSIOND2_REPLICAS:-1}" \
        RENDERED_POLICY_NETWORK="${RENDERED_POLICY_NETWORK-}" \
        RENDERED_ROUTER_FRONT_NETWORK="${RENDERED_ROUTER_FRONT_NETWORK-}" \
        RENDERED_ROUTER_BACK_NETWORK="${RENDERED_ROUTER_BACK_NETWORK-}" \
        INCOMPATIBLE_COMPOSE_CONTAINER="${INCOMPATIBLE_COMPOSE_CONTAINER-}" \
        MISSING_COMPOSE_SERVICE="${MISSING_COMPOSE_SERVICE-}" \
        POSTGRES_MIGRATION_PROBE="${POSTGRES_MIGRATION_PROBE-}" \
        FAIL_INGRESS_FINALIZE="${FAIL_INGRESS_FINALIZE:-false}" \
        FAIL_DISPLACED_REMOVE_SERVICE=\
"${FAIL_DISPLACED_REMOVE_SERVICE-}" \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode "$versiond_mode" --edge-mode "$mode" \
        >"$tmpdir/stdout" 2>"$tmpdir/stderr"; then
        fail "$mode upgrade unexpectedly succeeded when $failed_service failed"
    fi
}

run_auto_upgrade() {
    local containers=$1
    local log=$2
    local stdout=$3
    local state_dir=$log.state

    rm -rf "$state_dir"
    mkdir -p "$state_dir"
    rm -f "$tmpdir/upgrade-complete" "$tmpdir/upgrade-complete.in-progress"
    : >"$log"
    if ! DOCKER_BIN="$tmpdir/docker" \
        DOCKER_LOG="$log" \
        FAIL_SERVICE=none \
        BLOCK_SERVICE=none \
        BLOCK_SIGNAL=none \
        EXISTING_CONTAINERS="$containers proxy" \
        FAKE_STATE_DIR="$state_dir" \
        JOIN_DIR="$script_dir" \
        GONKA_CONFIG_ENV="$tmpdir/config.env" \
        UPGRADE_ROUTER_RELOAD_SETTLE=0 \
        RUNTIME_COMPOSE_FILES="${RUNTIME_COMPOSE_FILES-}" \
        RUNTIME_COMPOSE_WORKDIR="${RUNTIME_COMPOSE_WORKDIR-}" \
        RUNTIME_PGHOST="${RUNTIME_PGHOST-}" \
        RENDERED_PGHOST="${RENDERED_PGHOST-}" \
        RENDERED_POLICY_NETWORK="${RENDERED_POLICY_NETWORK-}" \
        RENDERED_ROUTER_FRONT_NETWORK="${RENDERED_ROUTER_FRONT_NETWORK-}" \
        RENDERED_ROUTER_BACK_NETWORK="${RENDERED_ROUTER_BACK_NETWORK-}" \
        INCOMPATIBLE_COMPOSE_CONTAINER="${INCOMPATIBLE_COMPOSE_CONTAINER-}" \
        VERSIOND2_STORAGE_IDENTITY="${VERSIOND2_STORAGE_IDENTITY-}" \
        MISSING_COMPOSE_SERVICE="${MISSING_COMPOSE_SERVICE-}" \
        POSTGRES_MIGRATION_PROBE="${POSTGRES_MIGRATION_PROBE-}" \
        "$script_dir/upgrade-devshard-v5.sh" >"$stdout" 2>"$tmpdir/stderr"; then
        cat "$tmpdir/stderr" >&2
        fail "automatic topology upgrade failed"
    fi
}

run_postcondition_interrupted_upgrade() {
    local log=$1
    local state_dir=$log.state
    local stdout=$log.stdout
    local stderr=$log.stderr
    local marker=$state_dir/interrupted-target-baseline-versiond2
    local upgrade_pid status=0
    local deadline=$((SECONDS + 10))

    rm -rf "$state_dir"
    mkdir -p "$state_dir"
    rm -f "$tmpdir/upgrade-complete" "$tmpdir/upgrade-complete.in-progress"
    : >"$log"
    DOCKER_BIN="$tmpdir/docker" \
        DOCKER_LOG="$log" \
        FAIL_SERVICE=none \
        BLOCK_SERVICE=none \
        BLOCK_SIGNAL=none \
        EXISTING_CONTAINERS="proxy versiond versiond2 versiond-router devshard-postgres edge-api" \
        FAKE_STATE_DIR="$state_dir" \
        JOIN_DIR="$script_dir" \
        GONKA_CONFIG_ENV="$tmpdir/config.env" \
        INTERRUPT_TARGET_BASELINE_SERVICE=versiond2 \
        UPGRADE_ROLLBACK_VERIFY_TIMEOUT=5 \
        UPGRADE_ROLLBACK_VERIFY_INTERVAL=1 \
        UPGRADE_ROLLBACK_STABILITY_CHECKS=1 \
        UPGRADE_ROUTER_RELOAD_SETTLE=0 \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode ha --edge-mode single \
        >"$stdout" 2>"$stderr" &
    upgrade_pid=$!

    while [[ ! -f $marker ]]; do
        if ! kill -0 "$upgrade_pid" 2>/dev/null; then
            wait "$upgrade_pid" || status=$?
            cat "$stderr" >&2
            fail "upgrade exited before the target route postcondition"
        fi
        ((SECONDS < deadline)) || {
            kill -KILL "$upgrade_pid" 2>/dev/null || true
            wait "$upgrade_pid" 2>/dev/null || true
            fail "upgrade did not reach the target route postcondition"
        }
        sleep 0.05
    done

    kill -TERM "$upgrade_pid"
    wait "$upgrade_pid" || status=$?
    ((status != 0)) || fail \
        "upgrade interrupted during the target postcondition exited successfully"
}

run_interrupted_upgrade() {
    local signal=$1
    local log=$2
    local stdout=$3
    local stderr=$4
    local state_dir=$log.state

    rm -rf "$state_dir"
    mkdir -p "$state_dir"
    rm -f "$tmpdir/upgrade-complete" "$tmpdir/upgrade-complete.in-progress"
    : >"$log"
    if DOCKER_BIN="$tmpdir/docker" \
        DOCKER_LOG="$log" \
        FAIL_SERVICE=none \
        BLOCK_SERVICE=versiond2 \
        BLOCK_SIGNAL="$signal" \
        EXISTING_CONTAINERS="proxy versiond versiond2 versiond-router devshard-postgres edge-api" \
        FAKE_STATE_DIR="$state_dir" \
        JOIN_DIR="$script_dir" \
        GONKA_CONFIG_ENV="$tmpdir/config.env" \
        ROLLBACK_EMPTY_VERSIOND_SERVICE='' \
        ROLLBACK_MISSING_VERSION_SERVICE='' \
        ROLLBACK_PROBE_FAIL_SERVICE='' \
        UPGRADE_ROLLBACK_VERIFY_TIMEOUT=5 \
        UPGRADE_ROLLBACK_VERIFY_INTERVAL=1 \
        UPGRADE_ROLLBACK_STABILITY_CHECKS=1 \
        UPGRADE_ROUTER_RELOAD_SETTLE=0 \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode ha --edge-mode single \
        >"$stdout" 2>"$stderr"; then
        fail "upgrade interrupted by $signal exited successfully"
    fi
}

resume_interrupted_upgrade() {
    local state_dir=$1 log=$2 stdout=$3 stderr=$4

    : >"$log"
    if ! DOCKER_BIN="$tmpdir/docker" \
        DOCKER_LOG="$log" \
        FAIL_SERVICE=none \
        BLOCK_SERVICE=none \
        BLOCK_SIGNAL=none \
        EXISTING_CONTAINERS="proxy versiond versiond2 versiond-router devshard-postgres edge-api" \
        FAKE_STATE_DIR="$state_dir" \
        JOIN_DIR="$script_dir" \
        GONKA_CONFIG_ENV="$tmpdir/config.env" \
        UPGRADE_ROLLBACK_VERIFY_TIMEOUT=5 \
        UPGRADE_ROLLBACK_VERIFY_INTERVAL=1 \
        UPGRADE_ROLLBACK_STABILITY_CHECKS=1 \
        UPGRADE_ROUTER_RELOAD_SETTLE=0 \
        POSTGRES_DEPLOYMENT_PREFLIGHT_FAIL_MODE=\
"${POSTGRES_DEPLOYMENT_PREFLIGHT_FAIL_MODE-}" \
        FAIL_DISPLACED_REMOVE_SERVICE=\
"${FAIL_DISPLACED_REMOVE_SERVICE-}" \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode ha --edge-mode single \
        >"$stdout" 2>"$stderr"; then
        cat "$stderr" >&2
        fail "interrupted upgrade did not resume"
    fi
}

recover_interrupted_rollback() {
    local state_dir=$1 log=$2 stdout=$3 stderr=$4

    : >"$log"
    if DOCKER_BIN="$tmpdir/docker" \
        DOCKER_LOG="$log" \
        FAIL_SERVICE=none \
        BLOCK_SERVICE=none \
        BLOCK_SIGNAL=none \
        EXISTING_CONTAINERS="proxy versiond versiond2 versiond-router devshard-postgres edge-api" \
        FAKE_STATE_DIR="$state_dir" \
        JOIN_DIR="$script_dir" \
        GONKA_CONFIG_ENV="$tmpdir/config.env" \
        UPGRADE_ROLLBACK_VERIFY_TIMEOUT=5 \
        UPGRADE_ROLLBACK_VERIFY_INTERVAL=1 \
        UPGRADE_ROLLBACK_STABILITY_CHECKS=1 \
        UPGRADE_ROUTER_RELOAD_SETTLE=0 \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode ha --edge-mode single \
        >"$stdout" 2>"$stderr"; then
        fail "interrupted rollback recovery continued into deployment"
    fi
}


assert_contains() {
    local file=$1
    local pattern=$2

    grep -F -- "$pattern" "$file" >/dev/null || {
        cat "$file" >&2
        fail "missing expected command: $pattern"
    }
}

assert_not_contains() {
    local file=$1
    local pattern=$2

    if grep -F -- "$pattern" "$file" >/dev/null; then
        cat "$file" >&2
        fail "unexpected command: $pattern"
    fi
}

assert_no_compose_mutation() {
    local file=$1

    if grep -E -- ' :: compose .* (pull|up|stop|start|rm)($| )' "$file" \
        >/dev/null; then
        cat "$file" >&2
        fail "topology validation performed a Compose mutation"
    fi
}

assert_edge_api_untouched() {
    local file=$1

    if grep -E -- \
        ' :: compose .* (pull|up|stop|start|rm)( |$).*edge-api| :: (tag|image rm).*edge-api| :: exec (cid-)?edge-api' \
        "$file" >/dev/null; then
        cat "$file" >&2
        fail "the devshard v5 updater mutated the existing edge-api deployment"
    fi
}

line_number() {
    local file=$1
    local pattern=$2

    grep -nFm1 -- "$pattern" "$file" | cut -d: -f1
}

line_number_regex() {
    local file=$1
    local pattern=$2

    grep -nEm1 -- "$pattern" "$file" | cut -d: -f1
}

write_fake_docker
cat >"$tmpdir/fleet" <<'EOF'
#!/usr/bin/env bash
set -eu
printf 'fleet %s\n' "$*" >>"$DOCKER_LOG"
printf 'fleet-networks %s %s\n' \
    "${VERSIOND_ROUTER_FRONT_NETWORK-}" \
    "${VERSIOND_ROUTER_BACK_NETWORK-}" >>"$DOCKER_LOG"
if [[ ${1:-} == spec-hash ]]; then
    if ! printenv FLEET_SPEC_HASH; then
        printf '%064d\n' 1
    fi
    exit 0
fi
EOF
cat >"$tmpdir/enable-router" <<'EOF'
#!/usr/bin/env bash
set -eu
printf 'enable-router %s\n' "$*" >>"$DOCKER_LOG"
printf 'enable-router-compose %s\n' \
    "${ROUTER_HA_EXPECTED_COMPOSE_SHA256-}" >>"$DOCKER_LOG"
printf 'enable-router-fleet %s\n' \
    "${ROUTER_HA_EXPECTED_FLEET_SPEC_SHA256-}" >>"$DOCKER_LOG"
if [[ " $* " == *" --recover-only "* ]]; then
    tmp=$(mktemp "$(dirname -- "$ROUTER_HA_TRANSACTION_JOURNAL")/.recover.XXXXXX")
    jq 'if .transaction.decision == "commit" then
            .transaction.ingress.state = "committed"
        else .transaction.ingress.state = "rolled_back"
        end |
        del(.transaction.ingress.rollback_models)' \
        "$ROUTER_HA_TRANSACTION_JOURNAL" >"$tmp"
    mv "$tmp" "$ROUTER_HA_TRANSACTION_JOURNAL"
    exit 0
fi
if [[ " $* " == *" --finalize-transaction "* ]]; then
	[[ $(jq -r '.transaction.decision // ""' \
		"$ROUTER_HA_TRANSACTION_JOURNAL") == commit ]] || exit 1
	[[ ${FAIL_INGRESS_FINALIZE:-false} != true ]] || exit 1
    tmp=$(mktemp "$(dirname -- "$ROUTER_HA_TRANSACTION_JOURNAL")/.finalize.XXXXXX")
    jq '.transaction.ingress.state = "committed" |
        del(.transaction.ingress.rollback_models)' \
        "$ROUTER_HA_TRANSACTION_JOURNAL" >"$tmp"
    mv "$tmp" "$ROUTER_HA_TRANSACTION_JOURNAL"
    exit 0
fi
printf '%s\n' "$PROXY_ROUTER_IMAGE" >"$FAKE_STATE_DIR/image-proxy"
printf '%s\n' "$PROXY_POLICY_IMAGE" >"$FAKE_STATE_DIR/image-proxy-policy"
if [[ ${INCOMPLETE_INGRESS_STATE:-false} != true ]]; then
    printf '%s\n' "$PROXY_POLICY_IMAGE" >"$FAKE_STATE_DIR/image-proxy-policy2"
fi
if [[ ${ROUTER_HA_DEFER_COMMIT:-false} == true ]]; then
    tmp=$(mktemp "$(dirname -- "$ROUTER_HA_TRANSACTION_JOURNAL")/.prepare.XXXXXX")
    jq '.transaction.ingress = {
        state:"prepared", rollback_models:{policy:{},proxy:{}}, touched:[]}' \
        "$ROUTER_HA_TRANSACTION_JOURNAL" >"$tmp"
    mv "$tmp" "$ROUTER_HA_TRANSACTION_JOURNAL"
fi
EOF
cat >"$tmpdir/postgres-deployment-preflight" <<'EOF'
#!/usr/bin/env bash
set -eu
printf 'postgres-deployment-preflight %s\n' "$*" >>"$DOCKER_LOG"
mode=live
expected=
while (($#)); do
    case $1 in
        --compose-only) mode=compose; shift ;;
        --runtime-contract-only) mode=runtime; shift ;;
        --expected-identity)
            (($# >= 2)) && [[ -n $2 ]] || exit 2
            expected=$2
            shift 2
            ;;
        --)
            shift
            (($# > 0)) || exit 2
            break
            ;;
        *) exit 2 ;;
    esac
done
[[ ${POSTGRES_DEPLOYMENT_PREFLIGHT_FAIL_MODE:-} != "$mode" ]] || exit 1
[[ -z $expected || $expected == "${POSTGRES_IDENTITY:-shared-database}" ]] || exit 1
EOF
chmod +x "$tmpdir/fleet" "$tmpdir/enable-router" \
    "$tmpdir/postgres-deployment-preflight"
printf 'export DEVSHARD_POSTGRES_DATA_DIR=%q\nexport UPGRADE_ENABLE_ROUTER_HA=true\nexport VERSIOND_ROUTER_FLEET_BIN=%q\nexport ROUTER_HA_ENABLE_BIN=%q\nexport DEVSHARD_V5_POSTGRES_PREFLIGHT_BIN=%q\nexport DEVSHARD_V5_UPGRADE_MARKER=%q\nexport DEVSHARD_V5_VERSIOND_IMAGE=untrusted-config-image\n' \
    "$tmpdir/postgres" "$tmpdir/fleet" "$tmpdir/enable-router" \
    "$tmpdir/postgres-deployment-preflight" \
    "$tmpdir/upgrade-complete" >"$tmpdir/config.env"

preflight_log=$tmpdir/preflight.log
: >"$preflight_log"
DOCKER_BIN="$tmpdir/docker" \
DOCKER_LOG="$preflight_log" \
FAIL_SERVICE=none \
EXISTING_CONTAINERS="proxy versiond versiond2 versiond-router devshard-postgres edge-api" \
FAKE_STATE_DIR="$tmpdir/preflight-state" \
JOIN_DIR="$script_dir" \
GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
    --versiond-mode ha --edge-mode single --preflight-only \
    >"$tmpdir/preflight.stdout" 2>"$tmpdir/preflight.stderr" || {
        cat "$tmpdir/preflight.stderr" >&2
        fail "read-only release preflight failed"
    }
assert_no_compose_mutation "$preflight_log"
assert_contains "$preflight_log" "fleet spec-hash"
assert_not_contains "$preflight_log" "fleet apply"
assert_not_contains "$preflight_log" "enable-router "
assert_not_contains "$preflight_log" "VERSIOND_IMAGE=untrusted-config-image"
assert_contains "$preflight_log" \
    "VERSIOND_IMAGE=ghcr.io/product-science/versiond:0.2.15-devshard-v5"
grep -q 'Devshard v5 release preflight passed' "$tmpdir/preflight.stdout" || {
    cat "$tmpdir/preflight.stdout" >&2
    fail "release preflight did not report success"
}

: >"$preflight_log"
if RENDERED_VERSIOND2_REPLICAS=0 \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$preflight_log" \
    FAIL_SERVICE=none \
    EXISTING_CONTAINERS="proxy versiond versiond2 versiond-router devshard-postgres edge-api" \
    FAKE_STATE_DIR="$tmpdir/preflight-state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode ha --edge-mode single --preflight-only \
        >"$tmpdir/preflight-replicas-zero.stdout" \
        2>"$tmpdir/preflight-replicas-zero.stderr"; then
    fail "release preflight accepted a replicas=0 first HA cutover"
fi
assert_no_compose_mutation "$preflight_log"
grep -q 'restore it to 1 for the one-time v5 cutover' \
    "$tmpdir/preflight-replicas-zero.stderr" || fail \
    "release preflight did not diagnose the disabled second HA member"

# The unsupported first cutover with the second member disabled must fail
# before creating a durable transaction that blocks a corrected retry.
RENDERED_VERSIOND2_REPLICAS=0 \
    run_upgrade single none "$tmpdir/first-cutover-replicas-zero.log"
[[ ! -e $tmpdir/upgrade-complete.in-progress ]] || fail \
    "replicas=0 first cutover left a blocking prepared journal"
assert_no_compose_mutation "$tmpdir/first-cutover-replicas-zero.log"
assert_not_contains "$tmpdir/first-cutover-replicas-zero.log" " :: tag "

if VERSION_CATALOG_JSON='{"versions":[{"name":"v4:hotfix"}]}' \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/incompatible-version-name.log" \
    FAIL_SERVICE=none \
    EXISTING_CONTAINERS="proxy versiond versiond2 versiond-router devshard-postgres edge-api" \
    FAKE_STATE_DIR="$tmpdir/incompatible-version-name.state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode ha --edge-mode single --preflight-only \
        >"$tmpdir/incompatible-version-name.stdout" \
        2>"$tmpdir/incompatible-version-name.stderr"; then
    fail "release preflight accepted a version name unsupported by HA routing"
fi
assert_no_compose_mutation "$tmpdir/incompatible-version-name.log"
grep -q 'HA-incompatible name' "$tmpdir/incompatible-version-name.stderr" || {
    cat "$tmpdir/incompatible-version-name.stderr" >&2
    fail "incompatible version name did not produce an actionable error"
}

if VERSION_CATALOG_JSON='{"versions":[{"name":"v4\n"}]}' \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/version-name-terminal-lf.log" \
    FAIL_SERVICE=none \
    EXISTING_CONTAINERS="proxy versiond versiond2 versiond-router devshard-postgres edge-api" \
    FAKE_STATE_DIR="$tmpdir/version-name-terminal-lf.state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode ha --edge-mode single --preflight-only \
        >"$tmpdir/version-name-terminal-lf.stdout" \
        2>"$tmpdir/version-name-terminal-lf.stderr"; then
    fail "release preflight accepted a version name with a terminal LF"
fi
assert_no_compose_mutation "$tmpdir/version-name-terminal-lf.log"

upgrade_lock=$tmpdir/.gonka-deployment.lock
: >"$upgrade_lock"
exec {upgrade_lock_fd}>"$upgrade_lock"
flock -n "$upgrade_lock_fd"
if (
    exec {upgrade_lock_fd}>&-
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/locked-preflight.log" \
    FAIL_SERVICE=none \
    EXISTING_CONTAINERS="proxy versiond edge-api" \
    FAKE_STATE_DIR="$tmpdir/locked-preflight.state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode single --edge-mode single --preflight-only \
        >"$tmpdir/locked-preflight.stdout" \
        2>"$tmpdir/locked-preflight.stderr"
); then
    fail "a concurrent deployment upgrade acquired the global lock"
fi
grep -q 'another deployment operation holds' \
    "$tmpdir/locked-preflight.stderr" || fail \
    "global lock contention did not produce a useful error"
flock -u "$upgrade_lock_fd"
exec {upgrade_lock_fd}>&-

# The first cutover must satisfy the same complete ingress postcondition as a
# rerun. A healthy public proxy alone cannot commit a missing policy reserve.
rm -f "$tmpdir/upgrade-complete" "$tmpdir/upgrade-complete.in-progress"
mkdir -p "$tmpdir/incomplete-ingress.state"
if INCOMPLETE_INGRESS_STATE=true \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/incomplete-ingress.log" \
    FAIL_SERVICE=none \
    BLOCK_SERVICE=none \
    BLOCK_SIGNAL=none \
    EXISTING_CONTAINERS="proxy versiond edge-api" \
    FAKE_STATE_DIR="$tmpdir/incomplete-ingress.state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode single --edge-mode single \
        >"$tmpdir/incomplete-ingress.stdout" \
        2>"$tmpdir/incomplete-ingress.stderr"; then
    fail "first cutover committed without the policy reserve"
fi
grep -q 'ingress state did not converge' "$tmpdir/incomplete-ingress.stderr" || {
    cat "$tmpdir/incomplete-ingress.stderr" >&2
    fail "incomplete ingress did not produce an actionable failure"
}
[[ ! -f $tmpdir/upgrade-complete ]] || fail \
    "incomplete ingress wrote the final release marker"
jq -e '.transaction.phase == "applications_verified"' \
    "$tmpdir/upgrade-complete.in-progress" >/dev/null || fail \
    "incomplete ingress did not preserve the last verified phase"
rm -f "$tmpdir/upgrade-complete.in-progress"

custom_port_log=$tmpdir/custom-port.log
: >"$custom_port_log"
RENDERED_PROXY_HTTP_PORT=9000 \
FAKE_PROXY_HTTP_PORT=9000 \
DOCKER_BIN="$tmpdir/docker" \
DOCKER_LOG="$custom_port_log" \
EXISTING_CONTAINERS="proxy versiond edge-api" \
FAKE_STATE_DIR="$tmpdir/custom-port-state" \
JOIN_DIR="$script_dir" \
GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
    --versiond-mode single --edge-mode single --preflight-only \
    >"$tmpdir/custom-port.stdout" 2>"$tmpdir/custom-port.stderr" || {
        cat "$tmpdir/custom-port.stderr" >&2
        fail "release preflight rejected an effective local port override"
    }
assert_no_compose_mutation "$custom_port_log"
grep -q 'public ports 9000/http' "$tmpdir/custom-port.stdout" || fail \
    "release preflight did not use the effective Compose port override"

undersized_log=$tmpdir/undersized.log
: >"$undersized_log"
if DEVSHARD_V5_PREFLIGHT_CPUS=1 \
    DEVSHARD_V5_PREFLIGHT_MEMORY_MIB=1024 \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$undersized_log" \
    EXISTING_CONTAINERS="proxy versiond edge-api" \
    FAKE_STATE_DIR="$tmpdir/undersized-state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode single --edge-mode single \
        --preflight-only --strict-capacity \
        >"$tmpdir/undersized.stdout" 2>"$tmpdir/undersized.stderr"; then
    fail "strict release preflight accepted an undersized host"
fi
assert_no_compose_mutation "$undersized_log"
grep -q 'host capacity is below' "$tmpdir/undersized.stderr" || {
    cat "$tmpdir/undersized.stderr" >&2
    fail "strict capacity failure was not actionable"
}

wrong_port_log=$tmpdir/wrong-port.log
: >"$wrong_port_log"
if FAKE_PROXY_HTTP_PORT=9000 \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$wrong_port_log" \
    EXISTING_CONTAINERS="proxy versiond edge-api" \
    FAKE_STATE_DIR="$tmpdir/wrong-port-state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode single --edge-mode single --preflight-only \
        >"$tmpdir/wrong-port.stdout" 2>"$tmpdir/wrong-port.stderr"; then
    fail "release preflight accepted unexpected public port ownership"
fi
assert_no_compose_mutation "$wrong_port_log"
grep -q 'does not own expected host port 8000' "$tmpdir/wrong-port.stderr" || {
    cat "$tmpdir/wrong-port.stderr" >&2
    fail "public port ownership failure was not actionable"
}

unacknowledged_log=$tmpdir/unacknowledged.log
: >"$unacknowledged_log"
if DEVSHARD_V5_MAINTENANCE_ACK=false \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$unacknowledged_log" \
    EXISTING_CONTAINERS="proxy versiond edge-api" \
    FAKE_STATE_DIR="$tmpdir/unacknowledged-state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode single --edge-mode single \
        >"$tmpdir/unacknowledged.stdout" \
        2>"$tmpdir/unacknowledged.stderr"; then
    fail "one-time cutover ran without maintenance acknowledgement"
fi
assert_no_compose_mutation "$unacknowledged_log"
grep -q -- '--acknowledge-maintenance' "$tmpdir/unacknowledged.stderr" || {
    cat "$tmpdir/unacknowledged.stderr" >&2
    fail "maintenance acknowledgement failure was not actionable"
}

cp "$tmpdir/config.env" "$tmpdir/config-with-ack.env"
printf 'export DEVSHARD_V5_MAINTENANCE_ACK=true\n' \
    >>"$tmpdir/config-with-ack.env"
: >"$tmpdir/persisted-ack.log"
if env -u DEVSHARD_V5_MAINTENANCE_ACK \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/persisted-ack.log" \
    EXISTING_CONTAINERS="proxy versiond edge-api" \
    FAKE_STATE_DIR="$tmpdir/persisted-ack-state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config-with-ack.env" \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode single --edge-mode single \
        >"$tmpdir/persisted-ack.stdout" \
        2>"$tmpdir/persisted-ack.stderr"; then
    fail "config.env persisted a maintenance acknowledgement"
fi
assert_no_compose_mutation "$tmpdir/persisted-ack.log"

if DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/unknown.log" \
    FAIL_SERVICE=none \
    EXISTING_CONTAINERS='' \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
    >"$tmpdir/unknown.stdout" 2>"$tmpdir/unknown.stderr"; then
    fail "upgrade unexpectedly accepted an unknown deployment topology"
fi
grep -q 'cannot detect versiond topology' "$tmpdir/unknown.stderr" || {
    cat "$tmpdir/unknown.stderr" >&2
    fail "unknown topology did not produce a useful error"
}

run_auto_upgrade "versiond edge-api" "$tmpdir/base.log" "$tmpdir/base.stdout"
grep -q 'versiond=single, edge-api=single' "$tmpdir/base.stdout" || fail \
    "base-only topology was not detected"
grep -q '^enable-router-fleet $' "$tmpdir/base.log" || fail \
    "single-versiond upgrade passed the non-hash journal sentinel to the ingress SHA gate"
assert_not_contains "$tmpdir/base.log" "docker-compose.versiond.yml"
assert_not_contains "$tmpdir/base.log" "docker-compose.edge-api-multi.yml"
assert_not_contains "$tmpdir/base.log" " pull devshard-postgres"
assert_not_contains "$tmpdir/base.log" \
    "--wait-timeout 2100 devshard-postgres"
if ! grep -E -- '--wait-timeout 2100 versiond$' "$tmpdir/base.log" >/dev/null; then
    cat "$tmpdir/base.log" >&2
    fail "base-only versiond was not replaced"
fi

runtime_with_observability="$script_dir/docker-compose.yml,$script_dir/docker-compose.observability.yml"
COMPOSE_FILE="$script_dir/docker-compose.yml:$script_dir/docker-compose.observability.yml" \
RUNTIME_COMPOSE_FILES=$runtime_with_observability \
    run_auto_upgrade "versiond edge-api" \
        "$tmpdir/observability.log" "$tmpdir/observability.stdout"
assert_contains "$tmpdir/observability.log" \
    "-f $script_dir/docker-compose.observability.yml"
grep -q 'source=COMPOSE_FILE' "$tmpdir/observability.stdout" || fail \
    "updater did not accept the complete COMPOSE_FILE model"

# Explicit recovery must not be vetoed by paths recorded from a checkout that
# no longer exists. The current explicit model remains subject to full Compose
# rendering and service-contract validation.
COMPOSE_FILE="$script_dir/docker-compose.yml" \
RUNTIME_COMPOSE_FILES=./removed-docker-compose.yml \
RUNTIME_COMPOSE_WORKDIR="$tmpdir/removed-checkout" \
    run_auto_upgrade "versiond edge-api" \
        "$tmpdir/stale-label.log" "$tmpdir/stale-label.stdout"
grep -q 'source=COMPOSE_FILE' "$tmpdir/stale-label.stdout" || fail \
    "explicit topology did not recover from stale runtime paths"
grep -q 'records stale Compose file metadata' "$tmpdir/stderr" || fail \
    "stale runtime metadata was not reported to the operator"

if DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/missing-override.log" \
    FAIL_SERVICE=none BLOCK_SERVICE=none BLOCK_SIGNAL=none \
    EXISTING_CONTAINERS="proxy versiond edge-api" \
    FAKE_STATE_DIR="$tmpdir/missing-override.state" \
    JOIN_DIR="$script_dir" \
    RUNTIME_COMPOSE_FILES="$runtime_with_observability" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode single --edge-mode single \
        --compose-file "$script_dir/docker-compose.yml" \
        >"$tmpdir/missing-override.stdout" \
        2>"$tmpdir/missing-override.stderr"; then
    fail "upgrade accepted an explicit model that omitted a runtime override"
fi
grep -q 'omits or reorders a file recorded by running containers' \
    "$tmpdir/missing-override.stderr" || {
    cat "$tmpdir/missing-override.stderr" >&2
    fail "missing runtime override did not produce a useful error"
}
assert_no_compose_mutation "$tmpdir/missing-override.log"

if DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/wrong-project.log" \
    FAIL_SERVICE=none BLOCK_SERVICE=none BLOCK_SIGNAL=none \
    EXISTING_CONTAINERS="proxy versiond edge-api" \
    FAKE_STATE_DIR="$tmpdir/wrong-project.state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode single --edge-mode single \
        --compose-project-name another-project \
        >"$tmpdir/wrong-project.stdout" \
        2>"$tmpdir/wrong-project.stderr"; then
    fail "upgrade accepted a Compose project-name change"
fi
grep -q "does not match running project 'gonka-test'" \
    "$tmpdir/wrong-project.stderr" || {
    cat "$tmpdir/wrong-project.stderr" >&2
    fail "project-name mismatch did not produce a useful error"
}
assert_no_compose_mutation "$tmpdir/wrong-project.log"

# Exercise first-run recovery from runtime labels, without the committed
# topology written by an earlier successful test case.
rm -f "$tmpdir/upgrade-complete" "$tmpdir/upgrade-complete.in-progress"
if INCOMPATIBLE_COMPOSE_CONTAINER=versiond \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/incompatible-metadata.log" \
    FAIL_SERVICE=none BLOCK_SERVICE=none BLOCK_SIGNAL=none \
    EXISTING_CONTAINERS="proxy versiond edge-api" \
    FAKE_STATE_DIR="$tmpdir/incompatible-metadata.state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode single --edge-mode single \
        >"$tmpdir/incompatible-metadata.stdout" \
        2>"$tmpdir/incompatible-metadata.stderr"; then
    fail "upgrade guessed an order for incompatible runtime Compose metadata"
fi
grep -q 'record incompatible Compose file sets' \
    "$tmpdir/incompatible-metadata.stderr" || {
    cat "$tmpdir/incompatible-metadata.stderr" >&2
    fail "incompatible runtime metadata did not produce a useful error"
}
assert_no_compose_mutation "$tmpdir/incompatible-metadata.log"
assert_edge_api_untouched "$tmpdir/base.log"

run_auto_upgrade \
    "versiond edge-api edge-api2 edge-api3 edge-api-router" \
    "$tmpdir/mixed.log" "$tmpdir/mixed.stdout"
grep -q 'versiond=single, edge-api=multi' "$tmpdir/mixed.stdout" || fail \
    "independent versiond and edge-api axes were not detected"
assert_not_contains "$tmpdir/mixed.log" "docker-compose.versiond.yml"
assert_contains "$tmpdir/mixed.log" "docker-compose.edge-api-multi.yml"
assert_not_contains "$tmpdir/mixed.log" \
    "--wait-timeout 2100 devshard-postgres"

run_auto_upgrade \
    "versiond versiond2 versiond-router devshard-postgres edge-api" \
    "$tmpdir/ha.log" "$tmpdir/ha.stdout"
grep -q 'versiond=ha, edge-api=single' "$tmpdir/ha.stdout" || fail \
    "HA versiond topology was not detected"
assert_contains "$tmpdir/ha.log" "docker-compose.versiond.yml"
assert_not_contains "$tmpdir/ha.log" "docker-compose.edge-api-multi.yml"
assert_contains "$tmpdir/ha.log" "--wait-timeout 2100 devshard-postgres"
compose_gate_line=$(grep -nm1 '^postgres-deployment-preflight --compose-only -- ' \
    "$tmpdir/ha.log" | cut -d: -f1)
first_mutation_line=$(grep -nEm1 ' :: (tag |compose .* (pull|up) )' \
    "$tmpdir/ha.log" | cut -d: -f1)
[[ -n $compose_gate_line && -n $first_mutation_line && \
    $compose_gate_line -lt $first_mutation_line ]] || fail \
    "rendered PostgreSQL contract was not checked before the first mutation"
live_gate_line=$(grep -nm1 \
    '^postgres-deployment-preflight --expected-identity shared-database -- ' \
    "$tmpdir/ha.log" | cut -d: -f1)
cleanup_line=$(grep -nm1 ' :: image rm gonka-upgrade-rollback/' \
    "$tmpdir/ha.log" | cut -d: -f1)
[[ -n $live_gate_line && -n $cleanup_line && \
    $live_gate_line -lt $cleanup_line ]] || fail \
    "live PostgreSQL proof did not run before rollback baseline cleanup"

mkdir -p "$tmpdir/postgres"
printf '1000000000000000000\n' >"$tmpdir/postgres/.gonka-cluster-lineage"
MISSING_COMPOSE_SERVICE=devshard-postgres \
POSTGRES_MIGRATION_PROBE='target-ready none 1000000000000000000 none' \
    run_auto_upgrade \
        "versiond versiond2 versiond-router edge-api" \
        "$tmpdir/missing-postgres-container.log" \
        "$tmpdir/missing-postgres-container.stdout"
assert_not_contains "$tmpdir/missing-postgres-container.log" \
    "--volumes-from cid-devshard-postgres:ro"
assert_contains "$tmpdir/missing-postgres-container.log" \
    "dst=/target\\,readonly"

VERSIOND2_STORAGE_IDENTITY=another-database \
    run_upgrade single none "$tmpdir/postgres-identity.log"
grep -q 'versiond uses PostgreSQL identity shared-database, expected another-database' \
	"$tmpdir/stderr" || {
    cat "$tmpdir/stderr" >&2
    fail "different live PostgreSQL identities did not produce a useful error"
}

cat >"$tmpdir/managed-postgres.yml" <<'EOF'
services:
  versiond:
    environment:
      PGHOST: managed-postgres.internal
  versiond2:
    environment:
      PGHOST: managed-postgres.internal
EOF
managed_files="$script_dir/docker-compose.yml,$script_dir/docker-compose.versiond.yml,$tmpdir/managed-postgres.yml"
RUNTIME_COMPOSE_FILES=$managed_files \
RUNTIME_PGHOST=managed-postgres.internal \
RENDERED_PGHOST=managed-postgres.internal \
RENDERED_ROUTER_FRONT_NETWORK=custom-router-front \
RENDERED_ROUTER_BACK_NETWORK=custom-router-back \
    run_auto_upgrade \
        "versiond versiond2 versiond-router edge-api" \
        "$tmpdir/managed-postgres.log" "$tmpdir/managed-postgres.stdout"
assert_contains "$tmpdir/managed-postgres.log" \
    "-f $tmpdir/managed-postgres.yml"
assert_contains "$tmpdir/managed-postgres.log" \
    "-f $script_dir/docker-compose.versiond-external-postgres.yml"
assert_not_contains "$tmpdir/managed-postgres.log" " pull devshard-postgres"
assert_not_contains "$tmpdir/managed-postgres.log" \
    "--wait-timeout 2100 devshard-postgres"
assert_not_contains "$tmpdir/managed-postgres.log" \
    "--volumes-from cid-devshard-postgres:ro"
assert_contains "$tmpdir/managed-postgres.log" \
    "fleet-networks custom-router-front custom-router-back"

if RUNTIME_COMPOSE_FILES=$managed_files \
    RUNTIME_PGHOST=managed-postgres.internal \
    RENDERED_PGHOST=other-postgres.internal \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/postgres-switch.log" \
    FAIL_SERVICE=none BLOCK_SERVICE=none BLOCK_SIGNAL=none \
    EXISTING_CONTAINERS="proxy versiond versiond2 edge-api" \
    FAKE_STATE_DIR="$tmpdir/postgres-switch.state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode ha --edge-mode single \
        >"$tmpdir/postgres-switch.stdout" \
        2>"$tmpdir/postgres-switch.stderr"; then
    fail "upgrade accepted an implicit PostgreSQL endpoint change"
fi
grep -q 'refuse an implicit PostgreSQL identity change' \
    "$tmpdir/postgres-switch.stderr" || {
    cat "$tmpdir/postgres-switch.stderr" >&2
    fail "PostgreSQL endpoint change did not produce a useful error"
}
assert_no_compose_mutation "$tmpdir/postgres-switch.log"

if RENDERED_PGDATABASE2=other-devshard \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/postgres-split.log" \
    FAIL_SERVICE=none BLOCK_SERVICE=none BLOCK_SIGNAL=none \
    EXISTING_CONTAINERS="proxy versiond versiond2 edge-api" \
    FAKE_STATE_DIR="$tmpdir/postgres-split.state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode ha --edge-mode single \
        >"$tmpdir/postgres-split.stdout" \
        2>"$tmpdir/postgres-split.stderr"; then
    fail "upgrade accepted versiond services connected to different databases"
fi
grep -q 'same non-empty PGDATABASE' "$tmpdir/postgres-split.stderr" || {
    cat "$tmpdir/postgres-split.stderr" >&2
    fail "split PostgreSQL database did not produce a useful error"
}
assert_no_compose_mutation "$tmpdir/postgres-split.log"

if RENDERED_DATABASE_URL=postgres://other/database \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/postgres-dsn.log" \
    FAIL_SERVICE=none BLOCK_SERVICE=none BLOCK_SIGNAL=none \
    EXISTING_CONTAINERS="proxy versiond versiond2 edge-api" \
    FAKE_STATE_DIR="$tmpdir/postgres-dsn.state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode ha --edge-mode single \
        >"$tmpdir/postgres-dsn.stdout" \
        2>"$tmpdir/postgres-dsn.stderr"; then
    fail "upgrade accepted DATABASE_URL in an HA topology"
fi
grep -q 'HA versiond must not set DATABASE_URL' \
    "$tmpdir/postgres-dsn.stderr" || {
    cat "$tmpdir/postgres-dsn.stderr" >&2
    fail "ambiguous PostgreSQL DSN did not produce a useful error"
}
assert_no_compose_mutation "$tmpdir/postgres-dsn.log"

for service_variable in RENDERED_PGSERVICE RENDERED_PGSERVICEFILE RENDERED_PGOPTIONS; do
	service_log="$tmpdir/postgres-${service_variable,,}.log"
	if env "$service_variable=unverified-service" \
		DOCKER_BIN="$tmpdir/docker" \
		DOCKER_LOG="$service_log" \
		FAIL_SERVICE=none BLOCK_SERVICE=none BLOCK_SIGNAL=none \
		EXISTING_CONTAINERS="proxy versiond versiond2 edge-api" \
		FAKE_STATE_DIR="$tmpdir/postgres-${service_variable,,}.state" \
		JOIN_DIR="$script_dir" \
		GONKA_CONFIG_ENV="$tmpdir/config.env" \
		"$script_dir/upgrade-devshard-v5.sh" \
			--versiond-mode ha --edge-mode single \
			>"$tmpdir/postgres-${service_variable,,}.stdout" \
			2>"$tmpdir/postgres-${service_variable,,}.stderr"; then
		fail "upgrade accepted $service_variable in an HA topology"
	fi
	grep -q "must not set ${service_variable#RENDERED_}" \
		"$tmpdir/postgres-${service_variable,,}.stderr" || fail \
		"$service_variable did not produce a useful error"
	assert_no_compose_mutation "$service_log"
done

if RENDERED_PGOPTIONS='-c search_path=ledger_a' \
    RENDERED_PGOPTIONS2='-c search_path=ledger_b' \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/postgres-rendered-pgoptions-split.log" \
    FAIL_SERVICE=none BLOCK_SERVICE=none BLOCK_SIGNAL=none \
    EXISTING_CONTAINERS="proxy versiond versiond2 edge-api" \
    FAKE_STATE_DIR="$tmpdir/postgres-rendered-pgoptions-split.state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode ha --edge-mode single \
        >"$tmpdir/postgres-rendered-pgoptions-split.stdout" \
        2>"$tmpdir/postgres-rendered-pgoptions-split.stderr"; then
    fail "upgrade accepted different rendered PGOPTIONS values"
fi
grep -q 'must not set PGOPTIONS' \
    "$tmpdir/postgres-rendered-pgoptions-split.stderr" || fail \
    "different rendered PGOPTIONS did not produce a useful error"
assert_no_compose_mutation "$tmpdir/postgres-rendered-pgoptions-split.log"

if RUNTIME_PGSERVICE=unverified-service \
	DOCKER_BIN="$tmpdir/docker" \
	DOCKER_LOG="$tmpdir/postgres-runtime-pgservice.log" \
	FAIL_SERVICE=none BLOCK_SERVICE=none BLOCK_SIGNAL=none \
	EXISTING_CONTAINERS="proxy versiond versiond2 edge-api" \
	FAKE_STATE_DIR="$tmpdir/postgres-runtime-pgservice.state" \
	JOIN_DIR="$script_dir" \
	GONKA_CONFIG_ENV="$tmpdir/config.env" \
	"$script_dir/upgrade-devshard-v5.sh" \
		--versiond-mode ha --edge-mode single \
		>"$tmpdir/postgres-runtime-pgservice.stdout" \
		2>"$tmpdir/postgres-runtime-pgservice.stderr"; then
	fail "upgrade accepted PGSERVICE from a running HA supervisor"
fi
grep -q 'running HA versiond sets PGSERVICE' \
	"$tmpdir/postgres-runtime-pgservice.stderr" || fail \
	"runtime PGSERVICE did not produce a useful error"
assert_no_compose_mutation "$tmpdir/postgres-runtime-pgservice.log"

for runtime_case in same split; do
	runtime_log="$tmpdir/postgres-runtime-pgoptions-$runtime_case.log"
	if [[ $runtime_case == same ]]; then
		runtime_pgoptions2='-c search_path=ledger_a'
	else
		runtime_pgoptions2='-c search_path=ledger_b'
	fi
	if RUNTIME_PGOPTIONS='-c search_path=ledger_a' \
		RUNTIME_PGOPTIONS2="$runtime_pgoptions2" \
		DOCKER_BIN="$tmpdir/docker" \
		DOCKER_LOG="$runtime_log" \
		FAIL_SERVICE=none BLOCK_SERVICE=none BLOCK_SIGNAL=none \
		EXISTING_CONTAINERS="proxy versiond versiond2 edge-api" \
		FAKE_STATE_DIR="$tmpdir/postgres-runtime-pgoptions-$runtime_case.state" \
		JOIN_DIR="$script_dir" \
		GONKA_CONFIG_ENV="$tmpdir/config.env" \
		"$script_dir/upgrade-devshard-v5.sh" \
			--versiond-mode ha --edge-mode single \
			>"$tmpdir/postgres-runtime-pgoptions-$runtime_case.stdout" \
			2>"$tmpdir/postgres-runtime-pgoptions-$runtime_case.stderr"; then
		fail "upgrade accepted $runtime_case non-empty runtime PGOPTIONS"
	fi
	grep -q 'running HA versiond sets PGOPTIONS' \
		"$tmpdir/postgres-runtime-pgoptions-$runtime_case.stderr" || fail \
		"$runtime_case runtime PGOPTIONS did not produce a useful error"
	assert_no_compose_mutation "$runtime_log"
done

if RUNTIME_DATABASE_URL=postgres://other/database \
	DOCKER_BIN="$tmpdir/docker" \
	DOCKER_LOG="$tmpdir/postgres-runtime-database-url.log" \
	FAIL_SERVICE=none BLOCK_SERVICE=none BLOCK_SIGNAL=none \
	EXISTING_CONTAINERS="proxy versiond versiond2 edge-api" \
	FAKE_STATE_DIR="$tmpdir/postgres-runtime-database-url.state" \
	JOIN_DIR="$script_dir" \
	GONKA_CONFIG_ENV="$tmpdir/config.env" \
	"$script_dir/upgrade-devshard-v5.sh" \
		--versiond-mode ha --edge-mode single \
		>"$tmpdir/postgres-runtime-database-url.stdout" \
		2>"$tmpdir/postgres-runtime-database-url.stderr"; then
	fail "upgrade accepted DATABASE_URL from a running HA supervisor"
fi
grep -q 'running HA versiond sets DATABASE_URL' \
	"$tmpdir/postgres-runtime-database-url.stderr" || fail \
	"runtime DATABASE_URL did not produce a useful error"
assert_no_compose_mutation "$tmpdir/postgres-runtime-database-url.log"

versiond_barrier_line=$(line_number "$tmpdir/ha.log" \
    "--env VERSIOND_HOSTS=versiond versiond-router")
versiond2_up_line=$(line_number "$tmpdir/ha.log" \
    "--wait-timeout 2100 versiond2")
versiond_barrier_hook_line=$(line_number "$tmpdir/ha.log" \
    "legacy-router-upgrade-barrier.sh versiond-router:/tmp/99-gonka-upgrade-barrier.sh")
versiond_router_up_line=$(line_number "$tmpdir/ha.log" \
    "--wait-timeout 60 versiond-router")
versiond_up_line=$(line_number_regex "$tmpdir/ha.log" \
    '--wait-timeout 2100 versiond$')
[[ -n $versiond_barrier_line && -n $versiond_barrier_hook_line && \
    -n $versiond2_up_line && \
    -n $versiond_router_up_line && -n $versiond_up_line && \
    $versiond_barrier_line -lt $versiond2_up_line && \
    $versiond_barrier_hook_line -lt $versiond2_up_line && \
    $versiond2_up_line -lt $versiond_router_up_line && \
    $versiond_router_up_line -lt $versiond_up_line ]] ||
    fail "versiond traffic barrier or HAProxy cutover order is wrong"

# A public proxy-router label proves only that ingress cutover happened. It
# must not suppress an unfinished application or PostgreSQL migration.
rm -f "$tmpdir/upgrade-complete" "$tmpdir/upgrade-complete.in-progress"
mkdir -p "$tmpdir/active-without-marker.state"
if DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/active-without-marker.log" \
    FAIL_SERVICE=versiond \
    EXISTING_PROXY_COMPONENT=proxy-router \
    EXISTING_CONTAINERS="versiond versiond2 devshard-postgres edge-api proxy" \
    FAKE_STATE_DIR="$tmpdir/active-without-marker.state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
    --versiond-mode ha --edge-mode single \
    >"$tmpdir/active-without-marker.stdout" \
    2>"$tmpdir/active-without-marker.stderr"; then
    fail "proxy-router label was accepted as an upgrade commit"
fi
[[ ! -f $tmpdir/upgrade-complete ]] || fail \
    "failed application convergence wrote the final marker"
jq -e '.transaction.phase == "prepared"' \
    "$tmpdir/upgrade-complete.in-progress" >/dev/null || fail \
    "failed application convergence did not preserve its recovery journal"

# A crash after the irreversible router cutover but before the atomic marker
# write is recoverable only after exact images and health have been proven.
rm -f "$tmpdir/upgrade-complete" "$tmpdir/upgrade-complete.in-progress"
mkdir -p "$tmpdir/recovered-marker.state"
ASSUME_RELEASE_STATE=true \
DOCKER_BIN="$tmpdir/docker" \
DOCKER_LOG="$tmpdir/recovered-marker.log" \
EXISTING_PROXY_COMPONENT=proxy-router \
EXISTING_CONTAINERS="versiond versiond2 devshard-postgres edge-api proxy proxy-policy" \
FAKE_STATE_DIR="$tmpdir/recovered-marker.state" \
JOIN_DIR="$script_dir" \
GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
    --versiond-mode ha --edge-mode single \
    >"$tmpdir/recovered-marker.stdout" \
    2>"$tmpdir/recovered-marker.stderr"
jq -e '
    .schema == 2 and
    .release_id == "0.2.15-devshard-v5" and
    (.fingerprint | type == "string" and length == 64) and
    (.compose.files | length > 0) and
    .images.postgres == "postgres@sha256:57c72fd2a128e416c7fcc499958864df5301e940bca0a56f58fddf30ffc07777" and
    .storage.postgres_identity == "shared-database" and
    .router_fleet.spec_sha256 ==
        "0000000000000000000000000000000000000000000000000000000000000001" and
    (.transaction.application_rollback? == null)
' "$tmpdir/upgrade-complete" >/dev/null || fail \
    "verified cutover did not reconstruct the desired-state marker"
assert_contains "$tmpdir/recovered-marker.log" \
    "enable-router --versiond-mode ha --edge-mode single"
grep -Eq '^enable-router-compose [0-9a-f]{64}$' \
    "$tmpdir/recovered-marker.log" || fail \
    "outer upgrade did not bind ingress to its Compose generation"
grep -q '^enable-router-fleet 0000000000000000000000000000000000000000000000000000000000000001$' \
    "$tmpdir/recovered-marker.log" || fail \
    "outer upgrade did not bind ingress to its router fleet specification"
grep -q 'release state is converged' "$tmpdir/recovered-marker.stdout" || fail \
    "marker recovery was not reported"

# A permanently decommissioned second member remains part of the Compose
# model with replicas=0. Day-2 reconciliation must neither recreate it nor
# require it as a PostgreSQL identity witness.
cp "$tmpdir/upgrade-complete" "$tmpdir/enabled-versiond2-marker"
mkdir -p "$tmpdir/decommissioned-versiond2.state"
ASSUME_RELEASE_STATE=true \
RENDERED_VERSIOND2_REPLICAS=0 \
DOCKER_BIN="$tmpdir/docker" \
DOCKER_LOG="$tmpdir/decommissioned-versiond2.log" \
EXISTING_PROXY_COMPONENT=proxy-router \
EXISTING_CONTAINERS="versiond devshard-postgres edge-api proxy proxy-policy" \
FAKE_STATE_DIR="$tmpdir/decommissioned-versiond2.state" \
JOIN_DIR="$script_dir" \
GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
    --versiond-mode ha --edge-mode single \
    >"$tmpdir/decommissioned-versiond2.stdout" \
    2>"$tmpdir/decommissioned-versiond2.stderr"
assert_not_contains "$tmpdir/decommissioned-versiond2.log" \
    "--wait-timeout 2100 versiond2"
assert_not_contains "$tmpdir/decommissioned-versiond2.log" \
    " pull versiond2"
grep -q 'release state is converged' \
    "$tmpdir/decommissioned-versiond2.stdout" || fail \
    "day-2 reconciliation failed after permanent versiond2 decommission"
mv "$tmpdir/enabled-versiond2-marker" "$tmpdir/upgrade-complete"

# Recovery of an already active ingress transaction must precede upgrade
# preflight and every slow application `up --wait`; otherwise a missing public
# listener can remain down for the complete versiond startup budget.
cp "$tmpdir/upgrade-complete" "$tmpdir/early-ingress-marker"
jq '. as $committed | . + {transaction: {
        id: "test-early-ingress",
        phase: "applications_verified",
        base_fingerprint: $committed.fingerprint,
        desired_fingerprint: $committed.fingerprint,
        compose_config_sha256: $committed.compose.config_sha256,
        fleet_spec_sha256: $committed.router_fleet.spec_sha256,
        postgres_identity: $committed.storage.postgres_identity,
        ingress: {
            state: "active",
            rollback_models: {policy: {}, proxy: {}},
            touched: []
        }
    }}' "$tmpdir/upgrade-complete" \
    >"$tmpdir/early-ingress-marker.in-progress"
mkdir -p "$tmpdir/early-ingress.state"
ASSUME_RELEASE_STATE=true \
DEVSHARD_V5_UPGRADE_MARKER="$tmpdir/early-ingress-marker" \
DOCKER_BIN="$tmpdir/docker" \
DOCKER_LOG="$tmpdir/early-ingress.log" \
EXISTING_PROXY_COMPONENT=proxy-router \
EXISTING_CONTAINERS="versiond versiond2 devshard-postgres edge-api proxy proxy-policy" \
FAKE_STATE_DIR="$tmpdir/early-ingress.state" \
JOIN_DIR="$script_dir" \
GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
    --versiond-mode ha --edge-mode single \
    >"$tmpdir/early-ingress.stdout" 2>"$tmpdir/early-ingress.stderr"
recovery_line=$(line_number "$tmpdir/early-ingress.log" \
    "enable-router --recover-only")
application_line=$(line_number_regex "$tmpdir/early-ingress.log" \
    ' up .*--wait-timeout 2100 versiond2$' || :)
[[ -n $recovery_line ]] || fail \
    "active ingress transaction was not recovered"
[[ -z $application_line ]] || fail \
    "converged day-2 applications were needlessly recreated after ingress recovery"

# A process crash can happen during a day-2 transaction while the previous
# committed marker still exists. The active journal must win, recover its HA
# mode before inspecting a degraded runtime, then atomically replace the old
# marker only after complete ingress verification.
jq -c '.topology.versiond = "single" | .fingerprint = ("a" * 64)' \
    "$tmpdir/upgrade-complete" >"$tmpdir/previous-marker"
jq '. as $state | . + {transaction:{
			id:"test-resume-1",
			phase:"ingress_verified",
			base_fingerprint:("a" * 64),
			desired_fingerprint:$state.fingerprint,
			compose_config_sha256:$state.compose.config_sha256,
			fleet_spec_sha256:$state.router_fleet.spec_sha256,
			postgres_identity:$state.storage.postgres_identity,
			updated_at_unix:1
		}}' \
    "$tmpdir/upgrade-complete" >"$tmpdir/upgrade-complete.in-progress"
mv "$tmpdir/previous-marker" "$tmpdir/upgrade-complete"
mkdir -p "$tmpdir/journal-missing-replicas.state"
ASSUME_RELEASE_STATE=true \
DOCKER_BIN="$tmpdir/docker" \
DOCKER_LOG="$tmpdir/journal-missing-replicas.log" \
EXISTING_PROXY_COMPONENT=proxy-router \
EXISTING_CONTAINERS="versiond devshard-postgres edge-api proxy proxy-policy" \
FAKE_STATE_DIR="$tmpdir/journal-missing-replicas.state" \
JOIN_DIR="$script_dir" \
GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
    >"$tmpdir/journal-missing-replicas.stdout" \
    2>"$tmpdir/journal-missing-replicas.stderr"
grep -q 'Resuming the saved Compose topology' \
    "$tmpdir/journal-missing-replicas.stdout" || fail \
    "interrupted upgrade did not restore its saved topology"
grep -q 'versiond=ha, edge-api=single' \
    "$tmpdir/journal-missing-replicas.stdout" || fail \
    "degraded runtime collapsed the journaled HA topology"
assert_contains "$tmpdir/journal-missing-replicas.log" \
    "--wait-timeout 2100 versiond2"
[[ -f $tmpdir/upgrade-complete && \
    ! -f $tmpdir/upgrade-complete.in-progress ]] || fail \
    "successful journal recovery did not atomically finalize the marker"
jq -e '.topology.versiond == "ha" and .transaction.id == "test-resume-1"' \
    "$tmpdir/upgrade-complete" >/dev/null || fail \
    "active transaction did not replace the older committed marker"

# Compose inputs are part of the transaction precondition. A changed rendered
# model must fail before any service or router mutation.
hash_drift_marker=$tmpdir/hash-drift-marker
hash_drift_journal=$hash_drift_marker.in-progress
cp "$tmpdir/upgrade-complete" "$hash_drift_marker"
jq '. as $state | . + {transaction:{
			id:"test-compose-drift",
			phase:"prepared",
			base_fingerprint:$state.fingerprint,
			desired_fingerprint:$state.fingerprint,
			compose_config_sha256:("0" * 64),
			fleet_spec_sha256:$state.router_fleet.spec_sha256,
			postgres_identity:$state.storage.postgres_identity,
			updated_at_unix:1
		}}' "$hash_drift_marker" >"$hash_drift_journal"
if ASSUME_RELEASE_STATE=true \
    DEVSHARD_V5_UPGRADE_MARKER="$hash_drift_marker" \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/hash-drift.log" \
    EXISTING_PROXY_COMPONENT=proxy-router \
    EXISTING_CONTAINERS="versiond versiond2 devshard-postgres edge-api proxy proxy-policy" \
    FAKE_STATE_DIR="$tmpdir/journal-missing-replicas.state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
        >"$tmpdir/hash-drift.stdout" 2>"$tmpdir/hash-drift.stderr"; then
    fail "active transaction accepted a changed Compose model"
fi
grep -q 'effective Compose model changed during active transaction' \
    "$tmpdir/hash-drift.stderr" || fail \
    "Compose transaction drift did not produce a useful error"
assert_no_compose_mutation "$tmpdir/hash-drift.log"

fleet_drift_marker=$tmpdir/fleet-drift-marker
fleet_drift_journal=$fleet_drift_marker.in-progress
cp "$tmpdir/upgrade-complete" "$fleet_drift_marker"
jq '. as $state | . + {transaction:{
			id:"test-fleet-drift",
			phase:"prepared",
			base_fingerprint:$state.fingerprint,
			desired_fingerprint:$state.fingerprint,
			compose_config_sha256:$state.compose.config_sha256,
			fleet_spec_sha256:$state.router_fleet.spec_sha256,
			postgres_identity:$state.storage.postgres_identity,
			updated_at_unix:1
		}}' "$fleet_drift_marker" >"$fleet_drift_journal"
if ASSUME_RELEASE_STATE=true \
    FLEET_SPEC_HASH=0000000000000000000000000000000000000000000000000000000000000002 \
    DEVSHARD_V5_UPGRADE_MARKER="$fleet_drift_marker" \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/fleet-drift.log" \
    EXISTING_PROXY_COMPONENT=proxy-router \
    EXISTING_CONTAINERS="versiond versiond2 devshard-postgres edge-api proxy proxy-policy" \
    FAKE_STATE_DIR="$tmpdir/journal-missing-replicas.state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
        >"$tmpdir/fleet-drift.stdout" 2>"$tmpdir/fleet-drift.stderr"; then
    fail "active transaction accepted a changed router fleet specification"
fi
grep -q 'router fleet specification changed during active transaction' \
    "$tmpdir/fleet-drift.stderr" || fail \
    "fleet transaction drift did not produce a useful error"
assert_no_compose_mutation "$tmpdir/fleet-drift.log"

printf '0.2.15-devshard-v5\n' >"$tmpdir/upgrade-complete"
mkdir -p "$tmpdir/active-with-marker.state"
ASSUME_RELEASE_STATE=true \
DOCKER_BIN="$tmpdir/docker" \
DOCKER_LOG="$tmpdir/active-with-marker.log" \
EXISTING_PROXY_COMPONENT=proxy-router \
EXISTING_CONTAINERS="versiond versiond2 devshard-postgres edge-api proxy" \
FAKE_STATE_DIR="$tmpdir/active-with-marker.state" \
JOIN_DIR="$script_dir" \
GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
    --versiond-mode ha --edge-mode single \
    >"$tmpdir/active-with-marker.stdout" \
    2>"$tmpdir/active-with-marker.stderr"
assert_contains "$tmpdir/active-with-marker.log" \
    "enable-router --versiond-mode ha --edge-mode single"
assert_contains "$tmpdir/active-with-marker.log" \
    "--compose-project-name gonka-test"
assert_contains "$tmpdir/active-with-marker.log" \
    "--compose-file $script_dir/docker-compose.versiond.yml"
assert_not_contains "$tmpdir/active-with-marker.log" \
    "--wait-timeout 2100 devshard-postgres"
assert_not_contains "$tmpdir/active-with-marker.log" \
    "--wait-timeout 2100 versiond2"
assert_not_contains "$tmpdir/active-with-marker.log" \
    "--wait-timeout 2100 versiond"
assert_edge_api_untouched "$tmpdir/active-with-marker.log"
jq -e '.schema == 2 and (.compose.files | length > 0)' \
    "$tmpdir/upgrade-complete" >/dev/null || fail \
    "legacy marker was not upgraded to the desired-state schema"

database_drift_marker=$tmpdir/database-drift-marker
jq '.storage.postgres_identity = "previous-database"' \
    "$tmpdir/upgrade-complete" >"$database_drift_marker"
if ASSUME_RELEASE_STATE=true \
    DEVSHARD_V5_UPGRADE_MARKER="$database_drift_marker" \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/database-drift.log" \
    EXISTING_PROXY_COMPONENT=proxy-router \
    EXISTING_CONTAINERS="versiond versiond2 devshard-postgres edge-api proxy proxy-policy" \
    FAKE_STATE_DIR="$tmpdir/active-with-marker.state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode ha --edge-mode single \
        >"$tmpdir/database-drift.stdout" \
        2>"$tmpdir/database-drift.stderr"; then
    fail "rerun accepted a different PostgreSQL database than the committed deployment"
fi
grep -q 'PostgreSQL identity changed from committed' \
    "$tmpdir/database-drift.stderr" || {
    cat "$tmpdir/database-drift.stderr" >&2
    fail "committed PostgreSQL identity drift did not produce a useful error"
}

# Once committed, the marker is the recovery authority. Drifted labels are a
# hint about current containers, not permission to forget the known topology.
mkdir -p "$tmpdir/marker-topology.state"
ASSUME_RELEASE_STATE=true \
INCOMPATIBLE_COMPOSE_CONTAINER=versiond \
DOCKER_BIN="$tmpdir/docker" \
DOCKER_LOG="$tmpdir/marker-topology.log" \
EXISTING_PROXY_COMPONENT=proxy-router \
EXISTING_CONTAINERS="versiond versiond2 devshard-postgres edge-api proxy proxy-policy" \
FAKE_STATE_DIR="$tmpdir/marker-topology.state" \
JOIN_DIR="$script_dir" \
GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
    --versiond-mode ha --edge-mode single \
    >"$tmpdir/marker-topology.stdout" \
    2>"$tmpdir/marker-topology.stderr"
grep -q 'Using the committed Compose topology' \
	"$tmpdir/marker-topology.stdout" || fail \
	"rerun did not recover the committed Compose topology"

# Explicit CLI modes win, but an automatic rerun must use the committed modes
# before inspecting a degraded runtime. Missing replicas are desired-state
# drift, not permission to collapse HA into a singleton topology.
mkdir -p "$tmpdir/marker-missing-replicas.state"
ASSUME_RELEASE_STATE=true \
DOCKER_BIN="$tmpdir/docker" \
DOCKER_LOG="$tmpdir/marker-missing-replicas.log" \
EXISTING_PROXY_COMPONENT=proxy-router \
EXISTING_CONTAINERS="versiond devshard-postgres edge-api proxy proxy-policy" \
FAKE_STATE_DIR="$tmpdir/marker-missing-replicas.state" \
JOIN_DIR="$script_dir" \
GONKA_CONFIG_ENV="$tmpdir/config.env" \
	"$script_dir/upgrade-devshard-v5.sh" \
	>"$tmpdir/marker-missing-replicas.stdout" \
	2>"$tmpdir/marker-missing-replicas.stderr"
grep -q 'versiond=ha, edge-api=single' \
	"$tmpdir/marker-missing-replicas.stdout" || fail \
	"runtime loss changed the committed topology modes"
assert_contains "$tmpdir/marker-missing-replicas.log" \
	"--wait-timeout 2100 versiond2"

# The public proxy is desired state too. Losing its container must not discard
# the committed Compose identity or require a second maintenance acknowledgement.
mkdir -p "$tmpdir/marker-missing-proxy.state"
sed 's/UPGRADE_ENABLE_ROUTER_HA=false/UPGRADE_ENABLE_ROUTER_HA=true/' \
	"$tmpdir/config.env" >"$tmpdir/config-recover-proxy.env"
DEVSHARD_V5_MAINTENANCE_ACK=false \
ASSUME_RELEASE_STATE=true \
DOCKER_BIN="$tmpdir/docker" \
DOCKER_LOG="$tmpdir/marker-missing-proxy.log" \
EXISTING_PROXY_COMPONENT='' \
EXISTING_CONTAINERS="versiond versiond2 devshard-postgres edge-api proxy-policy" \
FAKE_STATE_DIR="$tmpdir/marker-missing-proxy.state" \
JOIN_DIR="$script_dir" \
GONKA_CONFIG_ENV="$tmpdir/config-recover-proxy.env" \
	"$script_dir/upgrade-devshard-v5.sh" \
	>"$tmpdir/marker-missing-proxy.stdout" \
	2>"$tmpdir/marker-missing-proxy.stderr"
assert_contains "$tmpdir/marker-missing-proxy.log" \
	"enable-router --versiond-mode ha --edge-mode single"
grep -q 'public proxy is absent' "$tmpdir/marker-missing-proxy.stderr" || fail \
	"missing proxy recovery was not reported"

FAIL_INGRESS_FINALIZE=true \
    run_upgrade single none "$tmpdir/commit-finalize-failure.log"
jq -e '.transaction.phase == "ingress_verified" and
       .transaction.decision == "commit" and
       .transaction.ingress.state == "prepared" and
       (.transaction.application_rollback.services | length > 0)' \
    "$tmpdir/upgrade-complete.in-progress" >/dev/null || fail \
    "failed finalization did not retain the durable forward-only decision"
assert_not_contains "$tmpdir/commit-finalize-failure.log" \
    "enable-router --recover-only"
if grep -q 'gonka-upgrade-rollback/.* up ' \
    "$tmpdir/commit-finalize-failure.log"; then
    fail "application rollback ran after the durable commit decision"
fi
grep -q 'release commit is durable; completing forward' "$tmpdir/stderr" || fail \
    "post-decision failure did not enter forward-only recovery"

# A crash can leave the ingress decision committed before its rollback models
# are redacted. Recovery must finish the marker without consulting PostgreSQL,
# whose simultaneous outage cannot revoke an already durable release commit.
journal_tmp=$(mktemp "$tmpdir/.committed-ingress.XXXXXX")
jq '.release_id = "superseded-release-package" |
    .compose.files = ["/removed/operator-override.yml"] |
    .transaction.ingress.state = "committed"' \
	"$tmpdir/upgrade-complete.in-progress" >"$journal_tmp"
mv "$journal_tmp" "$tmpdir/upgrade-complete.in-progress"
POSTGRES_DEPLOYMENT_PREFLIGHT_FAIL_MODE=runtime \
	resume_interrupted_upgrade \
	"$tmpdir/commit-finalize-failure.log.state" \
	"$tmpdir/commit-recovery.log" \
	"$tmpdir/commit-recovery.stdout" \
	"$tmpdir/commit-recovery.stderr"
[[ -f $tmpdir/upgrade-complete && \
	! -f $tmpdir/upgrade-complete.in-progress ]] || fail \
	"committed ingress recovery did not finalize the release marker"
jq -e '
	.transaction.ingress.state == "committed" and
	((.transaction.ingress.rollback_models? // null) == null) and
	((.transaction.application_rollback? // null) == null)
' "$tmpdir/upgrade-complete" >/dev/null || fail \
	"committed ingress recovery retained rollback metadata"
assert_contains "$tmpdir/commit-recovery.log" \
	"enable-router --recover-only"
assert_not_contains "$tmpdir/commit-recovery.log" \
	"postgres-deployment-preflight"
assert_not_contains "$tmpdir/commit-recovery.log" \
    "compose --project-name gonka-test"
jq -e '.release_id == "superseded-release-package"' \
    "$tmpdir/upgrade-complete" >/dev/null || fail \
    "forward-only recovery replaced the journaled release identity"

run_upgrade single devshard-postgres "$tmpdir/postgres-failure.log"
assert_contains "$tmpdir/postgres-failure.log" " stop devshard-postgres"
assert_not_contains "$tmpdir/postgres-failure.log" \
    "gonka-upgrade-rollback/devshard-postgres:"
grep -q 'source volume and persistent target are preserved' \
    "$tmpdir/stderr" || {
    cat "$tmpdir/stderr" >&2
    fail "PostgreSQL failure did not explain its preservation contract"
}

UPGRADE_ROLLBACK_VERIFY_TIMEOUT='' \
UPGRADE_ROLLBACK_STABILITY_CHECKS=3 \
    run_upgrade single versiond2 "$tmpdir/versiond2.log"
grep -q 'Verifying rollback of versiond2 for up to 60s' \
    "$tmpdir/stderr" || {
    cat "$tmpdir/stderr" >&2
    fail "versiond rollback did not use the bounded dependency-outage budget"
}
preflight_line=$(line_number "$tmpdir/versiond2.log" \
    "dst=/target\\,readonly")
postgres_up_line=$(line_number "$tmpdir/versiond2.log" \
    "--wait-timeout 2100 devshard-postgres")
[[ -n $preflight_line && -n $postgres_up_line && \
    $preflight_line -lt $postgres_up_line ]] ||
    fail "PostgreSQL space preflight did not run before its first recreate"
assert_contains "$tmpdir/versiond2.log" \
    " :: create --name versiond2 --network none"
stop_candidate_line=$(line_number "$tmpdir/versiond2.log" \
    " :: stop versiond2")
create_rollback_line=$(line_number "$tmpdir/versiond2.log" \
    " :: create --name versiond2 --network none")
disconnect_backup_line=$(line_number_regex "$tmpdir/versiond2.log" \
    ' :: network disconnect -f gonka-test_default versiond2\.gonka-displaced-')
[[ -n $stop_candidate_line && -n $create_rollback_line && \
    $stop_candidate_line -lt $create_rollback_line ]] || fail \
    "rollback started its baseline before stopping the failed candidate"
[[ -n $disconnect_backup_line && \
    $disconnect_backup_line -lt $create_rollback_line ]] || fail \
    "rollback reused a static IP before disconnecting the failed candidate"
assert_contains "$tmpdir/versiond2.log" "--entrypoint tini"
assert_contains "$tmpdir/versiond2.log" \
    "gonka-upgrade-rollback/versiond2:"
assert_contains "$tmpdir/versiond2.log" " -- versiond"
assert_contains "$tmpdir/versiond2.log" \
    "--mac-address 02:42:ac:1e:00:2a"
assert_contains "$tmpdir/versiond2.log" \
    "--tmpfs /run:rw\,noexec\,size=65536k"
assert_contains "$tmpdir/versiond2.log" \
    "--health-start-interval 500000000ns"
assert_contains "$tmpdir/versiond2.log" "--ip 172.30.0.42"
assert_contains "$tmpdir/versiond2.log" "--link-local-ip 169.254.10.1"
assert_contains "$tmpdir/versiond2.log" "--driver-opt test=preserved"
assert_contains "$tmpdir/versiond2.log" "--link versiond:legacy"
assert_contains "$tmpdir/versiond2.log" "--gw-priority 10"
rollback_probe_count=$(grep -Ec \
    'exec cid-versiond2 .*http://127.0.0.1:8080/v4/healthz' \
    "$tmpdir/versiond2.log")
[[ $rollback_probe_count -eq 4 ]] || fail \
    "rollback was not held through the configured stability window"
assert_not_contains "$tmpdir/versiond2.log" \
    "VERSIOND_HOSTS=versiond\\ versiond2"
if grep -E -- '--wait-timeout 2100 versiond$' "$tmpdir/versiond2.log" >/dev/null; then
    cat "$tmpdir/versiond2.log" >&2
    fail "versiond was replaced after versiond2 failed"
fi

FAIL_DISPLACED_REMOVE_SERVICE=versiond2 \
    run_upgrade single versiond2 "$tmpdir/displaced-retry.log"
jq -e '
    .transaction.application_rollback.services.versiond2.touched == true
' "$tmpdir/upgrade-complete.in-progress" >/dev/null || fail \
    "incomplete displaced-container cleanup discarded durable recovery state"
[[ -f $tmpdir/displaced-retry.log.state/backup-versiond2 ]] || fail \
    "failed displaced-container cleanup did not retain its backup"
recover_interrupted_rollback \
    "$tmpdir/displaced-retry.log.state" \
    "$tmpdir/displaced-recovery.log" \
    "$tmpdir/displaced-recovery.stdout" \
    "$tmpdir/displaced-recovery.stderr"
remove_partial_line=$(line_number "$tmpdir/displaced-recovery.log" \
    " :: rm -f versiond2")
recreate_partial_line=$(line_number "$tmpdir/displaced-recovery.log" \
    " :: create --name versiond2 --network none")
[[ -n $remove_partial_line && -n $recreate_partial_line && \
    $remove_partial_line -lt $recreate_partial_line ]] || fail \
    "retry did not classify and rebuild the partially restored baseline"
[[ ! -e $tmpdir/displaced-retry.log.state/backup-versiond2 ]] || fail \
    "retry retained the displaced candidate after exact restoration"

ROLLBACK_PROBE_FAIL_SERVICE=versiond2 \
UPGRADE_ROLLBACK_VERIFY_TIMEOUT=1 \
    run_upgrade single versiond2 "$tmpdir/versiond2-unavailable.log"
[[ $(grep -Fc ' :: stop versiond2' \
    "$tmpdir/versiond2-unavailable.log") -eq 1 ]] || fail \
    "unavailable rollback was stopped after its failed availability proof"
jq -e '
    .transaction.application_rollback.services.versiond2.touched == true
' "$tmpdir/upgrade-complete.in-progress" >/dev/null || fail \
    "running but unavailable rollback was marked successfully restored"
grep -q 'automatic rollback of versiond2 failed' "$tmpdir/stderr" || fail \
    "unavailable rollback did not retain an operator-visible failure"

EXEC_HEALTH_OVERRIDE=true \
    run_upgrade single versiond2 "$tmpdir/versiond2-exec-health.log"
assert_not_contains "$tmpdir/versiond2-exec-health.log" \
    " :: create --name versiond2 --network none"
assert_not_contains "$tmpdir/versiond2-exec-health.log" \
    "--wait-timeout 2100 versiond2"
grep -q 'exec-form healthcheck' "$tmpdir/stderr" || fail \
    "inexact exec-form healthcheck was not rejected before replacement"

CUSTOM_RUNTIME_OVERRIDE=true \
    run_upgrade single versiond2 "$tmpdir/versiond2-custom-runtime.log"
assert_no_compose_mutation "$tmpdir/versiond2-custom-runtime.log"
assert_not_contains "$tmpdir/versiond2-custom-runtime.log" " :: tag "
assert_not_contains "$tmpdir/versiond2-custom-runtime.log" \
    " :: create --name versiond2 --network none"
grep -q 'unsupported exact-rollback fields:.*HostConfig.ExtraHosts' \
    "$tmpdir/stderr" || fail \
    "custom runtime model was not rejected before replacement"
grep -q 'NetworkSettings.Networks\["gonka-test_default"\].MacAddress' \
    "$tmpdir/stderr" || fail \
    "per-network MAC was not identified as inexact rollback state"

ROLLBACK_EMPTY_VERSIOND_SERVICE=versiond2 \
UPGRADE_ROLLBACK_VERIFY_TIMEOUT=1 \
    run_upgrade single versiond2 "$tmpdir/versiond2-empty.log"
[[ $(grep -Fc ' :: stop versiond2' \
    "$tmpdir/versiond2-empty.log") -eq 1 ]] || fail \
    "empty rollback was stopped after its failed generation proof"

ROLLBACK_MISSING_VERSION_SERVICE=versiond \
UPGRADE_ROLLBACK_VERIFY_TIMEOUT=1 \
    run_upgrade single versiond "$tmpdir/versiond-partial.log" single
[[ $(grep -Ec ' :: stop versiond$' \
    "$tmpdir/versiond-partial.log") -eq 1 ]] || fail \
    "partial rollback was stopped after its failed generation proof"
awk '
    / :: create --name versiond --network none/ { rollback = 1 }
    rollback && /http:\/\/127\.0\.0\.1:8080\/v4\/healthz/ { exit 1 }
' "$tmpdir/versiond-partial.log" || fail \
    "partial rollback probed a route missing from the restored process"

SPECIAL_VERSIOND_HEALTH_SERVICE=versiond \
    run_upgrade single versiond "$tmpdir/versiond-special-name.log" single
assert_contains "$tmpdir/versiond-special-name.log" \
    'http://127.0.0.1:8080/v4%2Bhotfix/healthz'
assert_contains "$tmpdir/versiond-special-name.log" \
    'http://127.0.0.1:8080/v4%7Dx/healthz'
[[ $(grep -Ec ' :: stop versiond$' \
    "$tmpdir/versiond-special-name.log") -eq 1 ]] || fail \
    "special-name rollback was stopped after restoration"

STOPPED_VERSIOND_SERVICE=versiond2 \
    run_upgrade single versiond2 "$tmpdir/versiond2-stopped.log"
assert_not_contains "$tmpdir/versiond2-stopped.log" " start versiond2"
assert_not_contains "$tmpdir/versiond2-stopped.log" \
    'exec cid-versiond2 /bin/busybox wget -q -T 3 -O - http://127.0.0.1:8080/healthz'
assert_contains "$tmpdir/versiond2-stopped.log" \
    " :: create --name versiond2 --network none"

assert_contains "$tmpdir/versiond2.log" \
    "--volume /srv/gonka/state:/var/lib/gonka:Z"

run_upgrade single versiond "$tmpdir/versiond-production-rollback.log"
assert_contains "$tmpdir/versiond-production-rollback.log" \
    'exec cid-versiond-router /bin/busybox wget -q -T 3 -O /dev/null http://127.0.0.1:8080/v3/healthz'

ROLLBACK_ROUTER_PROBE_FAIL_SERVICE=versiond \
UPGRADE_ROLLBACK_VERIFY_TIMEOUT=1 \
    run_upgrade single versiond \
        "$tmpdir/versiond-production-rollback-failure.log"
assert_contains "$tmpdir/versiond-production-rollback-failure.log" \
    " :: create --name versiond --network none"
[[ $(grep -Ec ' :: stop versiond$' \
    "$tmpdir/versiond-production-rollback-failure.log") -eq 1 ]] || fail \
    "production rollback was stopped after its failed route proof"

run_upgrade single versiond-router "$tmpdir/versiond-router.log"
assert_contains "$tmpdir/versiond-router.log" \
    "exec versiond-router rm -f /etc/gonka-upgrade-barrier"
assert_contains "$tmpdir/versiond-router.log" \
    "VERSIOND_HOSTS=versiond\\ versiond2"
assert_contains "$tmpdir/versiond-router.log" \
    'exec cid-versiond-router /bin/busybox wget -q -T 3 -O /dev/null http://127.0.0.1:8080/v3/healthz'
jq -e '.transaction.postgres_identity == "shared-database"' \
	"$tmpdir/upgrade-complete.in-progress" >/dev/null || fail \
	"the first upgraded supervisor did not durably fence PostgreSQL identity"

ROLLBACK_MISSING_ROUTE_SERVICE=versiond-router \
UPGRADE_ROLLBACK_VERIFY_TIMEOUT=1 \
    run_upgrade single versiond-router \
        "$tmpdir/versiond-router-missing-route.log"
assert_contains "$tmpdir/versiond-router-missing-route.log" \
    " :: create --name versiond-router --network none"
[[ $(grep -Ec ' :: stop versiond-router$' \
    "$tmpdir/versiond-router-missing-route.log") -eq 1 ]] || fail \
    "router rollback was stopped after its failed route proof"

PERSISTED_VERSIOND_BARRIER=true \
VERSIOND2_UNIQUE_VERSION=true \
    run_upgrade single versiond "$tmpdir/versiond-barrier-retry.log"
barrier_remove_line=$(line_number "$tmpdir/versiond-barrier-retry.log" \
    "exec versiond-router rm -f /etc/gonka-upgrade-barrier")
router_v5_probe_line=$(line_number "$tmpdir/versiond-barrier-retry.log" \
    'exec cid-versiond-router /bin/busybox wget -q -T 3 -O /dev/null http://127.0.0.1:8080/v5/healthz')
retry_router_up_line=$(line_number "$tmpdir/versiond-barrier-retry.log" \
    "--wait-timeout 60 versiond-router")
[[ -n $barrier_remove_line && -n $router_v5_probe_line && \
    -n $retry_router_up_line && \
    $barrier_remove_line -lt $router_v5_probe_line && \
    $router_v5_probe_line -lt $retry_router_up_line ]] ||
    fail "persisted barrier was not reconciled before router baseline capture"

STOPPED_VERSIOND_SERVICE=versiond2 \
TARGET_ROUTER_MISSING_VERSION=v5 \
VERSIOND2_UNIQUE_VERSION=true \
    run_upgrade single none "$tmpdir/versiond2-router-postcondition.log"
assert_contains "$tmpdir/versiond2-router-postcondition.log" \
    " :: create --name versiond2 --network none"
assert_not_contains "$tmpdir/versiond2-router-postcondition.log" \
    "--wait-timeout 60 versiond-router"

run_postcondition_interrupted_upgrade \
    "$tmpdir/versiond2-postcondition-interrupt.log"
assert_contains "$tmpdir/versiond2-postcondition-interrupt.log" \
    " :: create --name versiond2 --network none"
assert_not_contains "$tmpdir/versiond2-postcondition-interrupt.log" \
    "VERSIOND_HOSTS=versiond\\ versiond2"
grep -q 'received TERM' "$tmpdir/versiond2-postcondition-interrupt.log.stderr" || {
    cat "$tmpdir/versiond2-postcondition-interrupt.log.stderr" >&2
    fail "postcondition interruption did not reach the signal handler"
}

for signal in HUP INT TERM; do
    signal_name=${signal,,}
    run_interrupted_upgrade \
        "$signal" \
        "$tmpdir/interrupted-$signal_name.log" \
        "$tmpdir/interrupted-$signal_name.stdout" \
        "$tmpdir/interrupted-$signal_name.stderr"
    assert_contains "$tmpdir/interrupted-$signal_name.log" \
        " :: create --name versiond2 --network none"
    assert_not_contains "$tmpdir/interrupted-$signal_name.log" \
        "VERSIOND_HOSTS=versiond\\ versiond2"
    grep -q "received $signal" \
        "$tmpdir/interrupted-$signal_name.stderr" || {
        cat "$tmpdir/interrupted-$signal_name.stderr" >&2
        fail "interrupted upgrade did not report $signal"
    }
    jq -e '
        .transaction.application_rollback.services.versiond2.image
            | startswith("gonka-upgrade-rollback/versiond2:")
    ' "$tmpdir/upgrade-complete.in-progress" >/dev/null || fail \
        "interrupted upgrade did not preserve the original rollback image in its journal"
done

# Model a SIGKILL after the touched bit became durable but before the EXIT trap
# could restore versiond2. Recovery must rollback before any live proof that
# depends on the interrupted application generation.
jq '.transaction.application_rollback.services.versiond2.touched = true' \
    "$tmpdir/upgrade-complete.in-progress" \
    >"$tmpdir/interrupted-kill-journal"
mv "$tmpdir/interrupted-kill-journal" \
    "$tmpdir/upgrade-complete.in-progress"
recover_interrupted_rollback \
    "$tmpdir/interrupted-term.log.state" \
    "$tmpdir/interrupted-recovery.log" \
    "$tmpdir/interrupted-recovery.stdout" \
    "$tmpdir/interrupted-recovery.stderr"
assert_contains "$tmpdir/interrupted-recovery.log" \
    " :: create --name versiond2 --network none"
assert_not_contains "$tmpdir/interrupted-recovery.log" \
    "postgres-deployment-preflight --runtime-contract-only"
grep -q 'recovering durable application rollback' \
    "$tmpdir/interrupted-recovery.stderr" || {
    cat "$tmpdir/interrupted-recovery.stderr" >&2
    fail "interrupted recovery did not report its rollback-only pass"
}
jq -e '
    .transaction.application_rollback.services.versiond2.touched == false
' "$tmpdir/upgrade-complete.in-progress" >/dev/null || fail \
    "successful interrupted rollback was not persisted"

resume_interrupted_upgrade \
    "$tmpdir/interrupted-term.log.state" \
    "$tmpdir/interrupted-resume.log" \
    "$tmpdir/interrupted-resume.stdout" \
    "$tmpdir/interrupted-resume.stderr"
assert_not_contains "$tmpdir/interrupted-resume.log" " :: tag "
assert_contains "$tmpdir/interrupted-resume.stdout" \
    "Reusing durable versiond2 rollback baseline"

run_auto_upgrade \
    "versiond versiond2 versiond-router devshard-postgres edge-api edge-api2 edge-api3 edge-api-router" \
    "$tmpdir/edge-api-preserved.log" "$tmpdir/edge-api-preserved.stdout"
assert_edge_api_untouched "$tmpdir/edge-api-preserved.log"
assert_not_contains "$tmpdir/edge-api-preserved.log" \
    "docker-compose.edge-api-v5-compat.yml"

echo "upgrade-devshard-v5_test: ok"
