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

  artifactory_host = regex("https?://([^/]+)", var.artifactory_url)[0]

  fqdn_suffix = var.network_domain != "" ? ".${var.network_domain}" : ""

  env_folder  = join("/", compact([var.vsphere_folder, var.environment]))
  mgmt_folder = local.env_folder != "" ? "${local.env_folder}/MANAGEMENT" : "MANAGEMENT"
}

resource "vsphere_folder" "environment" {
  count         = var.environment != "" ? 1 : 0
  path          = local.env_folder
  type          = "vm"
  datacenter_id = data.vsphere_datacenter.dc.id
}

resource "vsphere_folder" "management" {
  path          = local.mgmt_folder
  type          = "vm"
  datacenter_id = data.vsphere_datacenter.dc.id
  depends_on    = [vsphere_folder.environment]
}

data "cloudinit_config" "vm" {
  count         = length(var.vms)
  gzip          = true
  base64_encode = true

  part {
    content_type = "text/cloud-config"
    content = templatefile("${path.module}/templates/cloud-init.yaml.tpl", {
      hostname         = var.vms[count.index].name
      fqdn             = "${var.vms[count.index].name}${local.fqdn_suffix}"
      ip_address       = var.vms[count.index].ip
      netmask          = var.network_netmask
      gateway          = var.network_gateway
      dns_servers      = join(";", var.network_dns)
      domain           = var.network_domain
      ca_cert          = var.artifactory_ca_cert
      artifactory_url  = var.artifactory_url
      artifactory_host = local.artifactory_host
      artifactory_user = var.artifactory_user
      artifactory_pass = var.artifactory_pass
      generic_repo     = var.artifactory_generic_repo
      docker_repo      = var.artifactory_docker_repo
      k3s_version      = var.k3s_version
      ingress_ip       = var.ingress_ip
    })
  }
}

resource "vsphere_virtual_machine" "k3s" {
  count = length(var.vms)

  name             = var.vms[count.index].name
  resource_pool_id = local.resource_pool_id
  datastore_id     = data.vsphere_datastore.datastore.id
  folder           = vsphere_folder.management.path

  num_cpus = var.vm_specs.cpu
  memory   = var.vm_specs.memory_mb

  guest_id  = data.vsphere_virtual_machine.template.guest_id
  scsi_type = data.vsphere_virtual_machine.template.scsi_type
  firmware  = "efi"

  network_interface {
    network_id   = data.vsphere_network.network.id
    adapter_type = data.vsphere_virtual_machine.template.network_interface_types[0]
  }

  disk {
    label            = "disk0"
    size             = data.vsphere_virtual_machine.template.disks[0].size
    thin_provisioned = data.vsphere_virtual_machine.template.disks[0].thin_provisioned
    eagerly_scrub    = data.vsphere_virtual_machine.template.disks[0].eagerly_scrub
  }

  clone {
    template_uuid = data.vsphere_virtual_machine.template.id
  }

  extra_config = {
    "guestinfo.metadata" = base64encode(jsonencode({
      "instance-id"    = var.vms[count.index].name
      "local-hostname" = var.vms[count.index].name
    }))
    "guestinfo.metadata.encoding" = "base64"
    "guestinfo.userdata"          = data.cloudinit_config.vm[count.index].rendered
    "guestinfo.userdata.encoding" = "gzip+base64"
  }

  lifecycle {
    ignore_changes = [
      extra_config,
      disk[0].size,
    ]
  }
}
