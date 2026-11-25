locals {
  fqdn_suffix = var.network_domain != "" ? ".${var.network_domain}" : ""

  vm_folder = var.vsphere_folder != "" ? "${var.vsphere_folder}/${var.rke2_cluster_name}" : var.rke2_cluster_name

  rancher_host = regex("^https?://([^/]+)", var.rancher_api_url)[0]

  master_nodes = {
    for idx, ip in var.rke2_master_ips :
    "master-${idx + 1}" => {
      name = "${var.rke2_cluster_name}-master-${idx + 1}"
      ip   = ip
    }
  }
  worker_nodes = {
    for idx, ip in var.rke2_worker_ips :
    "worker-${idx + 1}" => {
      name = "${var.rke2_cluster_name}-worker-${idx + 1}"
      ip   = ip
    }
  }

  mirror_ca = var.mirror_ca_pem != "" ? var.mirror_ca_pem : (
    var.mirror_ca_cert_path != "" ? file(var.mirror_ca_cert_path) : ""
  )

  # Registry mirrors with rewrites: an image asked of the registry, or of a public registry, as
  # <path> is pulled as <rke2_registry_host>/<mirror_registry_rewrite>/<path>. A path already under
  # the repository is left as is.
  rewrite_repo = replace(var.mirror_registry_rewrite, ".", "\\.")
  registry_mirrors = {
    for host, rewrites in merge(
      { for upstream in var.mirror_upstream_registries : upstream => { "^(.*)$" = "${var.mirror_registry_rewrite}/$1" } },
      { (var.rke2_registry_host) = { "^(${local.rewrite_repo}/)?(.*)$" = "${var.mirror_registry_rewrite}/$2" } },
    ) : host => rewrites if var.mirror_registry_rewrite != ""
  }

  cloud_config_common = {
    cluster_ca    = var.cluster_ca_pem != "" ? var.cluster_ca_pem : file(var.cluster_ca_cert_path)
    mirror_ca     = local.mirror_ca
    netmask       = var.network_netmask
    gateway       = var.network_gateway
    dns_servers   = join(";", var.network_dns)
    domain        = var.network_domain
    ingress_ip    = var.rancher_ingress_ip
    rancher_host  = local.rancher_host
    registry_host = var.rke2_registry_host
    registry_ip   = var.rke2_registry_ip
  }
}

data "vsphere_datacenter" "dc" {
  name = var.vsphere_datacenter
}

resource "vsphere_folder" "cluster" {
  path          = local.vm_folder
  type          = "vm"
  datacenter_id = data.vsphere_datacenter.dc.id
}

resource "rancher2_cloud_credential" "vsphere" {
  name        = "${var.rke2_cluster_name}-vsphere-creds"
  description = "vSphere credentials for RKE2 downstream cluster ${var.rke2_cluster_name}"

  vsphere_credential_config {
    vcenter      = var.vsphere_server
    username     = var.vsphere_user
    password     = var.vsphere_password
    vcenter_port = "443"
  }
}

resource "rancher2_machine_config_v2" "master" {
  for_each = local.master_nodes

  generate_name = each.key

  vsphere_config {
    cpu_count     = tostring(var.rke2_master_cpu)
    memory_size   = tostring(var.rke2_master_memory_mb)
    disk_size     = tostring(var.rke2_master_disk_gb * 1024)
    vcenter       = var.vsphere_server
    datacenter    = var.vsphere_datacenter
    datastore     = var.vsphere_datastore
    network       = [var.vsphere_network]
    pool          = var.vsphere_resource_pool
    folder        = vsphere_folder.cluster.path
    clone_from    = var.vsphere_template
    creation_type = "template"
    cfgparam      = ["disk.enableUUID=TRUE"]
    cloud_config = templatefile("${path.module}/templates/cloud-config.yaml.tpl", merge(local.cloud_config_common, {
      hostname   = each.value.name
      fqdn       = "${each.value.name}${local.fqdn_suffix}"
      ip_address = each.value.ip
    }))
    os = "linux"
  }
}

resource "rancher2_machine_config_v2" "worker" {
  for_each = local.worker_nodes

  generate_name = each.key

  vsphere_config {
    cpu_count     = tostring(var.rke2_worker_cpu)
    memory_size   = tostring(var.rke2_worker_memory_mb)
    disk_size     = tostring(var.rke2_worker_disk_gb * 1024)
    vcenter       = var.vsphere_server
    datacenter    = var.vsphere_datacenter
    datastore     = var.vsphere_datastore
    network       = [var.vsphere_network]
    pool          = var.vsphere_resource_pool
    folder        = vsphere_folder.cluster.path
    clone_from    = var.vsphere_template
    creation_type = "template"
    cfgparam      = ["disk.enableUUID=TRUE"]
    cloud_config = templatefile("${path.module}/templates/cloud-config.yaml.tpl", merge(local.cloud_config_common, {
      hostname   = each.value.name
      fqdn       = "${each.value.name}${local.fqdn_suffix}"
      ip_address = each.value.ip
    }))
    os = "linux"
  }
}

resource "rancher2_cluster_v2" "rke2_downstream" {
  name               = var.rke2_cluster_name
  kubernetes_version = var.rke2_k8s_version

  rke_config {
    machine_global_config = yamlencode({
      "system-default-registry" = var.rke2_registry_host
      "cni"                     = var.rke2_cni
      "write-kubeconfig-mode"   = "0644"
    })

    dynamic "registries" {
      for_each = var.rke2_registry_auth || length(local.registry_mirrors) > 0 ? [1] : []
      content {
        dynamic "configs" {
          for_each = var.rke2_registry_auth ? [var.rke2_registry_host] : []
          content {
            hostname                = configs.value
            auth_config_secret_name = "regcred"
          }
        }
        dynamic "mirrors" {
          for_each = local.registry_mirrors
          content {
            hostname  = mirrors.key
            endpoints = ["https://${var.rke2_registry_host}"]
            rewrites  = mirrors.value
          }
        }
      }
    }

    dynamic "machine_pools" {
      for_each = rancher2_machine_config_v2.master
      content {
        name                         = machine_pools.key
        cloud_credential_secret_name = rancher2_cloud_credential.vsphere.id
        control_plane_role           = true
        etcd_role                    = true
        worker_role                  = false
        quantity                     = 1

        machine_config {
          kind = machine_pools.value.kind
          name = machine_pools.value.name
        }
      }
    }

    dynamic "machine_pools" {
      for_each = rancher2_machine_config_v2.worker
      content {
        name                         = machine_pools.key
        cloud_credential_secret_name = rancher2_cloud_credential.vsphere.id
        control_plane_role           = false
        etcd_role                    = false
        worker_role                  = true
        quantity                     = 1

        machine_config {
          kind = machine_pools.value.kind
          name = machine_pools.value.name
        }
      }
    }

  }
}
