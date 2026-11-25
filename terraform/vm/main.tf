data "vsphere_datacenter" "dc" {
  name = var.vsphere_datacenter
}

data "vsphere_compute_cluster" "cluster" {
  name          = var.vsphere_cluster
  datacenter_id = data.vsphere_datacenter.dc.id
}

data "vsphere_datastore" "datastore" {
  name          = var.vsphere_datastore
  datacenter_id = data.vsphere_datacenter.dc.id
}

data "vsphere_network" "network" {
  name          = var.vsphere_network
  datacenter_id = data.vsphere_datacenter.dc.id
}

data "vsphere_virtual_machine" "template" {
  name          = var.vsphere_template
  datacenter_id = data.vsphere_datacenter.dc.id
}

data "vsphere_resource_pool" "pool" {
  count         = var.vsphere_resource_pool != "" ? 1 : 0
  name          = var.vsphere_resource_pool
  datacenter_id = data.vsphere_datacenter.dc.id
}

locals {
  resource_pool_id = var.vsphere_resource_pool != "" ? data.vsphere_resource_pool.pool[0].id : data.vsphere_compute_cluster.cluster.resource_pool_id

  fqdn_suffix = var.network_domain != "" ? ".${var.network_domain}" : ""

  component_folder = var.vsphere_folder != "" ? "${var.vsphere_folder}/${var.component}" : var.component

  machines = merge([
    for g in var.groups : {
      for idx, ip in g.ips : "${g.name}-${idx + 1}" => {
        group     = g.name
        index     = idx + 1
        ip        = ip
        name      = "${var.component}-${g.name}-${idx + 1}"
        cpu       = g.cpu
        memory_mb = g.memory_mb
        disk_gb   = g.disk_gb
      }
    }
  ]...)
}

resource "vsphere_folder" "component" {
  path          = local.component_folder
  type          = "vm"
  datacenter_id = data.vsphere_datacenter.dc.id
}

data "cloudinit_config" "vm" {
  for_each = local.machines

  gzip          = true
  base64_encode = true

  part {
    content_type = "text/cloud-config"
    filename     = "cloud-config.yaml"
    content = templatefile("${path.module}/templates/cloud-init.yaml.tpl", {
      hostname           = each.value.name
      fqdn               = "${each.value.name}${local.fqdn_suffix}"
      ip_address         = each.value.ip
      netmask            = var.network_netmask
      gateway            = var.network_gateway
      dns_servers        = join(";", var.network_dns)
      domain             = var.network_domain
      ssh_user           = var.ssh_user
      ssh_authorized_key = var.ssh_authorized_key
      mirror_ca_pem      = var.mirror_ca_pem
      extra_packages     = var.extra_packages
      group              = each.value.group
      component          = var.component
    })
  }
}

resource "vsphere_virtual_machine" "vm" {
  for_each = local.machines

  name             = each.value.name
  resource_pool_id = local.resource_pool_id
  datastore_id     = data.vsphere_datastore.datastore.id
  folder           = vsphere_folder.component.path

  num_cpus = each.value.cpu
  memory   = each.value.memory_mb

  guest_id  = data.vsphere_virtual_machine.template.guest_id
  scsi_type = data.vsphere_virtual_machine.template.scsi_type
  firmware  = "efi"

  network_interface {
    network_id   = data.vsphere_network.network.id
    adapter_type = data.vsphere_virtual_machine.template.network_interface_types[0]
  }

  disk {
    label            = "disk0"
    size             = max(each.value.disk_gb, data.vsphere_virtual_machine.template.disks[0].size)
    thin_provisioned = data.vsphere_virtual_machine.template.disks[0].thin_provisioned
    eagerly_scrub    = data.vsphere_virtual_machine.template.disks[0].eagerly_scrub
  }

  clone {
    template_uuid = data.vsphere_virtual_machine.template.id
  }

  extra_config = {
    "guestinfo.metadata" = base64encode(jsonencode({
      "instance-id"    = each.value.name
      "local-hostname" = each.value.name
    }))
    "guestinfo.metadata.encoding" = "base64"
    "guestinfo.userdata"          = data.cloudinit_config.vm[each.key].rendered
    "guestinfo.userdata.encoding" = "gzip+base64"
  }

  wait_for_guest_net_timeout = 5

  lifecycle {
    ignore_changes = [
      extra_config,
      disk[0].size,
    ]
  }
}
