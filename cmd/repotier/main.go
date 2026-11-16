// @spec CLI-BUILD-001
package main

import "os"

var Version = "2026.11.0"

func main() {
	store, err := DefaultStore()
	if err != nil {
		os.Exit(1)
	}
	env := DefaultCLIEnv(store)
	os.Exit(env.Run(os.Args[1:]))
}
