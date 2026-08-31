package main

import (
	"context"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

func insp(status string, withHealth bool) container.InspectResponse {
	if !withHealth {
		return container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{State: &container.State{}}}
	}
	return container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{State: &container.State{Health: &container.Health{Status: status}}}}
}

func TestExecControlParse(t *testing.T) {
	cli, _ := client.NewClientWithOpts(client.WithHost("unix:///nonexistent.sock"))
	ctx := context.Background()
	prom := make(chan MetricUpdate, 4)

	cases := []struct{ cmd, want string }{
		{"stop", "error: expected '<op> <service_name>'"},
		{"restart a b", "error: expected '<op> <service_name>'"},
		{"foo bar", "error: unknown op foo"},
	}
	for _, c := range cases {
		if got := execControl(ctx, cli, []string{"test"}, c.cmd, prom); got != c.want {
			t.Errorf("cmd %q: got %q want %q", c.cmd, got, c.want)
		}
	}

	// A valid op must reach the docker call and surface its error.
	if got := execControl(ctx, cli, []string{"test"}, "stop abc123", prom); !strings.HasPrefix(got, "error:") {
		t.Errorf("stop op: expected an error response, got %q", got)
	}
}

func TestHasHealthCheck(t *testing.T) {
	cases := []struct {
		resp container.InspectResponse
		want bool
	}{
		{container.InspectResponse{}, false},         // empty: no state
		{insp("", false), false},                     // state present, no health
		{insp(container.NoHealthcheck, true), false}, // explicit none
		{insp(container.Starting, true), true},       // starting counts as having one
		{insp(container.Healthy, true), true},
		{insp(container.Unhealthy, true), true},
	}
	for i, c := range cases {
		if got := hasHealthCheck(c.resp); got != c.want {
			t.Errorf("case %d: got %v want %v", i, got, c.want)
		}
	}
}
