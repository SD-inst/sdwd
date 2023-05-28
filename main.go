package main

import (
	"context"
	"log"
	"strings"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/coreos/go-systemd/v22/sdjournal"
)

const sdServiceName = "sd.service"

func main() {
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
					_, err := conn.RestartUnitContext(context.Background(), sdServiceName, "replace", nil)
					if err != nil {
						log.Println("Error restarting service:", err)
					}
				} else {
					log.Println("Invalid sd unit state, ignoring:", state.Value.Value())
				}
			}
		}
	}
}
