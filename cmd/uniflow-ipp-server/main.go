package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/uniflow-ipp-server/internal/cloud"
	"github.com/uniflow-ipp-server/internal/config"
	"github.com/uniflow-ipp-server/internal/server"
)

var Version = "1.0.0"

func main() {
	configPath := flag.String("config", "/etc/uniflow-ipp/config.toml", "path to configuration file")
	authorize := flag.Bool("authorize", false, "authorize using explicitly configured RFC 8628 device endpoints")
	version := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *version {
		log.Print(Version)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if err := cfg.Validate(); err != nil {
		log.Fatalf("config validation: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	tokens := cloud.NewTokenManager(cloud.AuthConfig{
		ClientID: cfg.Cloud.OAuthClientID, ClientSecret: cfg.Cloud.OAuthClientSecret,
		Scope: cfg.Cloud.OAuthScope, DeviceEndpoint: cfg.Cloud.DeviceEndpoint,
		TokenEndpoint: cfg.Cloud.TokenEndpoint, TokenPath: cfg.Cloud.TokenPath,
	})
	if *authorize {
		if cfg.Cloud.AuthStatePath != "" {
			log.Fatal("device authorization is unavailable with managed peer credentials")
		}
		dar, err := tokens.BeginDeviceAuthorization(ctx)
		if err != nil {
			log.Fatalf("begin authorization: %v", err)
		}
		log.Printf("Open %s and enter code %s", dar.VerificationURI, dar.UserCode)
		if _, err := tokens.PollForToken(ctx, dar); err != nil {
			log.Fatalf("authorize: %v", err)
		}
		log.Print("token saved; tenant registration and user ID are still required for cloud printing")
		return
	}
	var submitter server.Submitter
	if cfg.Cloud.Enabled {
		var source cloud.TokenSource = tokens
		if cfg.Cloud.AuthStatePath != "" {
			managed := cloud.NewManagedAuth(cfg.Cloud.AuthStatePath, cfg.Cloud.TenantDomain, cfg.Cloud.UserID, cfg.Cloud.ManagementServer, nil)
			if err := managed.Load(); err != nil {
				log.Fatalf("load managed cloud credentials: %v", err)
			}
			source = managed
			go managed.Run(ctx)
		} else {
			if err := tokens.Load(); err != nil {
				log.Fatalf("load cloud token: %v", err)
			}
			if _, err := tokens.AccessToken(ctx); err != nil {
				log.Fatalf("cloud credentials: %v", err)
			}
		}
		submitter = cloud.NewClient(cfg.Cloud.SubmissionURL(), source)
	} else {
		log.Print("cloud disabled: jobs are retained locally, not uploaded or printed")
	}

	log.Printf("uniFLOW IPP Server %s", Version)
	log.Printf("listening on %s", cfg.Server.Listen)
	log.Printf("spool directory: %s", cfg.Server.SpoolDir)
	log.Printf("printer URI: %s", cfg.PrinterURI())

	if err := os.MkdirAll(cfg.Server.SpoolDir, 0700); err != nil {
		log.Fatalf("create spool dir: %v", err)
	}

	handler := server.NewHandler(server.Config{
		QueueName:  cfg.CUPS.QueueName,
		SpoolDir:   cfg.Server.SpoolDir,
		PrinterURI: cfg.PrinterURI(),
		Submitter:  submitter,
	})

	mux := http.NewServeMux()
	mux.Handle("/ipp", handler)
	mux.Handle("/ipp/", handler)

	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       120 * time.Second,
		WriteTimeout:      150 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		log.Println("shutting down...")
		srv.Close()
	}()

	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
	log.Println("server stopped")
}
