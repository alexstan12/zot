package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"runtime"
	"strings"
	goSync "sync"
	"syscall"
	"time"

	"github.com/docker/distribution/registry/storage/driver/factory"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"zotregistry.io/zot/errors"
	"zotregistry.io/zot/pkg/api/config"
	ext "zotregistry.io/zot/pkg/extensions"
	"zotregistry.io/zot/pkg/extensions/monitoring"
	"zotregistry.io/zot/pkg/log"
	"zotregistry.io/zot/pkg/storage"
	"zotregistry.io/zot/pkg/storage/s3"
)

const (
	idleTimeout = 120 * time.Second
)

type Controller struct {
	Config          *config.Config
	Router          *mux.Router
	StoreController storage.StoreController
	Log             log.Logger
	Audit           *log.Logger
	Server          *http.Server
	Metrics         monitoring.MetricServer
	wgShutDown      *goSync.WaitGroup // use it to gracefully shutdown goroutines
}

func NewController(config *config.Config) *Controller {
	var controller Controller

	logger := log.NewLogger(config.Log.Level, config.Log.Output)
	controller.Config = config
	controller.Log = logger
	controller.wgShutDown = new(goSync.WaitGroup)

	if config.Log.Audit != "" {
		audit := log.NewAuditLogger(config.Log.Level, config.Log.Audit)
		controller.Audit = audit
	}

	return &controller
}

func (c *Controller) CORSHeaders() mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			// CORS
			c.CORSHandler(response, request)

			next.ServeHTTP(response, request)
		})
	}
}

func (c *Controller) CORSHandler(response http.ResponseWriter, request *http.Request) {
	// allow origin as specified in config if not accept request from anywhere.
	if c.Config.HTTP.AllowOrigin == "" {
		response.Header().Set("Access-Control-Allow-Origin", "*")
	} else {
		response.Header().Set("Access-Control-Allow-Origin", c.Config.HTTP.AllowOrigin)
	}

	response.Header().Set("Access-Control-Allow-Methods", "HEAD,GET,POST,OPTIONS")
	response.Header().Set("Access-Control-Allow-Headers", "Authorization")
}

func DumpRuntimeParams(log log.Logger) {
	var rLimit syscall.Rlimit

	evt := log.Info().Int("cpus", runtime.NumCPU())

	err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err == nil {
		evt = evt.Uint64("max. open files", rLimit.Cur)
	}

	if content, err := ioutil.ReadFile("/proc/sys/net/core/somaxconn"); err == nil {
		evt = evt.Str("listen backlog", strings.TrimSuffix(string(content), "\n"))
	}

	if content, err := ioutil.ReadFile("/proc/sys/user/max_inotify_watches"); err == nil {
		evt = evt.Str("max. inotify watches", strings.TrimSuffix(string(content), "\n"))
	}

	evt.Msg("runtime params")
}

