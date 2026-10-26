# Bothy

A small, self-hosted AI agent that lives in your chat apps and is secure by
default. Bring your own API keys or local models.

This repository is how you install Bothy: the signed releases, the compose
file, AWS task definitions and the install script, all for Bothy
0.1.0.

## Run it

You need Docker.

```sh
docker run -d --name bothy --restart unless-stopped \
  -p 127.0.0.1:8080:8080 -v bothy_data:/data \
  ghcr.io/rielicat/bothy:0.1.0
docker logs bothy 2>&1 | grep setup
```

Open the `http://127.0.0.1:8080/setup#t=…` link it prints. Setup walks you
through your account, a model provider, safety settings, a daily spend cap
and your chat apps. The same image is on Docker Hub as
`rielicat/bothy:0.1.0`.

Your data lives in the `bothy_data` volume. To upgrade, remove the container
and run the same command with the new version.

**What works this way:** chat in the web UI, Telegram, Discord and Slack; any
model provider or local model; memory, schedules and approvals; web fetch
and search; files in the workspace; MCP servers over HTTP.

**What does not:** running code. Shell, Python and MCP servers that run as
local programs need the sandbox (next step). Without it those tools are
simply missing, Bothy shows a "No sandbox" banner, and nothing ever runs on
your machine instead.

Local models (Ollama, LM Studio, vLLM) on the same machine are found
automatically. On Linux, add `--add-host host.docker.internal:host-gateway`
to the `docker run` command.

## Run code and tests too

The sandbox is a second container next to Bothy that runs code in throwaway,
locked-down boxes. Docker Compose starts both:

```sh
curl -fsSLO https://raw.githubusercontent.com/rielicat/bothy/main/install.sh
sh install.sh --compose ~/bothy     # checks the release signature with cosign
cd ~/bothy && docker compose up -d
docker compose logs bothy | grep setup
```

Coming from `docker run`? Run `docker rm -f bothy` first: compose uses the
same `bothy_data` volume, so your setup and history stay.

`install.sh` writes the Docker socket settings for your machine into
`~/bothy/.env`. The same `compose.yaml` and `.env.example` are in this
repository if you prefer to set them yourself.

## AWS ECS

The `aws/` folder has ready task definitions that use the public images,
pinned to 0.1.0:

- `aws/ecs-task-definition.json`: Bothy and the sandbox on one EC2 container
  instance. The sandbox needs the EC2 launch type, because Fargate has no
  container runtime it could start boxes with.
- `aws/fargate-task-definition.json`: Bothy alone on Fargate (no sandbox),
  with its data on EFS.

Both fetch the vault key from AWS Secrets Manager with a small AWS CLI init
container. On EC2:

1. **Instance.** An ECS-optimized Amazon Linux 2023 instance in cluster
   `bothy`, IMDSv2 required with hop limit 1. In its user data, join the
   cluster and let the sandbox use Docker:

   ```sh
   #!/bin/bash
   echo ECS_CLUSTER=bothy >> /etc/ecs/ecs.config
   dnf install -y acl
   mkdir -p /etc/systemd/system/docker.service.d
   printf '[Service]\nExecStartPost=/usr/bin/setfacl -m g:65532:rw /var/run/docker.sock\n' \
     > /etc/systemd/system/docker.service.d/bothy-sandboxd.conf
   systemctl daemon-reload && setfacl -m g:65532:rw /var/run/docker.sock
   ```

2. **Fill in the placeholders** `<ACCOUNT_ID>`, `<REGION>` and the AWS CLI
   image in every file under `aws/`:

   ```sh
   ACCOUNT=123456789012 REGION=us-east-1 AWSCLI=2.31.0
   AWSCLI_DIGEST=$(docker buildx imagetools inspect public.ecr.aws/aws-cli/aws-cli:$AWSCLI \
     --format '{{json .Manifest}}' | jq -r .digest)
   sed -i.orig -e "s/<ACCOUNT_ID>/$ACCOUNT/g" -e "s/<REGION>/$REGION/g" \
     -e "s/<AWSCLI_TAG>/$AWSCLI/g" -e "s/sha256:<AWSCLI_DIGEST>/$AWSCLI_DIGEST/g" aws/*.json
   ```

3. **Roles, vault key and log group.**

   ```sh
   aws iam create-role --role-name bothy-task \
     --assume-role-policy-document file://aws/ecs-task-role-trust-policy.json
   for p in bedrock kek-secret secrets; do
     aws iam put-role-policy --role-name bothy-task --policy-name $p \
       --policy-document file://aws/iam-$p-policy.json
   done
   aws iam create-role --role-name bothy-task-execution \
     --assume-role-policy-document file://aws/ecs-task-role-trust-policy.json
   aws iam attach-role-policy --role-name bothy-task-execution \
     --policy-arn arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy

   (umask 077 && openssl rand -hex 32 > bothy_kek)
   aws secretsmanager create-secret --name bothy/kek --secret-string file://bothy_kek
   shred -u bothy_kek
   aws logs create-log-group --log-group-name /ecs/bothy
   ```

4. **Register and run.** One task at a time (one database, one host port):

   ```sh
   aws ecs register-task-definition --cli-input-json file://aws/ecs-task-definition.json
   aws ecs create-service --cluster bothy --service-name bothy --task-definition bothy \
     --desired-count 1 --launch-type EC2 \
     --deployment-configuration maximumPercent=100,minimumHealthyPercent=0
   ```

5. **Setup.** No inbound rule is needed: tunnel to the instance with SSM and
   open the setup link from the logs.

   ```sh
   aws ssm start-session --target <instance id> \
     --document-name AWS-StartPortForwardingSession \
     --parameters '{"portNumber":["8080"],"localPortNumber":["8080"]}'
   aws logs tail /ecs/bothy --since 15m | grep setup
   ```

**Fargate** takes the same roles, key and log group, plus an EFS file system
with an access point for uid 65532 (`aws/iam-efs-policy.json` on the task
role) and an internal HTTPS load balancer. Fill in `<EFS_ID>`,
`<ACCESS_POINT_ID>`, `BOTHY_SERVER__PUBLIC_URL` and
`BOTHY_SERVER__TRUSTED_PROXIES` in `aws/fargate-task-definition.json`,
register it and create the service with `--launch-type FARGATE`.

The images are tagged by version and pinned by digest in these files. Keep
the digest when you change versions: a tag can be moved, a digest cannot.

## Other ways to install

- **Native binary** (Linux, macOS, Windows): `sh install.sh` puts `bothy` in
  `~/.local/bin`; then `BOTHY_STORAGE__DATA_DIR=~/bothy-data bothy serve`.
- **Homebrew** (macOS, Linux): `brew install --cask rielicat/tap/bothy`.
- **Archives** for every platform are on the
  [releases page](https://github.com/rielicat/bothy/releases).

## Verifying a release

Every release is signed with Sigstore by Bothy's release workflow, and every
image carries a cosign signature and an SBOM. The workflow lives in Bothy's
private source repository, rielicat/bothycore, which the signing identity
names. `install.sh` checks the signature for you. By hand:

```sh
cosign verify ghcr.io/rielicat/bothy:0.1.0 \
  --certificate-identity https://github.com/rielicat/bothycore/.github/workflows/release.yml@refs/tags/v0.1.0 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

See [SECURITY.md](SECURITY.md) for more, and to report a vulnerability.

## License

See [LICENSE](LICENSE).
