terraform {
  required_version = ">= 1.11.0"

  backend "http" {}

  required_providers {
    vsphere = {
      source  = "hashicorp/vsphere"
      version = ">= 2.9.0"
    }
    cloudinit = {
      source  = "hashicorp/cloudinit"
      version = ">= 2.3.0"
    }
  }
}
