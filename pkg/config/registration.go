package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// TargetDefinition describes one concrete server that implements an API. An API
// may have several targets (e.g. production, staging, a self-hosted instance).
// Each target carries the *credentials* needed to reach it (keys, login
// username/password); where those credentials/keys are placed is decided by the
// API's AuthConfig, not by the target. Targets are never exposed to MCP clients
// beyond their name.
type TargetDefinition struct {
	// Name uniquely identifies the target within its API ("" is normalized to
	// "default" when it is the only target).
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// BaseURL is where the target API is served. When empty, the base URL is
	// taken from the OpenAPI spec's own server/host declarations.
	BaseURL string `json:"base_url,omitempty" yaml:"base_url,omitempty"`

	// APIKey is a literal API key value (prefer APIKeyEnv for security). The
	// header/query/cookie placement of this key is governed by the API's Auth.
	APIKey string `json:"api_key,omitempty" yaml:"api_key,omitempty"`
	// APIKeyEnv is the name of an environment variable (on the MCP server) that
	// holds the API key. Values are resolved at request time and are never
	// exposed to MCP clients.
	APIKeyEnv string `json:"api_key_env,omitempty" yaml:"api_key_env,omitempty"`

	// CustomHeaders are additional headers added to every request to this target.
	CustomHeaders map[string]string `json:"custom_headers,omitempty" yaml:"custom_headers,omitempty"`

	// InsecureSkipVerify disables TLS certificate verification for this target
	// (e.g. for self-signed HTTPS like a local mofli device). Use with care.
	InsecureSkipVerify bool `json:"insecure_skip_verify,omitempty" yaml:"insecure_skip_verify,omitempty"`

	// Login-based authentication, for APIs that expose a login endpoint instead
	// of a static API token. Before routing API calls, the MCP server logs in
	// against the API's login operation or OAuth token endpoint (see
	// APIDefinition.Auth), extracts the returned session token, and attaches it
	// to each request. Where that token goes is governed by the API's Auth.
	LoginUsername    string `json:"login_username,omitempty" yaml:"login_username,omitempty"`
	LoginPassword    string `json:"login_password,omitempty" yaml:"login_password,omitempty"`
	LoginUsernameEnv string `json:"login_username_env,omitempty" yaml:"login_username_env,omitempty"` // env var (on the MCP server) holding the username
	LoginPasswordEnv string `json:"login_password_env,omitempty" yaml:"login_password_env,omitempty"` // env var (on the MCP server) holding the password
}

// APIKeyValue resolves the target's API key, preferring the environment
// variable (APIKeyEnv) over the literal value.
func (t *TargetDefinition) APIKeyValue() string {
	if t.APIKeyEnv != "" {
		if v := os.Getenv(t.APIKeyEnv); v != "" {
			return v
		}
	}
	return t.APIKey
}

// UsesLogin reports whether the target authenticates through a login endpoint
// (i.e. any login credential is configured).
func (t *TargetDefinition) UsesLogin() bool {
	return t.LoginUsername != "" || t.LoginUsernameEnv != "" || t.LoginPassword != "" || t.LoginPasswordEnv != ""
}

// LoginCredentials resolves the target's login username/password, preferring
// environment variables (LoginUsernameEnv/LoginPasswordEnv) over literals.
func (t *TargetDefinition) LoginCredentials() (username, password string) {
	username, password = t.LoginUsername, t.LoginPassword
	if t.LoginUsernameEnv != "" {
		if v := os.Getenv(t.LoginUsernameEnv); v != "" {
			username = v
		}
	}
	if t.LoginPasswordEnv != "" {
		if v := os.Getenv(t.LoginPasswordEnv); v != "" {
			password = v
		}
	}
	return username, password
}

// ToConfig converts the target definition into the runtime Config used when
// executing tool calls routed to this target. Authentication specifics (key
// placement, session tokens) are applied by the registry at request time using
// the API's Auth config, so they are not part of this Config.
func (t *TargetDefinition) ToConfig() *Config {
	cfg := &Config{
		APIKey:             t.APIKey,
		APIKeyFromEnvVar:   t.APIKeyEnv,
		ServerBaseURL:      strings.TrimSuffix(t.BaseURL, "/"),
		InsecureSkipVerify: t.InsecureSkipVerify,
	}
	if len(t.CustomHeaders) > 0 {
		keys := make([]string, 0, len(t.CustomHeaders))
		for k := range t.CustomHeaders {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+":"+t.CustomHeaders[k])
		}
		cfg.CustomHeaders = strings.Join(parts, ",")
	}
	return cfg
}