func (c *Controller) Run(reloadCtx context.Context) error {
	// print the current configuration, but strip secrets
	c.Log.Info().Interface("params", c.Config.Sanitize()).Msg("configuration settings")

	// print the current runtime environment
	DumpRuntimeParams(c.Log)

	// setup HTTP API router
	engine := mux.NewRouter()

	// rate-limit HTTP requests if enabled
	if c.Config.HTTP.Ratelimit != nil {
		if c.Config.HTTP.Ratelimit.Rate != nil {
			engine.Use(RateLimiter(c, *c.Config.HTTP.Ratelimit.Rate))
		}

		for _, mrlim := range c.Config.HTTP.Ratelimit.Methods {
			engine.Use(MethodRateLimiter(c, mrlim.Method, mrlim.Rate))
		}
	}

	engine.Use(
		c.CORSHeaders(),
		SessionLogger(c),
		handlers.RecoveryHandler(handlers.RecoveryLogger(c.Log),
			handlers.PrintRecoveryStack(false)))

	if c.Audit != nil {
		engine.Use(SessionAuditLogger(c.Audit))
	}

	c.Router = engine
	c.Router.UseEncodedPath()

	var enabled bool
	if c.Config != nil &&
		c.Config.Extensions != nil &&
		c.Config.Extensions.Metrics != nil &&
		*c.Config.Extensions.Metrics.Enable {
		enabled = true
	}

	c.Metrics = monitoring.NewMetricsServer(enabled, c.Log)

	if err := c.InitImageStore(reloadCtx); err != nil {
		return err
	}

	monitoring.SetServerInfo(c.Metrics, c.Config.Commit, c.Config.BinaryType, c.Config.GoVersion,
		c.Config.DistSpecVersion)

	// nolint: contextcheck
	_ = NewRouteHandler(c)

	addr := fmt.Sprintf("%s:%s", c.Config.HTTP.Address, c.Config.HTTP.Port)
	server := &http.Server{
		Addr:        addr,
		Handler:     c.Router,
		IdleTimeout: idleTimeout,
	}
	c.Server = server

	// Create the listener
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	if c.Config.HTTP.TLS != nil && c.Config.HTTP.TLS.Key != "" && c.Config.HTTP.TLS.Cert != "" {
		server.TLSConfig = &tls.Config{
			CipherSuites: []uint16{
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
				tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			},
			CurvePreferences: []tls.CurveID{
				tls.CurveP256,
				tls.X25519,
			},
			PreferServerCipherSuites: true,
			MinVersion:               tls.VersionTLS12,
		}

		if c.Config.HTTP.TLS.CACert != "" {
			clientAuth := tls.VerifyClientCertIfGiven
			if (c.Config.HTTP.Auth == nil || c.Config.HTTP.Auth.HTPasswd.Path == "") && !c.Config.HTTP.AllowReadAccess {
				clientAuth = tls.RequireAndVerifyClientCert
			}

			caCert, err := ioutil.ReadFile(c.Config.HTTP.TLS.CACert)
			if err != nil {
				panic(err)
			}

			caCertPool := x509.NewCertPool()

			if !caCertPool.AppendCertsFromPEM(caCert) {
				panic(errors.ErrBadCACert)
			}

			server.TLSConfig.ClientAuth = clientAuth
			server.TLSConfig.ClientCAs = caCertPool
		}

		return server.ServeTLS(listener, c.Config.HTTP.TLS.Cert, c.Config.HTTP.TLS.Key)
	}

	return server.Serve(listener)
}

func (c *Controller) InitImageStore(reloadCtx context.Context) error {
	c.StoreController = storage.StoreController{}

	if c.Config.Storage.RootDirectory != "" {
		// no need to validate hard links work on s3
		if c.Config.Storage.Dedupe && c.Config.Storage.StorageDriver == nil {
			err := storage.ValidateHardLink(c.Config.Storage.RootDirectory)
			if err != nil {
				c.Log.Warn().Msg("input storage root directory filesystem does not supports hardlinking," +
					"disabling dedupe functionality")

				c.Config.Storage.Dedupe = false
			}
		}

		var defaultStore storage.ImageStore
		if c.Config.Storage.StorageDriver == nil {
			defaultStore = storage.NewImageStore(c.Config.Storage.RootDirectory,
				c.Config.Storage.GC, c.Config.Storage.GCDelay, c.Config.Storage.Dedupe, c.Config.Storage.Commit, c.Log, c.Metrics)
		} else {
			storeName := fmt.Sprintf("%v", c.Config.Storage.StorageDriver["name"])
			if storeName != storage.S3StorageDriverName {
				c.Log.Fatal().Err(errors.ErrBadConfig).Msgf("unsupported storage driver: %s",
					c.Config.Storage.StorageDriver["name"])
			}
			// Init a Storager from connection string.
			store, err := factory.Create(storeName, c.Config.Storage.StorageDriver)
			if err != nil {
				c.Log.Error().Err(err).Str("rootDir", c.Config.Storage.RootDirectory).Msg("unable to create s3 service")

				return err
			}

			/* in the case of s3 c.Config.Storage.RootDirectory is used for caching blobs locally and
			c.Config.Storage.StorageDriver["rootdirectory"] is the actual rootDir in s3 */
			rootDir := "/"
			if c.Config.Storage.StorageDriver["rootdirectory"] != nil {
				rootDir = fmt.Sprintf("%v", c.Config.Storage.StorageDriver["rootdirectory"])
			}

			defaultStore = s3.NewImageStore(rootDir, c.Config.Storage.RootDirectory,
				c.Config.Storage.GC, c.Config.Storage.GCDelay, c.Config.Storage.Dedupe,
				c.Config.Storage.Commit, c.Log, c.Metrics, store)
		}

		c.StoreController.DefaultStore = defaultStore
	} else {
		// we can't proceed without global storage
		c.Log.Error().Err(errors.ErrImgStoreNotFound).Msg("controller: no storage config provided")

		return errors.ErrImgStoreNotFound
	}

	if c.Config.Storage.SubPaths != nil {
		if len(c.Config.Storage.SubPaths) > 0 {
			subPaths := c.Config.Storage.SubPaths

			subImageStore := make(map[string]storage.ImageStore)

			// creating image store per subpaths
			for route, storageConfig := range subPaths {
				// no need to validate hard links work on s3
				if storageConfig.Dedupe && storageConfig.StorageDriver == nil {
					err := storage.ValidateHardLink(storageConfig.RootDirectory)
					if err != nil {
						c.Log.Warn().Msg("input storage root directory filesystem does not supports hardlinking, " +
							"disabling dedupe functionality")

						storageConfig.Dedupe = false
					}
				}

				if storageConfig.StorageDriver == nil {
					subImageStore[route] = storage.NewImageStore(storageConfig.RootDirectory,
						storageConfig.GC, storageConfig.GCDelay, storageConfig.Dedupe, storageConfig.Commit, c.Log, c.Metrics)
				} else {
					storeName := fmt.Sprintf("%v", storageConfig.StorageDriver["name"])
					if storeName != storage.S3StorageDriverName {
						c.Log.Fatal().Err(errors.ErrBadConfig).Msgf("unsupported storage driver: %s", storageConfig.StorageDriver["name"])
					}

					// Init a Storager from connection string.
					store, err := factory.Create(storeName, storageConfig.StorageDriver)
					if err != nil {
						c.Log.Error().Err(err).Str("rootDir", storageConfig.RootDirectory).Msg("Unable to create s3 service")

						return err
					}

					/* in the case of s3 c.Config.Storage.RootDirectory is used for caching blobs locally and
					c.Config.Storage.StorageDriver["rootdirectory"] is the actual rootDir in s3 */
					rootDir := "/"
					if c.Config.Storage.StorageDriver["rootdirectory"] != nil {
						rootDir = fmt.Sprintf("%v", c.Config.Storage.StorageDriver["rootdirectory"])
					}

					subImageStore[route] = s3.NewImageStore(rootDir, storageConfig.RootDirectory,
						storageConfig.GC, storageConfig.GCDelay, storageConfig.Dedupe, storageConfig.Commit, c.Log, c.Metrics, store)
				}
			}

			c.StoreController.SubStore = subImageStore
		}
	}

	c.StartBackgroundTasks(reloadCtx)

	return nil
}

