variable "vsphere_server" {
  description = "vCenter address"
  type        = string
}

variable "vsphere_user" {
  description = "vCenter user"
  type        = string
}

variable "vsphere_password" {
  description = "vCenter password"
  type        = string
  sensitive   = true
}

variable "vsphere_allow_unverified_ssl" {
  description = "Allow unverified SSL certificates for vCenter"
  type        = bool
  default     = false
}

variable "vsphere_datacenter" {
  description = "vSphere datacenter name"
  type        = string
}

variable "vsphere_cluster" {
  description = "vSphere cluster name"
  type        = string
}

variable "vsphere_datastore" {
  description = "vSphere datastore name"
  type        = string
}

variable "vsphere_network" {
  description = "vSphere port group / network name"
  type        = string
}

variable "vsphere_template" {
  description = "Name of the RHEL VM template with cloud-init"
  type        = string
}

variable "vsphere_folder" {
  description = "VM folder in vSphere (optional)"
  type        = string
  default     = ""
}

variable "environment" {
  description = "RONDO environment, VMs go under <vsphere_folder>/<environment>"
  type        = string
  default     = ""
}

variable "vsphere_resource_pool" {
  description = "vSphere resource pool (optional, empty = cluster root)"
  type        = string
  default     = ""
}

variable "vm_prefix" {
  description = "VM name prefix"
  type        = string
  default     = "k3s"
}

variable "vm_specs" {
  description = "VM hardware specifications"
  type = object({
    cpu       = number
    memory_mb = number
    disk_gb   = number
  })
  default = {
    cpu       = 4
    memory_mb = 12288
    disk_gb   = 50
  }
}

variable "vms" {
  description = "VMs to create, with their network settings"
  type = list(object({
    name = string
    ip   = string
  }))
  validation {
    condition     = length(var.vms) == 3
    error_message = "Exactly 3 VMs are required for the HA k3s cluster."
  }
}

variable "network_gateway" {
  description = "Network gateway"
  type        = string
}

variable "network_netmask" {
  description = "Subnet mask length (e.g. 24)"
  type        = number
  default     = 24
}

variable "network_dns" {
  description = "DNS servers"
  type        = list(string)
}

variable "network_domain" {
  description = "DNS domain"
  type        = string
  default     = ""
}

variable "ssh_user" {
  description = "SSH user on the VMs"
  type        = string
}

variable "ssh_password" {
  description = "SSH password"
  type        = string
  sensitive   = true
}

variable "mirror_files_url" {
  description = "Root of RONDO's files on the mirror's file server (e.g. https://files.example.local/rondo); the k3s binary and airgap images are under k3s/<version>/"
  type        = string
  validation {
    condition     = can(regex("^https?://", var.mirror_files_url))
    error_message = "mirror_files_url must start with http:// or https://"
  }
}

variable "mirror_registry" {
  description = "OCI registry of the mirror, host[:port]"
  type        = string
}

variable "mirror_registry_rewrite" {
  description = "Repository of the mirror's images: a public image (docker.io/rancher/x) is pulled as <mirror_registry>/<mirror_registry_rewrite>/rancher/x. Empty, the nodes keep pulling public images from their own registries."
  type        = string
  default     = ""
}

variable "mirror_upstream_registries" {
  description = "Public registries whose images the nodes pull from the mirror, with mirror_registry_rewrite"
  type        = list(string)
  default     = ["docker.io", "registry.k8s.io", "quay.io", "ghcr.io", "gcr.io"]
}

variable "mirror_username" {
  description = "Username on the mirror; empty, it is read anonymously"
  type        = string
  default     = ""
}

variable "mirror_password" {
  description = "Password on the mirror; empty with mirror_username"
  type        = string
  sensitive   = true
  default     = ""
}

variable "mirror_ca" {
  description = "CA bundle (PEM) of the mirror; empty, the nodes trust it through their system store"
  type        = string
  default     = ""
}

variable "k3s_version" {
  description = "k3s version to install"
  type        = string
}

variable "k3s_token" {
  description = "Pre-shared token for the k3s cluster"
  type        = string
  sensitive   = true
}

variable "ingress_ip" {
  description = "Single IP announced by MetalLB for the Traefik Service (entry point of every Ingress)"
  type        = string
  validation {
    condition     = can(regex("^[0-9]+\\.[0-9]+\\.[0-9]+\\.[0-9]+$", var.ingress_ip))
    error_message = "ingress_ip must be a valid IPv4 address."
  }
}
