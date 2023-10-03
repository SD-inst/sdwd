package main

import (
	"bufio"
	"context"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/coreos/go-systemd/v22/sdjournal"
	"github.com/jessevdk/go-flags"
)

const sdServiceName = "sd.service"

var badLines = []string{"torch.cuda.OutOfMemoryError", "TypeError: VanillaTemporalModule.forward()", "RuntimeError: Expected all tensors", "RuntimeError: The size of tensor a", "RuntimeError: CUDA error"}

var params struct {
	DockerDir   string `short:"d" description:"Main directory with docker-compose.yml" required:"true"`
	ServiceName string `short:"s" description:"Stable diffusion docker-compose service name to watch and restart" required:"true"`
}

func restarter(dockerDir string) chan string {
	svcChan := make(chan string, 10)
	go func() {
		for serviceName := range svcChan {
			restartCmd := exec.Command("docker", "compose", "restart", serviceName, "-t", "0")
			restartCmd.Dir = dockerDir
			restartCmd.Run()
			log.Printf("Service %s restarted", serviceName)
		}
	}()
	return svcChan
}

func watchOOM(dockerDir string, serviceName string, restarter chan string) {
	for {
		logCmd := exec.Command("docker", "compose", "logs", serviceName, "-n", "1", "-f")
		logCmd.Dir = dockerDir
		logPipe, err := logCmd.StdoutPipe()
		if err != nil {
			log.Fatal(err)
		}
		s := bufio.NewScanner(logPipe)
		logCmd.Start()
		for s.Scan() {
			line := s.Text()
			for _, l := range badLines {
				if strings.Contains(line, l) {
					log.Println("Stable Diffusion misbehaving, restarting...")
					restarter <- serviceName
				}
			}
		}
		logCmd.Wait()
		time.Sleep(time.Second * 5)
		log.Println("Reconnecting to the log...")
	}
}

func main() {
	_, err := flags.Parse(&params)
	if err != nil {
		os.Exit(1)
	}
	restarterChan := restarter(params.DockerDir)
	go watchOOM(params.DockerDir, params.ServiceName, restarterChan)
	conn, err := dbus.NewSystemConnectionContext(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	j, err := sdjournal.NewJournal()
	j.AddMatch(sdjournal.SD_JOURNAL_FIELD_SYSLOG_IDENTIFIER + "=kernel")
	if err != nil {
		log.Fatal(err)
	}
	err = j.SeekTail()
	if err != nil {
		log.Print(err)
	}
	for {
		i, err := j.Next()
		if err != nil {
			log.Print(err)
		}
		if i == 0 {
			j.Wait(sdjournal.IndefiniteWait)
			continue
		}
		e, err := j.GetEntry()
		if err != nil {
			log.Print(err)
			continue
		}
		v := e.Fields[sdjournal.SD_JOURNAL_FIELD_MESSAGE]
		if strings.Contains(v, "Xid") && strings.Contains(v, "python") {
			log.Printf("GPU error detected: %+v", v)
			state, err := conn.GetUnitPropertyContext(context.Background(), sdServiceName, "ActiveState")
			if err != nil {
				log.Println(err)
			} else {
				if state.Value.Value() == "active" {
					log.Println("sd unit is active, restarting")
					restarterChan <- params.ServiceName
				} else {
					log.Println("Invalid sd unit state, ignoring:", state.Value.Value())
				}
			}
		}
	}
}
