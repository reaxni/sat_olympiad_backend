package main

import (
	"log"
	"os"
	"satbackend/internal/server"
)

func main() {
	if err := server.Run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
