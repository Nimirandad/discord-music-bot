variable "aws_region" {
  description = "AWS region in which to deploy the bot."
  type        = string
  default     = "us-east-1"
}

variable "availability_zone" {
  description = "One availability zone in aws_region. Set this explicitly when changing regions."
  type        = string
  default     = "us-east-1a"
}

variable "name" {
  description = "Prefix used for AWS resource names."
  type        = string
  default     = "discord-music-bot"
}

variable "vpc_cidr" {
  description = "IPv4 range for the dedicated VPC."
  type        = string
  default     = "10.40.0.0/16"
}

variable "public_subnet_cidr" {
  description = "IPv4 range for the single public subnet."
  type        = string
  default     = "10.40.1.0/24"
}

variable "discord_token_parameter_name" {
  description = "Existing SSM SecureString parameter containing the Discord bot token."
  type        = string
  default     = "/discord-music-bot/discord-token"
}

variable "spotify_client_id_parameter_name" {
  description = "Optional existing SSM SecureString parameter containing the Spotify client ID."
  type        = string
  default     = ""
}

variable "spotify_client_secret_parameter_name" {
  description = "Optional existing SSM SecureString parameter containing the Spotify client secret."
  type        = string
  default     = ""
}

variable "image_tag" {
  description = "Version tag of the bot image already pushed to this stack's ECR repository."
  type        = string
  default     = "latest"
}

variable "desired_count" {
  description = "Number of running bot tasks. Set to zero only during initial infrastructure provisioning."
  type        = number
  default     = 1

  validation {
    condition     = var.desired_count == 0 || var.desired_count == 1
    error_message = "desired_count must be 0 during bootstrap or 1 for normal operation."
  }
}

variable "auto_leave_seconds" {
  description = "Seconds the bot waits before leaving an empty voice channel."
  type        = number
  default     = 180

  validation {
    condition     = var.auto_leave_seconds >= 1 && floor(var.auto_leave_seconds) == var.auto_leave_seconds
    error_message = "auto_leave_seconds must be a positive integer."
  }
}

variable "log_retention_days" {
  description = "CloudWatch log retention period."
  type        = number
  default     = 7

  validation {
    condition     = contains([1, 3, 5, 7, 14, 30], var.log_retention_days)
    error_message = "log_retention_days must be one of 1, 3, 5, 7, 14, or 30."
  }
}
