package httpeasy

import (
	"context"
	"crypto/tls"
	"database/sql"
	"github.com/julienschmidt/httprouter"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kaatinga/assets"
	"github.com/kaatinga/prettylogger"
	"golang.org/x/crypto/acme/autocert"
)

var timeOutDuration = 5 * time.Second

// SetUpHandlers type to announce handlers.
type SetUpHandlers func(r *httprouter.Router, db *sql.DB)

// Config - http service configuration compatible to settings package.
// https://github.com/kaatinga/settings
type Config struct {
	DB             *sql.DB              `env:"-"`
	Logger         *prettylogger.Logger `env:"-"`
	ProductionMode bool                 `env:"PROD"`
	HasDB          bool                 `env:"HAS_DB"`
	HTTP
	SSL *SSL `validate:"required_if=ProductionMode true"`

	ReadTimeout       time.Duration `env:"READ_TIMEOUT" default:"1m"`
	ReadHeaderTimeout time.Duration `env:"READ_HEADER_TIMEOUT" default:"15s"`
	WriteTimeout      time.Duration `env:"WRITE_TIMEOUT" default:"1m"`
}

type HTTP struct {
	Port uint16 `env:"PORT" validate:"min=80,max=9999"`
}

type SSL struct {
	Domain string `env:"DOMAIN" validate:"fqdn"`
	Email  string `env:"EMAIL" validate:"email"`
}

// newWebService creates new http router and creates http.Server structure
// with the created router inside.
func (config *Config) newWebService(logger *prettylogger.Logger) http.Server {

	config.Logger = logger

	return http.Server{
		Addr:              net.JoinHostPort("", assets.Uint162String(config.Port)),
		Handler:           httprouter.New(),
		ReadTimeout:       config.ReadTimeout,
		ReadHeaderTimeout: config.ReadHeaderTimeout,
		WriteTimeout:      config.WriteTimeout,
	}
}

// Launch enables the configured web service with the handlers that
// announced in a function matched with SetUpHandlers type.
func (config *Config) Launch(handlers SetUpHandlers, logger *prettylogger.Logger) error {

	// Launching
	webServer := config.newWebService(logger)
	config.Logger.Title.Info().Uint16("port", config.HTTP.Port).Msg("launching the service")

	// enable handlers inside SetUpHandlers function
	handlers(webServer.Handler.(*httprouter.Router), config.DB)
	config.Logger.SubMsg.Info().Msg("handlers have been announced")

	// shutdown is a special channel to handle errors
	shutdown := make(chan error, 2)

	switch config.ProductionMode {
	case true:
		config.Logger.SubMsg.Info().Msg("production mode is enabled")
		certManager := autocert.Manager{
			Prompt: autocert.AcceptTOS,

			// Domain
			HostPolicy: autocert.HostWhitelist(config.SSL.Domain),

			// Folder to store certificates
			Cache: autocert.DirCache("certs"),
			Email: config.SSL.Email,
		}

		webServer.TLSConfig = &tls.Config{
			GetCertificate: certManager.GetCertificate,
			MinVersion:     tls.VersionTLS12,
		}

		// Config server to redirect
		go func() {
			funcErr := http.ListenAndServe(
				":http",
				certManager.HTTPHandler(

					// Redirect from http to https
					http.RedirectHandler(
						"https://"+config.SSL.Domain,
						http.StatusPermanentRedirect),
				),
			)
			if funcErr != nil {
				config.Logger.SubMsg.Info().Msg("redirect to https failed")
				shutdown <- funcErr
				close(shutdown)
			}
		}()

		// HTTPS server to handle the service
		go func() {
			funcErr := webServer.ListenAndServeTLS("", "")
			if funcErr != nil {
				shutdown <- funcErr
				close(shutdown)
			}
		}()
	default:
		config.Logger.SubMsg.Warn().Msg("development mode is enabled")

		go func() {
			funcErr := webServer.ListenAndServe()
			if funcErr != nil {
				shutdown <- funcErr
				close(shutdown)
			}
		}()
	}

	config.Logger.SubMsg.Info().Msg("the service has been launched!")

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	select {
	case osSignal := <-interrupt:
		config.Logger.SubMsg.Error().Str("signal", osSignal.String()).Msg("received interrupt")
	case shutdownErr := <-shutdown:
		config.Logger.SubMsg.Err(shutdownErr).Msg("received shutdown message")
	}

	timeout, cancelFunc := context.WithTimeout(context.Background(), timeOutDuration)
	defer cancelFunc()

	config.Logger.SubMsg.Debug().Str("timeout", timeOutDuration.String()).Msg("delay is set")
	err := webServer.Shutdown(timeout)
	config.Logger.SubMsg.Debug().Msg("delayed shutdown is executed")
	return err
}
