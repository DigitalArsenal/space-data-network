package web

import (
	"log"
	"net/http"
)

type Server struct {
	// Server configuration fields, if any
}

func NewServer() *Server {
	return &Server{}
}

func (s *Server) Start() {

	http.HandleFunc("/", CORS(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Hello, world!"))
	}))

	// This call should block and listen for incoming requests
	err := http.ListenAndServe(":5006", nil)
	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
