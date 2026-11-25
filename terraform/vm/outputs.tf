output "ansible_inventory" {
  description = <<-EOT
    Ansible inventory in INI format, one group per role.

    Each host carries the variables the playbook needs: its group, its IP, its
    rank in the group. `rondo_group_index == 1` marks the FIRST host of a group,
    which lets a playbook run non-repeatable actions only once (initialize a
    database, apply a schema) without the component having to invent an
    election mechanism.
  EOT
  value = join("\n", concat(
    [for g in var.groups : join("\n", concat(
      ["[${g.name}]"],
      [for k, m in local.machines : format(
        "%s ansible_host=%s rondo_group=%s rondo_group_index=%d rondo_group_size=%d",
        m.name, m.ip, m.group, m.index, length(g.ips),
      ) if m.group == g.name],
      [""],
    ))],
    [
      "[all:vars]",
      "ansible_user=${var.ssh_user}",
      "ansible_python_interpreter=/usr/bin/python3",
      "rondo_component=${var.component}",
      "",
      "[rondo_machines:children]",
      join("\n", [for g in var.groups : g.name]),
      "",
    ],
  ))
}

output "machines" {
  description = "Details of the created machines, by stable key `<group>-<index>`."
  value = {
    for k, m in local.machines : k => {
      name  = m.name
      group = m.group
      ip    = m.ip
      fqdn  = "${m.name}${local.fqdn_suffix}"
    }
  }
}

output "ips_by_group" {
  description = "IPs by group, which a component consumes for its own settings (trusted domains, cluster peers)."
  value       = { for g in var.groups : g.name => g.ips }
}

output "component" {
  description = "Component name, reported in the Enrollment status."
  value       = var.component
}

output "machine_count" {
  description = "Total machine count. A plan that makes it DROP destroys VMs; that is what plan approval is there to catch."
  value       = length(local.machines)
}
