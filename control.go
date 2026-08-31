package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

const healthPollInterval = 1 * time.Second

// serveControl runs a plain TCP server on 0.0.0.0:<port>. Each newline-delimited
// line received on a connection is a command: "<op> <service_name>" where op is
// one of stop/start/restart. The service name is resolved to its container(s)
// the same way the auto-restart does, so recreation is handled. A one-line
// result ("ok" or "error: ...") is sent back.
func serveControl(cli *client.Client, serviceNames []string, port int, promchan chan<- MetricUpdate) {
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		log.Printf("Control listener error: %s", err)
		return
	}
	log.Printf("Control listening on %s", ln.Addr())
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("Control accept error: %s", err)
			continue
		}
		go handleControl(cli, serviceNames, conn, promchan)
	}
}

func handleControl(cli *client.Client, serviceNames []string, conn net.Conn, promchan chan<- MetricUpdate) {
	defer conn.Close()
	ctx := context.Background()
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			break
		}
		cmd := strings.TrimSpace(line)
		if cmd == "" {
			continue
		}
		resp := execControl(ctx, cli, serviceNames, cmd, promchan)
		conn.Write([]byte(resp + "\n"))
	}
}

func execControl(ctx context.Context, cli *client.Client, serviceNames []string, cmd string, promchan chan<- MetricUpdate) string {
	parts := strings.Fields(cmd)
	if len(parts) != 2 {
		return "error: expected '<op> <service_name>'"
	}
	op, svc := parts[0], parts[1]
	if !validOp(op) {
		return "error: unknown op " + op
	}

	ids, err := resolveContainers(cli, projectName(), svc)
	if err != nil {
		return "error: " + err.Error()
	}
	if len(ids) == 0 {
		return "error: no containers found for service " + svc
	}

	var applyErr error
	for _, id := range ids {
		if e := applyOp(ctx, cli, op, id); e != nil {
			applyErr = e
			continue
		}
		if op == "start" || op == "restart" {
			if e := waitHealthy(cli, ctx, id); e != nil {
				applyErr = e
			}
		}
	}
	if applyErr != nil {
		return "error: " + applyErr.Error()
	}

	if op == "restart" && len(serviceNames) > 0 && svc == serviceNames[0] {
		promchan <- MetricUpdate{Reason: "timeout", Value: 1}
	}
	log.Printf("Control %s %s done", op, svc)
	return "ok"
}

func validOp(op string) bool {
	switch op {
	case "stop", "start", "restart":
		return true
	}
	return false
}

func applyOp(ctx context.Context, cli *client.Client, op, id string) error {
	switch op {
	case "stop":
		return cli.ContainerStop(ctx, id, container.StopOptions{})
	case "start":
		// Idempotent: only start if not already running. The proxy assumes all
		// containers are stopped, but one may already be up when it starts.
		insp, iErr := cli.ContainerInspect(ctx, id)
		if iErr != nil {
			return iErr
		}
		if insp.State == nil || !insp.State.Running {
			return cli.ContainerStart(ctx, id, container.StartOptions{})
		}
		return nil
	case "restart":
		timeout := 0
		return cli.ContainerRestart(ctx, id, container.StopOptions{Timeout: &timeout})
	}
	return fmt.Errorf("unknown op %s", op)
}

// waitHealthy blocks until the container's healthcheck reports healthy. It
// returns immediately if the container has no healthcheck defined.
func waitHealthy(cli *client.Client, ctx context.Context, id string) error {
	insp, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		return err
	}
	if !hasHealthCheck(insp) || insp.State.Health.Status == container.Healthy {
		return nil
	}
	log.Printf("Waiting for %s to become healthy", id)
	deadline := time.Now().Add(time.Duration(params.HealthWait) * time.Second)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %ds waiting for %s to become healthy", params.HealthWait, id)
		}
		time.Sleep(healthPollInterval)
		insp, err = cli.ContainerInspect(ctx, id)
		if err != nil {
			return err
		}
		switch insp.State.Health.Status {
		case container.Healthy:
			return nil
		case container.Unhealthy:
			return fmt.Errorf("container %s is unhealthy", id)
		}
	}
}

func hasHealthCheck(insp container.InspectResponse) bool {
	if insp.ContainerJSONBase == nil || insp.State == nil || insp.State.Health == nil {
		return false
	}
	return insp.State.Health.Status != container.NoHealthcheck
}
