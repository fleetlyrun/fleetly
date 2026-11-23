package functions

import (
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

func TestFromBuildRoundTrip(t *testing.T) {
	body := []byte(`{"schemaVersion":1,"handler":{"name":"handler","fromBuild":"handler","healthcheck":{"exec":{"command":["/bin/sh","-c","grep -q :1F90 /proc/net/tcp /proc/net/tcp6"]}},"ports":[{"port":8080}]}}`)
	s := &specv1.FunctionSpec{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, s); err != nil {
		t.Fatal(err)
	}
	hc := s.GetHandler().GetHealthcheck()
	t.Logf("hc=%v exec=%q", hc != nil, hc.GetExec().GetCommand())
}
