output "vm_ips" {
  description = "IP addresses of the k3s VMs"
  value = {
    for vm in var.vms : vm.name => vm.ip
  }
}

output "server1_ip" {
  description = "IP of the first k3s server (cluster-init)"
  value       = var.vms[0].ip
}

output "kubeconfig_hint" {
  description = "Command to fetch the kubeconfig"
  value       = "sshpass -p <password> scp ${var.ssh_user}@${var.vms[0].ip}:/etc/rancher/k3s/k3s.yaml ./kubeconfig.yaml"
}
