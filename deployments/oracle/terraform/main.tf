data "oci_identity_availability_domains" "available" {
  compartment_id = var.compartment_ocid
}

data "oci_core_images" "ubuntu_arm" {
  compartment_id           = var.compartment_ocid
  operating_system         = "Canonical Ubuntu"
  operating_system_version = "24.04"
  shape                    = "VM.Standard.A1.Flex"
  sort_by                  = "TIMECREATED"
  sort_order               = "DESC"
}

resource "oci_core_vcn" "bot" {
  compartment_id = var.compartment_ocid
  display_name   = "discord-music-bot-vcn"
  cidr_blocks    = ["10.40.0.0/16"]
  dns_label      = "discordbot"
}

resource "oci_core_internet_gateway" "bot" {
  compartment_id = var.compartment_ocid
  vcn_id         = oci_core_vcn.bot.id
  display_name   = "discord-music-bot-internet-gateway"
  enabled        = true
}

resource "oci_core_route_table" "public" {
  compartment_id = var.compartment_ocid
  vcn_id         = oci_core_vcn.bot.id
  display_name   = "discord-music-bot-public-routes"

  route_rules {
    destination       = "0.0.0.0/0"
    destination_type  = "CIDR_BLOCK"
    network_entity_id = oci_core_internet_gateway.bot.id
  }
}

resource "oci_core_security_list" "bot" {
  compartment_id = var.compartment_ocid
  vcn_id         = oci_core_vcn.bot.id
  display_name   = "discord-music-bot-security"

  egress_security_rules {
    destination      = "0.0.0.0/0"
    destination_type = "CIDR_BLOCK"
    protocol         = "all"
    stateless        = false
  }

  dynamic "ingress_security_rules" {
    for_each = var.ssh_ingress_cidr == "" ? [] : [var.ssh_ingress_cidr]

    content {
      source      = ingress_security_rules.value
      source_type = "CIDR_BLOCK"
      protocol    = "6"
      stateless   = false

      tcp_options {
        min = 22
        max = 22
      }
    }
  }
}

resource "oci_core_subnet" "public" {
  compartment_id             = var.compartment_ocid
  vcn_id                     = oci_core_vcn.bot.id
  display_name               = "discord-music-bot-public-subnet"
  cidr_block                 = "10.40.1.0/24"
  route_table_id             = oci_core_route_table.public.id
  security_list_ids          = [oci_core_security_list.bot.id]
  prohibit_public_ip_on_vnic = false
  prohibit_internet_ingress  = false
  dns_label                  = "bot"
}

resource "oci_core_instance" "bot" {
  compartment_id      = var.compartment_ocid
  availability_domain = data.oci_identity_availability_domains.available.availability_domains[0].name
  display_name        = "discord-music-bot"
  shape               = "VM.Standard.A1.Flex"

  shape_config {
    ocpus         = 2
    memory_in_gbs = 12
  }

  source_details {
    source_type             = "image"
    source_id               = data.oci_core_images.ubuntu_arm.images[0].id
    boot_volume_size_in_gbs = var.boot_volume_size_gbs
  }

  create_vnic_details {
    subnet_id        = oci_core_subnet.public.id
    assign_public_ip = true
    hostname_label   = "discordbot"
  }

  metadata = {
    ssh_authorized_keys = trimspace(var.ssh_public_key)
    user_data = base64encode(templatefile("${path.module}/cloud_init.sh.tftpl", {
      container_image = var.container_image
    }))
  }
}
