terraform {
  required_version = ">= 1.10.0"

  backend "http" {}

  required_providers {
    rancher2 = {
      source  = "rancher/rancher2"
      version = "~> 15.3"
    }
    vsphere = {
      source  = "hashicorp/vsphere"
      version = "~> 2.0"
    }
  }
}
