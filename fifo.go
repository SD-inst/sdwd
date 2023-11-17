package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"syscall"
)

func fifo(path string, serviceName string, restarterChan chan string, promchan chan<- MetricUpdate) error {
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
			cmd := string(buf)
			switch cmd {
			case "restart":
				log.Printf("Restarting %s by FIFO command %s...", serviceName, cmd)
				restarterChan <- serviceName
				promchan <- MetricUpdate{Reason: "timeout", Value: 1}
			default:
				log.Printf("Unknown command %s", cmd)
			}
		}
	}()
	return nil
}
