package main

import (
	"log"

	"myexampleapp/wire"
)

func main() {
	app, err := wire.InitApp()
	if err != nil {
		log.Fatal(err)
	}
	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
	log.Println("bye.")
}
