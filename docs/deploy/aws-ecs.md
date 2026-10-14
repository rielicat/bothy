# AWS ECS on EC2

One ECS task on one EC2 container instance: the gateway, the sandbox sidecar
and an init container that fetches the vault key from AWS Secrets Manager.

Files: [`deploy/aws/ecs-task-definition.json`](../../deploy/aws/ecs-task-definition.json),
[`ecs-task-role-trust-policy.json`](../../deploy/aws/ecs-task-role-trust-policy.json),
[`iam-bedrock-policy.json`](../../deploy/aws/iam-bedrock-policy.json),
[`iam-kek-secret-policy.json`](../../deploy/aws/iam-kek-secret-policy.json),
[`iam-secrets-policy.json`](../../deploy/aws/iam-secrets-policy.json).

## Docker 25 on the ECS-optimized AMI

The ECS-optimized AL2023 AMI ships Docker 25.0.16 as of October 2026
([changelog](https://github.com/aws/amazon-ecs-ami/blob/main/CHANGELOG.md)),
and AWS documents ECS support for Docker 20.10 and 25.x only
([ECS: Docker support](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/ecs-agent-versions.html#docker-support)).
Docker 25 (engine API 1.44) has no volume subpaths, which sandboxd uses on
Docker 26+, so there it binds the same directories from the volumes' host
paths and checks every box's mounts, by device and inode, against its own
mounts of `bothy_work` (read-only) and `bothy_run` before the box is used. The
sandbox works on the stock AMI with these constraints:

- runc only: gVisor (`runsc`) is not used without volume subpaths, and
  `BOTHY_SBX_RUNTIME=runsc` refuses to start boxes.
- sandboxd mounts `bothy_work` read-only at `/work` (the task definition
  does), and both volumes stay on the `local` driver.
- `bothy doctor` shows `sandbox.bind_mounts` (info), and the sandbox status
  in the UI names the engine as "(api 1.44, bind mounts)".

An AMI with Docker 20.10 (API 1.41) is too old: sandboxd reports "Docker 25+
required"; run [without the sandbox](#without-the-sandbox) there.

## Layout

The task uses the `bridge` network mode and runs alone on its instance.

| Container | Runs as | What it does |
|---|---|---|
| `kek` | `0:0`, only `CHOWN` | Fetches `bothy/kek` with the task role, writes `/kek/bothy_kek` (0600, owner 65532) and exits. `bothy` starts only after it succeeds (`dependsOn: SUCCESS`). |
| `bothy` | `65532:10001` | The gateway: read-only root, no capabilities, `no-new-privileges`, tmpfs `/tmp`, port 8080 on the host, health check `/bothy healthcheck`. |
| `sandboxd` | `65532:65532` | `disableNetworking: true` (loopback only, like compose's `network_mode: none`), the Docker socket, `bothy_work` read-only (for the Docker 25 mount check), restarted on failure without stopping the task. |

Two deliberate differences from compose, because ECS task definitions have no
supplementary groups (`--group-add`):

- The gateway's primary group is 10001, the workspace group it shares with
  the boxes. As `65532:65532` it cannot hand workspaces and egress sockets to
  the boxes, and every box fails closed (tested).
- sandboxd reaches the socket through an ACL for its group 65532, set on the
  instance ([step 1](#steps)).

`bothy_data`, `bothy_work` and `bothy_run` are Docker volumes with
`scope: shared`. For that scope the ECS agent uses the task definition's name
as the Docker volume name; task-scoped volumes get a generated one
([agent source](https://github.com/aws/amazon-ecs-agent/blob/master/agent/api/task/task.go)).
sandboxd needs the fixed names: it mounts subpaths of `bothy_work` and
`bothy_run` into boxes by engine name (on Docker 25, directories under the host
paths the engine reports for those names). They live in `/var/lib/docker/volumes` on the instance's EBS
volume. The `kek` volume is task-scoped and deleted when the task stops. There
is no `/keys` volume: the KEK comes from Secrets Manager.

## The sandbox and the Docker socket

sandboxd mounts the host's Docker socket, which is root on the instance. A
compromised sandboxd owns the instance and every task on it, and on EC2 tasks
are not isolated from each other anyway
([ECS: task IAM role](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task-iam-roles.html)).
What limits the risk: sandboxd has no network, serves only the fixed `/v1`
API on a unix socket, accepts only the gateway's uid, takes no image, mount
or capability from the caller, and creates every box from its compiled-in
spec with digest-pinned images. Give Bothy its own instance and cluster.

## The vault key

ECS passes secrets from Secrets Manager or Parameter Store to containers only
as environment variables
([ECS: sensitive data](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/specifying-sensitive-data.html)).
An environment variable is visible to `docker inspect` on the host and to
anything that can read the process environment, and Bothy flags `vault.kek:
env` as high. AWS itself suggests a sidecar that writes the secret to a shared
volume. That is the `kek` container: the key never appears in the task
definition, the environment or the logs. It sits in the task-scoped volume on
the encrypted instance disk, readable only by uid 65532, until the task stops.

Alternatives, if this does not fit:

- `vault.kek: passphrase`: nothing stored; the vault stays sealed after every
  restart until an admin unlocks it in the UI.
- The auto-generated keyfile on a `bothy_keys` Docker volume: simplest, but the
  key lives on the instance and is lost with it unless you copy it out.
- Parameter Store `SecureString`: the same init container with
  `aws ssm get-parameter --name /bothy/kek --with-decryption --query Parameter.Value --output text`
  and `ssm:GetParameter` (plus `kms:Decrypt` for a customer managed key) in the
  task role.

## Steps

1. **Container instance.** One instance in an Auto Scaling group of one,
   30 GB encrypted root volume. In the launch template require IMDSv2 with a
   PUT response hop limit of 1, so bridge-mode containers cannot get instance
   credentials; tasks use their task role through the ECS agent
   ([ECS: task IAM role](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task-iam-roles.html)
   also shows an iptables rule for the same purpose). User data:

   ```sh
   #!/bin/bash
   echo ECS_CLUSTER=bothy >> /etc/ecs/ecs.config
   ```

   For the sandbox, also give group 65532 access to the socket, now and
   after every Docker restart:

   ```sh
   command -v setfacl >/dev/null || dnf install -y acl || apt-get install -y acl
   mkdir -p /etc/systemd/system/docker.service.d
   printf '[Service]\nExecStartPost=/usr/bin/setfacl -m g:65532:rw /var/run/docker.sock\n' \
     > /etc/systemd/system/docker.service.d/bothy-sandboxd.conf
   systemctl daemon-reload && setfacl -m g:65532:rw /var/run/docker.sock
   ```

2. **IAM.** Replace `<REGION>` and `<ACCOUNT_ID>` in the JSON files, then:

   ```sh
   aws iam create-role --role-name bothy-task \
     --assume-role-policy-document file://deploy/aws/ecs-task-role-trust-policy.json
   aws iam put-role-policy --role-name bothy-task --policy-name bedrock \
     --policy-document file://deploy/aws/iam-bedrock-policy.json
   aws iam put-role-policy --role-name bothy-task --policy-name vault-key \
     --policy-document file://deploy/aws/iam-kek-secret-policy.json
   aws iam put-role-policy --role-name bothy-task --policy-name secrets \
     --policy-document file://deploy/aws/iam-secrets-policy.json
   aws iam create-role --role-name bothy-task-execution \
     --assume-role-policy-document file://deploy/aws/ecs-task-role-trust-policy.json
   aws iam attach-role-policy --role-name bothy-task-execution \
     --policy-arn arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy
   ```

   The task role is what the containers use: Bedrock, the vault key and the
   secrets Bothy references in Secrets Manager. With a task role, SDKs in the
   task use only its credentials, never the instance's. The execution role
   only lets ECS write the logs.

3. **Vault key and log group.**

   ```sh
   (umask 077 && openssl rand -hex 32 > bothy_kek)
   aws secretsmanager create-secret --name bothy/kek --secret-string file://bothy_kek
   shred -u bothy_kek
   aws logs create-log-group --log-group-name /ecs/bothy
   ```

   The secret is now the only copy of the key. Restrict who can read or
   delete it.

4. **Task definition.** Pin every image by digest. The digests are in the
   release's signed compose bundle (`install.sh --compose`), or:

   ```sh
   VERSION=0.2.0
   digest() { docker buildx imagetools inspect "$1" --format '{{json .Manifest}}' | jq -r .digest; }
   sed -e "s/<ACCOUNT_ID>/123456789012/g" -e "s/<REGION>/us-east-1/g" -e "s/<VERSION>/$VERSION/g" \
       -e "s/sha256:<BOTHY_DIGEST>/$(digest ghcr.io/rielicat/bothy:$VERSION)/g" \
       -e "s/sha256:<SANDBOX_DIGEST>/$(digest ghcr.io/rielicat/bothy-sandbox:$VERSION)/g" \
       -e "s/sha256:<SANDBOX_NODE_DIGEST>/$(digest ghcr.io/rielicat/bothy-sandbox-node:$VERSION)/g" \
       -e "s/<AWSCLI_TAG>/2.31.0/g" -e "s/sha256:<AWSCLI_DIGEST>/$(digest public.ecr.aws/aws-cli/aws-cli:2.31.0)/g" \
       deploy/aws/ecs-task-definition.json > bothy-task.json
   aws ecs register-task-definition --cli-input-json file://bothy-task.json
   ```

   Pick an AWS CLI version you have reviewed; `2.31.0` is only an example.

5. **Service.** One task, and the old one stops before the new one starts
   (one SQLite writer, one host port):

   ```sh
   aws ecs create-service --cluster bothy --service-name bothy --task-definition bothy \
     --desired-count 1 --launch-type EC2 \
     --deployment-configuration maximumPercent=100,minimumHealthyPercent=0
   ```

6. **Setup.** The instance's security group needs no inbound rule. Open a
   tunnel with SSM port forwarding: the SSM agent connects to
   `localhost:8080` on the instance, which reaches the gateway through
   Docker's published port, so Bothy sees a local peer and allows setup.

   ```sh
   aws ssm start-session --target i-0123456789abcdef0 \
     --document-name AWS-StartPortForwardingSession \
     --parameters '{"portNumber":["8080"],"localPortNumber":["8080"]}'
   aws logs tail /ecs/bothy --since 15m | grep setup
   ```

   Open the printed `http://127.0.0.1:8080/setup#t=…` link.

## Without the sandbox

Where a task holding the Docker socket is not acceptable, or the instance runs
Docker 20.10, drop sandboxd and the socket mount and tell the gateway:

```sh
jq 'del(.containerDefinitions[] | select(.name == "sandboxd"))
  | del(.volumes[] | select(.name == "docker-sock"))
  | (.containerDefinitions[] | select(.name == "bothy") | .environment[]
     | select(.name == "BOTHY_SANDBOX__BACKEND") | .value) = "none"' \
  bothy-task.json > bothy-task-nosandbox.json
```

Skip the ACL in step 1. Bothy shows the "No sandbox" banner.

## An internal ALB instead of a tunnel

- An internal ALB in subnets that hold nothing else, an HTTPS listener with an
  ACM certificate, and a target group of type `instance`, protocol HTTP, port
  8080, health check path `/healthz`, success code `200` (Bothy answers health
  checks whatever their Host header; tested). Attach it
  with `--load-balancers targetGroupArn=<arn>,containerName=bothy,containerPort=8080`
  on `create-service`.
- Instance security group: port 8080 from the ALB's security group only.
- Add to the `bothy` container's environment:

  ```json
  {"name": "BOTHY_SERVER__PUBLIC_URL", "value": "https://bothy.internal.example.com"},
  {"name": "BOTHY_SERVER__TRUSTED_PROXIES", "value": "[\"10.0.10.0/24\",\"10.0.11.0/24\"]"}
  ```

  The CIDRs are the ALB's subnets: only the proxy may be trusted.
- For a second login in front of Bothy's, add an `authenticate-oidc` or
  `authenticate-cognito` action to the HTTPS listener; an internal ALB needs a
  NAT gateway to reach the IdP
  ([ELB: authenticate users](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/listener-authenticate-users.html)).
- First-run setup is refused through the ALB, whatever address it forwards:
  use the SSM tunnel once.

## Models

The task role reaches Amazon Bedrock with no keys: Bothy picks up the ECS
container credentials like any AWS SDK. Configure the provider in setup ("Use
this server's AWS access") or with environment variables as in
[vm.md](vm.md#models), which also covers model access and global profiles.

## Provider keys and channel tokens

Leave them in Secrets Manager and reference them
([how](README.md#platform-secrets)): the gateway reads them with the task role,
so no key passes through the task definition, ECS's environment injection or
the vault. Add to the `bothy` container's environment, for example:

```json
{"name": "BOTHY_SECRETS__AWS__REGION", "value": "us-east-1"},
{"name": "BOTHY_CHANNELS__SLACK__BOT_TOKEN", "value": "aws-sm:bothy/providers#slack_bot_token"}
```

and for a provider the whole entry, as [vm.md](vm.md#models) shows, with
`"api_key": "aws-sm:bothy/providers#anthropic_api_key"`. These are references,
not secrets, so environment variables are fine here.

## Operations

- **Logs:** CloudWatch Logs, group `/ecs/bothy`.
- **Backups:** ECS Exec does not work here (it needs a writable root
  filesystem and runs as root). Use an SSM shell on the instance:

  ```sh
  c=$(docker ps -q --filter label=com.amazonaws.ecs.container-name=bothy)
  docker exec "$c" /bothy backup
  docker cp "$c":/data/backups/bothy-<time>.tar .
  ```

  Snapshot the instance's EBS volume with Data Lifecycle Manager: the Docker
  volumes are on it. To replace the instance, restore the volume from a
  snapshot, or unpack a backup into a fresh `bothy_data` volume; the KEK comes
  from Secrets Manager either way.
- **Upgrades:** register a revision with the new digests and
  `aws ecs update-service --cluster bothy --service bothy --task-definition bothy`.
  With `minimumHealthyPercent=0` there is a short gap while the task restarts.
- ECS task definitions have no PID limit field, unlike compose's `pids_limit`;
  boxes keep their own limits from sandboxd.

## Not tested here

Run locally on Docker 27.4: the user and group layout, the init container
and the KEK file, `disableNetworking`, real boxes with egress, and the ALB
health check status. The Docker 25 mode ran as the Docker acceptance suite
with sandboxd limited to engine API 1.44 on Docker Desktop 27.4, not on a real
Docker 25 engine. Validated with the AWS CLI's parameter validation: the
task definition and the jq variant. Not run on AWS: the ECS agent with this
task definition, the stock AMI's Docker 25, the socket ACL, IAM, the ALB.
The ECS docs say the agent must register `ECS_SELINUX_CAPABLE=true` or
`ECS_APPARMOR_CAPABLE=true` before containers use `dockerSecurityOptions`;
whether that applies to `no-new-privileges` is not stated. If the task does
not place, add one of them to `/etc/ecs/ecs.config` or drop the option.