// AuthScheme values for APIDefinition.Auth.Type.
const (
	AuthNone        = "none"
	AuthAPIKey      = "apiKey"
	AuthHTTP        = "http"
	AuthOAuth2      = "oauth2"
	AuthOpenID      = "openIdConnect"
	AuthCustomLogin = "custom"
)

// AuthConfig describes how an API expects its clients to authenticate. It is
// API-level configuration: it states the authentication *type* (apiKey, http
// bearer/basic, oauth2, ...) and where credentials/session tokens are placed on
// each request (header/query/cookie + parameter name + value prefix).
//
// Targets only provide the credential *values* (API keys, login username and
// password) that get placed according to this config.
//
// It is inferred from the OpenAPI spec's security schemes where possible (see
// server.InferAuthConfig), and can be overridden explicitly in the config file
// or via register_openapi_api.
type AuthConfig struct {
	// Type is the effective authentication scheme:
	// "", "none", "apiKey", "http", "oauth2", "openIdConnect" or "custom".
	Type string `json:"type,omitempty" yaml:"type,omitempty"`

	// In is where the credential/token is attached: "header", "query" or "cookie".
	// For apiKey schemes this comes from the spec's security scheme; default header.
	In string `json:"in,omitempty" yaml:"in,omitempty"`

	// Name is the header/query/cookie parameter name (e.g. "X-API-Key",
	// "Authorization", "session"). For apiKey this comes from the spec.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Prefix is prepended to the credential/token value (e.g. "Bearer ",
	// "Basic "). Applies when the value is wrapped (http bearer/basic, oauth2).
	Prefix string `json:"prefix,omitempty" yaml:"prefix,omitempty"`

	// HTTPScheme is the HTTP authentication scheme for Type "http":
	// "basic", "bearer", "digest".
	HTTPScheme string `json:"http_scheme,omitempty" yaml:"http_scheme,omitempty"`

	// Flow is the OAuth2 flow for Type "oauth2": "password",
	// "clientCredentials", "authorizationCode" or "implicit".
	Flow string `json:"flow,omitempty" yaml:"flow,omitempty"`

	// TokenURL is the OAuth2 token endpoint (Type "oauth2").
	TokenURL string `json:"token_url,omitempty" yaml:"token_url,omitempty"`

	// LoginOperation names the operation (operationId) in the spec to call to
	// authenticate a target that uses login credentials. It is inferred when
	// possible (from an OAuth2 tokenUrl or from common operation names) and can
	// be set explicitly when the API's login endpoint cannot be inferred.
	LoginOperation string `json:"login_operation,omitempty" yaml:"login_operation,omitempty"`
}

// Effective returns a copy of the auth config with sensible defaults filled in
// for the given scheme type.
func (a AuthConfig) Effective() AuthConfig {
	cfg := a
	if cfg.In == "" {
		cfg.In = "header"
	}
	if cfg.Name == "" {
		cfg.Name = "Authorization"
	}
	switch cfg.Type {
	case AuthHTTP:
		if strings.EqualFold(cfg.HTTPScheme, "basic") && cfg.Prefix == "" {
			cfg.Prefix = "Basic "
		} else if cfg.Prefix == "" && cfg.In == "header" {
			cfg.Prefix = "Bearer "
		}
	case AuthOAuth2, AuthOpenID:
		if cfg.Prefix == "" && cfg.In == "header" {
			cfg.Prefix = "Bearer "
		}
	case AuthCustomLogin:
		// Session tokens are usually bearer tokens in the Authorization header;
		// for explicit query/cookie placement use them verbatim.
		if cfg.Prefix == "" && cfg.In == "header" {
			cfg.Prefix = "Bearer "
		}
	case "", AuthNone:
		cfg.In, cfg.Name, cfg.Prefix = "", "", ""
	case AuthAPIKey:
		// The apiKey name/in must come from the spec or explicit config; fall
		// back to a sensible default if missing.
		if cfg.Name == "" {
			cfg.Name = "Authorization"
		}
	}
	return cfg
}

// IsConfigured reports whether the API declares any authentication.
func (a AuthConfig) IsConfigured() bool {
	return a.Type != "" && a.Type != AuthNone
}

