variable "component" {
  description = "Component name (prefix of the machine names and of the vCenter folder)."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9]([-a-z0-9]*[a-z0-9])?$", var.component))
    error_message = "component must be kebab-case (vCenter machine name and Ansible host name)."
  }
}

variable "groups" {
  description = <<-EOT
    Machine groups, by role. A single group = archetype B; several = C.

    IPs are SUPPLIED, never allocated: the air gap has no DHCP on these
    networks, and a randomly picked IP would make the plan non-reproducible.
    Their COUNT sets the actual group size, rather than a `replicas` field that
    would have to be kept in step with the list.
  EOT
  type = list(object({
    name      = string
    ips       = list(string)
    cpu       = number
    memory_mb = number
    disk_gb   = number
  }))

  validation {
    condition     = length(var.groups) > 0
    error_message = "at least one group is required."
  }

  validation {
    condition     = alltrue([for g in var.groups : length(g.ips) > 0])
    error_message = "each group must carry at least one IP (IPs are supplied, not allocated)."
  }

  validation {
    condition     = length(distinct([for g in var.groups : g.name])) == length(var.groups)
    error_message = "group names must be unique (they become Ansible inventory groups)."
  }

  validation {
    condition     = length(distinct(flatten([for g in var.groups : g.ips]))) == length(flatten([for g in var.groups : g.ips]))
    error_message = "the same IP is assigned to two machines."
  }
}

variable "vsphere_server" { type = string }
variable "vsphere_user" {
  type      = string
  sensitive = true
}
variable "vsphere_password" {
  type      = string
  sensitive = true
}
variable "vsphere_allow_unverified_ssl" {
  type    = bool
  default = false
}
variable "vsphere_datacenter" { type = string }
variable "vsphere_cluster" { type = string }
variable "vsphere_datastore" { type = string }
variable "vsphere_network" { type = string }
variable "vsphere_template" {
  description = "Template to clone. Must ship cloud-init and the VMware datasource."
  type        = string
}
variable "vsphere_folder" {
  type    = string
  default = ""
}
variable "vsphere_resource_pool" {
  type    = string
  default = ""
}

variable "network_gateway" { type = string }
variable "network_netmask" {
  type    = number
  default = 24
}
variable "network_dns" { type = list(string) }
variable "network_domain" {
  type    = string
  default = ""
}

variable "ssh_user" {
  description = "Admin account created on the machines."
  type        = string
  default     = "rondo"
}

variable "ssh_authorized_key" {
  description = <<-EOT
    PUBLIC key authorized for this account.

    A key rather than a password: the install Job is what connects, so the
    secret is non-interactive, and a public key placed by cloud-init exposes
    nothing if the tofu plan is read.
  EOT
  type        = string
  sensitive   = true
}

variable "artifactory_ca_pem" {
  description = "Artifactory CA, placed in the machines' trust store."
  type        = string
  default     = ""
}

variable "extra_packages" {
  description = <<-EOT
    Packages installed by cloud-init, BEFORE any application install.

    Deliberately minimal: enough for Ansible to work (python3) and for the
    artifacts to download (curl, tar). Everything else is the job of the
    component's installer, which knows what it needs.
  EOT
  type        = list(string)
  default     = ["python3", "curl", "tar"]
}
