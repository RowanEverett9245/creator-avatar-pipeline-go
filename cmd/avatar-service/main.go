package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/infrai-examples/creator-avatar-pipeline/internal/avatar"
)

const maxAvatarBytes = 8 << 20

type server struct {
	client *avatar.Client
}

func main() {
	client, err := avatar.NewClient(os.Getenv("INFRAI_BASE_URL"), os.Getenv("INFRAI_API_KEY"), nil)
	if err != nil {
		log.Fatal(err)
	}
	address := os.Getenv("LISTEN_ADDR")
	if address == "" {
		address = ":8080"
	}
	s := &server{client: client}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /avatars/{userID}", s.handleAvatar)
	log.Printf("avatar service listening on %s", address)
	log.Fatal(http.ListenAndServe(address, mux))
}

func (s *server) handleAvatar(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarBytes+(1<<20))
	if err := r.ParseMultipartForm(maxAvatarBytes); err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "avatar must be a multipart image up to 8 MiB"})
		return
	}
	file, header, err := r.FormFile("avatar")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "avatar field is required"})
		return
	}
	defer file.Close()

	imageBytes, err := io.ReadAll(io.LimitReader(file, maxAvatarBytes+1))
	if err != nil || len(imageBytes) > maxAvatarBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "avatar exceeds 8 MiB"})
		return
	}
	mediaType := http.DetectContentType(imageBytes)
	if mediaType != "image/jpeg" && mediaType != "image/png" && mediaType != "image/webp" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "avatar must be JPEG, PNG, or WebP"})
		return
	}
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "X-Request-ID is required"})
		return
	}

	result, err := s.client.Process(r.Context(), r.PathValue("userID"), requestID, header.Filename, imageBytes)
	if err != nil {
		var apiErr *avatar.APIError
		if errors.As(err, &apiErr) && apiErr.HTTPStatus >= 400 && apiErr.HTTPStatus < 500 {
			writeJSON(w, apiErr.HTTPStatus, map[string]string{"error": apiErr.Code, "message": apiErr.Message})
			return
		}
		log.Printf("avatar request %q: %v", requestID, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "avatar processing failed"})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write response: %v", err)
	}
}

func init() {
	log.SetFlags(0)
	log.SetPrefix(fmt.Sprintf("avatar-service[%d] ", os.Getpid()))
}
