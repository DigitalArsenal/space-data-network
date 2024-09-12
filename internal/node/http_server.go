package node

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/DigitalArsenal/space-data-network/serverconfig"
)

var httpServer *http.Server // Global variable to keep track of the HTTP server

func helloHandler(w http.ResponseWriter, r *http.Request) {
	// Handling root path for non-file requests
	if r.URL.Path == "/" {
		fmt.Fprintf(w, "Welcome to the root of the HTTP server!")
		return
	}
	// Pass other requests to the next handler
	http.FileServer(http.Dir(serverconfig.Conf.Folders.RootFolder)).ServeHTTP(w, r)
}

// Starts the HTTP server
func startHTTPServer() {
	// Create a new ServeMux
	mux := http.NewServeMux()

	// Register handler to serve files from the root directory
	mux.HandleFunc("/", helloHandler) // helloHandler now also handles file serving

	// Create a server instance
	httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", serverconfig.Conf.Webserver.Port),
		Handler: mux, // Use the new ServeMux
	}

	// Start server in a goroutine so that it doesn't block.
	go func() {
		log.Printf("Starting HTTP server on %s serving files from %s\n", httpServer.Addr, serverconfig.Conf.Folders.RootFolder)
		if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("Failed to start HTTP server: %v", err)
		}
	}()
}

// Stops the HTTP server gracefully
func stopHTTPServer() {
	// Create a deadline to wait for.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Doesn't block if no connections, but will wait for the timeout if there are active connections.
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP server Shutdown: %v", err)
	} else {
		log.Println("HTTP server stopped gracefully")
	}
}
