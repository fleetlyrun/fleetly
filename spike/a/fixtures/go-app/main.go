// Spike A Go fixture: minimal HTTP server, stdlib only.
package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	stamp := os.Getenv("BUILD_STAMP")
	if stamp == "" {
		stamp = "unset"
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "spike-a-go-app OK stamp=%s\n", stamp)
	})
	http.ListenAndServe(":"+port, nil)
}
