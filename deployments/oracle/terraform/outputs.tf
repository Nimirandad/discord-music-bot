output "instance_public_ip" {
  description = "Public IPv4 address of the bot instance."
  value       = oci_core_instance.bot.public_ip
}

output "ssh_command" {
  description = "SSH command template; SSH ingress must be enabled for your IP."
  value       = "ssh ubuntu@${oci_core_instance.bot.public_ip}"
}

output "container_image" {
  description = "ARM64 container image configured on the instance."
  value       = var.container_image
}
