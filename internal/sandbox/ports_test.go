package sandbox

import (
	"strings"
	"testing"
)

func portSite() *site {
	return &site{Clusters: []cluster{
		{Name: "rondo-sbx", Target: managementRole, Roles: []string{managementRole}, IngressIP: "10.0.0.100", Management: true},
		{Name: "rondo-sbx-obs", Target: "obs", Roles: []string{"monitoring"}, IngressIP: "10.0.0.101"},
	}}
}

func TestResolvePortsRefusesWhatCannotBeForwarded(t *testing.T) {
	for name, c := range map[string]struct {
		spec portSpec
		want string
	}{
		"workstation port":  {portSpec{HostPort: 0, Role: "monitoring", Port: 514}, "hostPort"},
		"host without role": {portSpec{HostPort: 1, Host: "grafana.${INGRESS_BASE_DOMAIN}"}, "<name>.<role>"},
		"other domain":      {portSpec{HostPort: 1, Host: "grafana.monitoring.example.org"}, "<name>.<role>"},
		"unknown role":      {portSpec{HostPort: 1, Host: "grafana.dmz.${INGRESS_BASE_DOMAIN}"}, "dmz"},
		"missing raw role":  {portSpec{HostPort: 1, Role: "dmz", Port: 514}, "dmz"},
		"raw port":          {portSpec{HostPort: 1, Role: "monitoring"}, "port 0"},
		"protocol":          {portSpec{HostPort: 1, Role: "monitoring", Port: 1, Protocol: "sctp"}, "tcp, udp or http"},
		"neither":           {portSpec{HostPort: 1}, "exactly one of the three"},
		"both": {portSpec{HostPort: 1, Host: "a.monitoring.${INGRESS_BASE_DOMAIN}", Role: "monitoring", Port: 1},
			"exactly one of the three"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := resolvePorts(portSite(), &plan{}, []portSpec{c.spec})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want %q", err, c.want)
			}
		})
	}
}

func TestResolvePortsAllowsTCPAndUDPOnOnePortOnly(t *testing.T) {
	udp := portSpec{HostPort: 5514, Role: "monitoring", Port: 514, Protocol: "udp"}
	tcp := portSpec{HostPort: 5514, Role: "monitoring", Port: 514}
	if _, err := resolvePorts(portSite(), &plan{}, []portSpec{udp, tcp}); err != nil {
		t.Errorf("5514 over udp and tcp: %v", err)
	}
	web := portSpec{HostPort: 5514, Host: "web.monitoring.${INGRESS_BASE_DOMAIN}"}
	if _, err := resolvePorts(portSite(), &plan{}, []portSpec{tcp, web}); err == nil {
		t.Error("two forwards on 5514/tcp accepted")
	}
}

func TestResolvePortsRelaysAnHTTPRawPortAsTCP(t *testing.T) {
	fs, err := resolvePorts(portSite(), &plan{}, []portSpec{{HostPort: 8082, Role: "monitoring", Port: 8080, Protocol: "http"}})
	if err != nil {
		t.Fatal(err)
	}
	f := fs[0]
	if f.listen() != "8082/tcp" || f.String() != "http://localhost:8082 → 10.0.0.101:8080" {
		t.Errorf("listens on %s, shown as %q", f.listen(), f.String())
	}
	if conf := proxyConf(fs); strings.Contains(conf, "http {") ||
		!strings.Contains(conf, "server { listen 8082; proxy_pass 10.0.0.101:8080; }") {
		t.Errorf("want a tcp relay:\n%s", conf)
	}
	tcp := portSpec{HostPort: 8082, Role: "monitoring", Port: 22}
	if _, err := resolvePorts(portSite(), &plan{}, []portSpec{{HostPort: 8082, Role: "monitoring", Port: 8080, Protocol: "http"}, tcp}); err == nil {
		t.Error("8082 over http and tcp accepted: it is the same tcp port")
	}
}

func TestProxyConfRewritesWhatWouldLeaveLocalhost(t *testing.T) {
	fs, err := resolvePorts(portSite(), &plan{}, []portSpec{
		{HostPort: 3000, Host: "grafana.monitoring.${INGRESS_BASE_DOMAIN}"},
		{HostPort: 5514, Role: "monitoring", Port: 514, Protocol: "udp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	conf := proxyConf(fs)
	for _, want := range []string{
		"listen 3000;",
		"proxy_pass https://10.0.0.101;",
		"proxy_set_header Host grafana.monitoring.sandbox.test;",
		"proxy_ssl_name grafana.monitoring.sandbox.test;",
		"proxy_redirect https://grafana.monitoring.sandbox.test/ /;",
		`map $http_origin $origin_3000 { "" ""; default "https://grafana.monitoring.sandbox.test"; }`,
		"listen 5514 udp; proxy_pass 10.0.0.101:514;",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("configuration without %q:\n%s", want, conf)
		}
	}
	if got := fs[0].String(); got != "http://localhost:3000 → https://grafana.monitoring.sandbox.test" {
		t.Errorf("String = %q", got)
	}
	if got := fs[1].String(); got != "udp://localhost:5514 → 10.0.0.101:514" {
		t.Errorf("String = %q", got)
	}
	if raw := proxyConf(fs[1:]); strings.Contains(raw, "http {") {
		t.Errorf("http block without an HTTP port:\n%s", raw)
	}
}
