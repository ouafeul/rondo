#cloud-config
# ASCII only: the MIME part is 7bit and one non-ASCII byte rejects the whole cloud-config.
hostname: ${hostname}
fqdn: ${fqdn}
manage_etc_hosts: true

users:
  - name: ${ssh_user}
    groups: [wheel]
    shell: /bin/bash
    lock_passwd: true
    ssh_authorized_keys:
      - ${ssh_authorized_key}
    sudo: ["ALL=(ALL) NOPASSWD:ALL"]

write_files:
%{ if mirror_ca_pem != "" ~}
  - path: /etc/pki/ca-trust/source/anchors/artifactory-ca.pem
    permissions: "0644"
    content: |
      ${indent(6, mirror_ca_pem)}
%{ endif ~}

  - path: /etc/NetworkManager/system-connections/ens32.nmconnection
    permissions: "0600"
    content: |
      [connection]
      id=ens32
      type=ethernet
      interface-name=ens32
      autoconnect=true

      [ipv4]
      method=manual
      addresses=${ip_address}/${netmask}
      gateway=${gateway}
      dns=${dns_servers}
      %{ if domain != "" ~}
      dns-search=${domain}
      %{ endif ~}

      [ipv6]
      method=disabled

  - path: /etc/rondo-release
    permissions: "0644"
    content: |
      RONDO_COMPONENT=${component}
      RONDO_GROUP=${group}
      RONDO_HOSTNAME=${hostname}

packages:
%{ for pkg in extra_packages ~}
  - ${pkg}
%{ endfor ~}

runcmd:
%{ if mirror_ca_pem != "" ~}
  - update-ca-trust extract
%{ endif ~}
  - nmcli connection reload
  - nmcli connection up ens32 || true
  - mkdir -p /etc/systemd/journald.conf.d
  - printf '[Journal]\nSystemMaxUse=2G\nSystemMaxFileSize=100M\n' > /etc/systemd/journald.conf.d/00-size-limit.conf
  - systemctl restart systemd-journald || true
  - touch /var/lib/rondo-cloud-init-done

final_message: "rondo: pre-stage done for ${hostname} (group ${group}) in $UPTIME s"
