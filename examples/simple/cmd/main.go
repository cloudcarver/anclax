package main

import (
	"flag"
	"log"

	"myexampleapp/app/wire"
)

func main() {
	initOnly := flag.Bool("init", false, "initialize the application only")
	flag.Parse()

	if err := run(*initOnly); err != nil {
		log.Fatal(err)
	}
}

func run(initOnly bool) error {
	app, cleanup, err := wire.InitApp()
	if err != nil {
		return err
	}
	defer cleanup()

	if initOnly {
		log.Println("initialization completed")
		return nil
	}

	if err := app.Start(); err != nil {
		return err
	}
	log.Println("bye.")
	return nil
}
