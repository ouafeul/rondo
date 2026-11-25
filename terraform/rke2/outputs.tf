output "cluster_name" {
  description = "Name of the downstream RKE2 cluster in Rancher"
  value       = rancher2_cluster_v2.rke2_downstream.name
}

output "cluster_id" {
  description = "Cluster ID (v3 management format)"
  value       = rancher2_cluster_v2.rke2_downstream.cluster_v1_id
}

output "kubeconfig" {
  description = "Kubeconfig of the downstream RKE2 cluster (save it as kubeconfig-downstream.yaml)"
  value       = rancher2_cluster_v2.rke2_downstream.kube_config
  sensitive   = true
}

output "api_endpoint" {
  description = "Cluster API URL, taken from the kubeconfig (so consistent with it)"
  value       = try(yamldecode(rancher2_cluster_v2.rke2_downstream.kube_config).clusters[0].cluster.server, "")
  sensitive   = true
}
