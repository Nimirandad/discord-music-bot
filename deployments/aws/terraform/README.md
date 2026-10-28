# AWS ECS Fargate

Terraform deploys a single Discord bot task on ECS Fargate ARM64. It creates one VPC, one public subnet, an Internet Gateway, an ECR repository, an ECS cluster/service/task definition, a CloudWatch log group, and narrowly scoped task execution permissions.

## Cost-conscious choices

- One 0.25 vCPU / 0.5 GiB ARM64 Fargate task, always running.
- One public subnet and task public IP for outbound connections; no NAT Gateway, load balancer, or inbound security-group rules.
- No EC2 hosts to patch or keep running.
- Seven-day CloudWatch log retention and an ECR lifecycle rule that retains three images.
- Standard Fargate rather than Spot: Spot may interrupt the bot's voice sessions.

There is no guaranteed free tier for this deployment. Fargate runtime, the public IPv4 address, ECR storage, CloudWatch Logs, and internet data transfer can incur charges. Audio streaming can increase outbound data costs. Review AWS pricing for the selected region and configure a billing budget before applying. The public IPv4 address is required by this no-NAT design and is billed separately.

## 1. Prerequisites

- Terraform 1.5 or later.
- AWS CLI v2 configured with credentials able to create VPC, IAM, ECR, ECS, and CloudWatch resources.
- Docker with Buildx enabled.
- Discord bot token.
- Spotify credentials are optional.

Use an AWS profile with least-privilege access for the deployment. Terraform's state contains infrastructure metadata, so store it in an encrypted, access-controlled backend before using this outside a personal test account. Do not commit state files or secret values.

## 2. Create SSM SecureString parameters

Create these parameters in **AWS Systems Manager → Parameter Store**, with type **SecureString** and the AWS-managed `alias/aws/ssm` key:

| Name | Value |
|---|---|
| `/discord-music-bot/discord-token` | Discord bot token |

Spotify is optional. To enable it, also create SecureString parameters:

| Name | Value |
|---|---|
| `/discord-music-bot/spotify-client-id` | Spotify client ID |
| `/discord-music-bot/spotify-client-secret` | Spotify client secret |

The values are not Terraform variables and are not written to Terraform state. The ECS execution role can read only the parameter ARNs passed to the task. If you choose a customer-managed KMS key instead of `alias/aws/ssm`, grant that key's decrypt permission to the ECS task execution role.

If using different parameter names, set the corresponding Terraform variables. Leave both Spotify parameter-name variables empty to run without Spotify.

## 3. Provision AWS resources

From this directory:

```powershell
terraform init
terraform plan -out=tfplan -var="desired_count=0"
terraform apply tfplan
```

The initial apply creates the ECR repository and ECS infrastructure without starting a task before its image exists. Get the account ID and authenticate Docker:

```powershell
$region = terraform output -raw aws_region
$accountId = aws sts get-caller-identity --query Account --output text
$repository = terraform output -raw ecr_repository_url
aws ecr get-login-password --region $region |
  docker login --username AWS --password-stdin "$accountId.dkr.ecr.$region.amazonaws.com"
```

The default region/zone pair is `us-east-1` / `us-east-1a`. When changing `aws_region`, also pass a valid `availability_zone` for that region (for example, with `-var="availability_zone=us-west-2a"`). No availability-zone discovery permission is required.

## 4. Build and deploy the ARM64 image

Use a unique image tag for each release (for example, a Git commit or release number):

```powershell
$tag = "1.0.0"
docker buildx build --platform linux/arm64 --push -t "${repository}:$tag" .
terraform apply -var "desired_count=1" -var "image_tag=$tag"
```

Run the build command from the repository root; set `repository` to the Terraform output as shown above. If `buildx` is not initialized, run `docker buildx create --use` once.

Check ECS status and logs:

```powershell
$cluster = terraform output -raw ecs_cluster_name
$service = terraform output -raw ecs_service_name
aws ecs describe-services --cluster $cluster --services $service
aws logs tail (terraform output -raw cloudwatch_log_group) --follow --region $region
```

The service has exactly one desired task. Deployment settings stop the old task before starting its replacement, keeping replica count and cost down at the expense of a short interruption during updates.

## Secret rotation

Updating a Parameter Store value does not change environment variables of an already-running ECS task. After rotating a credential, update its SecureString parameter and force the service to launch a replacement task:

```powershell
aws ecs update-service `
  --cluster (terraform output -raw ecs_cluster_name) `
  --service (terraform output -raw ecs_service_name) `
  --force-new-deployment `
  --region (terraform output -raw aws_region)
```

This reads the latest parameter values at task startup. Spotify does not provide an API for generating a replacement client secret; secret rotation in its dashboard and updating Parameter Store remain separate actions.

## Networking and health

The bot needs outbound HTTPS to Discord/Spotify/YouTube and outbound UDP for Discord voice. Its security group has no ingress rules. The container health check calls `/healthz` inside the task; no public HTTP endpoint, ALB, or ECS load balancer is provisioned.

## Cleanup

To stop billing and remove resources created by this stack:

```powershell
terraform destroy
```

ECR images may prevent repository deletion. If destroy reports that the repository is not empty, delete its images in ECR and run `terraform destroy` again. The SSM SecureString parameters are intentionally created outside Terraform and are not removed by this command.
