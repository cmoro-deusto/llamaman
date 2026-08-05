// llamaman-fakeserver mimics enough of llama-server's startup output for
// llamaman's run mode to drive its status state machine. Used by the dev
// loop and integration tests so we don't need a real model on disk.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	host := flag.String("host", "127.0.0.1", "")
	port := flag.Int("port", 9080, "")
	model := flag.String("m", "", "")
	alias := flag.String("alias", "", "")
	preset := flag.String("models-preset", "", "path to a my-models.ini; enables router mode")
	delay := flag.Duration("ready-delay", 500*time.Millisecond, "delay before emitting the 'server is listening' line")
	flag.CommandLine.SetOutput(os.Stderr)
	flag.CommandLine.Init("llamaman-fakeserver", flag.ContinueOnError)

	// Parse only the flags we recognize; ignore everything else so a
	// real-looking argv vector doesn't blow up.
	args := tolerantParse(os.Args[1:])
	_ = flag.CommandLine.Parse(args)

	fmt.Printf("build = 1234 (fakeserver)\n")
	if *model != "" {
		fmt.Printf("loading model from %s\n", *model)
	}
	if *alias != "" {
		fmt.Printf("alias: %s\n", *alias)
	}
	if *preset != "" {
		n := 0
		if data, err := os.ReadFile(*preset); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "[") {
					n++
				}
			}
		}
		fmt.Printf("server: router mode with %d models from %s\n", n, *preset)
	}
	fmt.Println("llm_load_print_meta: format          = GGUF v3 (fake)")
	fmt.Println("llm_load_print_meta: arch            = qwen3moe")
	fmt.Println("ggml_cuda_init: GGML_CUDA_FORCE_MMQ:   no")
	fmt.Println("llama_kv_cache_init: kv_size = 262144")

	time.Sleep(*delay)

	// Start HTTP server with /props endpoint for readiness detection.
	go func() {
		http.HandleFunc("/props", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"default_generation_settings": map[string]any{
					"n_ctx": float64(8192),
				},
				"server_info": map[string]any{
					"server_version": "fakeserver",
				},
			})
		})
		http.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			models := []map[string]any{
				{"id": "model-a", "object": "model", "owned_by": "llamaman-fakeserver"},
				{"id": "model-b", "object": "model", "owned_by": "llamaman-fakeserver"},
			}
			if *preset == "" {
				models = models[:1]
				models[0]["id"] = *alias
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": models})
		})
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			models := []string{"model-a", "model-b"}
			if *preset == "" {
				models = []string{*alias}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "models": models})
		})
		http.HandleFunc("/slots", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"is_processing":             false,
					"n_ctx":                     8192,
					"n_prompt_tokens":           0,
					"n_prompt_tokens_processed": 0,
					"n_prompt_tokens_cache":     0,
					"next_token":                []any{},
				},
			})
		})
		http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, `llamacpp:tokens_predicted_total 0
llamacpp:tokens_predicted_seconds_total 0
llamacpp:prompt_tokens_total 0
llamacpp:prompt_seconds_total 0
llamacpp:predicted_tokens_seconds 0
llamacpp:prompt_tokens_seconds 0
llamacpp:requests_processing 0
llamacpp:requests_deferred 0
llamacpp:n_tokens_max 0
llamacpp:n_decode_total 0
`)
		})
		addr := fmt.Sprintf("%s:%d", *host, *port)
		fmt.Printf("main: server is listening on http://%s\n", addr)
		fmt.Printf("main: HTTP server is listening, hostname: %s, port: %d\n", *host, *port)
		if err := http.ListenAndServe(addr, nil); err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "http: %v\n", err)
		}
	}()

	// Trap SIGTERM/SIGINT so we exit promptly when llamaman stops us.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)

	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	i := 0
	for {
		select {
		case <-sig:
			fmt.Println("main: caught signal, shutting down")
			os.Exit(0)
		case <-tick.C:
			i++
			fmt.Printf("slot launch_slot_: id  0 | task %d | processing task\n", i)
			fmt.Printf("slot update_slots: id  0 | task %d | n_past = 16, n_tokens = 4\n", i)
		}
	}
}

// tolerantParse keeps only flags the fakeserver knows about, along with
// their values. Real llama-server takes dozens of flags; we ignore the
// rest so an authentic argv vector doesn't blow up flag.Parse.
func tolerantParse(in []string) []string {
	known := map[string]bool{
		"-host":           true,
		"--host":          true,
		"-port":           true,
		"--port":          true,
		"-m":              true,
		"--alias":         true,
		"--models-preset": true,
		"-ready-delay":    true,
	}
	out := make([]string, 0, len(in))
	for i := 0; i < len(in); i++ {
		head := in[i]
		if eq := strings.Index(head, "="); eq >= 0 {
			head = head[:eq]
		}
		if known[head] {
			out = append(out, in[i])
			if !strings.Contains(in[i], "=") && i+1 < len(in) {
				out = append(out, in[i+1])
				i++
			}
		}
	}
	return out
}
