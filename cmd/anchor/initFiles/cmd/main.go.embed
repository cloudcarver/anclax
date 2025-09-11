package main

import (
	"flag"
	"log"

	"myexampleapp/wire"

	"github.com/cloudcarver/anchor/pkg/utils"
)

func main() {
	init := flag.Bool("init", false, "initialize the applicaiton only")
	flag.Parse()

	app, err := wire.InitApp()
	if err != nil {
		log.Fatal(err)
	}
	defer app.Close()

	if utils.UnwrapOrDefault(init, false) {
		log.Println("initialization completed")
		return
	}

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
	log.Println("bye.")
}
