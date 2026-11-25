package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// portSpec is a workstation port forwarded into the sandbox (rondo-sandbox.yaml, ports): an
// ingress host, reached through the ingress as on a site, or a raw port of a role's ingress IP
// (a LoadBalancer Service sharing it, like a syslog input).
type portSpec struct {
	HostPort int    `json:"hostPort"`
	Host     string `json:"host,omitempty"`
	Role     string `json:"role,omitempty"`
	Machine  string `json:"machine,omitempty"`
	Port     int    `json:"port,omitempty"`
	Protocol string `json:"protocol,omitempty"`
}

// forward is a resolved portSpec: what the port proxy listens on and where it sends.
type forward struct {
	HostPort int    `json:"hostPort"`
	Protocol string `json:"protocol"`
	Host     string `json:"host,omitempty"`
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Cluster  string `json:"cluster,omitempty"`
	Machine  string `json:"machine,omitempty"`
	// what a raw port serves, for status's link: http for protocol: http, relayed as tcp
	Scheme string `json:"scheme,omitempty"`
}

func (f forward) listen() string {
	if f.Protocol == "udp" {
		return fmt.Sprintf("%d/udp", f.HostPort)
	}
	return fmt.Sprintf("%d/tcp", f.HostPort)
}

func (f forward) String() string {
	if f.Protocol == "http" {
		return fmt.Sprintf("http://localhost:%d → https://%s", f.HostPort, f.Host)
	}
	return fmt.Sprintf("%s://localhost:%d → %s:%d", orDefault(f.Scheme, f.Protocol), f.HostPort, f.IP, f.Port)
}

// resolvePorts sends each port of rondo-sandbox.yaml to the cluster of the site that carries its
// role, or to a machine of the component.
func resolvePorts(st *site, p *plan, specs []portSpec) ([]forward, error) {
	var out []forward
	taken := map[string]bool{}
	for i, s := range specs {
		where := fmt.Sprintf("rondo-sandbox.yaml, ports[%d]", i)
		if s.HostPort < 1 || s.HostPort > 65535 {
			return nil, fmt.Errorf("%s: hostPort %d outside 1-65535", where, s.HostPort)
		}
		var f forward
		switch {
		case s.Host != "" && s.Role == "":
			host := strings.ReplaceAll(s.Host, "${INGRESS_BASE_DOMAIN}", baseDomain)
			rest, ok := strings.CutSuffix(host, "."+baseDomain)
			if !ok || !strings.Contains(rest, ".") {
				return nil, fmt.Errorf("%s: host %q is not of the form <name>.<role>.${INGRESS_BASE_DOMAIN}", where, s.Host)
			}
			role := rest[strings.LastIndex(rest, ".")+1:]
			c := st.clusterOf(role)
			if c == nil {
				return nil, fmt.Errorf("%s: no sandbox cluster holds role %q of %s", where, role, host)
			}
			f = forward{HostPort: s.HostPort, Protocol: "http", Host: host, IP: c.IngressIP, Port: 443, Cluster: c.Name}
		case (s.Role != "") != (s.Machine != "") && s.Host == "":
			if s.Port < 1 || s.Port > 65535 {
				return nil, fmt.Errorf("%s: port %d outside 1-65535", where, s.Port)
			}
			f = forward{HostPort: s.HostPort, Protocol: orDefault(s.Protocol, "tcp"), Port: s.Port}
			switch f.Protocol {
			case "tcp", "udp":
			case "http": // relayed as tcp, as it is: only status's link says http
				f.Protocol, f.Scheme = "tcp", "http"
			default:
				return nil, fmt.Errorf("%s: protocol %q, want tcp, udp or http", where, s.Protocol)
			}
			if s.Machine != "" {
				m := machineOf([]*plan{p}, s.Machine)
				if m == nil {
					return nil, fmt.Errorf("%s: no machine %q (machines: %s)", where, s.Machine,
						orDefault(strings.Join(machineList(p.Machines), ", "), "none"))
				}
				f.IP, f.Machine = m.IP, m.Name
				break
			}
			c := st.clusterOf(s.Role)
			if c == nil {
				return nil, fmt.Errorf("%s: no sandbox cluster is Target %q or holds that role", where, s.Role)
			}
			f.IP, f.Cluster = c.IngressIP, c.Name
		default:
			return nil, fmt.Errorf("%s: host (an ingress), role and port (a raw port of a cluster) or "+
				"machine and port (a port of a machine), exactly one of the three", where)
		}
		if taken[f.listen()] {
			return nil, fmt.Errorf("%s: %s already forwarded", where, f.listen())
		}
		taken[f.listen()] = true
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].listen() < out[j].listen() })
	return out, nil
}

// writePorts is the port proxy's configuration, and the ports Docker publishes for it.
func writePorts(s Settings, forwards []forward) error {
	dir := filepath.Join(s.StateDir, "ports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var listens []string
	for _, f := range forwards {
		listens = append(listens, f.listen())
	}
	if err := os.WriteFile(filepath.Join(dir, "ports.txt"), []byte(strings.Join(listens, "\n")), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "nginx.conf"), []byte(proxyConf(forwards)), 0o644)
}

// proxyConf sends an HTTP port through the ingress under its host, TLS name included, and
// rewrites what would take the browser away from localhost: redirects to the ingress host, and
// the Origin a CSRF check compares with that host.
func proxyConf(forwards []forward) string {
	var b strings.Builder
	b.WriteString("worker_processes 1;\nevents { worker_connections 1024; }\n")
	var web, raw []forward
	for _, f := range forwards {
		if f.Protocol == "http" {
			web = append(web, f)
		} else {
			raw = append(raw, f)
		}
	}
	if len(web) > 0 {
		b.WriteString("http {\n  map $http_upgrade $connection_upgrade { default upgrade; '' close; }\n")
		for _, f := range web {
			fmt.Fprintf(&b, `  map $http_origin $origin_%[1]d { "" ""; default "https://%[2]s"; }
  server {
    listen %[1]d;
    client_max_body_size 0;
    location / {
      proxy_pass https://%[3]s;
      proxy_set_header Host %[2]s;
      proxy_set_header Origin $origin_%[1]d;
      proxy_ssl_server_name on;
      proxy_ssl_name %[2]s;
      proxy_ssl_verify off;
      proxy_redirect https://%[2]s/ /;
      proxy_http_version 1.1;
      proxy_set_header Upgrade $http_upgrade;
      proxy_set_header Connection $connection_upgrade;
      proxy_buffering off;
      proxy_read_timeout 1h;
    }
  }
`, f.HostPort, f.Host, f.IP)
		}
		b.WriteString("}\n")
	}
	if len(raw) > 0 {
		b.WriteString("stream {\n")
		for _, f := range raw {
			udp := ""
			if f.Protocol == "udp" {
				udp = " udp"
			}
			fmt.Fprintf(&b, "  server { listen %d%s; proxy_pass %s:%d; }\n", f.HostPort, udp, f.IP, f.Port)
		}
		b.WriteString("}\n")
	}
	return b.String()
}

func machineList(machines []machine) []string {
	out := make([]string, len(machines))
	for i, m := range machines {
		out[i] = m.Name
	}
	return out
}
