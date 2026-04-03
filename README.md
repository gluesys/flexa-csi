# FlexA CSI Driver for Kubernetes

The official [Container Storage Interface](https://github.com/container-storage-interface) driver for FlexA Storage.

### Container Images & Kubernetes Compatibility
Driver Name: csi.flexa.com

| Driver Version                                                                   | Image                                                                     | Supported K8s Version |
| -------------------------------------------------------------------------------- | ------------------------------------------------------------------------- | --------------------- |
| [1.0.0](https://github.com/gluesys/flexa-csi)                                    | `ghcr.io/gluesys/flexa-csi:1.0.0`                                         | 1.21+                 |

The FlexA CSI driver supports:

- **Access modes (CSI):** `SINGLE_NODE_WRITER`, `MULTI_NODE_MULTI_WRITER` (typical Kubernetes mapping: RWO and RWX-style multi-writer NFS where applicable).
- **Controller:** Create/Delete volume, List volumes, Get capacity.
- **Node:** Stage/Unstage (no-op for current NFS flow), Publish/Unpublish, Get volume stats.

**Not supported at this time:** volume expansion (controller and node return not supported), snapshots, cloning.

## Installation

### Prerequisites

- Kubernetes versions 1.21 or above
- FlexA Storage (e.g. 1.4.0 ZFS) with at least one storage pool (for ZFS-backed volumes) or Lustre cluster configuration as required by your deployment
- Go version 1.21 or above is recommended for building from source

### Notice

1. Before installing the CSI driver, make sure you have created and initialized at least one **storage pool** on your FlexA Storage (for ZFS workflows).
2. After you complete the steps below, deploy the CSI driver using the install script.

### Procedure

1. Clone the git repository. `git clone https://github.com/gluesys/flexa-csi.git`
2. Enter the directory. `cd flexa-csi`
3. Copy the client-info template. `cp config/client-info-template.yml config/client-info.yml`
4. Edit `config/client-info.yml` to configure the FlexA CSI Proxy and VIP resolve behavior.

   - **Legacy default (optional):** If you omit `profiles` entirely, you can set a single default endpoint:
     - *host*: IPv4 address of the FlexA CSI Proxy.
     - *port*: TCP port for the proxy (commonly 9001).
     - *mountIP*: (Optional) Reference address used when resolving the NFS VIP (for example a CIDR string such as `192.168.0.0/18`). The proxy uses this value in VIP resolve requests so that, after storage fail-over, mounts still target the correct service address. If *mountIP* is empty, VIP resolve falls back to the proxy *host* only.
   - **Profiles (recommended):** Define named endpoints under `profiles`:
     - Each profile must set *proxyIP* and *proxyPort*.
     - *mountIP* is optional per profile with the same semantics as above (VIP resolve reference).
     - Select a profile per StorageClass with the `proxyProfile` parameter (see below).

5. Install using YAML from the `deploy/kubernetes` directory (the script uses local manifest paths):

   ```bash
   cd deploy/kubernetes
   sh install.sh
   ```

6. Verify all CSI driver pods are Running: `kubectl get pods -n flexa-csi`

## CSI Driver Configuration

You need a Secret (client-info) and StorageClasses. Volume snapshots are **not** implemented in this driver yet; do not rely on `VolumeSnapshotClass` for FlexA volumes until support is added.

This section covers:

1. Creating the storage system secret (often created by the install flow from `config/client-info.yml`)
2. Configuring StorageClasses
3. PVC annotations (volume creation options)
4. Pod annotations (mount behavior)

### Creating a Secret

Create a Secret that holds `client-info.yml`. The install scripts usually create this from your config; to create or recreate it manually:

1. Edit `config/client-info.yml`, for example:

   ```
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

2. Create the Secret:

   ```bash
   kubectl create secret -n <namespace> generic client-info-secret --from-file=config/client-info.yml
   ```

   - Replace `<namespace>` with `flexa-csi` unless you use a custom namespace.
   - If you rename `client-info-secret`, update references under `deploy/kubernetes/` accordingly.

The driver watches the Secret `client-info-secret` in the driver namespace and applies updates at runtime.

### Creating Storage Classes

Create and apply StorageClasses. Parameter names are **case-sensitive** and must match the driver (for example `poolName`, not `poolname`).

Reference examples in the repository:

- ZFS + NFS: [`deploy/kubernetes/storage-class-zfs.yml`](deploy/kubernetes/storage-class-zfs.yml)
- Lustre + NFS: [`deploy/kubernetes/storage-class-lustre.yml`](deploy/kubernetes/storage-class-lustre.yml)

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

| Name            | Required | Description |
| --------------- | -------- | ----------- |
| *fs*            | Yes      | Backing layout: `zfs` or `lustre`. |
| *poolName*      | For `fs=zfs` | ZFS pool name on FlexA. |
| *clusterName*   | For `fs=lustre` | Lustre cluster name for provisioning. |
| *protocol*      | Typically set | Storage access protocol (for example `nfs`). |
| *proxyProfile*  | Optional | Name of a profile under `profiles` in `client-info.yml`. When set, the driver uses that profile’s `proxyIP`, `proxyPort`, and `mountIP` for API calls and VIP resolve. If you rely on the legacy default only, omit it and ensure `host`/`port` are set in the Secret. |

`allowVolumeExpansion: true` does not enable expansion until the driver implements CSI expand; it is kept for forward compatibility.

Apply:

```bash
kubectl apply -f <storageclass_yaml>
```

### PVC annotations

At volume creation time, the controller reads **PersistentVolumeClaim** annotations with the `flexa.io/` prefix and passes them to FlexA (ZFS options, secure export, NFS export flags). See [`deploy/kubernetes/pvc_zfs.yaml`](deploy/kubernetes/pvc_zfs.yaml) and [`deploy/kubernetes/pvc_lustre.yaml`](deploy/kubernetes/pvc_lustre.yaml).

| Annotation | Purpose (summary) |
| ---------- | ----------------- |
| `flexa.io/optionISS` | Instant secure sync (ZFS-related option). |
| `flexa.io/optionSVS` | Secure volume service. |
| `flexa.io/optionComp` | Compression. |
| `flexa.io/optionDedup` | Deduplication (ZFS PVC example sets this; Lustre sample may omit). |
| `flexa.io/secureAddress` | Secure network address for export. |
| `flexa.io/secureSubnet` | Secure network mask. |
| `flexa.io/nfsAccess` | NFS access mode (e.g. RW). |
| `flexa.io/nfsNoRootSquashing` | NFS root squashing behavior. |
| `flexa.io/nfsInsecure` | NFS insecure port behavior. |

Values are typically `"on"` / `"off"` or addresses as in the examples; align with your FlexA documentation.

### Pod annotations

Used at **NodePublishVolume** (NFS mount path):

| Annotation | Purpose |
| ---------- | ------- |
| `flexa.io/mountOptions` | Comma-separated NFS mount options (for example `vers=4,ro,async,timeo=666`). See [`deploy/kubernetes/pod_zfs.yaml`](deploy/kubernetes/pod_zfs.yaml). |
| `flexa.io/serviceVIP` | If set, used as the NFS server address for the mount. If empty, the driver uses the controller-provisioned `vip` from the volume’s `VolumeContext`. At least one of these must be present for a successful publish. |

### VolumeContext (advanced)

On provision, the controller stores metadata on the PV (for example `vip`, `baseDir`, `poolName`, `fs`, `clusterName`, `protocol`, `pvcName`, `pvcNS`, `proxyProfile`, `proxyIP`, `proxyPort`, `mountIP`). You normally do not edit these; they are used for delete operations and node publish. If you troubleshoot mounts, confirm `vip` and `baseDir`, and whether the pod overrides the server with `flexa.io/serviceVIP`.

## Building & Manually Installing

By default, the CSI driver pulls the latest image from GHCR: `ghcr.io/gluesys/flexa-csi:latest`.

If you install with a locally built image, set `imagePullPolicy: IfNotPresent` on the `csi-plugin` containers in the manifests under `deploy/kubernetes/`.

### Building

- Build the binary: `make flexa-csi-driver`
- Build the image: `make docker-build`, then `docker images` to verify
- Publish to GHCR:
  - `docker login ghcr.io`
  - `make docker-publish` (publishes `ghcr.io/gluesys/flexa-csi:1.0.0` and `:latest`)

### Installation

From the repository root:

```bash
cd deploy/kubernetes
sh install.sh
```

### Uninstallation

Ensure no workloads still use FlexA volumes, then from `deploy/kubernetes` run `sh cleanup.sh` (review the script if you use custom StorageClass file names).
