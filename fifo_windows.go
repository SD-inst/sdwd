package main

import "log"

func fifo(path string, restarterChan chan string) error {
	log.Printf("FIFO is unavailable on Windows, doing nothing")
	return nil
}
