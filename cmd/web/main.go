package main

import "github.com/DigitalArsenal/space-data-network/internal/web"

func main() {
	server := web.NewServer()
	server.Start()
}
