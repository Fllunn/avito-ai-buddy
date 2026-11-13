package process

import (
	"ai-buddy/backend/platform/config"
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServe(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }), time.Second)
	}()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal(response.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown stuck")
	}
}

func TestClosedListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	if Serve(context.Background(), listener, http.NotFoundHandler(), time.Second) == nil {
		t.Fatal("listener failure lost")
	}
}

func TestShutdownDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { close(entered); <-release }), time.Millisecond)
	}()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		response, _ := (&http.Client{Timeout: time.Second}).Get("http://" + listener.Addr().String())
		if response != nil {
			response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler not reached")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("deadline did not force close")
	}
	<-clientDone
}

func TestStatic(t *testing.T) {
	handler := http.NotFoundHandler()
	actual, closeApp, err := Static(handler)(context.Background(), config.Config{})
	if err != nil || actual == nil {
		t.Fatal(actual, err)
	}
	closeApp()
}
