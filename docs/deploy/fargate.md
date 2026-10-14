# AWS Fargate

Bothy in single-container mode: the gateway alone, behind an internal ALB,
with its data on EFS. Fargate has no Docker socket, no host volumes and no
privileged containers, so there is never a sandbox here.

File: [`deploy/aws/fargate-task-definition.json`](../../deploy/aws/fargate-task-definition.json),
plus the IAM files listed in the steps.

## What works

| Works | Does not |
|---|---|
| Web UI and API, Telegram, Discord, Slack | `shell`, `python` and MCP stdio servers: absent, with the "No sandbox" banner |
| Models, including Amazon Bedrock through the task role | Anything that needs a tool box |
| `fs_*` tools on workspace files, `web_fetch`, `web_search` | ECS Exec, so no shell or tunnel into the task: it needs a writable root filesystem and runs as root ([ECS Exec](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/ecs-exec.html)) |
| HTTP MCP servers with vault-held headers, memory, schedules, approvals, audit | `bothy backup` on demand (no exec); rely on EFS backups |

## Storage: read this first

Fargate has no durable block storage for a service: EBS volumes attached to
service tasks are deleted when the task stops
([ECS: EBS volumes](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/ebs-volumes.html)).
The data therefore goes to EFS, through an access point that acts as uid and
gid 65532 and creates Bothy's root directory with mode 0700.

