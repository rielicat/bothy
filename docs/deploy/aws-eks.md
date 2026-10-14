# AWS EKS

The gateway as a hardened single-replica Deployment, with IRSA for Amazon
Bedrock and the vault key mounted from a Secret. Without the sandbox by
default; an optional overlay adds sandboxd on nodes where mounting a Docker
socket is acceptable.

Files: [`deploy/k8s/base/`](../../deploy/k8s/base) (Namespace, ServiceAccount,
StorageClass, PVC, Deployment, Service, NetworkPolicy),
[`deploy/k8s/secret-kek.example.yaml`](../../deploy/k8s/secret-kek.example.yaml),
[`deploy/k8s/sandboxd/`](../../deploy/k8s/sandboxd) (optional overlay),
[`deploy/aws/eks-irsa-trust-policy.json`](../../deploy/aws/eks-irsa-trust-policy.json),
[`deploy/aws/iam-bedrock-policy.json`](../../deploy/aws/iam-bedrock-policy.json).

## Why no sandbox by default

sandboxd talks the Docker Engine API (1.45 or newer, for volume subpaths) and
mounts subpaths of named Docker volumes into boxes. EKS nodes run containerd
without Docker, so there is nothing for it to talk to, and giving a pod the
node's runtime socket makes the pod root on the node. The base manifests set
`BOTHY_SANDBOX__BACKEND=none`: chat, channels, `fs_*`, web tools, HTTP MCP
servers, memory and schedules work; `shell`, `python` and MCP stdio servers
are absent and the UI shows the "No sandbox" banner.

## Prerequisites

