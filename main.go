package main

import "os"

var Version = "v0.1.0"

func main() {
	store, err := DefaultStore()
	if err != nil {
		os.Exit(1)
	}
	env := DefaultCLIEnv(store)
	os.Exit(env.Run(os.Args[1:]))
}
