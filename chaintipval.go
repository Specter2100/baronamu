package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	signet := flag.Bool("signet", false, "Enable Signet network")
	testnet3 := flag.Bool("testnet3", false, "Enable Testnet3 network")
	connect := flag.String("connect", "", "IP")

	flag.Parse()

	if *signet && *testnet3 {
		fmt.Println("Error: --signet and --testnet3 cannot be used together.")
		os.Exit(1)
	}

	if *connect == "" {
		fmt.Println("Error: --connect flag is required.")
		fmt.Println("Usage: --connect <IP address>")
		os.Exit(1)
	}

	if *signet {
		fmt.Println("signet flag given")
	}

	if *testnet3 {
		fmt.Println("testnet3 flag given")
	}

	if *connect != "" {
		fmt.Printf("Connecting to node: %s\n", *connect)
	}

	if !*signet && !*testnet3 && *connect == "" {
		fmt.Println("No options enabled. Use --signet, --testnet3, or --connect.")
	}
}
