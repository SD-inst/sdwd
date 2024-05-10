package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

func fifo(path string, serviceNames []string, restarterChan chan string, promchan chan<- MetricUpdate) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if err := syscall.Mkfifo(path, 0666); err != nil {
		return err
	}
	if err := syscall.Chmod(path, 0666); err != nil {
		return err
	}
	go func() {
		for {
			f, err := os.Open(path)
			if err != nil {
				log.Printf("Error opening FIFO: %s", err)
				continue
			}
			buf, err := io.ReadAll(f)
			if err != nil {
				log.Printf("Error reading FIFO: %s", err)
				continue
			}
			f.Close()
			line := string(buf)
			cmd := strings.Split(line, " ")
			if len(cmd) != 2 {
				log.Printf("Invalid line received from FIFO: %s", line)
				continue
			}
			switch cmd[0] {
			case "restart":
				idx := slices.Index(serviceNames, cmd[1])
				if idx < 0 {
					log.Printf("Service %s is not allowed", cmd[1])
					continue
				}
				serviceName := cmd[1]
				log.Printf("Restarting %s by FIFO command %s...", serviceName, cmd)
				restarterChan <- serviceName
				if serviceName == serviceNames[0] {
					promchan <- MetricUpdate{Reason: "timeout", Value: 1}
				}
			default:
				log.Printf("Unknown command %s", cmd)
			}
		}
	}()
	return nil
}