func (c *Controller) LoadNewConfig(reloadCtx context.Context, config *config.Config) {
	// reload access control config
	c.Config.AccessControl = config.AccessControl
	c.Config.HTTP.RawAccessControl = config.HTTP.RawAccessControl

	// Enable extensions if extension config is provided
	if config.Extensions != nil && config.Extensions.Sync != nil {
		// reload sync config
		c.Config.Extensions.Sync = config.Extensions.Sync
		ext.EnableSyncExtension(reloadCtx, c.Config, c.wgShutDown, c.StoreController, c.Log)
	} else if c.Config.Extensions != nil {
		c.Config.Extensions.Sync = nil
	}

	c.Log.Info().Interface("reloaded params", c.Config.Sanitize()).Msg("new configuration settings")
}

func (c *Controller) Shutdown() {
	// wait gracefully
	c.wgShutDown.Wait()

	ctx := context.Background()
	_ = c.Server.Shutdown(ctx)
}

func (c *Controller) StartBackgroundTasks(reloadCtx context.Context) {
	// Enable running garbage-collect periodically for DefaultStore
	if c.Config.Storage.GC && c.Config.Storage.GCInterval != 0 {
		c.StoreController.DefaultStore.RunGCPeriodically(c.Config.Storage.GCInterval)
	}

	// Enable extensions if extension config is provided for DefaultStore
	if c.Config != nil && c.Config.Extensions != nil {
		ext.EnableExtensions(c.Config, c.Log, c.Config.Storage.RootDirectory)
	}

	if c.Config.Storage.SubPaths != nil {
		for route, storageConfig := range c.Config.Storage.SubPaths {
			// Enable running garbage-collect periodically for subImageStore
			if storageConfig.GC && storageConfig.GCInterval != 0 {
				c.StoreController.SubStore[route].RunGCPeriodically(storageConfig.GCInterval)
			}

			// Enable extensions if extension config is provided for subImageStore
			if c.Config != nil && c.Config.Extensions != nil {
				ext.EnableExtensions(c.Config, c.Log, storageConfig.RootDirectory)
			}
		}
	}

	// Enable extensions if extension config is provided for storeController
	if c.Config.Extensions != nil {
		if c.Config.Extensions.Sync != nil && *c.Config.Extensions.Sync.Enable {
			ext.EnableSyncExtension(reloadCtx, c.Config, c.wgShutDown, c.StoreController, c.Log)
		}
	}

	if c.Config.Extensions != nil {
		ext.EnableScrubExtension(c.Config, c.StoreController, c.Log)
	}
}
