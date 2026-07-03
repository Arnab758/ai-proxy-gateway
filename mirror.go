package main

import (
	"bytes"
	"net/http"
)

func handleMirror(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status": "mirrored"}`))
}

func ShadowLogAsync(payload []byte) {
	go func() {
		mirrorURL := "http://localhost:8080/v1/mirror"
		resp, err := http.Post(mirrorURL, "application/json", bytes.NewBuffer(payload))
		if err == nil {
			resp.Body.Close()
		}
	}()
}
