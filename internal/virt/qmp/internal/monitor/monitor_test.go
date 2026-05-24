package monitor

import "testing"

func TestGoQEMUFactoryImplementsFactory(t *testing.T) {
	var _ Factory = GoQEMUFactory{}
}
