package qopt

import "testing"

func TestRenderBuildsCommaSeparatedOptions(t *testing.T) {
	got, err := Render("tap", Required("id", "net0"), Required("ifname", "gv-tap0"), Optional("vhost", "on"))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	want := "tap,id=net0,ifname=gv-tap0,vhost=on"
	if got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
}

func TestRenderOmitsEmptyOptionalValues(t *testing.T) {
	got, err := Render("socket", Required("id", "qmp0"), Optional("server", ""))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got != "socket,id=qmp0" {
		t.Fatalf("Render() = %q", got)
	}
}

func TestRenderRejectsInjectionCharacters(t *testing.T) {
	tests := []string{"tap0,script=/bad", "tap0\nscript=/bad", "tap0\x00bad"}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			if _, err := Render("tap", Required("ifname", value)); err == nil {
				t.Fatalf("Render() error = nil, want error")
			}
		})
	}
}

func TestRenderRejectsEmptyRequiredValues(t *testing.T) {
	if _, err := Render("tap", Required("id", "")); err == nil {
		t.Fatalf("Render() error = nil, want error")
	}
}

func TestRenderPairsBuildsKeyFirstOptions(t *testing.T) {
	got, err := RenderPairs(Required("driver", "qcow2"), Required("node-name", "root"))
	if err != nil {
		t.Fatalf("RenderPairs() error = %v", err)
	}
	if got != "driver=qcow2,node-name=root" {
		t.Fatalf("RenderPairs() = %q", got)
	}
}
