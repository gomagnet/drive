package main

import (
	"MagnetDrive/internal/drive"
	"log"
)

func main() {
	if err := drive.Run(); err != nil {
		log.Fatal(err)
	}
}
