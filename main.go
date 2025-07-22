package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

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
	Error     string `json:"-"` // Omit from JSON responses
}

// ErrorResponse structure for API errors
type ErrorResponse struct {
	Error string `json:"error"`
}

// Global worker pool to manage concurrent requests
var (
	maxWorkers = 5 // Reduced from 10 to be less aggressive
	jobQueue   = make(chan Job, 100)
	wg         sync.WaitGroup
	// Rate limiter: allow one request every 2 seconds
	rateLimiter = time.NewTicker(2 * time.Second)
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
	log.Println("Loading profanity words...")
	err := loadProfanityWords("eng.txt")
	if err != nil {
		log.Fatalf("Failed to load profanity words: %v", err)
	}
	log.Printf("Loaded profanity words successfully")

	// Initialize worker pool
	log.Println("Starting worker pool...")
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
		response := TranscriptResponse{
			VideoID: job.VideoID,
		}

		// Try multiple language codes as fallbacks
		languagesToTry := job.Languages
		if len(languagesToTry) == 1 && languagesToTry[0] == "en" {
			// Add more English variants and common languages as fallbacks
			languagesToTry = []string{
				"en", "en-US", "en-GB", "en-CA", "en-AU", "en-IN",
				"es", "es-ES", "es-MX", "es-AR",
				"fr", "fr-FR", "fr-CA",
				"de", "de-DE",
				"it", "it-IT",
				"pt", "pt-BR", "pt-PT",
				"ja", "ko", "zh", "zh-CN", "zh-TW",
				"hi", "ar", "ru", "nl", "sv", "no", "da", "fi",
			}
		}

		var lastError error
		var foundTranscript bool
		maxRetries := 3

		// Try each language with retry logic
		for _, lang := range languagesToTry {
			log.Printf("Attempting to fetch transcript for video %s with language: %s", job.VideoID, lang)

			// Rate limit requests to avoid overwhelming YouTube's servers
			<-rateLimiter.C

			// Retry logic for each language
			for attempt := 0; attempt < maxRetries; attempt++ {
				if attempt > 0 {
					// Add exponential backoff delay
					delay := time.Duration(math.Pow(2, float64(attempt))) * time.Second
					log.Printf("Retrying after %v delay (attempt %d/%d)", delay, attempt+1, maxRetries)
					time.Sleep(delay)
				}

				client := yt_transcript.NewClient()
				transcripts, err := client.GetTranscripts(job.VideoID, []string{lang})

				if err != nil {
					lastError = err
					log.Printf("Attempt %d failed to get transcript for video %s with language %s: %v",
						attempt+1, job.VideoID, lang, err)

					// Check if it's a temporary error that might benefit from retry
					errorStr := strings.ToLower(err.Error())
					if strings.Contains(errorStr, "timeout") ||
						strings.Contains(errorStr, "connection") ||
						strings.Contains(errorStr, "network") ||
						strings.Contains(errorStr, "temporary") {
						// Retry for network-related errors
						continue
					}

					// If it's a "captions not found" error, try next language immediately
					if strings.Contains(errorStr, "captions not found") {
						break // Break from retry loop, try next language
					}

					// For other errors, retry might help
					if attempt < maxRetries-1 {
						continue
					}

					// If all retries failed, break from retry loop
					break
				}

				// Success case
				if len(transcripts) > 0 {
					log.Printf("Successfully fetched transcript for video %s with language: %s (attempt %d)",
						job.VideoID, lang, attempt+1)

					formatter := yt_transcript_formatters.NewTextFormatter(
						yt_transcript_formatters.WithTimestamps(false),
					)
					formattedText, err := formatter.Format([]yt_transcript_models.Transcript{transcripts[0]})
					if err != nil {
						response.Error = fmt.Sprintf("failed to format transcript: %v", err)
						log.Printf("Failed to format transcript for video %s: %v", job.VideoID, err)
					} else {
						response.Profanity = containsProfanity(formattedText)
						log.Printf("Successfully processed transcript for video %s, profanity detected: %v",
							job.VideoID, response.Profanity)
						foundTranscript = true
					}
					break // Break from retry loop
				}
			}

			if foundTranscript {
				break // Break from language loop
			}
		}

		if !foundTranscript && response.Error == "" {
			if lastError != nil {
				// Provide more helpful error messages based on the error type
				errorStr := strings.ToLower(lastError.Error())
				if strings.Contains(errorStr, "captions not found") {
					response.Error = fmt.Sprintf("No captions/transcripts are available for video %s. This video may not have auto-generated or manual captions enabled.", job.VideoID)
				} else if strings.Contains(errorStr, "private") {
					response.Error = fmt.Sprintf("Video %s is private and transcripts cannot be accessed.", job.VideoID)
				} else if strings.Contains(errorStr, "unavailable") {
					response.Error = fmt.Sprintf("Video %s is unavailable or has been removed.", job.VideoID)
				} else {
					response.Error = fmt.Sprintf("Failed to fetch transcripts for video %s: %v", job.VideoID, lastError)
				}
			} else {
				response.Error = fmt.Sprintf("No transcripts found for video %s in any of the attempted languages: %v",
					job.VideoID, languagesToTry)
			}
			log.Printf("No transcripts found for video %s after trying all languages and retries", job.VideoID)
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
		log.Printf("Missing video_id in request")
		http.Error(w, "Missing video_id in URL", http.StatusBadRequest)
		return
	}

	// Get language from query parameters, default to English if not specified
	langParam := r.URL.Query().Get("lang")
	languages := []string{"en"}
	if langParam != "" {
		languages = []string{langParam}
	}

	log.Printf("Processing request for video: %s, language: %v", videoID, languages)

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

	if response.Error != "" {
		log.Printf("Error processing video %s: %s", videoID, response.Error)
		w.Header().Set("Content-Type", "application/json")

		// Provide more specific status codes based on error type
		if strings.Contains(strings.ToLower(response.Error), "no transcripts") {
			w.WriteHeader(http.StatusNotFound)
		} else if strings.Contains(strings.ToLower(response.Error), "captions not found") {
			w.WriteHeader(http.StatusNotFound)
		} else if strings.Contains(strings.ToLower(response.Error), "private") ||
			strings.Contains(strings.ToLower(response.Error), "unavailable") {
			w.WriteHeader(http.StatusForbidden)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}

		json.NewEncoder(w).Encode(ErrorResponse{Error: response.Error})
		return
	}

	// Return response
	log.Printf("Returning response for video %s: profanity=%v", videoID, response.Profanity)
	w.Header().Set("Content-Type", "application/json")
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