- EKS 1.28 or newer (Kubernetes API data, Secrets included, is envelope
  encrypted with KMS by default;
  [EKS: envelope encryption](https://docs.aws.amazon.com/eks/latest/userguide/envelope-encryption.html)).
- The Amazon EBS CSI driver add-on (the StorageClass uses `ebs.csi.aws.com`;
  on EKS Auto Mode use `ebs.csi.eks.amazonaws.com`).
- An IAM OIDC provider for the cluster (IRSA), or EKS Pod Identity.
- Optional: VPC CNI 1.21+ with network policy enabled, for the NetworkPolicy.
- Nodes that block the instance metadata service for pods (IMDSv2 with hop
  limit 1), so pods cannot use the node role.

## Steps

1. **IAM role for the service account.** In
   `deploy/aws/eks-irsa-trust-policy.json` replace `<ACCOUNT_ID>` and
   `<OIDC_PROVIDER>` (the cluster's issuer without `https://`, from
   `aws eks describe-cluster --name <cluster> --query cluster.identity.oidc.issuer --output text`).
   It trusts only `system:serviceaccount:bothy:bothy`
   ([EKS: IRSA](https://docs.aws.amazon.com/eks/latest/userguide/associate-service-account-role.html)).

   ```sh
   aws iam create-role --role-name bothy --assume-role-policy-document file://deploy/aws/eks-irsa-trust-policy.json
   aws iam put-role-policy --role-name bothy --policy-name bedrock \
     --policy-document file://deploy/aws/iam-bedrock-policy.json
   ```

   Put the role ARN in `deploy/k8s/base/serviceaccount.yaml`. The EKS webhook
   then injects `AWS_ROLE_ARN` and `AWS_WEB_IDENTITY_TOKEN_FILE` into the pod
   and mounts the token; Bothy's AWS credential chain uses them. With EKS Pod
   Identity, drop the annotation and create a pod identity association for
   `bothy/bothy` instead.

2. **Image digest.** Set `sha256:<BOTHY_DIGEST>` in
   `deploy/k8s/base/kustomization.yaml` (see [upgrades](README.md#upgrades)).

3. **Namespace and vault key.** Create the key once, keep a copy in Secrets
   Manager, and load it as a Secret from the file, so it never lands in a
   manifest or in shell history:

   ```sh
   kubectl apply -f deploy/k8s/base/namespace.yaml
   (umask 077 && openssl rand -hex 32 > bothy_kek)
   aws secretsmanager create-secret --name bothy/kek --secret-string file://bothy_kek
   kubectl -n bothy create secret generic bothy-kek --from-file=bothy_kek=./bothy_kek
   shred -u bothy_kek
   ```

   `deploy/k8s/secret-kek.example.yaml` shows the shape; do not commit a real
   key. The pod mounts the Secret read-only at `/etc/bothy/kek` (mode 0440,
   group 65532) and `BOTHY_VAULT__KEK=file:/etc/bothy/kek/bothy_kek` points the
   vault at it.

4. **Apply.**

   ```sh
   kubectl apply -k deploy/k8s/base
   kubectl -n bothy rollout status deploy/bothy
   kubectl -n bothy logs deploy/bothy | grep setup
   kubectl -n bothy port-forward svc/bothy 8080:8080
   ```

   Open the printed `http://127.0.0.1:8080/setup#t=…` link. `kubectl
   port-forward` connects inside the pod's network namespace to localhost, so
   Bothy sees a local peer and allows setup.

### Secrets Store CSI driver instead of a Secret

To mount the key straight from Secrets Manager, install the Secrets Store CSI
driver with the AWS provider (ASCP), allow `secretsmanager:GetSecretValue` and
`secretsmanager:DescribeSecret` on `bothy/kek` in the IRSA role
([`iam-kek-secret-policy.json`](../../deploy/aws/iam-kek-secret-policy.json)),
and replace the `kek` volume:

```yaml
apiVersion: secrets-store.csi.x-k8s.io/v1
kind: SecretProviderClass
metadata:
  name: bothy-kek
  namespace: bothy
spec:
  provider: aws
  parameters:
    objects: |
      - objectName: bothy/kek
        objectType: secretsmanager
        objectAlias: bothy_kek
```

```yaml
      volumes:
        - name: kek
          csi:
            driver: secrets-store.csi.k8s.io
            readOnly: true
            volumeAttributes:
              secretProviderClass: bothy-kek
```

([Secrets Manager: ASCP with IRSA](https://docs.aws.amazon.com/secretsmanager/latest/userguide/integrating_csi_driver.html)).
Not tested here.

## What the manifests do

- **Pod security:** the namespace enforces the `restricted` Pod Security
  Standard. The pod runs as 65532:65532 (`runAsNonRoot`), with a read-only
  root filesystem, all capabilities dropped, no privilege escalation and the
  `RuntimeDefault` seccomp profile. It mounts no Kubernetes API token
  (`automountServiceAccountToken: false`); the IRSA token is a separate
  projected volume.
- **Probes:** `exec` of `/bothy healthcheck`, which dials the gateway on
  loopback. An `httpGet` probe of `/healthz` from the node's address works
  too: `GET` and `HEAD` on `/healthz` and `/readyz` skip the Host check and the
  exposure gate.
- **Storage:** one EBS gp3 PVC, encrypted, `reclaimPolicy: Retain`. The
  Deployment uses the `Recreate` strategy: one writer, one ReadWriteOnce
  volume. `fsGroup` makes the volume root group-writable for 65532, and Bothy
  keeps its data in its own `0700` directory `/data/bothy`
  (`BOTHY_STORAGE__DATA_DIR`), so `bothy security-audit` has nothing to flag.
  Workspaces live in `/data/bothy/work`.
- **Memory:** 256 Mi limit with `GOMEMLIMIT=48MiB`, as in compose; `/tmp` and
  `/run/bothy` are small memory-backed `emptyDir`s.
- **Service:** `ClusterIP` only. Never expose Bothy with a `NodePort` or a
  public `LoadBalancer`.

## NetworkPolicy

[`networkpolicy.yaml`](../../deploy/k8s/base/networkpolicy.yaml) is defense in
depth. It denies all ingress (port-forward does not pass through it) and allows
egress only to cluster DNS and to TCP 443 outside private, carrier-grade NAT
and link-local ranges, which also keeps the pod away from instance metadata.
Bothy's own controls come first: tool boxes reach the network only through
its egress proxy with an allowlist, and the gateway's provider, channel and
MCP clients dial exact hosts with SSRF checks. Kubernetes policies match IP
ranges, not names, so this cannot narrow egress to Bedrock alone. Adjust it if
you use:

- VPC interface endpoints for Bedrock or STS (private IPs): allow their
  subnets on 443.
- Local model servers or private MCP servers: allow those addresses and ports.
- An internal ALB: allow ingress on 8080 from the ALB's subnets.

## An internal ALB instead of port-forward

With the AWS Load Balancer Controller, an Ingress with
`alb.ingress.kubernetes.io/scheme: internal`,
`alb.ingress.kubernetes.io/target-type: ip`,
`alb.ingress.kubernetes.io/listen-ports: '[{"HTTPS":443}]'`,
`alb.ingress.kubernetes.io/certificate-arn: <ACM certificate ARN>`,
`alb.ingress.kubernetes.io/healthcheck-path: /healthz` (the default success
code `200` holds: Bothy answers health checks whatever their Host header).
Then add to the container's environment `BOTHY_SERVER__PUBLIC_URL` (the HTTPS
URL) and
`BOTHY_SERVER__TRUSTED_PROXIES` (the ALB's subnets as a JSON list), and allow
that ingress in the NetworkPolicy. Run first-run setup through port-forward:
a client behind the ALB never counts as local.

## Optional: sandboxd on a Docker node

Only where a pod holding the node's Docker socket is acceptable. Requirements:

- A dedicated node group whose nodes run Docker Engine 26+ next to the
  kubelet's runtime, labelled `bothy-docker-engine=true` and tainted
  `bothy-docker-engine:NoSchedule`.
- On each such node, the two Docker volumes sandboxd refers to by name,
  initialised with the image's ownership by mounting them once:

  ```sh
  docker run --rm -v bothy_work:/work -v bothy_run:/run/bothy ghcr.io/rielicat/bothy:<version>@sha256:<digest> version
  ```

- The socket's group id in `supplementalGroups` (999 in the overlay;
  `stat -c %g /var/run/docker.sock` on the node).
- Digests for the sandbox images in `deploy/k8s/sandboxd/deployment-patch.yaml`
  and the gateway digest in `deploy/k8s/sandboxd/kustomization.yaml`.

```sh
kubectl apply -k deploy/k8s/sandboxd
```

The overlay adds the sidecar (non-root, read-only root, no capabilities, the
socket as a `hostPath`), mounts the Docker volumes' directories on the node at
`/work` and `/run/bothy` in both containers so the gateway and the boxes share
workspaces and egress sockets, gives the gateway the workspace group 10001,
and pins the pod to the Docker nodes. `hostPath` volumes need the namespace
at the `privileged` Pod Security level, which the overlay sets. Boxes are
created by the node's Docker, outside Kubernetes scheduling and resource
accounting. This overlay renders and validates but has not been run on a
cluster.

## Backups and upgrades

`kubectl -n bothy exec deploy/bothy -- /bothy backup` writes a consistent
copy inside the volume. The image has no `tar`, so `kubectl cp` cannot copy it
out: back up the volume with EBS snapshots (a `VolumeSnapshot` with the CSI
snapshot controller, or AWS Backup) and restore by creating the PVC from a
snapshot. To upgrade, back up, change the digest in `kustomization.yaml` and
apply; `Recreate` stops the old pod first.

## Not tested here

Rendered with `kubectl kustomize` and decoded strictly into the `k8s.io/api`
v0.34 types (base and overlay), with checks for the hardening fields. The
same container settings ran locally on Docker 27.4 (read-only root, a
group-writable volume root like `fsGroup` leaves, the key from a
secret-style symlinked file): healthy, vault open, no `data.permissions`
finding. Not run on EKS: IRSA, the EBS CSI driver, the NetworkPolicy, ASCP,
the ALB Ingress and the sandboxd overlay.
