{{/*
Expand the name of the chart.
*/}}
{{- define "tns-csi-driver.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
If release name contains "tns-csi", just use the release name to avoid duplication.
*/}}
{{- define "tns-csi-driver.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- if contains "tns-csi" .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-tns-csi" .Release.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "tns-csi-driver.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "tns-csi-driver.labels" -}}
helm.sh/chart: {{ include "tns-csi-driver.chart" . }}
{{ include "tns-csi-driver.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- with .Values.customLabels }}
{{ toYaml . }}
{{- end }}
{{- end }}

{{/*
Selector labels for controller
*/}}
{{- define "tns-csi-driver.controller.selectorLabels" -}}
app.kubernetes.io/name: {{ include "tns-csi-driver.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: controller
{{- end }}

{{/*
Selector labels for node
*/}}
{{- define "tns-csi-driver.node.selectorLabels" -}}
app.kubernetes.io/name: {{ include "tns-csi-driver.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: node
{{- end }}

{{/*
Selector labels
*/}}
{{- define "tns-csi-driver.selectorLabels" -}}
app.kubernetes.io/name: {{ include "tns-csi-driver.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the controller service account to use
*/}}
{{- define "tns-csi-driver.controller.serviceAccountName" -}}
{{- printf "%s-controller" (include "tns-csi-driver.fullname" .) }}
{{- end }}

{{/*
Create the name of the node service account to use
*/}}
{{- define "tns-csi-driver.node.serviceAccountName" -}}
{{- printf "%s-node" (include "tns-csi-driver.fullname" .) }}
{{- end }}

{{/*
Create the name of the secret
*/}}
{{- define "tns-csi-driver.secretName" -}}
{{- if .Values.truenas.existingSecret }}
{{- .Values.truenas.existingSecret }}
{{- else }}
{{- printf "%s-secret" (include "tns-csi-driver.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Return the appropriate apiVersion for RBAC APIs
*/}}
{{- define "tns-csi-driver.rbac.apiVersion" -}}
{{- if .Capabilities.APIVersions.Has "rbac.authorization.k8s.io/v1" -}}
rbac.authorization.k8s.io/v1
{{- else -}}
rbac.authorization.k8s.io/v1beta1
{{- end -}}
{{- end -}}

{{/*
Return the appropriate apiVersion for CSIDriver
*/}}
{{- define "tns-csi-driver.csidriver.apiVersion" -}}
{{- if .Capabilities.APIVersions.Has "storage.k8s.io/v1" -}}
storage.k8s.io/v1
{{- else -}}
storage.k8s.io/v1beta1
{{- end -}}
{{- end -}}

{{/*
Create the CSI driver name
*/}}
{{- define "tns-csi-driver.driverName" -}}
{{- .Values.driverName | default "tns.csi.io" }}
{{- end }}

{{/*
Validate required TrueNAS configuration
*/}}
{{- define "tns-csi-driver.validateConfig" -}}
{{- if not .Values.truenas.existingSecret }}
  {{- if not .Values.truenas.url }}
    {{- fail "\n\nCONFIGURATION ERROR: truenas.url is required.\nExample: --set truenas.url=\"wss://YOUR-TRUENAS-IP:443/api/current\"" }}
  {{- end }}
  {{- if not .Values.truenas.apiKey }}
    {{- fail "\n\nCONFIGURATION ERROR: truenas.apiKey is required.\nCreate an API key in TrueNAS UI: Settings > API Keys\nExample: --set truenas.apiKey=\"1-xxxxxxxxxx\"" }}
  {{- end }}
{{- end }}
{{- range .Values.storageClasses }}
{{- if .enabled }}
{{- if and (eq .protocol "nfs") (not .server) }}
  {{- fail (printf "\n\nCONFIGURATION ERROR: server is required for NFS storage class %q.\nExample: --set 'storageClasses[0].server=YOUR-TRUENAS-IP'" .name) }}
{{- end }}
{{- if and (eq .protocol "nvmeof") (not .server) }}
  {{- fail (printf "\n\nCONFIGURATION ERROR: server is required for NVMe-oF storage class %q.\nExample: --set 'storageClasses[1].server=YOUR-TRUENAS-IP'" .name) }}
{{- end }}
{{- if and (eq .protocol "iscsi") (not .server) }}
  {{- fail (printf "\n\nCONFIGURATION ERROR: server is required for iSCSI storage class %q." .name) }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Get the image tag to use.
Uses .Values.image.tag if explicitly set, otherwise falls back to .Chart.AppVersion.
If neither is set, defaults to "latest" as a last resort.
This allows users to either:
1. Pin a specific version: --set image.tag=v0.5.0
2. Use the chart's default (appVersion): helm install --version 0.5.0
*/}}
{{- define "tns-csi-driver.imageTag" -}}
{{- if .Values.image.tag }}
{{- .Values.image.tag }}
{{- else if .Chart.AppVersion }}
{{- .Chart.AppVersion }}
{{- else }}
{{- "latest" }}
{{- end }}
{{- end }}

{{/*
Render a StorageClass resource.
Accepts a dict with keys: protocol, sc (storage class config), root (root context).
*/}}
{{- define "tns-csi-driver.storageclass" -}}
{{- $protocol := .protocol -}}
{{- $sc := .sc -}}
{{- $ := .root -}}
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: {{ $sc.name }}
  labels:
    {{- include "tns-csi-driver.labels" $ | nindent 4 }}
  {{- if $sc.isDefault }}
  annotations:
    storageclass.kubernetes.io/is-default-class: "true"
  {{- end }}
provisioner: {{ include "tns-csi-driver.driverName" $ }}
parameters:
  protocol: {{ $protocol | quote }}
  pool: {{ $sc.pool | quote }}
  {{- if $sc.server }}
  server: {{ $sc.server | quote }}
  {{- end }}
  {{- if $sc.parentDataset }}
  parentDataset: {{ $sc.parentDataset | quote }}
  {{- end }}
  {{- if $sc.deleteStrategy }}
  deleteStrategy: {{ $sc.deleteStrategy | quote }}
  {{- end }}
  {{- if $sc.nameTemplate }}
  nameTemplate: {{ $sc.nameTemplate | quote }}
  {{- end }}
  {{- if $sc.namePrefix }}
  namePrefix: {{ $sc.namePrefix | quote }}
  {{- end }}
  {{- if $sc.nameSuffix }}
  nameSuffix: {{ $sc.nameSuffix | quote }}
  {{- end }}
  {{- if $sc.commentTemplate }}
  commentTemplate: {{ $sc.commentTemplate | quote }}
  {{- end }}
  {{- if $sc.markAdoptable }}
  markAdoptable: {{ $sc.markAdoptable | quote }}
  {{- end }}
  {{- if $sc.adoptExisting }}
  adoptExisting: {{ $sc.adoptExisting | quote }}
  {{- end }}
  {{- if $sc.encryption }}
  encryption: {{ $sc.encryption | quote }}
  {{- end }}
  {{- if $sc.encryptionAlgorithm }}
  encryptionAlgorithm: {{ $sc.encryptionAlgorithm | quote }}
  {{- end }}
  {{- if $sc.encryptionGenerateKey }}
  encryptionGenerateKey: {{ $sc.encryptionGenerateKey | quote }}
  {{- end }}
  {{- if eq $protocol "nvmeof" }}
  transport: {{ $sc.transport | default "tcp" | quote }}
  port: {{ $sc.port | default "4420" | quote }}
  {{- if $sc.fsType }}
  csi.storage.k8s.io/fstype: {{ $sc.fsType | quote }}
  {{- end }}
  {{- end }}
  {{- if eq $protocol "iscsi" }}
  port: {{ $sc.port | default "3260" | quote }}
  {{- if $sc.fsType }}
  csi.storage.k8s.io/fstype: {{ $sc.fsType | quote }}
  {{- end }}
  {{- end }}
  {{- if $sc.parameters }}
  {{- range $key, $value := $sc.parameters }}
  {{ $key }}: {{ $value | quote }}
  {{- end }}
  {{- end }}
allowVolumeExpansion: {{ $sc.allowVolumeExpansion | default true }}
reclaimPolicy: {{ $sc.reclaimPolicy | default "Delete" }}
volumeBindingMode: {{ $sc.volumeBindingMode | default "Immediate" }}
{{- if $sc.mountOptions }}
mountOptions:
  {{- toYaml $sc.mountOptions | nindent 2 }}
{{- end }}
{{ end }}
