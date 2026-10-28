variable "region" {
  description = "OCI home region for this tenancy."
  type        = string
}

variable "compartment_ocid" {
  description = "OCID of the compartment where the bot infrastructure will be created."
  type        = string
}

variable "ssh_public_key" {
  description = "OpenSSH public key installed for the ubuntu user."
  type        = string

  validation {
    condition     = can(regex("^ssh-(rsa|ed25519) ", trimspace(var.ssh_public_key)))
    error_message = "ssh_public_key must be an OpenSSH RSA or Ed25519 public key."
  }
}

variable "ssh_ingress_cidr" {
  description = "Optional trusted IPv4 CIDR allowed to SSH to the instance. Leave empty to disable SSH ingress."
  type        = string
  default     = ""

  validation {
    condition     = var.ssh_ingress_cidr == "" || can(cidrnetmask(var.ssh_ingress_cidr))
    error_message = "ssh_ingress_cidr must be empty or a valid IPv4 CIDR block."
  }
}

variable "container_image" {
  description = "Public ARM64 image reference, for example ghcr.io/owner/discord-music-bot:1.0.0."
  type        = string

  validation {
    condition     = can(regex("^[A-Za-z0-9][A-Za-z0-9._:/@-]*$", var.container_image))
    error_message = "container_image must be a valid public container image reference without whitespace."
  }
}

variable "boot_volume_size_gbs" {
  description = "Boot volume size in GB; the A1 Always Free minimum is 47 GB."
  type        = number
  default     = 50

  validation {
    condition     = var.boot_volume_size_gbs >= 47 && var.boot_volume_size_gbs <= 200 && floor(var.boot_volume_size_gbs) == var.boot_volume_size_gbs
    error_message = "boot_volume_size_gbs must be a whole number between 47 and 200."
  }
}
