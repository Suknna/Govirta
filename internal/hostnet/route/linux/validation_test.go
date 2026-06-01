//go:build linux

package linux

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/suknna/govirta/internal/hostnet/link"
	"github.com/suknna/govirta/internal/hostnet/route"
	"github.com/suknna/govirta/internal/hostnet/route/routeerr"
)

func validRouteSpec() route.RouteSpec {
	return route.RouteSpec{
		Family: route.FamilyIPv4,
		Destination: route.Destination{
			Mode: route.DestinationCIDR,
			CIDR: netip.MustParsePrefix("198.51.100.0/24"),
		},
		LinkName: link.Name("gvrt0"),
		Gateway: route.Gateway{
			Mode: route.GatewayNone,
		},
		Table:    route.RouteTableMain,
		Type:     route.RouteTypeUnicast,
		Scope:    route.RouteScopeLink,
		Protocol: route.RouteProtocolStatic,
		Metric:   route.ExplicitMetric(100),
	}
}

func TestValidationRejectsInvalidRouteSpecFields(t *testing.T) {
	tests := []struct {
		name string
		edit func(*route.RouteSpec)
		want error
	}{
		{
			name: "empty family",
			edit: func(spec *route.RouteSpec) { spec.Family = "" },
			want: routeerr.ErrInvalidRequest,
		},
		{
			name: "unsupported family",
			edit: func(spec *route.RouteSpec) { spec.Family = route.Family("ipv6") },
			want: routeerr.ErrUnsupported,
		},
		{
			name: "empty table",
			edit: func(spec *route.RouteSpec) { spec.Table = "" },
			want: routeerr.ErrInvalidRequest,
		},
		{
			name: "unsupported table",
			edit: func(spec *route.RouteSpec) { spec.Table = route.RouteTable("custom") },
			want: routeerr.ErrUnsupported,
		},
		{
			name: "empty type",
			edit: func(spec *route.RouteSpec) { spec.Type = "" },
			want: routeerr.ErrInvalidRequest,
		},
		{
			name: "unsupported type",
			edit: func(spec *route.RouteSpec) { spec.Type = route.RouteType("blackhole") },
			want: routeerr.ErrUnsupported,
		},
		{
			name: "empty protocol",
			edit: func(spec *route.RouteSpec) { spec.Protocol = "" },
			want: routeerr.ErrInvalidRequest,
		},
		{
			name: "unsupported protocol",
			edit: func(spec *route.RouteSpec) { spec.Protocol = route.RouteProtocolKernel },
			want: routeerr.ErrUnsupported,
		},
		{
			name: "empty scope",
			edit: func(spec *route.RouteSpec) { spec.Scope = "" },
			want: routeerr.ErrInvalidRequest,
		},
		{
			name: "unsupported host scope",
			edit: func(spec *route.RouteSpec) { spec.Scope = route.RouteScopeHost },
			want: routeerr.ErrUnsupported,
		},
		{
			name: "invalid custom scope",
			edit: func(spec *route.RouteSpec) { spec.Scope = route.RouteScope("site") },
			want: routeerr.ErrInvalidRequest,
		},
		{
			name: "missing metric",
			edit: func(spec *route.RouteSpec) { spec.Metric = route.Metric{} },
			want: routeerr.ErrInvalidRequest,
		},
		{
			name: "destination any",
			edit: func(spec *route.RouteSpec) { spec.Destination = route.Destination{Mode: route.DestinationAny} },
			want: routeerr.ErrInvalidRequest,
		},
		{
			name: "default route as cidr",
			edit: func(spec *route.RouteSpec) {
				spec.Destination = route.Destination{Mode: route.DestinationCIDR, CIDR: netip.MustParsePrefix("0.0.0.0/0")}
			},
			want: routeerr.ErrInvalidRequest,
		},
		{
			name: "gateway any",
			edit: func(spec *route.RouteSpec) { spec.Gateway = route.Gateway{Mode: route.GatewayAny} },
			want: routeerr.ErrInvalidRequest,
		},
		{
			name: "default without gateway",
			edit: func(spec *route.RouteSpec) {
				spec.Destination = route.Destination{Mode: route.DestinationDefault}
				spec.Gateway = route.Gateway{Mode: route.GatewayNone}
				spec.Scope = route.RouteScopeLink
			},
			want: routeerr.ErrInvalidRequest,
		},
		{
			name: "unspecified gateway",
			edit: func(spec *route.RouteSpec) {
				spec.Gateway = route.Gateway{Mode: route.GatewayIPv4, Addr: netip.MustParseAddr("0.0.0.0")}
				spec.Scope = route.RouteScopeGlobal
			},
			want: routeerr.ErrInvalidRequest,
		},
		{
			name: "multicast gateway",
			edit: func(spec *route.RouteSpec) {
				spec.Gateway = route.Gateway{Mode: route.GatewayIPv4, Addr: netip.MustParseAddr("224.0.0.1")}
				spec.Scope = route.RouteScopeGlobal
			},
			want: routeerr.ErrInvalidRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := validRouteSpec()
			tt.edit(&spec)
			if err := validateRouteSpec(spec); !errors.Is(err, tt.want) {
				t.Fatalf("validateRouteSpec error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestValidationAllowsExplicitZeroMetric(t *testing.T) {
	spec := validRouteSpec()
	spec.Metric = route.ExplicitMetric(0)

	if err := validateRouteSpec(spec); err != nil {
		t.Fatalf("validateRouteSpec error = %v, want nil", err)
	}
}

func TestValidationAllowsRouteShapeMatrix(t *testing.T) {
	tests := []struct {
		name string
		spec route.RouteSpec
	}{
		{
			name: "cidr direct link",
			spec: validRouteSpec(),
		},
		{
			name: "cidr via gateway",
			spec: route.RouteSpec{
				Family: route.FamilyIPv4,
				Destination: route.Destination{
					Mode: route.DestinationCIDR,
					CIDR: netip.MustParsePrefix("10.0.0.0/8"),
				},
				LinkName: link.Name("gvbr0"),
				Gateway: route.Gateway{
					Mode: route.GatewayIPv4,
					Addr: netip.MustParseAddr("192.168.100.1"),
				},
				Table:    route.RouteTableMain,
				Type:     route.RouteTypeUnicast,
				Scope:    route.RouteScopeGlobal,
				Protocol: route.RouteProtocolStatic,
				Metric:   route.ExplicitMetric(100),
			},
		},
		{
			name: "default via gateway",
			spec: route.RouteSpec{
				Family: route.FamilyIPv4,
				Destination: route.Destination{
					Mode: route.DestinationDefault,
				},
				LinkName: link.Name("eth0"),
				Gateway: route.Gateway{
					Mode: route.GatewayIPv4,
					Addr: netip.MustParseAddr("192.168.1.1"),
				},
				Table:    route.RouteTableMain,
				Type:     route.RouteTypeUnicast,
				Scope:    route.RouteScopeGlobal,
				Protocol: route.RouteProtocolStatic,
				Metric:   route.ExplicitMetric(100),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateRouteSpec(tt.spec); err != nil {
				t.Fatalf("validateRouteSpec error = %v, want nil", err)
			}
		})
	}
}

func TestValidationAllowsExplicitRouteFilterModes(t *testing.T) {
	filter := route.RouteFilter{
		Family: route.FamilyIPv4,
		Table:  route.RouteTableMain,
		Link: route.LinkFilter{
			Mode: route.LinkName,
			Name: link.Name("gvrt0"),
		},
		Destination: route.Destination{
			Mode: route.DestinationAny,
		},
		Gateway: route.Gateway{
			Mode: route.GatewayAny,
		},
		Metric: route.FilterMetric(0),
	}

	if err := validateRouteFilter(filter); err != nil {
		t.Fatalf("validateRouteFilter error = %v, want nil", err)
	}
}

func TestValidationRejectsDefaultRouteAsCIDRFilter(t *testing.T) {
	filter := route.RouteFilter{
		Family: route.FamilyIPv4,
		Table:  route.RouteTableMain,
		Link: route.LinkFilter{
			Mode: route.LinkAny,
		},
		Destination: route.Destination{
			Mode: route.DestinationCIDR,
			CIDR: netip.MustParsePrefix("0.0.0.0/0"),
		},
		Gateway: route.Gateway{
			Mode: route.GatewayAny,
		},
		Metric: route.AnyMetric(),
	}

	if err := validateRouteFilter(filter); !errors.Is(err, routeerr.ErrInvalidRequest) {
		t.Fatalf("validateRouteFilter error = %v, want ErrInvalidRequest", err)
	}
}

func TestValidationRejectsInvalidRouteQuery(t *testing.T) {
	query := route.RouteQuery{Family: route.FamilyIPv4, Destination: netip.MustParseAddr("2001:db8::1")}

	if err := validateRouteQuery(query); !errors.Is(err, routeerr.ErrInvalidRequest) {
		t.Fatalf("validateRouteQuery error = %v, want ErrInvalidRequest", err)
	}
}
