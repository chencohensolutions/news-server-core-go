package newsserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/enroll"
	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/graph/generated"
	"github.com/chencohensolutions/news-server-core-go/graph/resolver"
	"github.com/chencohensolutions/news-server-core-go/sites"
	"github.com/chencohensolutions/news-server-core-go/summary"
	"github.com/vektah/gqlparser/v2/ast"
)

const (
	queryReadTimeout  = 30 * time.Second
	queryWriteTimeout = 2 * time.Minute
)

// serve runs the server until the process ends.
func serve(config auth.AppConfig) {
	log.Printf("news-server %s", version)
	tlsConfig, err := createTLSConfig(config)
	if err != nil {
		log.Fatalf("failed to create TLS config: %v", err)
	}
	enrollment, err := openEnrollment(config)
	if err != nil {
		log.Fatal(err)
	}
	found, err := sites.Load(config.SourcesDir)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Read %d sites from %s", len(found), config.SourcesDir)
	// Ctrl+C or a service stop removes the router's mapping before the process ends.
	stopping, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	unmapped := make(chan struct{})
	mapPort(stopping, config, unmapped)
	if err := startDiscovery(stopping, config); err != nil {
		log.Printf("Discovery is off: %v", err)
	}
	go func() {
		<-stopping.Done()
		select {
		case <-unmapped:
		case <-time.After(5 * time.Second):
		}
		os.Exit(0)
	}()
	go serveAdmin(config, enrollment)
	db, settings, job, summaries := startReading(config, found)
	api := &resolver.Resolver{Enrollment: enrollment, Sites: found, Settings: settings, Version: version, Store: db, Discovery: forPhones, Loop: loop.stats}
	if job != nil {
		api.Stories = job.Stories
		api.Grouping, api.StoryModel = job, job.model.ID
		api.Summaries = job.summaries
	}
	if summaries != nil {
		api.Gemini = &resolver.GeminiInfo{Model: orDefault(config.GeminiModel, gemini.DefaultModel),
			Budget: orDefault(config.SummaryBudget, summary.DefaultBudget), Status: summaries.Status}
	}

	server := &http.Server{
		Addr:              ":" + config.Port,
		Handler:           newMux(config, api),
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	ln, err := net.Listen("tcp", server.Addr)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	log.Printf("Listening on https://0.0.0.0:%s", config.Port)
	log.Fatal(server.Serve(tls.NewListener(ln, tlsConfig)))
}

// createTLSConfig loads the server certificate and trusts the client CA for client certificates.
func createTLSConfig(config auth.AppConfig) (*tls.Config, error) {
	caBytes, err := os.ReadFile(config.ClientCA)
	if err != nil {
		return nil, fmt.Errorf("failed to read client CA file: %w", err)
	}
	clientCAPool := x509.NewCertPool()
	if !clientCAPool.AppendCertsFromPEM(caBytes) {
		return nil, fmt.Errorf("failed to append client CA certificates")
	}
	serverCert, err := tls.LoadX509KeyPair(config.TLSCert, config.TLSKey)
	if err != nil {
		return nil, fmt.Errorf("failed to load server TLS certificate: %w", err)
	}
	return tlsConfigFor(serverCert, clientCAPool), nil
}

// tlsConfigFor is the server's TLS setup, shared with the tests.
func tlsConfigFor(serverCert tls.Certificate, clientCAs *x509.CertPool) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		// Devices that are enrolling have no certificate yet. A certificate that is sent is still
		// verified against the client CA; identify and auth.RequireUserUnlessPublic decide access.
		ClientAuth: tls.VerifyClientCertIfGiven,
		ClientCAs:  clientCAs,
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
	}
}

// openEnrollment loads the client CA's key (which signs enrolled devices) and the device registry.
func openEnrollment(config auth.AppConfig) (*enroll.Service, error) {
	if config.ClientCAKey == "" {
		return nil, fmt.Errorf("config.json: set client_ca_key to the private key of %s (signs enrolled devices)", config.ClientCA)
	}
	issuer, err := enroll.LoadIssuer(config.ClientCA, config.ClientCAKey, time.Now)
	if err != nil {
		return nil, fmt.Errorf("failed to load client CA for enrollment: %w", err)
	}
	registry, err := enroll.OpenRegistry(config.DevicesFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open device registry: %w", err)
	}
	return enroll.NewService(issuer, registry, time.Now), nil
}

// newMux is the routing table. /query lets anonymous clients in only for the @public enrollment fields.
func newMux(config auth.AppConfig, api *resolver.Resolver) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/", playground.Handler("GraphQL playground", "/query"))
	mux.Handle("/query", withDeadlines(queryReadTimeout, queryWriteTimeout,
		gzipped(identify(config, api.Enrollment.Registry, true, newGraphQLServer(api)))))
	return mux
}

func newGraphQLServer(api *resolver.Resolver) *handler.Server {
	srv := handler.New(generated.NewExecutableSchema(generated.Config{
		Resolvers:  api,
		Directives: generated.DirectiveRoot{Public: auth.PassThroughDirective},
	}))
	srv.AddTransport(transport.Options{})
	srv.AddTransport(transport.GET{})
	srv.AddTransport(transport.POST{})
	srv.SetQueryCache(lru.New[*ast.QueryDocument](1000))
	// Introspection is a root field too, so anonymous clients cannot read the schema.
	srv.Use(extension.Introspection{})
	srv.Use(extension.AutomaticPersistedQuery{Cache: lru.New[string](100)})
	srv.AroundRootFields(auth.RequireUserUnlessPublic)
	return srv
}
