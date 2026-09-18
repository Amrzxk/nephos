// Command imds is a minimal instance metadata service for SP4.
//
// It answers the paths cloud-init's Ec2 datasource asks for, on
// 169.254.169.254:80 inside a VPC namespace (ARCHITECTURE §5.1). The question
// SP4 asks is whether stock cloud-init, unmodified, will accept a Nephos-served
// metadata service — because if it will not, user data and key injection have
// to be built some other way, and M2 changes shape.
//
// IMDSv2's token flow is implemented too: `http_tokens=required` is a lesson
// Nephos wants to be able to teach, so the mechanism has to be real rather than
// stubbed.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// instance is the metadata served for the single instance in this spike. The
// real IMDS identifies the caller by source address and incoming interface;
// here there is only one caller.
type instance struct {
	InstanceID       string
	InstanceType     string
	AvailabilityZone string
	Region           string
	PrivateIP        string
	Hostname         string
	PublicKey        string
	UserData         string
	RequireToken     bool // http_tokens=required
}

type tokenStore struct {
	mu     sync.Mutex
	tokens map[string]time.Time
}

func newTokenStore() *tokenStore { return &tokenStore{tokens: map[string]time.Time{}} }

func (s *tokenStore) issue(ttl time.Duration) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A spike token needs to be unguessable enough to be realistic, not
	// cryptographically strong; the real one comes from crypto/rand.
	tok := strconv.FormatInt(time.Now().UnixNano(), 36) + strconv.Itoa(len(s.tokens))
	s.tokens[tok] = time.Now().Add(ttl)
	return tok
}

func (s *tokenStore) valid(tok string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.tokens[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.tokens, tok)
		return false
	}
	return true
}

func main() {
	addr := flag.String("addr", "169.254.169.254:80", "address to listen on")
	requireToken := flag.Bool("require-token", false, "reject IMDSv1 requests (http_tokens=required)")
	userDataFile := flag.String("user-data", "", "file whose contents are served at /latest/user-data")
	publicKeyFile := flag.String("public-key", "", "file whose contents are served as the instance's public key")
	privateIP := flag.String("private-ip", "10.0.1.4", "the instance's private address")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	inst := instance{
		InstanceID:       "i-0a1b2c3d4e5f67890",
		InstanceType:     "t3.micro",
		AvailabilityZone: "local-1a",
		Region:           "local-1",
		PrivateIP:        *privateIP,
		Hostname:         "ip-" + strings.ReplaceAll(*privateIP, ".", "-") + ".local-1.compute.internal",
		RequireToken:     *requireToken,
	}
	if *userDataFile != "" {
		data, err := os.ReadFile(*userDataFile)
		if err != nil {
			logger.Error("reading user data", slog.Any("error", err))
			os.Exit(1)
		}
		inst.UserData = string(data)
	}
	if *publicKeyFile != "" {
		data, err := os.ReadFile(*publicKeyFile)
		if err != nil {
			logger.Error("reading the public key", slog.Any("error", err))
			os.Exit(1)
		}
		inst.PublicKey = strings.TrimSpace(string(data))
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           newHandler(inst, logger),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		logger.Error("listening", slog.String("addr", *addr), slog.Any("error", err))
		os.Exit(1)
	}
	logger.Info("imds listening", slog.String("addr", *addr), slog.Bool("require_token", inst.RequireToken))
	if err := srv.Serve(ln); err != nil {
		logger.Error("serving", slog.Any("error", err))
		os.Exit(1)
	}
}

// apiVersions is the list served at "/". cloud-init fetches this first and
// then addresses metadata under a DATED version, not under /latest — it asks
// for /2009-04-04/meta-data/instance-id. An IMDS that only answers /latest
// looks healthy to curl and makes cloud-init fall back to DataSourceNone,
// which is exactly what SP4 caught.
var apiVersions = []string{
	"1.0",
	"2007-01-19",
	"2007-03-01",
	"2007-08-29",
	"2007-10-10",
	"2007-12-15",
	"2008-02-01",
	"2008-09-01",
	"2009-04-04",
	"2011-01-01",
	"2011-05-01",
	"2012-01-12",
	"2014-02-25",
	"2016-09-02",
	"latest",
}

// splitVersion strips a leading API version segment from the path, returning
// the remainder and whether the version was recognised.
func splitVersion(path string) (rest string, ok bool) {
	trimmed := strings.TrimPrefix(path, "/")
	head, tail, _ := strings.Cut(trimmed, "/")
	for _, v := range apiVersions {
		if head == v {
			return "/" + tail, true
		}
	}
	return path, false
}

