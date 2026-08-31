package main

import (
	"bufio"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/jessevdk/go-flags"
)

var badLines = []string{"torch.cuda.OutOfMemoryError", "torch.OutOfMemoryError", "TypeError: VanillaTemporalModule.forward()", "RuntimeError: Expected all tensors", "RuntimeError: The size of tensor a", "RuntimeError: CUDA error", "einops.EinopsError", "ZeroDivisionError", "ValueError: range", "cudaMalloc failed: out of memory"}

var params struct {
	DockerHost     string   `short:"H" description:"Docker daemon host, e.g. unix:///var/run/docker.sock"`
	DockerDir      string   `short:"d" description:"Main directory with docker-compose.yml" required:"true"`
	Project        string   `short:"P" description:"Compose project name (default: derived from -d basename)"`
	ServiceNames   []string `short:"s" description:"Docker compose service name to watch and restart, can be specified multiple times" required:"true"`
	ControlPort    int      `short:"c" default:"8888" description:"Control TCP listen port (0 to disable)"`
	HealthWait     int      `short:"w" default:"60" description:"Seconds to wait for a container to become healthy on start/restart"`
	PrometheusPort int      `short:"p" description:"Prometheus HTTP metrics port"`
	KmsgPath       string   `short:"k" default:"/dev/kmsg" description:"Kernel log device to watch for GPU Xid"`
}

func dockerHost() string {
	if params.DockerHost != "" {
		return params.DockerHost
	}
	if h := os.Getenv("DOCKER_HOST"); h != "" {
		return h
	}
	return "unix:///var/run/docker.sock"
}

func projectName() string {
	if params.Project != "" {
		return params.Project
	}
	return deriveProject(params.DockerDir)
}

// deriveProject reproduces compose's default project name: the lowercase
// basename of the project directory, keeping only [a-z0-9_-].
func deriveProject(dir string) string {
	base := strings.ToLower(filepath.Base(strings.TrimRight(dir, "/")))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// resolveContainers maps a compose service name to container IDs using the
// compose labels compose sets on every container it creates.
func resolveContainers(cli *client.Client, project, svc string) ([]string, error) {
	f := filters.NewArgs()
	f.Add("label", "com.docker.compose.service="+svc)
	f.Add("label", "com.docker.compose.project="+project)
	cs, err := cli.ContainerList(context.Background(), container.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, c := range cs {
		ids = append(ids, c.ID)
	}
	return ids, nil
}

func restarter(cli *client.Client) chan string {
	svcChan := make(chan string, 10)
	go func() {
		ctx := context.Background()
		for svc := range svcChan {
			ids, err := resolveContainers(cli, projectName(), svc)
			if err != nil {
				log.Printf("Error resolving %s: %s", svc, err)
				continue
			}
			if len(ids) == 0 {
				log.Printf("No containers found for %s", svc)
				continue
			}
			timeout := 0
			for _, id := range ids {
				if err := cli.ContainerRestart(ctx, id, container.StopOptions{Timeout: &timeout}); err != nil {
					log.Printf("Error restarting %s (%s): %s", svc, id, err)
					continue
				}
				log.Printf("Service %s container %s restarted", svc, id)
			}
		}
	}()
	return svcChan
}

func watchLog(cli *client.Client, serviceNames []string, restarter chan string, promchan chan<- MetricUpdate) {
	for _, svc := range serviceNames {
		go seedService(cli, svc, restarter, promchan)
	}
}

func seedService(cli *client.Client, svc string, restarter chan string, promchan chan<- MetricUpdate) {
	var ids []string
	for {
		var err error
		ids, err = resolveContainers(cli, projectName(), svc)
		if err != nil || len(ids) == 0 {
			log.Printf("No containers found for %s: %v", svc, err)
			time.Sleep(time.Second * 5)
			continue
		}
		break
	}
	for _, id := range ids {
		go streamOne(cli, svc, id, restarter, promchan)
	}
}

func streamOne(cli *client.Client, svc, id string, restarter chan string, promchan chan<- MetricUpdate) {
	ctx := context.Background()
	for {
		stream, err := cli.ContainerLogs(ctx, id, container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Tail:       "1",
			Follow:     true,
		})
		if err != nil {
			log.Printf("Error reading logs of %s: %s", svc, err)
			// The id may be stale (container recreated); re-resolve.
			ids, rerr := resolveContainers(cli, projectName(), svc)
			if rerr != nil || len(ids) == 0 {
				time.Sleep(time.Second * 5)
				continue
			}
			id = ids[0]
			time.Sleep(time.Second * 2)
			continue
		}
		sc := bufio.NewScanner(stream)
		for sc.Scan() {
			line := sc.Text()
			for _, l := range badLines {
				if strings.Contains(line, l) {
					log.Printf("Service %s misbehaving, restarting...", svc)
					restarter <- svc
					promchan <- MetricUpdate{Reason: "python", Value: 1}
				}
			}
		}
		if err := sc.Err(); err != nil {
			log.Printf("Error scanning logs of %s: %s", svc, err)
		}
		stream.Close()
		log.Printf("Reconnecting to the log of %s...", svc)
		time.Sleep(time.Second * 5)
	}
}

func watchKmsg(kmsgPath string, serviceNames []string, restarter chan string, promchan chan<- MetricUpdate) {
	for {
		f, err := os.Open(kmsgPath)
		if err != nil {
			log.Printf("Error opening %s: %s", kmsgPath, err)
			time.Sleep(time.Second * 5)
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if strings.Contains(line, "Xid") && strings.Contains(line, "python") {
				log.Printf("GPU error detected: %s", line)
				for _, s := range serviceNames {
					restarter <- s
				}
				promchan <- MetricUpdate{Reason: "xid", Value: 1}
			}
		}
		if err := sc.Err(); err != nil {
			log.Printf("Error reading %s: %s", kmsgPath, err)
		}
		f.Close()
		time.Sleep(time.Second * 5)
	}
}

func main() {
	_, err := flags.Parse(&params)
	if err != nil {
		os.Exit(1)
	}
	cli, err := client.NewClientWithOpts(client.WithHost(dockerHost()))
	if err != nil {
		log.Fatal("Error creating docker client: ", err)
	}
	promchan := addMetrics(params.PrometheusPort)
	restarterChan := restarter(cli)
	go watchLog(cli, params.ServiceNames, restarterChan, promchan)
	if params.ControlPort > 0 {
		go serveControl(cli, params.ServiceNames, params.ControlPort, promchan)
	}
	watchKmsg(params.KmsgPath, params.ServiceNames, restarterChan, promchan)
}
