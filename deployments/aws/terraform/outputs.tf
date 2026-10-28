output "aws_region" {
  description = "AWS region where the bot was deployed."
  value       = var.aws_region
}

output "ecr_repository_url" {
  description = "Push the multi-platform bot image to this ECR repository."
  value       = aws_ecr_repository.bot.repository_url
}

output "ecs_cluster_name" {
  description = "ECS cluster containing the bot service."
  value       = aws_ecs_cluster.bot.name
}

output "ecs_service_name" {
  description = "Single-task ECS service name."
  value       = aws_ecs_service.bot.name
}

output "cloudwatch_log_group" {
  description = "CloudWatch Logs group for the bot."
  value       = aws_cloudwatch_log_group.bot.name
}
