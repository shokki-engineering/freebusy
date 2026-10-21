// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"os"

	// The binary carries what an empty filesystem lacks: the IANA zone
	// database, and root certificates for HTTPS webhook endpoints -- used only
	// when the host has none, as in a scratch container.
	_ "time/tzdata"

	_ "github.com/joho/godotenv/autoload"
	_ "golang.org/x/crypto/x509roots/fallback"
)

func main() {
	// Initialize the application (loads config from APP_ENV)
	app := InitializeApp()

	// Start the application and server
	message, err := app.Start()
	if err != nil {
		// Nothing is serving: exit rather than wait on a server that is not
		// there.
		fmt.Println(err)
		_ = app.Stop()
		os.Exit(1)
	}
	fmt.Println(message)

	// Wait for shutdown signal (SIGINT or SIGTERM)
	fmt.Println("Application running. Press Ctrl+C to shutdown...")
	app.Wait()

	// Graceful shutdown
	fmt.Println("Initiating graceful shutdown...")
	if err := app.Stop(); err != nil {
		fmt.Printf("Error during application stop: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Application shutdown complete")
}
