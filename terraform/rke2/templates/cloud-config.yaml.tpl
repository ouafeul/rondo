#cloud-config
hostname: ${hostname}
fqdn: ${fqdn}

write_files:
  - path: /etc/hosts
    append: true
    content: |
      ${ingress_ip} ${rancher_host}
      %{ if registry_ip != "" ~}
      ${registry_ip} ${registry_host}
      %{ endif ~}
      ${ip_address} ${fqdn} ${hostname}

  - path: /etc/rancher/rke2/config.yaml.d/99-node-ip.yaml
    permissions: "0644"
    content: |
      node-ip: ${ip_address}
      node-external-ip: ${ip_address}
      advertise-address: ${ip_address}

  - path: /var/lib/rancher/rke2/server/manifests/rke2-coredns-hosts.yaml
    permissions: "0644"
    content: |
      apiVersion: helm.cattle.io/v1
      kind: HelmChartConfig
      metadata:
        name: rke2-coredns
        namespace: kube-system
      spec:
        valuesContent: |-
          servers:
            - zones:
                - zone: .
              port: 53
              plugins:
                - name: errors
                - name: health
                  configBlock: |-
                    lameduck 5s
                - name: ready
                - name: kubernetes
                  parameters: cluster.local in-addr.arpa ip6.arpa
                  configBlock: |-
                    pods insecure
                    fallthrough in-addr.arpa ip6.arpa
                    ttl 30
                - name: prometheus
                  parameters: 0.0.0.0:9153
                - name: forward
                  parameters: . /etc/resolv.conf
                - name: cache
                  parameters: 30
                - name: loop
                - name: reload
                - name: loadbalance
                - name: hosts
                  configBlock: |-
                    ${ingress_ip} ${rancher_host}
                    fallthrough

  - path: /etc/pki/ca-trust/source/anchors/artifactory-ca.pem
    permissions: "0644"
    content: |
      ${indent(6, artifactory_ca)}

  - path: /etc/pki/ca-trust/source/anchors/cluster-ca.pem
    permissions: "0644"
    content: |
      ${indent(6, cluster_ca)}

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

runcmd:
  - update-ca-trust extract

  - nmcli connection reload
  - nmcli connection up ens32 || true
