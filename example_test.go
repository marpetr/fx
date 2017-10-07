// Copyright (c) 2017 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package fx_test

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"go.uber.org/fx"
)

// NewLogger constructs a logger. It's just a regular Go function, without any
// special relationship to Fx.
//
// Since it returns a *log.Logger, Fx will treat NewLogger as the constructor
// function for the standard library's logger. (We'll see how to integrate
// NewLogger into an Fx app in the main function.) Since NewLogger doesn't
// have any parameters, Fx will infer that loggers don't depend on any other
// types - we can create them from thin air.
//
// Fx calls constructors lazily, so NewLogger will only be called only if some
// other function needs a logger. Once instantiated, the logger is cached and
// reused - within the application, it's effectively a singleton.
//
// By default, Fx applications only allow one constructor for each type. See
// the advanced example for ways around this restriction.
func NewLogger() *log.Logger {
	logger := log.New(os.Stdout, "" /* prefix */, 0 /* flags */)
	logger.Print("Executing NewLogger.")
	return logger
}

// NewHandler constructs a simple HTTP handler. Since it returns an
// http.Handler, Fx will treat NewHandler as the constructor for the
// http.Handler type.
//
// Like many Go functions, NewHander also returns an error. If the error is
// non-nil, the caller should assume that NewHandler failed and the other
// returned values aren't safe to use. Fx understands this idiom, and assumes
// that any function whose last return value is an error follows this
// convention.
//
// Unlike NewLogger, NewHandler has formal parameters. Fx will interpret these
// parameters as dependencies: in order to construct an HTTP handler,
// NewHandler needs a logger. If the application has access to a *log.Logger
// constructor (like NewLogger above), it will use that constructor or its
// cached output and supply a logger to NewHandler. If the application doesn't
// know how to construct a logger and needs an HTTP handler, it will fail to
// start.
//
// Functions may also return multiple objects. For example, we could combine
// NewHandler and NewLogger into a single function:
//
//   func NewHandlerAndLogger() (*log.Logger, http.Handler, error)
//
// Fx also understands this idiom, and would treat NewHandlerAndLogger as the
// constructor for both the *log.Logger and http.Handler types.
func NewHandler(logger *log.Logger) (http.Handler, error) {
	if logger == nil {
		return nil, errors.New("logger is required")
	}
	logger.Print("Executing NewHandler.")
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		logger.Print("Got a request.")
	}), nil
}

// NewMux constructs an HTTP mux. Like NewHandler, it depends on *log.Logger.
// However, it also depends on the Fx-specific Lifecycle interface.
//
// A Lifecycle is available in every Fx application. It lets objects hook into
// the application's start and stop phases. In a non-Fx application, the main
// function often includes blocks like this:
//
//   srv, err := NewServer() // some long-running network server
//   if err != nil {
//     log.Fatalf("failed to construct server: %v", err)
//   }
//   // Construct other objects as necessary.
//   go srv.Start()
//   defer srv.Stop()
//
// In this example, the programmer explicitly constructs a bunch of objects,
// crashing the program if any of the constructors encounter unrecoverable
// errors. Once all the objects are constructed, we start any background
// goroutines and defer cleanup functions.
//
// Fx removes the manual object construction with dependency injection. It
// replaces the inline goroutine spawning and deferred cleanups with the
// Lifecycle type.
//
// Here, NewMux makes an HTTP mux available to other functions. Since
// constructors are called lazily, we know that NewMux won't be called unless
// some other function wants to register a handler. This makes it easy to
// start an HTTP server only if other functions have registered handlers.
func NewMux(lc fx.Lifecycle, logger *log.Logger) (*http.ServeMux, error) {
	if logger == nil {
		return nil, errors.New("logger is required")
	}
	logger.Print("Executing NewMux.")
	// First, we actually construct the mux and server. We don't want to start
	// the server until all handlers are registered.
	mux := http.NewServeMux()
	server := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}
	// If NewMux is called, we know that another function is registering
	// handlers. In that case, we'll use the Lifecycle type to register a Hook
	// that starts and stops our HTTP server.
	//
	// Hooks are executed in dependency order. At startup, NewLogger's hooks run
	// before NewMux's. On shutdown, the order is reversed.
	//
	// Returning an error from OnStart hooks interrupts application startup. Fx
	// immediately runs the OnStop portions of any successfully-executed OnStart
	// hooks (so that types which started cleanly can also shut down cleanly),
	// then exits.
	//
	// Returning an error from OnStop hooks logs a warning, but Fx continues to
	// run the remaining hooks.
	lc.Append(fx.Hook{
		// To mitigate the impact of deadlocks in application startup and
		// shutdown, Fx imposes a time limit on OnStart and OnStop hooks. By
		// default, hooks have a total of 30 seconds to complete. Timeouts are
		// passed via Go's usual context.Context.
		OnStart: func(context.Context) error {
			logger.Print("Starting HTTP server.")
			// In production, we'd need better error-handling here. See the
			// Lifecycle documentation for an example.
			go server.ListenAndServe()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.Print("Stopping HTTP server.")
			return server.Shutdown(ctx)
		},
	})

	return mux, nil
}

// Register mounts our HTTP
func Register(mux *http.ServeMux, h http.Handler) {
	mux.Handle("/", h)
}

func Example() {
	app := fx.New(
		// Provide all the constructors we need.
		fx.Provide(NewLogger, NewHandler, NewMux),
		// Before starting, register the handler. This forces resolution of all
		// the types Register function depends on: *http.ServeMux and http.Handler.
		// Since the mux is now being used, its startup hook gets registered
		// and the application includes an HTTP server.
		fx.Invoke(Register),
	)

	// In a real application, we could just use app.Run() here. Since we don't
	// want this example to run forever, we'll use Start and Stop.
	startCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := app.Start(startCtx); err != nil {
		log.Fatal(err)
	}

	// Normally, we'd block here with <-app.Done().
	http.Get("http://localhost:8080/")

	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := app.Stop(stopCtx); err != nil {
		log.Fatal(err)
	}
	// Output:
	// [Example] Executing NewLogger.
	// [Example] Executing NewMux.
	// [Example] Executing NewHandler.
	// [Example] Starting HTTP server.
	// [Example] Got a request.
	// [Example] Stopping HTTP server.
}