// MonitorConfig optionally watches an API's spec source for changes.
type MonitorConfig struct {
	// Enabled turns on monitoring: the server periodically checks the spec
	// source (file mtime / HTTP Last-Modified) and notifies MCP clients with a
	// notifications/api/spec_changed message when it changes.
	Enabled bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	// AutoReload additionally re-loads the spec (and regenerates the tools)
	// after notifying, so clients immediately see an updated toolset. It implies
	// monitoring: setting it without Enabled enables monitoring too.
	AutoReload bool `json:"auto_reload,omitempty" yaml:"auto_reload,omitempty"`
}

// IsConfigured reports whether any monitoring is requested.
func (m MonitorConfig) IsConfigured() bool {
	return m.Enabled || m.AutoReload
}

// KnowledgeConfig configures the API's semantic knowledge base: a library of
// Markdown documents (glossary, endpoint/schema/field annotations and
// high-level "capability" tasks). The library doubles as a human manual and is
// indexed by the server to power knowledge_* tools, capabilities discovery and
// (later) high-level task execution.
//
// Values that come from the host environment use the X_env pattern and take
// precedence over their literal counterparts (same rule as api_key_env /
// login_*_env). Credentials are never stored literally.
type KnowledgeConfig struct {
	// Enabled turns the knowledge layer on for this API. While disabled,
	// knowledge_* tools report it as disabled.
	Enabled bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`

	// Language is the language of the knowledge base (e.g. "es", "en"). All
	// documents are assumed to be written in this language; it drives skeleton
	// generation (knowledge_init) and search normalization.
	Language string `json:"language,omitempty" yaml:"language,omitempty"`

	// Root is the local directory of the knowledge library. For a local backend
	// this is the manual itself; for a git backend it is the checkout managed by
	// the server. Empty means the platform default
	// (<configDir>/knowledge/<api>).
	Root string `json:"root,omitempty" yaml:"root,omitempty"`

	// Backend selects where/how the library is stored and kept in sync.
	Backend KnowledgeBackendConfig `json:"backend,omitempty" yaml:"backend,omitempty"`

	// Learning opts into recording successful tool calls per session so
	// sequences can be suggested as capability drafts (never persisted without
	// explicit confirmation).
	Learning KnowledgeLearningConfig `json:"learning,omitempty" yaml:"learning,omitempty"`
}

// KnowledgeBackendConfig describes how the knowledge library is stored.
type KnowledgeBackendConfig struct {
	// Type is "local" (default) or "git" (remote repo synced by the server).
	Type string `json:"type,omitempty" yaml:"type,omitempty"`

	// Repository is the git URL (literal) or RepositoryEnv holds the name of an
	// environment variable with the URL. Required when Type == "git".
	Repository    string `json:"repository,omitempty" yaml:"repository,omitempty"`
	RepositoryEnv string `json:"repository_env,omitempty" yaml:"repository_env,omitempty"`

	// Branch checked out (default "main"), literal or via BranchEnv.
	Branch    string `json:"branch,omitempty" yaml:"branch,omitempty"`
	BranchEnv string `json:"branch_env,omitempty" yaml:"branch_env,omitempty"`

	// AuthTokenEnv is an environment variable holding an https token for git
	// operations; SSHKeyEnv is an environment variable pointing at a private key
	// path for ssh remotes. Credentials are only ever read from the host env.
	AuthTokenEnv string `json:"auth_token_env,omitempty" yaml:"auth_token_env,omitempty"`
	SSHKeyEnv    string `json:"ssh_key_env,omitempty" yaml:"ssh_key_env,omitempty"`

	// Sync is "auto" (pull before load, push after edits — default) or "manual"
	// (only via knowledge_sync).
	Sync string `json:"sync,omitempty" yaml:"sync,omitempty"`
	// Conflict is the divergence policy on pull/push: "rebase" (default) or
	// "ff_only".
	Conflict string `json:"conflict,omitempty" yaml:"conflict,omitempty"`

	// Commit identity for git knowledge edits, resolved from env vars.
	AuthorNameEnv  string `json:"author_name_env,omitempty" yaml:"author_name_env,omitempty"`
	AuthorEmailEnv string `json:"author_email_env,omitempty" yaml:"author_email_env,omitempty"`
}

// KnowledgeLearningConfig controls in-session learning features.
type KnowledgeLearningConfig struct {
	// Enabled records successful tool calls per connection so capability drafts
	// can be suggested; nothing is persisted without explicit confirmation.
	Enabled bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
}

// ResolveKnowledgeBackend returns a copy of the backend config with literal
// fields filled from their *_env counterparts where the env var is set.
func (k *KnowledgeConfig) ResolveKnowledgeBackend() KnowledgeBackendConfig {
	b := k.Backend
	if b.RepositoryEnv != "" {
		if v := os.Getenv(b.RepositoryEnv); v != "" {
			b.Repository = v
		}
	}
	if b.BranchEnv != "" {
		if v := os.Getenv(b.BranchEnv); v != "" {
			b.Branch = v
		}
	}
	if b.Type == "" {
		b.Type = "local"
	}
	if b.Sync == "" {
		b.Sync = "auto"
	}
	if b.Conflict == "" {
		b.Conflict = "rebase"
	}
	if b.Branch == "" {
		b.Branch = "main"
	}
	return b
}

// GitAuth returns the resolved auth material for a git backend, preferring the
// environment. safe is a flag to let callers know a value is non-empty without
// exposing it.
func (k *KnowledgeConfig) GitAuth() (authToken, sshKey string) {
	authToken = os.Getenv(k.Backend.AuthTokenEnv)
	sshKey = os.Getenv(k.Backend.SSHKeyEnv)
	return authToken, sshKey
}

// GitIdentity returns author name/email for knowledge commits.
func (k *KnowledgeConfig) GitIdentity() (name, email string) {
	name = os.Getenv(k.Backend.AuthorNameEnv)
	email = os.Getenv(k.Backend.AuthorEmailEnv)
	return name, email
}

// APIDefinition describes an OpenAPI-backed API registered with the MCP server.
// It is the canonical description shared between:
//   - the startup config file (loaded via --config),
//   - the runtime MCP management tools (register_openapi_api / register_api_target),
//   - the persisted state written back to the config file on runtime changes.
//
// Exactly one of Source or Spec must be provided.
type APIDefinition struct {
	// Name is the namespace used to prefix the tools generated from this API
	// (e.g. "weather" turns operation "getCurrent" into tool "weather__getCurrent").
	// Empty means no prefix.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Source is a filesystem path or http(s) URL of an OpenAPI/Swagger spec.
	Source string `json:"source,omitempty" yaml:"source,omitempty"`
	// Spec is an inline OpenAPI/Swagger 2.0 or 3.x JSON document.
	Spec string `json:"spec,omitempty" yaml:"spec,omitempty"`

	// ActiveTarget is the target currently selected for this API. While set, tool
	// calls are routed to it by default; otherwise callers must pass a target.
	ActiveTarget string `json:"active_target,omitempty" yaml:"active_target,omitempty"`

	// Auth describes how this API authenticates: the scheme type and where
	// credentials/session tokens are placed. It is inferred from the spec's
	// security schemes when possible, and can be set explicitly here. Targets
	// supply only credential values (see TargetDefinition).
	Auth AuthConfig `json:"auth,omitempty" yaml:"auth,omitempty"`

	// Monitoring optionally watches this API's spec source for changes and, when
	// configured, notifies clients / auto-reloads (see MonitorConfig).
	Monitoring MonitorConfig `json:"monitoring,omitempty" yaml:"monitoring,omitempty"`

	// Knowledge configures the API's semantic knowledge base (see
	// KnowledgeConfig). When enabled, the server indexes the Markdown library
	// and exposes knowledge_* tools to extend and query it during a session.
	Knowledge KnowledgeConfig `json:"knowledge,omitempty" yaml:"knowledge,omitempty"`

	// IncludeTags/ExcludeTags/IncludeOps/ExcludeOps filter which spec operations
	// are exposed (same semantics as the --include-* / --exclude-* CLI flags).
	// They form the API's allow-set: the hard boundary no runtime activation may
	// cross. Operations excluded here can only be re-exposed by editing this
	// config; the runtime exposure tools (see Exposure) act strictly within it.
	IncludeTags []string `json:"include_tags,omitempty" yaml:"include_tags,omitempty"`
	ExcludeTags []string `json:"exclude_tags,omitempty" yaml:"exclude_tags,omitempty"`
	IncludeOps  []string `json:"include_ops,omitempty" yaml:"include_ops,omitempty"`
	ExcludeOps  []string `json:"exclude_ops,omitempty" yaml:"exclude_ops,omitempty"`

	// Exposure is the runtime tool-footprint projection: which of the allowed
	// operations are actually served to sessions (whole-API on/off, slim mode
	// "none", per-tag/per-operation force-on/off). Empty mirrors the defaults
	// (Active=true, Mode=all), i.e. everything in the allow-set is exposed. A
	// session can deviate from this baseline for itself via the
	// update_session_api_exposure tool.
	Exposure ExposureConfig `json:"exposure,omitempty" yaml:"exposure,omitempty"`

	// Targets are the concrete servers implementing this API.
	Targets []TargetDefinition `json:"targets,omitempty" yaml:"targets,omitempty"`
}

// ToConfig converts the API-level definition into the runtime Config used by
// the parser when generating tools. Besides filtering, it carries the API-level
// auth so the parser can omit the credential parameter (e.g. an apiKey header
// param) from exposed tool input schemas.
func (a *APIDefinition) ToConfig() *Config {
	auth := a.Auth.Effective()
	cfg := &Config{
		IncludeTags:       a.IncludeTags,
		ExcludeTags:       a.ExcludeTags,
		IncludeOperations: a.IncludeOps,
		ExcludeOperations: a.ExcludeOps,
		APIKeyName:        auth.Name,
		APIKeyLocation:    APIKeyLocation(auth.In),
	}
	return cfg
}

// Target returns the target with the given name and whether it exists.
func (a *APIDefinition) Target(name string) (TargetDefinition, bool) {
	for _, t := range a.Targets {
		if t.Name == name {
			return t, true
		}
	}
	return TargetDefinition{}, false
}

// Exposure mode values for the dynamic tool-footprint runtime projection.
const (
	// ExposureModeAll serves every operation that passes the API's allow-set,
	// minus the disabled lists. It is the default and the backward-compatible
	// behavior for existing registrations.
	ExposureModeAll = "all"
	// ExposureModeNone serves only the operations explicitly force-on through
	// the active lists ("slim footprint" bootstrap). The full spec stays indexed
	// and discoverable regardless.
	ExposureModeNone = "none"
)

// ExposureConfig is the runtime footprint projection of an API's operations:
// which of its (allowed) tools are actually served to sessions. The allow-set
// (IncludeTags/ExcludeTags/IncludeOps/ExcludeOps) is the hard boundary; exposure
// only narrows/widens within it. A session override can deviate from this global
// baseline for its own session only (see Registry.sessionExposure).
type ExposureConfig struct {
	// Active toggles the whole API. When explicitly false, none of its
	// operation tools are served (the API stays registered; targets, auth and
	// knowledge intact). Nil means "default on" (ActiveEnabled() == true).
	Active *bool `json:"active,omitempty" yaml:"active,omitempty"`
	// Mode selects the projection rule: "all" (everything allowed except the
	// disabled lists) or "none" (only the active lists are served).
	Mode string `json:"mode,omitempty" yaml:"mode,omitempty"`
	// ActiveTags are force-on sections for mode "none".
	ActiveTags []string `json:"active_tags,omitempty" yaml:"active_tags,omitempty"`
	// ActiveOps are force-on operations for mode "none".
	ActiveOps []string `json:"active_ops,omitempty" yaml:"active_ops,omitempty"`
	// DisabledTags are force-off sections for mode "all".
	DisabledTags []string `json:"disabled_tags,omitempty" yaml:"disabled_tags,omitempty"`
	// DisabledOps are force-off operations for mode "all".
	DisabledOps []string `json:"disabled_ops,omitempty" yaml:"disabled_ops,omitempty"`
}

// ActiveEnabled resolves the whole-API switch: nil means "not configured", which
// defaults to on.
func (e ExposureConfig) ActiveEnabled() bool {
	if e.Active == nil {
		return true
	}
	return *e.Active
}

// boolPtr returns a pointer to a bool literal (for optional enum-like fields).
func boolPtr(v bool) *bool { return &v }

// IsZero reports whether no exposure override is configured (so the caller can
// fall back to the defaults Active=true, Mode=all).
func (e ExposureConfig) IsZero() bool {
	return e.Active == nil && e.Mode == "" && len(e.ActiveTags) == 0 && len(e.ActiveOps) == 0 && len(e.DisabledTags) == 0 && len(e.DisabledOps) == 0
}

// NormalizeDefaults fills in the default exposure (Active=true, Mode=all) for an
// all-zero config and validates the mode value. It returns a copy; the receiver
// is not mutated.
func (e ExposureConfig) NormalizeDefaults() ExposureConfig {
	if e.Mode == "" {
		e.Mode = ExposureModeAll
	} else if e.Mode != ExposureModeAll && e.Mode != ExposureModeNone {
		e.Mode = ExposureModeAll
	}
	if e.Active == nil {
		e.Active = boolPtr(true)
	}
	return e
}

// ServerConfig holds settings for the MCP server process itself.
type ServerConfig struct {
	// Port is the port the MCP HTTP server listens on. When set in the config
	// file it overrides the --port flag.
	Port int `json:"port,omitempty" yaml:"port,omitempty"`

	// LogLevel is the minimum log level emitted by the server: "debug", "info",
	// "warn" or "error". It overrides --log-level and is applied at startup and
	// on every reload_config (and can be changed at runtime via the
	// set_log_level management tool). Empty keeps the flag default.
	LogLevel string `json:"log_level,omitempty" yaml:"log_level,omitempty"`

	// UI configures the browser-facing web UI shell served alongside /mcp.
	UI UIServerConfig `json:"ui,omitempty" yaml:"ui,omitempty"`
}

// UIServerConfig configures the interactive web UI shell (/ui, /ui/manifest,
// /ui/chat, /ui/events). The Go server serves a pre-built static bundle and a
// JSON bridge into the live registry; each browser tab is its own MCP session.
type UIServerConfig struct {
	// Enabled turns the UI endpoints on/off. Absent means enabled (the UI is on
	// by default); set enabled: false to switch it off.
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	// SessionHeader is the request header carrying the opaque per-tab session
	// token (default "X-Ui-Session"); the "?session=" query parameter is also
	// accepted.
	SessionHeader string `json:"session_header,omitempty" yaml:"session_header,omitempty"`
	// MaxSessions caps concurrent browser sessions (default 100); new sessions
	// beyond the cap are rejected.
	MaxSessions int `json:"max_sessions,omitempty" yaml:"max_sessions,omitempty"`
	// TokenEnv names an environment variable holding a bearer token required to
	// use the UI endpoints. Empty leaves the UI open (e.g. on the local network).
	TokenEnv string `json:"token_env,omitempty" yaml:"token_env,omitempty"`
}

// IsEnabled reports whether the web UI is enabled (default true).
func (u UIServerConfig) IsEnabled() bool { return u.Enabled == nil || *u.Enabled }

// ResolveSessionHeader returns the configured session header, defaulting to
// "X-Ui-Session".
func (u UIServerConfig) ResolveSessionHeader() string {
	if strings.TrimSpace(u.SessionHeader) == "" {
		return "X-Ui-Session"
	}
	return u.SessionHeader
}

// ResolveMaxSessions returns the configured concurrent-session cap (default 100).
func (u UIServerConfig) ResolveMaxSessions() int {
	if u.MaxSessions <= 0 {
		return 100
	}
	return u.MaxSessions
}

// ResolveToken returns the bearer token gating the UI endpoints, read from
// TokenEnv. Empty means the UI is open.
func (u UIServerConfig) ResolveToken() string {
	if u.TokenEnv == "" {
		return ""
	}
	return os.Getenv(u.TokenEnv)
}

// MetaConfig configures the meta knowledge base: a global knowledge scope
// ("_meta") that is not tied to any OpenAPI API. It hosts reusable patterns,
// tool/integration write-ups, ideas and scripts across every registered API,
// and is addressed by passing api: "_meta" to the knowledge_* tools.
type MetaConfig struct {
	// Knowledge configures the meta knowledge library (see KnowledgeConfig).
	// It uses the same backend/learning semantics as per-API knowledge.
	Knowledge KnowledgeConfig `json:"knowledge,omitempty" yaml:"knowledge,omitempty"`

	// Scripting configures the tengo script executor: the operator allowlists
	// that gate the privileged host modules and the default run budget.
	Scripting ScriptingConfig `json:"scripting,omitempty" yaml:"scripting,omitempty"`
}

// ScriptingConfig configures execution of kind: script knowledge documents.
// Scripts always run with the safe standard-library modules; the privileged
// modules (mcp/os/exec/fs/http) are denied unless a script declares the
// permission and, for exec/fs/http, the operator allowlist admits the call.
type ScriptingConfig struct {
	// ExecAllowlist is the set of command names (or basenames) the exec module
	// may run. Empty denies every exec call.
	ExecAllowlist []string `json:"exec_allowlist,omitempty" yaml:"exec_allowlist,omitempty"`
	// FSReadRoots are the directory prefixes the fs module may read from. An
	// empty list denies every fs read.
	FSReadRoots []string `json:"fs_read_roots,omitempty" yaml:"fs_read_roots,omitempty"`
	// HTTPAllowlist are the URL patterns the http module may request. A pattern
	// ending in "*" matches by prefix; otherwise it must match exactly. Empty
	// denies every request.
	HTTPAllowlist []string `json:"http_allowlist,omitempty" yaml:"http_allowlist,omitempty"`
	// DefaultTimeoutS is the default per-run wall-clock budget in seconds
	// (default 10).
	DefaultTimeoutS int `json:"default_timeout_s,omitempty" yaml:"default_timeout_s,omitempty"`
}

// DefaultTimeout returns the configured default run budget, or 10s.
func (s ScriptingConfig) DefaultTimeout() time.Duration {
	if s.DefaultTimeoutS > 0 {
		return time.Duration(s.DefaultTimeoutS) * time.Second
	}
	return 10 * time.Second
}

// NormalizeKnowledgeConfig fills in the knowledge defaults shared by per-API and
// meta knowledge bases: when Root is empty it resolves to
// <configDir>/knowledge/<scope>. Backend defaults (type local, branch main, sync
// auto, conflict rebase) are applied by ResolveKnowledgeBackend at use time and
// are left untouched here.
func NormalizeKnowledgeConfig(kc KnowledgeConfig, scope, configDir string) KnowledgeConfig {
	if kc.Root == "" {
		if configDir == "" {
			if h := os.Getenv("HOME"); h != "" {
				configDir = filepath.Join(h, ".config", "openapi-mcp")
			} else {
				configDir = "."
			}
		}
		kc.Root = filepath.Join(configDir, "knowledge", scope)
	}
	return kc
}

// FileConfig is the persisted configuration file format. It lists every API the
// server should expose (each with its targets); runtime registrations are
// written back to it so they survive restarts.
type FileConfig struct {
	Server ServerConfig    `json:"server,omitempty" yaml:"server,omitempty"`
	Meta   *MetaConfig     `json:"meta,omitempty" yaml:"meta,omitempty"`
	APIs   []APIDefinition `json:"apis" yaml:"apis"`
}

// EffectiveServerPort returns the configured server port, or fallback when unset.
func (f *FileConfig) EffectiveServerPort(fallback int) int {
	if f.Server.Port > 0 {
		return f.Server.Port
	}
	return fallback
}

// ParseAPIKeyLocation converts a string to an APIKeyLocation, validating the
// value. An empty string returns an empty (unset) location without error.
func ParseAPIKeyLocation(s string) (APIKeyLocation, error) {
	switch s {
	case "":
		return "", nil
	case string(APIKeyLocationHeader):
		return APIKeyLocationHeader, nil
	case string(APIKeyLocationQuery):
		return APIKeyLocationQuery, nil
	case string(APIKeyLocationPath):
		return APIKeyLocationPath, nil
	case string(APIKeyLocationCookie):
		return APIKeyLocationCookie, nil
	default:
		return "", fmt.Errorf("invalid api key location %q: must be 'header', 'query', 'path' or 'cookie'", s)
	}
}

// LoadFile reads and parses the registry config file at path. The file may be
// YAML (preferred) or JSON; yaml.v3 accepts both.
func LoadFile(path string) (*FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed reading config file '%s': %w", path, err)
	}
	fc := &FileConfig{}
	if err := yaml.Unmarshal(data, fc); err != nil {
		return nil, fmt.Errorf("failed parsing config file '%s': %w", path, err)
	}
	return fc, nil
}

// SaveFile atomically writes fc to path as YAML (write to a temp file in the
// same directory, then rename), so a crash never leaves a truncated config
// behind.
func SaveFile(path string, fc *FileConfig) error {
	data, err := yaml.Marshal(fc)
	if err != nil {
		return fmt.Errorf("failed encoding config file: %w", err)
	}
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, ".openapi-mcp-config-*.tmp")
	if err != nil {
		return fmt.Errorf("failed creating temp config file in '%s': %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("failed writing config file '%s': %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("failed syncing config file '%s': %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed closing config file '%s': %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("failed replacing config file '%s': %w", path, err)
	}
	return nil
}
