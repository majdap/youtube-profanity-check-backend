package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/horiagug/youtube-transcript-api-go/pkg/yt_transcript"
	"github.com/horiagug/youtube-transcript-api-go/pkg/yt_transcript_formatters"
	"github.com/horiagug/youtube-transcript-api-go/pkg/yt_transcript_models"
)

// Response structure for the API
type TranscriptResponse struct {
	VideoID   string `json:"video_id"`
	Profanity bool   `json:"profanity"`
	Error     string `json:"error,omitempty"`
}

// Global worker pool to manage concurrent requests
var (
	maxWorkers = 10
	jobQueue   = make(chan Job, 100)
	wg         sync.WaitGroup
)

// Job represents a transcript fetch request
type Job struct {
	VideoID   string
	Languages []string
	Response  chan TranscriptResponse
}

var profanityWords map[string]struct{}

func main() {
	// Load profanity words
	err := loadProfanityWords("eng.txt")
	if err != nil {
		log.Fatalf("Failed to load profanity words: %v", err)
	}

	// Initialize worker pool
	startWorkerPool()

	// Set up router
	r := mux.NewRouter()
	r.HandleFunc("/transcript/{video_id}", getTranscriptHandler).Methods("GET")

	// Add CORS middleware
	corsHandler := handlers.CORS(
		handlers.AllowedOrigins([]string{"*"}),
		handlers.AllowedMethods([]string{"GET", "HEAD", "OPTIONS"}),
		handlers.AllowedHeaders([]string{"Content-Type", "X-Requested-With"}),
	)(r)

	fmt.Println("Server is running on port 8080")
	log.Fatal(http.ListenAndServe(":8080", corsHandler))
}

func startWorkerPool() {
	// Start worker goroutines
	for i := 0; i < maxWorkers; i++ {
		wg.Add(1)
		go worker(jobQueue)
	}
}

func worker(jobs <-chan Job) {
	defer wg.Done()

	for job := range jobs {
		client := yt_transcript.NewClient()
		response := TranscriptResponse{
			VideoID: job.VideoID,
		}

		transcripts, err := client.GetTranscripts(job.VideoID, job.Languages)
		if err != nil {
			response.Error = err.Error()
		} else if len(transcripts) > 0 {
			formatter := yt_transcript_formatters.NewTextFormatter(
				yt_transcript_formatters.WithTimestamps(false),
			)
			formattedText, err := formatter.Format([]yt_transcript_models.Transcript{transcripts[0]})
			if err != nil {
				response.Error = err.Error()
			} else {
				response.Profanity = containsProfanity(formattedText)
			}
		} else {
			response.Error = "No transcripts found"
		}

		job.Response <- response
	}
}

func getTranscriptHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Get video ID from query parameters
	vars := mux.Vars(r)
	videoID, ok := vars["video_id"]
	if !ok || videoID == "" {
		http.Error(w, "Missing video_id in URL", http.StatusBadRequest)
		return
	}

	// Get language from query parameters, default to English if not specified
	langParam := r.URL.Query().Get("lang")
	languages := []string{"en"}
	if langParam != "" {
		languages = []string{langParam}
	}

	// Create response channel
	respChan := make(chan TranscriptResponse, 1)

	// Submit job to the worker pool
	jobQueue <- Job{
		VideoID:   videoID,
		Languages: languages,
		Response:  respChan,
	}

	// Wait for response
	response := <-respChan

	// Return response
	json.NewEncoder(w).Encode(response)
}

func loadProfanityWords(filename string) error {
	profanityWords = make(map[string]struct{})
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		word := strings.TrimSpace(scanner.Text())
		if word != "" {
			profanityWords[strings.ToLower(word)] = struct{}{}
		}
	}
	return scanner.Err()
}

func containsProfanity(text string) bool {
	words := strings.Fields(strings.ToLower(text))
	for _, word := range words {
		if _, exists := profanityWords[word]; exists {
			return true
		}
	}
	return false
}
