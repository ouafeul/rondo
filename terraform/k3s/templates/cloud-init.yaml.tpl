#cloud-config

hostname: ${hostname}
fqdn: ${fqdn}
manage_etc_hosts: true

write_files:
  - path: /etc/pki/ca-trust/source/anchors/artifactory-ca.pem
    permissions: "0644"
    content: |
      ${indent(6, ca_cert)}

  - path: /etc/rancher/k3s/registries.yaml
    permissions: "0644"
    content: |
      configs:
        "${artifactory_host}":
          auth:
            username: "${artifactory_user}"
            password: "${artifactory_pass}"
          tls:
            ca_file: /etc/pki/ca-trust/source/anchors/artifactory-ca.pem

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

  - path: /etc/rancher/k3s/config.yaml
    permissions: "0644"
    content: |
      kube-controller-manager-arg:
        - "terminated-pod-gc-threshold=100"
      kubelet-arg:
        - "image-gc-high-threshold=70"
        - "image-gc-low-threshold=50"

  - path: /var/lib/rancher/k3s/server/manifests/traefik-config.yaml
    permissions: "0644"
    content: |
      apiVersion: helm.cattle.io/v1
      kind: HelmChartConfig
      metadata:
        name: traefik
        namespace: kube-system
      spec:
        valuesContent: |-
          service:
            annotations:
              metallb.universe.tf/loadBalancerIPs: ${ingress_ip}

runcmd:
  - update-ca-trust extract

  - nmcli connection reload
  - nmcli connection up ens32 || true

  - mkdir -p /etc/systemd/journald.conf.d
  - printf '[Journal]\nSystemMaxUse=2G\nSystemMaxFileSize=100M\n' > /etc/systemd/journald.conf.d/00-size-limit.conf
  - systemctl restart systemd-journald || true

  - |
    curl -sSf --cacert /etc/pki/ca-trust/source/anchors/artifactory-ca.pem -u "${artifactory_user}:${artifactory_pass}" -o /dev/null "${artifactory_url}/api/system/ping" || { echo "ERROR: Artifactory unreachable"; exit 1; }

  - mkdir -p /usr/local/bin
  - mkdir -p /var/lib/rancher/k3s/agent/images

  - curl -sSfL --cacert /etc/pki/ca-trust/source/anchors/artifactory-ca.pem -u "${artifactory_user}:${artifactory_pass}" -o /usr/local/bin/k3s "${artifactory_url}/${generic_repo}/k3s/${urlencode(k3s_version)}/k3s"
  - chmod 755 /usr/local/bin/k3s
  - |
    test -x /usr/local/bin/k3s || { echo "ERROR: k3s binary not installed"; exit 1; }

  - curl -sSfL --cacert /etc/pki/ca-trust/source/anchors/artifactory-ca.pem -u "${artifactory_user}:${artifactory_pass}" -o /var/lib/rancher/k3s/agent/images/k3s-airgap-images-amd64.tar.zst "${artifactory_url}/${generic_repo}/k3s/${urlencode(k3s_version)}/k3s-airgap-images-amd64.tar.zst"
  - |
    test -s /var/lib/rancher/k3s/agent/images/k3s-airgap-images-amd64.tar.zst || { echo "ERROR: airgap images not downloaded"; exit 1; }

  - ln -sf /usr/local/bin/k3s /usr/local/bin/kubectl
  - ln -sf /usr/local/bin/k3s /usr/local/bin/crictl
  - ln -sf /usr/local/bin/k3s /usr/local/bin/ctr

  - systemctl disable --now firewalld || true

  - touch /var/lib/cloud/instance/cloud-init-complete
