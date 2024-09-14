package httpserver

import (
	"fmt"
	"net/http"
	"os"

	"github.com/DigitalArsenal/space-data-network/serverconfig"
)

func indexHandler(w http.ResponseWriter, r *http.Request) {
	// Handling root path for non-file requests
	if r.URL.Path == "/" {
		// Construct the path to index.html inside the RootFolder
		indexFilePath := serverconfig.Conf.Folders.RootFolder + "/index.html"

		// Check if index.html exists in the RootFolder
		if _, err := os.Stat(indexFilePath); err == nil {
			// Serve index.html from RootFolder
			http.ServeFile(w, r, indexFilePath)
		} else {
			// Serve a simple HTML page if index.html is missing
			fmt.Fprintf(w, "<html><body><h1>INDEX.HTML MISSING</h1></body></html>")
		}
		return
	}

	// Serve other files from the RootFolder
	http.FileServer(http.Dir(serverconfig.Conf.Folders.RootFolder)).ServeHTTP(w, r)
}