Bothy's database is SQLite in WAL mode, and SQLite states that WAL does not
work over a network filesystem, because readers share memory through the
`-shm` file ([SQLite: WAL](https://www.sqlite.org/wal.html)). With exactly one
task, one process uses the database and this usually holds, but it is outside
SQLite's supported setups and was not tested here. Never let two tasks overlap
(`maximumPercent=100`, `minimumHealthyPercent=0`) and keep EFS backups on. If
the data matters and you cannot accept that, use [ECS on EC2](aws-ecs.md) or a
[VM](vm.md).

## The vault key

The same init container as [on ECS](aws-ecs.md#the-vault-key) fetches the KEK
from Secrets Manager into a file. The volume is the task's ephemeral storage,
encrypted by Fargate with a service-managed key and gone when the task stops.
Two Fargate-specific details, both in the task definition:

- Fargate lets a container add only `SYS_PTRACE`, so instead of dropping all
  capabilities and adding `CHOWN`, the init container drops every default
  capability except `CHOWN`.
- Bind-mount volumes start owned by root with mode 0755
  ([ECS: bind mounts](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/bind-mounts.html)),
  and the gateway runs as 65532 with a read-only root, so the init container
  also opens the shared `/tmp` volume (`chmod 1777`).

## Steps

1. **EFS.** An encrypted file system with mount targets in the task's subnets
   (NFS, TCP 2049, from the task's security group only) and an access point:

   ```sh
   aws efs create-access-point --file-system-id fs-0123456789abcdef0 \
     --posix-user Uid=65532,Gid=65532 \
     --root-directory 'Path=/bothy,CreationInfo={OwnerUid=65532,OwnerGid=65532,Permissions=0700}'
   ```

   The task definition mounts it with transit encryption and IAM
   authorization; when it names an access point, the root directory must be
   omitted or `/` ([ECS: EFS volumes](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/efs-volumes.html)).

2. **IAM.** As in [ECS step 2](aws-ecs.md#steps), with a third policy on the
   task role,
   [`iam-efs-policy.json`](../../deploy/aws/iam-efs-policy.json), which allows
   mounting and writing only through the access point. Replace
   `<REGION>`, `<ACCOUNT_ID>`, `<EFS_ID>` and `<ACCESS_POINT_ID>` first.

3. **Vault key and log group:** as in [ECS step 3](aws-ecs.md#steps).

4. **Task definition.** Fill the placeholders as in
   [ECS step 4](aws-ecs.md#steps), plus `<EFS_ID>` and `<ACCESS_POINT_ID>`.
   Set `BOTHY_SERVER__PUBLIC_URL` to the HTTPS URL users open and
   `BOTHY_SERVER__TRUSTED_PROXIES` to the ALB's subnets; the values in the
   file are examples. `runtimePlatform` says ARM64 (Graviton); the image is
   multi-arch, so X86_64 works too. 0.25 vCPU and 512 MiB is enough.

5. **ALB.** Internal, in subnets that hold nothing else, with an HTTPS
   listener and an ACM certificate. Target group: type `ip`, HTTP, port 8080,
   health check path `/healthz`, success code `200` (Bothy answers health
   checks whatever their Host header; tested). The task's security group
   allows 8080 from the ALB's security group only.

6. **Service.**

   ```sh
   aws ecs create-service --cluster bothy --service-name bothy --task-definition bothy-fargate \
     --desired-count 1 --launch-type FARGATE \
     --deployment-configuration maximumPercent=100,minimumHealthyPercent=0 \
     --network-configuration 'awsvpcConfiguration={subnets=[subnet-aaaa,subnet-bbbb],securityGroups=[sg-bothy],assignPublicIp=DISABLED}' \
     --load-balancers targetGroupArn=<target group ARN>,containerName=bothy,containerPort=8080
   ```

   Private subnets need a NAT gateway or VPC endpoints to pull from
   `ghcr.io` and reach your model and chat providers.

7. **First-run setup.** Setup is served only to local peers, and nothing
   behind the ALB is one: a forwarded client address never counts as local.
   Reach the task directly instead, once, from an instance in the VPC that
   runs the SSM agent (any small instance with the
   `AmazonSSMManagedInstanceCore` policy, outside the ALB's subnets):

   - In the task's security group, allow 8080 from the instance's security
     group.
   - Add the instance's private address to the `bothy` container's environment
     and redeploy:

     ```json
     {"name": "BOTHY_SERVER__LOCAL_CIDRS", "value": "[\"10.0.1.23/32\"]"}
     ```

   - Forward a local port to the task's private address (`aws ecs describe-tasks`
     shows it):

     ```sh
     aws ssm start-session --target i-0123456789abcdef0 \
       --document-name AWS-StartPortForwardingSessionToRemoteHost \
       --parameters '{"host":["10.0.2.45"],"portNumber":["8080"],"localPortNumber":["8080"]}'
     ```

   - Open `http://127.0.0.1:8080/setup#t=<token>` with the token from the
     logged setup link (`aws logs tail /ecs/bothy --since 15m | grep setup`; the
     link itself uses `server.public_url`).

   Then remove `BOTHY_SERVER__LOCAL_CIDRS` and the security group rule,
   redeploy, and stop the instance.

## Models

The task role reaches Amazon Bedrock with no keys; see [vm.md](vm.md#models)
for the provider settings and model access. Other providers' keys and channel
tokens can stay in Secrets Manager: reference them as `aws-sm:<secret>#<key>`
and allow the task role
[`iam-secrets-policy.json`](../../deploy/aws/iam-secrets-policy.json)
([how](README.md#platform-secrets); environment variables as in
[aws-ecs.md](aws-ecs.md#provider-keys-and-channel-tokens)).

## Backups and upgrades

Turn on AWS Backup for the EFS file system and restore into the access point's
path. Bothy also writes a backup before every database migration. To upgrade,
register a revision with the new digests and run `aws ecs update-service`.

## Not tested here

Run locally on Docker 27.4: the access point's ownership (root 65532, 0700),
the init container with only `CHOWN` writing the KEK and opening `/tmp`, and
the gateway with a read-only root and no `/run/bothy` mount (healthy, vault
open, no `data.permissions` finding). The `httpapi` tests cover setup from a
direct peer in `server.local_cidrs` and its refusal through a trusted proxy.
The task definition passes the AWS CLI's parameter validation. Not run on AWS:
Fargate itself, EFS and SQLite on it, the ALB, the SSM remote-host tunnel and
IAM.
