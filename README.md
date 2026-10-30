# Gluesys FlexA CSI Driver for Kubernetes

The official [Container Storage Interface](https://github.com/container-storage-interface) driver for Gluesys FlexA storage (FlexStor, ExaStor).

### Container Images & Kubernetes Compatibility

Driver Name: `csi.flexa.com`

| Driver Version | Image | Supported K8s Version |
| -------------- | ----- | --------------------- |
| [1.0.0](https://github.com/gluesys/flexa-csi) | `ghcr.io/gluesys/flexa-csi:1.0.0` | 1.21+ |

The Gluesys FlexA CSI driver supports:

- **Access modes (CSI):** `SINGLE_NODE_WRITER`, `MULTI_NODE_MULTI_WRITER` (typical Kubernetes mapping: RWO and RWX-style multi-writer NFS where applicable).
- **Controller:** Create/Delete volume, List volumes, Get capacity, Create/Delete snapshot (ZFS).
- **Node:** Stage/Unstage (no-op for the current NFS flow), Publish/Unpublish, Get volume stats.

Volume snapshots and restore from a snapshot are supported for ZFS volumes (see [Volume snapshots](#volume-snapshots-zfs)).
**Not supported at this time:** Lustre snapshots, cloning a PVC directly (`dataSource: PersistentVolumeClaim`). Volume expansion is supported (expand-only; shrink is not supported).

## Prerequisites

- Kubernetes 1.21 or above
- Gluesys FlexA storage (**FlexStor**, **ExaStor**) **1.4.2 or above**, with at least one storage pool for ZFS-backed volumes, or Lustre cluster configuration as required by your deployment
- Go 1.21+ recommended when building from source

### Before you install

1. For ZFS workflows, create and initialize at least one **storage pool** on Gluesys FlexA storage.
2. Deploy the driver using **Helm** (recommended) or **raw manifests** (`deploy/kubernetes`) as described in [Installation](#installation).

## Installation

From the repository root (directory that contains `charts/` and `config/`).

### Helm (recommended)

Chart: [`charts/flexa-csi`](charts/flexa-csi).

Minimal install (uses [`charts/flexa-csi/values.yaml`](charts/flexa-csi/values.yaml) for `clientInfo.content` and other defaults):

```bash
helm upgrade --install flexa-csi ./charts/flexa-csi \
  --namespace flexa-csi
```

Create the namespace first if it does not exist (`kubectl create namespace flexa-csi`), or add **`--create-namespace`** to the `helm` line so Helm creates it. The chart itself does not install a `Namespace` resource.

- **Proxy / VIP config:** Edit `clientInfo.content` in `values.yaml`, or use `-f my-values.yaml`, or `--set-file clientInfo.content=/absolute/path/to/client-info.yml` (paths are relative to your shell cwd). You do not need a separate `config/client-info.yml` unless you choose the file-based workflow.
- **Secret managed by chart:** With `clientInfo.create: true` (default), the chart creates the Secret from `clientInfo.content`. To use a Secret you created yourself: `kubectl create secret ...` then install with `--set clientInfo.create=false` (Secret name must match `clientInfo.secretName`, default `client-info-secret`).
- **Optional:** Tune `image`, `sidecars`, host paths, and **`storageClass`** entries under `values.yaml` ([`templates/storageclass.yaml`](charts/flexa-csi/templates/storageclass.yaml) renders enabled entries). Run `helm lint ./charts/flexa-csi` before applying.

### Raw manifests

1. `git clone https://github.com/gluesys/flexa-csi.git` and `cd flexa-csi`
2. `cp config/client-info-template.yml config/client-info.yml` and edit `config/client-info.yml` (proxy endpoints and optional `mountIP`; see [client-info / Secret](#client-info--secret) and [StorageClass parameters](#creating-storage-classes)).
3. Create the `client-info` Secret in the driver namespace (see [Creating the Secret manually](#creating-the-secret-manually)).
4. Install:

   ```bash
   cd deploy/kubernetes
   sh install.sh
   ```

5. Verify: `kubectl get pods -n flexa-csi`

## CSI Driver Configuration

You need a **Secret** (`client-info.yml`) and **StorageClasses**. For ZFS snapshots, see [Volume snapshots](#volume-snapshots-zfs).

Contents: [client-info / Secret](#client-info--secret) · [StorageClasses](#creating-storage-classes) · [Volume snapshots](#volume-snapshots-zfs) · [PVC annotations](#pvc-annotations) · [Pod annotations](#pod-annotations) · [VolumeContext](#volumecontext-advanced)

### client-info / Secret

**Helm:** Configure proxy endpoints in `charts/flexa-csi/values.yaml` (`clientInfo.content`) or use `-f` / `--set-file` as described in [Installation](#installation).

**Token:** provisiond rejects `/csi` requests without a valid client token (HTTP 401). Issue one per Kubernetes cluster on the storage's MGMT_VIP controller and put it in `client-info.yml` as `token` (top level, or per profile when profiles point to different storage):

```bash
cd /anystor/provision && python3.12 -m provisiond.ctl client-token create --name csi-<cluster>
```

The token is printed once. With Helm, pass it as `--set clientInfo.token=<token>`. To rotate, issue a new token with the same name and update the Secret; the driver reloads it without a restart, and existing volumes pick it up because the token is looked up by proxy address, not stored in the PV.

**Raw manifests:** Prepare `config/client-info.yml` with `token` and `profiles` (`proxyIP`, `proxyPort`, optional `mountIP` for VIP resolve). See the template at [`config/client-info-template.yml`](config/client-info-template.yml).

#### Creating the Secret manually

Use when not using Helm, or when you pre-create the Secret for Helm (`clientInfo.create=false`):

1. Example `config/client-info.yml`:

   ```yaml
   token: "<provisiond csi token>"
   profiles:
     zfs:
       proxyIP: 10.0.0.11
       proxyPort: 9001
       mountIP: "192.168.0.0/18"
     lustre:
       proxyIP: 10.0.0.12
       proxyPort: 9001
       mountIP: "192.168.0.0/18"
   ```

2. Create the Secret so the data key is **`client-info.yml`** (required by the driver):

   ```bash
   kubectl create secret generic client-info-secret -n flexa-csi \
     --from-file=client-info.yml=./config/client-info.yml
   ```

   Replace the namespace or file path as needed. If you rename the Secret, update the same name in `deploy/kubernetes/controller.yml` and `node.yml` (or Helm `clientInfo.secretName`).

The driver watches `client-info-secret` in the driver namespace and applies updates at runtime.

### Creating Storage Classes

Parameter names are **case-sensitive** (`poolName`, not `poolname`).

**Helm:** Define entries under `storageClass` in [`charts/flexa-csi/values.yaml`](charts/flexa-csi/values.yaml). Each key can enable a class; ZFS entries set `poolName`, Lustre entries set `clusterName`. The chart template is [`charts/flexa-csi/templates/storageclass.yaml`](charts/flexa-csi/templates/storageclass.yaml).

**Backend volume name (optional):** `volumeNameFormat` sets the name of the FlexA volume/share (Lustre directory, ZFS dataset). The default is `{pvname}` (`pvc-<uid>`). Placeholders: `{pvname}` (required), `{namespace}`, `{pvcname}`. Use `_` as the separator, for example `{namespace}_{pvname}` gives `gluesys05_pvc-<uid>`; it is valid for both Lustre and ZFS and never appears in a namespace name. Lustre allows `[A-Za-z0-9._-]`; ZFS allows `[A-Za-z0-9_-]` and must start with a letter; at most 128 characters. A name that breaks these rules fails CreateVolume with `InvalidArgument`. StorageClass parameters cannot be changed in place, so recreate the class to change it. Existing volumes keep their names.

**Lustre volume directory:** CSI creates Lustre volumes under `k8s/<namespace>/` in the filesystem, for example `/lustre/agent/<clusterName>/k8s/gluesys05/pvc-<uid>`, so they do not mix with other directories at the filesystem root. The namespace directory stays when its last volume is deleted. Volumes created before this change stay at the filesystem root and keep working. With the namespace already in the path, the default `volumeNameFormat` (`{pvname}`) is usually enough.

**Raw YAML examples:** [`deploy/kubernetes/storage-class-zfs.yml`](deploy/kubernetes/storage-class-zfs.yml), [`deploy/kubernetes/storage-class-lustre.yml`](deploy/kubernetes/storage-class-lustre.yml)

**ZFS example:**

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: flexa-sc-zfs
provisioner: csi.flexa.com
parameters:
  fs: "zfs"
  poolName: "kubernetes"
  protocol: "nfs"
  proxyProfile: "zfs"
reclaimPolicy: Delete
allowVolumeExpansion: true
```

**Lustre example:**

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: flexa-sc-lustre
provisioner: csi.flexa.com
parameters:
  fs: "lustre"
  clusterName: "cvol"
  protocol: "nfs"
  proxyProfile: "lustre"
reclaimPolicy: Delete
allowVolumeExpansion: true
```

**Parameters**

| Name | Required | Description |
| ---- | -------- | ----------- |
| *fs* | Yes | `zfs` or `lustre`. |
| *poolName* | For `fs=zfs` | ZFS pool name on Gluesys FlexA storage. |
| *clusterName* | For `fs=lustre` | Lustre cluster name. |
| *protocol* | Typically set | e.g. `nfs`. |
| *proxyProfile* | Optional | Profile name under `profiles` in `client-info`. When set, the driver uses that profile’s `proxyIP`, `proxyPort`, and `mountIP`. |
| *mountNetwork* | Optional | IP or CIDR of the network nodes mount this class over (e.g. `198.51.100.0/24` for an IB data network). Replaces the profile `mountIP` for this class: the server address (`vip`) is resolved in this network and the default NFS secure zone is this network. A single IP means its /24; give a CIDR for wider networks. Creating fails when the storage has no address in this network. |
| *flexa.io/&lt;option&gt;* | Optional | Default for a [PVC annotation](#pvc-annotations) with the same name, e.g. `flexa.io/optionComp: "on"`. Used only when the PVC does not set that annotation. With Helm, set `storageClass.<key>.volumeDefaults` (keys without the `flexa.io/` prefix). |

Volume expansion is **expand-only**. Shrink (reducing requested size) is not supported.

Apply raw YAML:

```bash
kubectl apply -f <storageclass_yaml>
```

### Volume snapshots (ZFS)

The chart adds the `csi-snapshotter` sidecar (`snapshot.enabled`, default `true`) and a
`VolumeSnapshotClass` named `flexa-snapclass-zfs` (`snapshot.class.*`). The cluster needs the
snapshot CRDs and `snapshot-controller` from
[external-snapshotter](https://github.com/kubernetes-csi/external-snapshotter) (once per
cluster, before installing the chart; the class is created only when the CRDs exist):

```bash
B=https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v8.2.0
for f in client/config/crd/snapshot.storage.k8s.io_volumesnapshotclasses.yaml \
         client/config/crd/snapshot.storage.k8s.io_volumesnapshotcontents.yaml \
         client/config/crd/snapshot.storage.k8s.io_volumesnapshots.yaml \
         deploy/kubernetes/snapshot-controller/rbac-snapshot-controller.yaml \
         deploy/kubernetes/snapshot-controller/setup-snapshot-controller.yaml; do
  kubectl apply -f $B/$f
done
```

Behavior:

- A snapshot is a ZFS snapshot of the PVC's dataset, ready immediately. It shows in the
  FlexA UI snapshot list; the FlexA UI/REST API cannot delete it (or the PVC's volume).
- Restore with `dataSource: {kind: VolumeSnapshot, ...}` in a PVC of a StorageClass on the
  **same pool**. The new volume is an independent copy (`zfs send | recv`), at least as large
  as the source, so the snapshot and the source PVC can be deleted afterwards.
- A PVC whose volume still has snapshots is not deleted on the storage: its PV stays
  `Released` (`FailedPrecondition`) and is deleted once the snapshots are gone.

### PVC annotations

The controller reads **PersistentVolumeClaim** annotations with the `flexa.io/` prefix at volume creation. If a PVC does not set an annotation, the StorageClass parameter with the same name is used; if neither is set, the driver sends the proxy default listed below (the legacy proxy rejects an empty ZFS option, so the driver fills it in). Examples: [`deploy/kubernetes/pvc_zfs.yaml`](deploy/kubernetes/pvc_zfs.yaml), [`deploy/kubernetes/pvc_lustre.yaml`](deploy/kubernetes/pvc_lustre.yaml).

| Annotation | Purpose (summary) |
| ---------- | ----------------- |
| `flexa.io/optionISS` | Include Snapshot Space (ZFS). `on`: snapshots count toward the size (`quota`, `reservation`). `off`: they do not (`refquota`, `refreservation`). Proxy default `on`. |
| `flexa.io/optionSVS` | Shared Volume Space (ZFS). `on`: no space is reserved; the volume shares free pool space. `off`: the full size is reserved. Proxy default `off`. |
| `flexa.io/optionComp` | Compression (ZFS). Proxy default `off`. |
| `flexa.io/optionDedup` | Deduplication (ZFS). Proxy default `off`. |
| `flexa.io/secureAddress` | Secure network address for export. |
| `flexa.io/secureSubnet` | Secure network mask. |
| `flexa.io/nfsAccess` | NFS access mode (`RW`, `RO`). Proxy default `RW`. |
| `flexa.io/nfsNoRootSquashing` | `on` disables root squashing. Proxy default `off`. |
| `flexa.io/nfsInsecure` | `on` allows client ports above 1024. Proxy default `off`. |
| `flexa.io/pvcQos` | Lustre PVC QoS (FlexA 1.5 or later). |

Use values such as `"on"` / `"off"` or addresses as in the samples; follow Gluesys FlexA documentation for your environment.

### Pod annotations

Used at **NodePublishVolume** (NFS mount):

| Annotation | Purpose |
| ---------- | ------- |
| `flexa.io/mountOptions` | Comma-separated NFS options (e.g. `vers=4,ro,async,timeo=666`). See [`deploy/kubernetes/pod_zfs.yaml`](deploy/kubernetes/pod_zfs.yaml). |
| `flexa.io/serviceVIP` | If set, NFS server address for the mount; otherwise the controller-provisioned `vip` from `VolumeContext` is used. One of these must be available for publish to succeed. |

### VolumeContext (advanced)

On provision, the PV carries metadata such as `vip`, `baseDir`, `poolName`, `fs`, `clusterName`, `protocol`, `pvcName`, `pvcNS`, `proxyProfile`, `proxyIP`, `proxyPort`, `mountIP`. Do not edit these under normal operation; they support delete and node publish. For troubleshooting, check `vip`, `baseDir`, and pod `flexa.io/serviceVIP`.

## Building and publishing images

- Build binary: `make flexa-csi-driver`
- Build image: `make docker-build`
- Publish to GHCR: `docker login ghcr.io` then `make docker-publish`

`IMAGE_VERSION` defaults to `git describe --tags --always --dirty`, so a local
build is tagged after the checked-out commit rather than a fixed version. Pass
it explicitly for a reproducible tag: `make docker-publish IMAGE_VERSION=1.0.0`
publishes `ghcr.io/gluesys/flexa-csi:1.0.0` and `:latest`.

By default the cluster pulls `ghcr.io/gluesys/flexa-csi` from GHCR. For a locally built image, set `imagePullPolicy: IfNotPresent` and the image reference in Helm `values.yaml` or in `deploy/kubernetes/controller.yml` / `node.yml`.

**Installing** a build is covered in [Installation](#installation); override `image.repository` / `image.tag` in Helm or edit the manifests before `kubectl apply`.

### Continuous integration

- **Gate:** `make verify` builds the driver binary and runs `go test -race ./...`.
  Run it before opening a pull request.
- **Advisory:** `make fmt-check` (gofmt) and `make lint` (golangci-lint).
- **Chart:** `make chart-package` builds the chart, and `make chart-push`
  pushes it as an OCI artifact to `CHART_REGISTRY` (default `oci://ghcr.io/gluesys`).

## Uninstall

- **Helm:** `helm uninstall flexa-csi -n flexa-csi` (use `helm list -n flexa-csi` if the release name differs).
- **Raw manifests:** From `deploy/kubernetes`, run `sh cleanup.sh` (review the script if you customized StorageClass names).

Ensure no workloads still depend on Gluesys FlexA volumes. PVCs, PVs, and backend volumes may remain depending on `reclaimPolicy` and storage behavior; delete or retain them according to your operations policy.
