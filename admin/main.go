package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
)

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func main() {
	url := "http://localhost:8080/admin"
	fmt.Println("==================================================")
	fmt.Println("      DRS SCREEN MONITORING LAUNCHER         ")
	fmt.Println("==================================================")
	fmt.Printf("Launching monitoring console at: %s\n\n", url)

	err := openBrowser(url)
	if err != nil {
		log.Printf("Could not open browser automatically: %v\n", err)
		fmt.Printf("Please open your browser manually and navigate to: %s\n", url)
	} else {
		fmt.Println("Dashboard opened successfully in your default web browser.")
	}

	fmt.Println("\nPress [Enter] to exit the launcher...")
	var dummy string
	fmt.Scanln(&dummy)
	os.Exit(0)
}
