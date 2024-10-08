package main

import (
	"bufio"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/coreos/go-systemd/v22/sdjournal"
	"github.com/jessevdk/go-flags"
)

var badLines = []string{"torch.cuda.OutOfMemoryError", "torch.OutOfMemoryError", "TypeError: VanillaTemporalModule.forward()", "RuntimeError: Expected all tensors", "RuntimeError: The size of tensor a", "RuntimeError: CUDA error", "einops.EinopsError", "ZeroDivisionError", "ValueError: range", "cudaMalloc failed: out of memory"}

var params struct {
	DockerDir      string   `short:"d" description:"Main directory with docker-compose.yml" required:"true"`
	ServiceNames   []string `short:"s" description:"Docker compose service name to watch and restart, can be specified multiple times" required:"true"`
	FifoPath       string   `short:"f" description:"FIFO control file"`
	PrometheusPort int      `short:"p" description:"Prometheus HTTP metrics port"`
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

func watchLog(dockerDir string, serviceNames []string, restarter chan string, promchan chan<- MetricUpdate) {
	quit := make(chan struct{})
	for _, serviceName := range serviceNames {
		go func() {
			for {
				logCmd := exec.Command("docker", "compose", "logs", serviceName, "-n", "1", "-f")
				logCmd.Dir = dockerDir
				logPipe, err := logCmd.StdoutPipe()
				if err != nil {
					log.Fatal("Error watching log: ", err)
				}
				s := bufio.NewScanner(logPipe)
				logCmd.Start()
				for s.Scan() {
					line := s.Text()
					for _, l := range badLines {
						if strings.Contains(line, l) {
							log.Printf("Service %s misbehaving, restarting...", serviceName)
							restarter <- serviceName
							promchan <- MetricUpdate{Reason: "python", Value: 1}
						}
					}
				}
				logCmd.Wait()
				time.Sleep(time.Second * 5)
				log.Println("Reconnecting to the log...")
			}
		}()
	}
	<-quit
}

func main() {
	_, err := flags.Parse(&params)
	if err != nil {
		os.Exit(1)
	}
	promchan := addMetrics(params.PrometheusPort)
	restarterChan := restarter(params.DockerDir)
	go watchLog(params.DockerDir, params.ServiceNames, restarterChan, promchan)
	if params.FifoPath != "" {
		err = fifo(params.FifoPath, params.ServiceNames, restarterChan, promchan)
		if err != nil {
			log.Fatal(err)
		}
	}
	j, err := sdjournal.NewJournal()
	if err != nil {
		log.Fatal(err)
	}
	err = j.AddMatch(sdjournal.SD_JOURNAL_FIELD_SYSLOG_IDENTIFIER + "=kernel")
	if err != nil {
		log.Print(err)
	}
	err = j.SeekTail()
	if err != nil {
		log.Print(err)
	}
	_, err = j.Previous()
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
			for _, s := range params.ServiceNames {
				restarterChan <- s
			}
			promchan <- MetricUpdate{Reason: "xid", Value: 1}
		}
	}
}
