data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}

locals {
  configured_secrets = {
    DISCORD_TOKEN         = var.discord_token_parameter_name
    SPOTIFY_CLIENT_ID     = var.spotify_client_id_parameter_name
    SPOTIFY_CLIENT_SECRET = var.spotify_client_secret_parameter_name
  }

  secrets = {
    for environment_name, parameter_name in local.configured_secrets :
    environment_name => "arn:${data.aws_partition.current.partition}:ssm:${var.aws_region}:${data.aws_caller_identity.current.account_id}:parameter${parameter_name}"
    if parameter_name != ""
  }

  image_uri = "${aws_ecr_repository.bot.repository_url}:${var.image_tag}"
}

resource "aws_vpc" "bot" {
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = {
    Name = var.name
  }
}

resource "aws_internet_gateway" "bot" {
  vpc_id = aws_vpc.bot.id

  tags = {
    Name = var.name
  }
}

resource "aws_subnet" "public" {
  vpc_id                  = aws_vpc.bot.id
  cidr_block              = var.public_subnet_cidr
  availability_zone       = var.availability_zone
  map_public_ip_on_launch = true

  tags = {
    Name = "${var.name}-public"
  }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.bot.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.bot.id
  }

  tags = {
    Name = "${var.name}-public"
  }
}

resource "aws_route_table_association" "public" {
  subnet_id      = aws_subnet.public.id
  route_table_id = aws_route_table.public.id
}

resource "aws_security_group" "task" {
  name        = "${var.name}-task"
  description = "No inbound access; the Discord bot only initiates outbound connections."
  vpc_id      = aws_vpc.bot.id

  egress {
    description = "Discord Gateway, voice UDP, Spotify, YouTube, ECR, SSM, and CloudWatch Logs"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name = "${var.name}-task"
  }
}

resource "aws_ecr_repository" "bot" {
  name                 = var.name
  image_tag_mutability = "MUTABLE"

  image_scanning_configuration {
    scan_on_push = false
  }

  tags = {
    Name = var.name
  }
}

resource "aws_ecr_lifecycle_policy" "bot" {
  repository = aws_ecr_repository.bot.name

  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep the newest three tagged images."
      selection = {
        tagStatus      = "tagged"
        tagPatternList = ["*"]
        countType      = "imageCountMoreThan"
        countNumber    = 3
      }
      action = {
        type = "expire"
      }
    }]
  })
}

resource "aws_cloudwatch_log_group" "bot" {
  name              = "/ecs/${var.name}"
  retention_in_days = var.log_retention_days

  tags = {
    Name = var.name
  }
}

resource "aws_iam_role" "execution" {
  name = "${var.name}-execution"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Principal = {
        Service = "ecs-tasks.amazonaws.com"
      }
      Action = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "execution" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

resource "aws_iam_role_policy" "read_parameters" {
  name = "${var.name}-read-parameters"
  role = aws_iam_role.execution.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["ssm:GetParameters"]
      Resource = values(local.secrets)
    }]
  })
}

resource "aws_ecs_cluster" "bot" {
  name = var.name

  tags = {
    Name = var.name
  }
}

resource "aws_ecs_task_definition" "bot" {
  family                   = var.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = "256"
  memory                   = "512"
  execution_role_arn       = aws_iam_role.execution.arn
  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "ARM64"
  }

  container_definitions = jsonencode([{
    name      = var.name
    image     = local.image_uri
    essential = true
    environment = [
      { name = "HTTP_ADDR", value = ":8080" },
      { name = "AUTO_LEAVE_SECONDS", value = tostring(var.auto_leave_seconds) },
    ]
    secrets = [
      for environment_name, parameter_arn in local.secrets : {
        name      = environment_name
        valueFrom = parameter_arn
      }
    ]
    healthCheck = {
      command = [
        "CMD-SHELL",
        "python3 -c \"import urllib.request; urllib.request.urlopen('http://127.0.0.1:8080/healthz', timeout=3)\" || exit 1"
      ]
      interval    = 30
      timeout     = 5
      retries     = 3
      startPeriod = 60
    }
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        "awslogs-group"         = aws_cloudwatch_log_group.bot.name
        "awslogs-region"        = var.aws_region
        "awslogs-stream-prefix" = "bot"
      }
    }
  }])

  depends_on = [
    aws_iam_role_policy_attachment.execution,
    aws_iam_role_policy.read_parameters,
  ]

  tags = {
    Name = var.name
  }
}

resource "aws_ecs_service" "bot" {
  name                               = var.name
  cluster                            = aws_ecs_cluster.bot.id
  task_definition                    = aws_ecs_task_definition.bot.arn
  desired_count                      = var.desired_count
  launch_type                        = "FARGATE"
  platform_version                   = "LATEST"
  deployment_minimum_healthy_percent = 0
  deployment_maximum_percent         = 100
  health_check_grace_period_seconds  = 90

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = [aws_subnet.public.id]
    security_groups  = [aws_security_group.task.id]
    assign_public_ip = true
  }

  depends_on = [
    aws_route_table_association.public,
    aws_iam_role_policy_attachment.execution,
    aws_iam_role_policy.read_parameters,
  ]

  tags = {
    Name = var.name
  }
}