func newHandler(inst instance, logger *slog.Logger) http.Handler {
	tokens := newTokenStore()
	mux := http.NewServeMux()

	// IMDSv2: PUT /latest/api/token with a TTL header returns a session token.
	mux.HandleFunc("/latest/api/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		ttl := 21600
		if v := r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil {
				ttl = parsed
			}
		}
		fmt.Fprint(w, tokens.issue(time.Duration(ttl)*time.Second))
	})

	serve := func(w http.ResponseWriter, r *http.Request, body string) {
		// With http_tokens=required, an IMDSv1 request gets 401. This is the
		// behaviour the IMDSv2 lesson depends on.
		if inst.RequireToken {
			tok := r.Header.Get("X-aws-ec2-metadata-token")
			if tok == "" || !tokens.valid(tok) {
				logger.Info("rejected an IMDSv1 request", slog.String("path", r.URL.Path))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		logger.Info("metadata served", slog.String("path", r.URL.Path))
		fmt.Fprint(w, body)
	}

	// Directory listings matter: cloud-init walks them.
	metaData := func(w http.ResponseWriter, r *http.Request) {
		rest, _ := splitVersion(r.URL.Path)
		path := strings.TrimPrefix(rest, "/meta-data/")
		path = strings.TrimPrefix(path, "meta-data/")
		if path == "/meta-data" || path == "meta-data" {
			path = ""
		}
		switch path {
		case "":
			serve(w, r, strings.Join([]string{
				"ami-id", "hostname", "instance-id", "instance-type",
				"local-hostname", "local-ipv4", "mac", "placement/",
				"public-keys/", "security-groups",
			}, "\n"))
		case "ami-id":
			serve(w, r, "ami-0a1b2c3d4e5f67890")
		case "instance-id":
			serve(w, r, inst.InstanceID)
		case "instance-type":
			serve(w, r, inst.InstanceType)
		case "hostname", "local-hostname":
			serve(w, r, inst.Hostname)
		case "local-ipv4":
			serve(w, r, inst.PrivateIP)
		case "mac":
			serve(w, r, "02:00:00:00:00:01")
		case "security-groups":
			serve(w, r, "default")
		case "placement/":
			serve(w, r, "availability-zone\nregion")
		case "placement/availability-zone":
			serve(w, r, inst.AvailabilityZone)
		case "placement/region":
			serve(w, r, inst.Region)
		case "public-keys/":
			serve(w, r, "0=nephos-key")
		case "public-keys/0/":
			serve(w, r, "openssh-key")
		case "public-keys/0/openssh-key":
			serve(w, r, inst.PublicKey)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}

	userData := func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, inst.UserData)
	}

	// The Ec2 datasource probes this. Returning something well-formed with
	// strict_id disabled is what lets stock cloud-init accept Nephos.
	identityDoc := func(w http.ResponseWriter, r *http.Request) {
		doc, _ := json.Marshal(map[string]any{
			"instanceId":         inst.InstanceID,
			"instanceType":       inst.InstanceType,
			"availabilityZone":   inst.AvailabilityZone,
			"region":             inst.Region,
			"privateIp":          inst.PrivateIP,
			"imageId":            "ami-0a1b2c3d4e5f67890",
			"architecture":       "x86_64",
			"pendingTime":        time.Now().UTC().Format(time.RFC3339),
			"version":            "2017-09-30",
			"accountId":          "000000000000",
			"billingProducts":    nil,
			"devpayProductCodes": nil,
		})
		serve(w, r, string(doc))
	}

	// One catch-all router, because every metadata path is version-prefixed and
	// http.ServeMux cannot pattern-match a variable first segment.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// The version index. cloud-init reads this before anything else and
		// picks the newest version it understands.
		if r.URL.Path == "/" || r.URL.Path == "" {
			serve(w, r, strings.Join(apiVersions, "\n"))
			return
		}

		rest, versioned := splitVersion(r.URL.Path)
		if !versioned {
			logger.Info("unversioned metadata path", slog.String("path", r.URL.Path))
			w.WriteHeader(http.StatusNotFound)
			return
		}

		switch {
		case rest == "/" || rest == "":
			serve(w, r, "meta-data\nuser-data\ndynamic")
		case rest == "/user-data":
			userData(w, r)
		case rest == "/dynamic" || rest == "/dynamic/":
			serve(w, r, "instance-identity/")
		case rest == "/dynamic/instance-identity" || rest == "/dynamic/instance-identity/":
			serve(w, r, "document")
		case rest == "/dynamic/instance-identity/document":
			identityDoc(w, r)
		case rest == "/meta-data" || strings.HasPrefix(rest, "/meta-data/"):
			metaData(w, r)
		default:
			logger.Info("unhandled metadata path", slog.String("path", r.URL.Path))
			w.WriteHeader(http.StatusNotFound)
		}
	})

	return mux
}
