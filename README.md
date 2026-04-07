# FlexA CSI Driver for Kubernetes

The official [Container Storage Interface](https://github.com/container-storage-interface) driver for FlexA Storage.

### Container Images & Kubernetes Compatibility

Driver Name: `csi.flexa.com`

| Driver Version | Image | Supported K8s Version |
| -------------- | ----- | --------------------- |
| [1.0.0](https://github.com/gluesys/flexa-csi) | `ghcr.io/gluesys/flexa-csi:1.0.0` | 1.21+ |

The FlexA CSI driver supports:

- **Access modes (CSI):** `SINGLE_NODE_WRITER`, `MULTI_NODE_MULTI_WRITER` (typical Kubernetes mapping: RWO and RWX-style multi-writer NFS where applicable).
- **Controller:** Create/Delete volume, List volumes, Get capacity.
- **Node:** Stage/Unstage (no-op for the current NFS flow), Publish/Unpublish, Get volume stats.

**Not supported at this time:** volume expansion (controller and node return not supported), snapshots, cloning.

## Prerequisites

- Kubernetes 1.21 or above
- FlexA Storage (e.g. 1.4.0 ZFS) with at least one storage pool for ZFS-backed volumes, or Lustre cluster configuration as required by your deployment
- Go 1.21+ recommended when building from source

### Before you install

1. For ZFS workflows, create and initialize at least one **storage pool** on FlexA Storage.
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

You need a **Secret** (`client-info.yml`) and **StorageClasses**. Volume snapshots are **not** implemented; do not rely on `VolumeSnapshotClass` until support exists.

Contents: [client-info / Secret](#client-info--secret) · [StorageClasses](#creating-storage-classes) · [PVC annotations](#pvc-annotations) · [Pod annotations](#pod-annotations) · [VolumeContext](#volumecontext-advanced)

### client-info / Secret

**Helm:** Configure proxy endpoints in `charts/flexa-csi/values.yaml` (`clientInfo.content`) or use `-f` / `--set-file` as described in [Installation](#installation).

**Raw manifests:** Prepare `config/client-info.yml` with either a legacy `host`/`port` default and/or `profiles` (`proxyIP`, `proxyPort`, optional `mountIP` for VIP resolve). See the template at [`config/client-info-template.yml`](config/client-info-template.yml).

#### Creating the Secret manually

Use when not using Helm, or when you pre-create the Secret for Helm (`clientInfo.create=false`):

1. Example `config/client-info.yml`:

   ```yaml
   host: 192.168.1.2
   port: 5001
   mountIP: "192.168.0.0/18"

   profiles:
     prodA:
       proxyIP: 10.0.0.11
       proxyPort: 9001
       mountIP: "192.168.0.0/18"
     prodB:
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
  proxyProfile: "prodA"
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
| *poolName* | For `fs=zfs` | ZFS pool name on FlexA. |
| *clusterName* | For `fs=lustre` | Lustre cluster name. |
| *protocol* | Typically set | e.g. `nfs`. |
| *proxyProfile* | Optional | Profile name under `profiles` in `client-info`. When set, the driver uses that profile’s `proxyIP`, `proxyPort`, and `mountIP`. For legacy-only `host`/`port`, omit and ensure the Secret has `host`/`port`. |

`allowVolumeExpansion: true` does not enable expansion until the driver implements CSI expand; it is forward-looking.

Apply raw YAML:

```bash
kubectl apply -f <storageclass_yaml>
```

### PVC annotations

The controller reads **PersistentVolumeClaim** annotations with the `flexa.io/` prefix at volume creation. Examples: [`deploy/kubernetes/pvc_zfs.yaml`](deploy/kubernetes/pvc_zfs.yaml), [`deploy/kubernetes/pvc_lustre.yaml`](deploy/kubernetes/pvc_lustre.yaml).

| Annotation | Purpose (summary) |
| ---------- | ----------------- |
| `flexa.io/optionISS` | Instant secure sync (ZFS). |
| `flexa.io/optionSVS` | Secure volume service (ZFS). |
| `flexa.io/optionComp` | Compression (ZFS). |
| `flexa.io/optionDedup` | Deduplication (ZFS). |
| `flexa.io/secureAddress` | Secure network address for export. |
| `flexa.io/secureSubnet` | Secure network mask. |
| `flexa.io/nfsAccess` | NFS access mode (e.g. RW). |
| `flexa.io/nfsNoRootSquashing` | NFS root squashing. |
| `flexa.io/nfsInsecure` | NFS insecure port. |

Use values such as `"on"` / `"off"` or addresses as in the samples; follow FlexA documentation for your environment.

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
- Publish to GHCR: `docker login ghcr.io` then `make docker-publish` (publishes `ghcr.io/gluesys/flexa-csi:1.0.0` and `:latest`)

By default the cluster pulls `ghcr.io/gluesys/flexa-csi` from GHCR. For a locally built image, set `imagePullPolicy: IfNotPresent` and the image reference in Helm `values.yaml` or in `deploy/kubernetes/controller.yml` / `node.yml`.

**Installing** a build is covered in [Installation](#installation); override `image.repository` / `image.tag` in Helm or edit the manifests before `kubectl apply`.

## Uninstall

- **Helm:** `helm uninstall flexa-csi -n flexa-csi` (use `helm list -n flexa-csi` if the release name differs).
- **Raw manifests:** From `deploy/kubernetes`, run `sh cleanup.sh` (review the script if you customized StorageClass names).

Ensure no workloads still depend on FlexA volumes. PVCs, PVs, and backend volumes may remain depending on `reclaimPolicy` and storage behavior; delete or retain them according to your operations policy.
