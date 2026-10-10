{{/* Keep the membership compared during upgrades identical to router startup. */}}
{{- define "gonka.versiondEndpoints" -}}
{{- $root := . -}}
{{- $name := include "gonka.fullname" . -}}
{{- $endpoints := list -}}
{{- range $ordinal := until (int .Values.versiond.replicas) -}}
{{- $endpoints = append $endpoints (dict "id" (printf "versiond-%d" $ordinal) "host" (printf "%s-versiond-%d.%s.svc.%s" $name $ordinal $root.Release.Namespace $root.Values.clusterDomain) "port" 8080) -}}
{{- end -}}
{{- $endpoints | toJson -}}
{{- end -}}

{{/*
Lookup is intentionally performed by the caller. Offline rendering cannot prove
live upgrade safety; an actual Helm install/upgrade uses API discovery and must
be able to list these objects. Even terminating routing Pods count as live.
*/}}
{{- define "gonka.validateLiveUpgrade" -}}
{{- $root := .root -}}
{{- $name := include "gonka.fullname" $root -}}
{{- $routingPods := false -}}
{{- $routingControllers := false -}}
{{- range .pods.items | default list -}}
{{- $labels := .metadata.labels | default dict -}}
{{- if and (eq (get $labels "app.kubernetes.io/name") "gonka-ha") (eq (get $labels "app.kubernetes.io/instance") $root.Release.Name) (has (get $labels "app.kubernetes.io/component") (list "router" "ingress")) -}}
{{- $routingPods = true -}}
{{- end -}}
{{- end -}}
{{- range .statefulSets.items | default list -}}
{{- $labels := .metadata.labels | default dict -}}
{{- $component := get (.spec.template.metadata.labels | default dict) "app.kubernetes.io/component" -}}
{{- if and (eq (get $labels "app.kubernetes.io/name") "gonka-ha") (eq (get $labels "app.kubernetes.io/instance") $root.Release.Name) (has $component (list "versiond" "router" "ingress")) -}}
{{- if ne .metadata.name (printf "%s-%s" $name $component) -}}
{{- fail "changing serving StatefulSet names is unsupported, including during maintenance; keep fullnameOverride unchanged after installation" -}}
{{- end -}}
{{- end -}}
{{- if and (eq (get $labels "app.kubernetes.io/name") "gonka-ha") (eq (get $labels "app.kubernetes.io/instance") $root.Release.Name) (has $component (list "router" "ingress")) (gt (int .spec.replicas) 0) -}}
{{- $routingControllers = true -}}
{{- end -}}
{{- end -}}
{{- if or $routingPods $routingControllers (not $root.Values.maintenance) -}}
{{- if $root.Values.maintenance -}}
{{- fail "maintenance requires ingress/router StatefulSets scaled to zero and all their Pods to finish draining first; use the offline maintenance procedure" -}}
{{- end -}}
{{- $seen := dict -}}
{{- range .statefulSets.items | default list -}}
{{- $labels := .metadata.labels | default dict -}}
{{- if and (eq (get $labels "app.kubernetes.io/name") "gonka-ha") (eq (get $labels "app.kubernetes.io/instance") $root.Release.Name) -}}
{{- $component := get (.spec.template.metadata.labels | default dict) "app.kubernetes.io/component" -}}
{{- if has $component (list "versiond" "router" "ingress") -}}
{{- if hasKey $seen $component -}}
{{- fail (printf "cannot verify %s membership: multiple StatefulSets belong to this release; drain routing Pods before maintenance" $component) -}}
{{- end -}}
{{- $_ := set $seen $component true -}}
{{- $desired := int (get (get $root.Values $component) "replicas") -}}
{{- $previous := int .spec.replicas -}}
{{- $annotations := .metadata.annotations | default dict -}}
{{- if hasKey $annotations "gonka.ai/desired-replicas" -}}
{{- $previous = int (get $annotations "gonka.ai/desired-replicas") -}}
{{- end -}}
{{- if ne $previous $desired -}}
{{- fail (printf "changing %s replica count requires offline maintenance=true with ingress/router scaled to zero and no remaining Pods" $component) -}}
{{- end -}}
{{- if eq $component "router" -}}
{{- $oldEndpoints := "" -}}
{{- range .spec.template.spec.containers -}}
{{- if eq .name "router" -}}
{{- range .env | default list -}}
{{- if eq .name "VERSIOND_POOL_ENDPOINTS" -}}
{{- $oldEndpoints = .value | default "" -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if ne ($oldEndpoints | fromJsonArray | toJson) (include "gonka.versiondEndpoints" $root) -}}
{{- fail "changing versiond endpoints requires offline maintenance=true with ingress/router scaled to zero and no remaining Pods" -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- range $component := list "versiond" "router" "ingress" -}}
{{- if and (or $routingPods $routingControllers) (not (hasKey $seen $component)) -}}
{{- fail (printf "cannot verify live %s membership: StatefulSet is missing; drain routing Pods before maintenance" $component) -}}
{{- end -}}
{{- end -}}
{{- $oracleSeen := false -}}
{{- range .deployments.items | default list -}}
{{- $labels := .metadata.labels | default dict -}}
{{- if and (eq (get $labels "app.kubernetes.io/name") "gonka-ha") (eq (get $labels "app.kubernetes.io/instance") $root.Release.Name) (eq (get (.spec.template.metadata.labels | default dict) "app.kubernetes.io/component") "oracle") -}}
{{- range .spec.template.spec.containers -}}
{{- if eq .name "oracle" -}}
{{- range .env | default list -}}
{{- if eq .name "ORACLE_ALLOW" -}}
{{- $oracleSeen = true -}}
{{- range regexSplit "\\s+" (trim .value) -1 -}}
{{- if and (ne . "") (not (has . $root.Values.protocols)) -}}
{{- fail (printf "removing protocol %s requires offline maintenance=true with ingress/router scaled to zero and no remaining Pods" .) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if and (or $routingPods $routingControllers) (not $oracleSeen) -}}
{{- fail "cannot verify live protocol allowlist: oracle configuration is missing; drain routing Pods before maintenance" -}}
{{- end -}}
{{- end -}}
{{- end -}}
