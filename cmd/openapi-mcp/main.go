package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/server"
)

func main() {
	// --- Flag Definitions ---
	configPath := flag.String("config", "", "Path to a YAML config file listing APIs, targets and auth. Runtime registrations via MCP tools are written back to this file so they survive restarts. When omitted the server starts with an empty, non-persisted registry.")
	portFlag := flag.Int("port", 8080, "Port to run the MCP server on (overridden by server.port in the config file)")

	flag.Parse()

	// --- Registry (persists runtime registrations to --config when given) ---
	reg := server.NewRegistry(*configPath)

	// --- Seed from config file (if any) ---
	var fc *config.FileConfig
	if *configPath != "" {
		var err error
		fc, err = config.LoadFile(*configPath)
		if err != nil {
			// Use errors.Is (not os.IsNotExist) so the check works across the
			// %w-wrapped error under the Go 1.23 toolchain declared in go.mod.
			if errors.Is(err, fs.ErrNotExist) {
				// First run: no config file yet. Start with an empty registry;
				// the file will be created on the first runtime registration.
				log.Printf("Info: config file %s does not exist yet; starting with an empty registry (it will be created on first runtime registration).", *configPath)
			} else {
				log.Fatalf("Failed to load config file: %v", err)
			}
		} else {
			reg.SetServerConfig(fc.Server)
			for i := range fc.APIs {
				def := fc.APIs[i]
				if def.Name == "" {
					log.Printf("Warning: skipping API #%d in %s: missing 'name'", i+1, *configPath)
					continue
				}
				summary, err := reg.RegisterAPI(def, false)
				if err != nil {
					log.Printf("Warning: skipping API %q from %s: %v", def.Name, *configPath, err)
					continue
				}
				log.Printf("Registered API %q from config file: %s (OpenAPI %s, %d tool(s), %d target(s))",
					summary.Name, summary.Title, summary.SpecVersion, summary.ToolCount, len(summary.Targets))
			}
		}
	}

	// --- Start Server ---
	port := *portFlag
	if fc != nil {
		port = fc.EffectiveServerPort(port)
	}
	addr := fmt.Sprintf(":%d", port)
	log.Printf("Starting MCP server on %s...", addr)
	if path := reg.PersistencePath(); path == "" {
		log.Println("Info: no --config given; runtime registrations are NOT persisted across restarts.")
	} else {
		log.Printf("Runtime registrations will be persisted to: %s", path)
	}
	err := server.ServeMCP(addr, reg)
	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
