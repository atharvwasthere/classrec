// Package sarvamtest is an in-memory Sarvam batch STT server for tests.
package sarvamtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

type Server struct {
	*httptest.Server
	Key string

	api      atomic.Int64 // calls to Sarvam's own endpoints
	blob     atomic.Int64 // presigned upload/download calls
	mu       sync.Mutex
	jobs     map[string]*job
	nextJob  int
	failures map[string]int    // path suffix -> remaining 503 responses
	FailFile string            // input file name to report as failed
	PollsRun int               // status polls answered "Running" before "Completed"
	Params   []json.RawMessage // job_parameters received by init
	Bad      []string          // protocol violations seen
	Uploaded []string          // file names PUT, in order
}

type job struct {
	files  map[string]bool
	out    map[string]string // output file -> input file
	polls  int
	starts int
}

func New(key string) *Server {
	s := &Server{Key: key, jobs: map[string]*job{}, failures: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// APICalls counts requests to Sarvam endpoints (not the presigned blob URLs).
func (s *Server) APICalls() int  { return int(s.api.Load()) }
func (s *Server) BlobCalls() int { return int(s.blob.Load()) }

func (s *Server) Fail503(pathSuffix string, times int) {
	s.mu.Lock()
	s.failures[pathSuffix] = times
	s.mu.Unlock()
}

func (s *Server) bad(format string, a ...any) {
	s.mu.Lock()
	s.Bad = append(s.Bad, fmt.Sprintf(format, a...))
	s.mu.Unlock()
}

func (s *Server) injected(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for suffix, n := range s.failures {
		if n > 0 && strings.HasSuffix(path, suffix) {
			s.failures[suffix]--
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/blob/") || strings.HasPrefix(r.URL.Path, "/out/") {
		s.blob.Add(1)
		s.handleBlob(w, r)
		return
	}
	s.api.Add(1)
	if r.Header.Get("api-subscription-key") != s.Key {
		writeJSON(w, 403, map[string]any{"error": map[string]any{"message": "bad key", "code": "invalid_api_key_error"}})
		return
	}
	if s.injected(r.URL.Path) {
		writeJSON(w, 503, map[string]any{"error": map[string]any{"message": "overloaded", "code": "internal_server_error"}})
		return
	}
	switch {
	case r.Method == "POST" && r.URL.Path == "/speech-to-text/job/v1":
		s.handleInit(w, r)
	case r.Method == "POST" && r.URL.Path == "/speech-to-text/job/v1/upload-files":
		s.handleLinks(w, r, "upload_urls", "/blob/")
	case r.Method == "POST" && r.URL.Path == "/speech-to-text/job/v1/download-files":
		s.handleLinks(w, r, "download_urls", "/out/")
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/start"):
		s.handleStart(w, r)
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/status"):
		s.handleStatus(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleInit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		JobParameters json.RawMessage `json:"job_parameters"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	s.nextJob++
	id := fmt.Sprintf("job-%d", s.nextJob)
	s.jobs[id] = &job{files: map[string]bool{}, out: map[string]string{}}
	s.Params = append(s.Params, body.JobParameters)
	s.mu.Unlock()
	writeJSON(w, 202, map[string]any{"job_id": id, "job_state": "Accepted", "storage_container_type": "Azure_V1"})
}

func (s *Server) handleLinks(w http.ResponseWriter, r *http.Request, field, prefix string) {
	var body struct {
		JobID string   `json:"job_id"`
		Files []string `json:"files"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	urls := map[string]any{}
	for _, f := range body.Files {
		urls[f] = map[string]string{"file_url": s.URL + prefix + body.JobID + "/" + f + "?sig=SECRETSIG"}
	}
	writeJSON(w, 200, map[string]any{"job_id": body.JobID, "job_state": "Accepted", field: urls})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	id := strings.Split(strings.TrimPrefix(r.URL.Path, "/speech-to-text/job/v1/"), "/")[0]
	s.mu.Lock()
	j := s.jobs[id]
	if j != nil {
		j.starts++
	}
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"job_id": id, "job_state": "Pending"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	id := strings.Split(strings.TrimPrefix(r.URL.Path, "/speech-to-text/job/v1/"), "/")[0]
	s.mu.Lock()
	j := s.jobs[id]
	if j == nil {
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	j.polls++
	running := j.polls <= s.PollsRun
	var names []string
	for n := range j.files {
		names = append(names, n)
	}
	sort.Strings(names)
	for i, n := range names {
		j.out[fmt.Sprintf("%d.json", i)] = n
	}
	s.mu.Unlock()
	if running {
		writeJSON(w, 200, map[string]any{"job_id": id, "job_state": "Running"})
		return
	}
	var details []map[string]any
	succeeded := 0
	for i, n := range names {
		d := map[string]any{"inputs": []map[string]string{{"file_name": n}}}
		if n == s.FailFile {
			d["state"], d["error_message"] = "API Error", "could not decode audio"
		} else {
			d["state"] = "Success"
			d["outputs"] = []map[string]string{{"file_name": fmt.Sprintf("%d.json", i)}}
			succeeded++
		}
		details = append(details, d)
	}
	writeJSON(w, 200, map[string]any{"job_id": id, "job_state": "Completed", "total_files": len(names),
		"successful_files_count": succeeded, "job_details": details})
}

func (s *Server) handleBlob(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("api-subscription-key") != "" {
		s.bad("api key sent to a presigned URL")
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3) // blob|out, job, file
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case "PUT":
		if r.Header.Get("x-ms-blob-type") != "BlockBlob" {
			s.bad("upload without x-ms-blob-type: BlockBlob")
		}
		n, _ := io.Copy(io.Discard, r.Body)
		if n == 0 {
			s.bad("empty upload")
		}
		s.mu.Lock()
		if j := s.jobs[parts[1]]; j != nil {
			j.files[parts[2]] = true
		}
		s.Uploaded = append(s.Uploaded, parts[2])
		s.mu.Unlock()
		w.WriteHeader(201)
	case "GET":
		s.mu.Lock()
		in := s.jobs[parts[1]].out[parts[2]]
		s.mu.Unlock()
		writeJSON(w, 200, Output(in))
	}
}

// Output is the transcript the fake returns for an output file: two segments at 0-5s and 5-10s.
func Output(name string) map[string]any {
	return map[string]any{
		"request_id":    "req",
		"transcript":    "alpha " + name + " beta",
		"language_code": "hi-IN",
		"timestamps": map[string]any{
			"chunks":             []string{"alpha " + name, "beta " + name},
			"start_time_seconds": []float64{0, 5},
			"end_time_seconds":   []float64{5, 10},
		},
	}
}
